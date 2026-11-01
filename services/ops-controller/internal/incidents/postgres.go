package incidents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/alerts"
)

const (
	lockFingerprintSQL = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`
	findObservationSQL = `SELECT i.id, COALESCE((i.evidence->>'occurrence_count')::bigint, 1)
FROM ops_incident_observations o
JOIN ops_incidents i ON i.id = o.incident_id
WHERE (o.source = $1 AND o.source_event_id = $2)
   OR o.idempotency_key = $3
ORDER BY o.created_at, o.id
LIMIT 1`
	findCorrelatedSQL = `SELECT id, COALESCE((evidence->>'occurrence_count')::bigint, 1)
FROM ops_incidents
WHERE fingerprint = $1
  AND status IN ('open', 'investigating', 'mitigated')
  AND last_observed_at >= $2
  AND last_observed_at <= $3
ORDER BY last_observed_at DESC, id
LIMIT 1
FOR UPDATE`
	updateIncidentSQL = `UPDATE ops_incidents
SET summary = $2,
    severity = CASE
      WHEN severity = 'critical' OR $3 = 'critical' THEN 'critical'
      WHEN severity = 'warning' OR $3 = 'warning' THEN 'warning'
      ELSE 'info'
    END,
    last_observed_at = GREATEST(last_observed_at, $4),
    evidence = jsonb_build_object(
      'classification', $5::text,
      'service', $6::text,
      'environment', $7::text,
      'occurrence_count', $8::bigint,
      'last_delivery_id', $9::text,
      'last_received_at', $10::timestamptz,
      'last_idempotency_key', $11::text
    ),
    updated_at = GREATEST(updated_at, $10)
WHERE id = $1`
	insertIncidentSQL = `INSERT INTO ops_incidents (
  source, source_event_id, fingerprint, title, summary, severity, status,
  evidence, first_observed_at, last_observed_at, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, 'open', $7, $8, $8, $9, $9)
RETURNING id`
	insertObservationSQL = `INSERT INTO ops_incident_observations (
  incident_id, source, source_event_id, idempotency_key,
  observed_at, received_at, evidence, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $6)`
)

var ErrRepositoryUnavailable = errors.New("incident repository unavailable")

// DB and Tx are intentionally database/sql-compatible seams. Tests do not
// need a driver, while production wiring can wrap *sql.DB without exposing
// arbitrary SQL to the alert boundary.
type DB interface {
	BeginTx(context.Context, *sql.TxOptions) (Tx, error)
}

type Tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) Row
	Commit() error
	Rollback() error
}

type Row interface{ Scan(...any) error }

type SQLDB struct{ DB *sql.DB }

func (d SQLDB) BeginTx(ctx context.Context, options *sql.TxOptions) (Tx, error) {
	if d.DB == nil {
		return nil, ErrRepositoryUnavailable
	}
	tx, err := d.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return sqlTx{Tx: tx}, nil
}

type sqlTx struct{ Tx *sql.Tx }

func (t sqlTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, query, args...)
}
func (t sqlTx) QueryRowContext(ctx context.Context, query string, args ...any) Row {
	return t.Tx.QueryRowContext(ctx, query, args...)
}
func (t sqlTx) Commit() error   { return t.Tx.Commit() }
func (t sqlTx) Rollback() error { return t.Tx.Rollback() }

type PostgresRepository struct {
	db                DB
	correlationWindow time.Duration
	maxAttempts       int
}

func NewPostgresRepository(db DB, correlationWindow time.Duration) (*PostgresRepository, error) {
	if db == nil || correlationWindow <= 0 || correlationWindow > 24*time.Hour {
		return nil, ErrRepositoryUnavailable
	}
	return &PostgresRepository{db: db, correlationWindow: correlationWindow, maxAttempts: 3}, nil
}

func (r *PostgresRepository) Observe(ctx context.Context, observation Observation) (Result, error) {
	if err := validateObservation(observation); err != nil {
		return Result{}, err
	}
	var last error
	for attempt := 0; attempt < r.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		result, err := r.observeOnce(ctx, observation)
		if err == nil {
			return result, nil
		}
		last = err
		if !isRetryable(err) {
			break
		}
	}
	return Result{}, fmt.Errorf("%w: %v", ErrRepositoryUnavailable, last)
}

func (r *PostgresRepository) observeOnce(ctx context.Context, o Observation) (Result, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Result{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, lockFingerprintSQL, o.Fingerprint); err != nil {
		return Result{}, err
	}

	id, count, err := findObservation(ctx, tx, o)
	if err == nil {
		if err = tx.Commit(); err != nil {
			return Result{}, err
		}
		committed = true
		return Result{IncidentID: id, OccurrenceCount: count}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Result{}, err
	}

	id, count, err = findCorrelatedIncident(ctx, tx, o, r.correlationWindow)
	created := false
	if errors.Is(err, sql.ErrNoRows) {
		id, err = insertIncident(ctx, tx, o)
		count, created = 1, true
	}
	if err != nil {
		return Result{}, err
	}
	if err = insertObservation(ctx, tx, id, o); err != nil {
		return Result{}, err
	}
	if !created {
		count++
		if err = updateIncident(ctx, tx, id, count, o); err != nil {
			return Result{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Result{}, err
	}
	committed = true
	return Result{IncidentID: id, OccurrenceCount: count, Created: created}, nil
}

func findObservation(ctx context.Context, tx Tx, o Observation) (string, int64, error) {
	var id string
	var count int64
	err := tx.QueryRowContext(ctx, findObservationSQL, o.Source, o.SourceEventID, o.IdempotencyKey).Scan(&id, &count)
	return id, count, err
}

func findCorrelatedIncident(ctx context.Context, tx Tx, o Observation, correlationWindow time.Duration) (string, int64, error) {
	var id string
	var count int64
	err := tx.QueryRowContext(ctx, findCorrelatedSQL, o.Fingerprint, o.ObservedAt.Add(-correlationWindow), o.ObservedAt.Add(correlationWindow)).Scan(&id, &count)
	return id, count, err
}

func validateObservation(o Observation) error {
	if o.Source == "" || o.SourceEventID == "" || !validDigest(o.Fingerprint) || !validDigest(o.IdempotencyKey) || o.Title == "" || o.Summary == "" || o.ObservedAt.IsZero() || o.ReceivedAt.IsZero() || o.Classification != ClassificationVersion {
		return ErrInvalidObservation
	}
	switch o.Severity {
	case alerts.SeverityInfo, alerts.SeverityWarning, alerts.SeverityCritical:
		return nil
	default:
		return ErrInvalidObservation
	}
}

func updateIncident(ctx context.Context, tx Tx, id string, count int64, o Observation) error {
	_, err := tx.ExecContext(ctx, updateIncidentSQL, id, o.Summary, o.Severity, o.ObservedAt, o.Classification, o.Service, o.Environment, count, o.DeliveryID, o.ReceivedAt, o.IdempotencyKey)
	return err
}

func insertIncident(ctx context.Context, tx Tx, o Observation) (string, error) {
	evidence, err := json.Marshal(map[string]any{
		"classification":       o.Classification,
		"environment":          o.Environment,
		"last_delivery_id":     o.DeliveryID,
		"last_idempotency_key": o.IdempotencyKey,
		"last_received_at":     o.ReceivedAt,
		"occurrence_count":     1,
		"service":              o.Service,
	})
	if err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRowContext(ctx, insertIncidentSQL, o.Source, o.SourceEventID, o.Fingerprint, o.Title, o.Summary, o.Severity, evidence, o.ObservedAt, o.ReceivedAt).Scan(&id)
	return id, err
}

func insertObservation(ctx context.Context, tx Tx, incidentID string, o Observation) error {
	evidence, err := json.Marshal(map[string]any{
		"classification": o.Classification,
		"delivery_id":    o.DeliveryID,
		"environment":    o.Environment,
		"service":        o.Service,
	})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, insertObservationSQL, incidentID, o.Source, o.SourceEventID, o.IdempotencyKey, o.ObservedAt, o.ReceivedAt, evidence)
	return err
}

type sqlStateError interface{ SQLState() string }

func isRetryable(err error) bool {
	var state sqlStateError
	return errors.As(err, &state) && (state.SQLState() == "40001" || state.SQLState() == "40P01" || state.SQLState() == "23505")
}

// Keep the typed severity import compile-checked at this persistence boundary.
var _ = alerts.SeverityInfo

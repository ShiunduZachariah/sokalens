// Package replaystore implements the aiworkflow.ReplayStore contract on
// PostgreSQL with an atomic claim. The (incident_id, idempotency_key) primary
// key makes Claim an atomic INSERT ... ON CONFLICT DO NOTHING, so exactly one
// controller runs a work item and concurrent controllers receive the immutable
// completed result instead of double-running the workflow.
package replaystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/aiworkflow"
)

var (
	ErrRepositoryUnavailable = errors.New("AI replay store unavailable")
	ErrClaimLost             = errors.New("AI replay claim is no longer owned")
)

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

func (database SQLDB) BeginTx(ctx context.Context, options *sql.TxOptions) (Tx, error) {
	if database.DB == nil {
		return nil, ErrRepositoryUnavailable
	}
	tx, err := database.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return sqlTx{Tx: tx}, nil
}

type sqlTx struct{ Tx *sql.Tx }

func (transaction sqlTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return transaction.Tx.ExecContext(ctx, query, args...)
}
func (transaction sqlTx) QueryRowContext(ctx context.Context, query string, args ...any) Row {
	return transaction.Tx.QueryRowContext(ctx, query, args...)
}
func (transaction sqlTx) Commit() error   { return transaction.Tx.Commit() }
func (transaction sqlTx) Rollback() error { return transaction.Tx.Rollback() }

const (
	insertClaimSQL = `INSERT INTO ops_ai_replay (incident_id, idempotency_key, status, lease_until)
VALUES ($1, $2, 'running', $3)
ON CONFLICT (incident_id, idempotency_key) DO NOTHING`
	selectClaimSQL = `SELECT status, result, lease_until FROM ops_ai_replay WHERE incident_id = $1 AND idempotency_key = $2 FOR UPDATE`
	renewClaimSQL  = `UPDATE ops_ai_replay SET lease_until = $3, updated_at = now() WHERE incident_id = $1 AND idempotency_key = $2 AND status = 'running'`
	completeSQL    = `UPDATE ops_ai_replay SET status = 'done', result = $3, completed_at = now() WHERE incident_id = $1 AND idempotency_key = $2`
)

type Store struct {
	db    DB
	lease time.Duration
	now   func() time.Time
}

func New(db DB, lease time.Duration) (*Store, error) {
	if db == nil {
		return nil, ErrRepositoryUnavailable
	}
	if lease <= 0 {
		lease = time.Minute
	}
	return &Store{db: db, lease: lease, now: time.Now}, nil
}

// Claim atomically acquires the work item. It returns Owned=true when this
// controller must run the workflow, or Replayed=true with the completed result
// when another controller already finished it. A live unexpired claim owned by
// another controller returns aiworkflow.ErrReplayClaimed.
func (s *Store) Claim(ctx context.Context, incidentID, idempotencyKey string) (aiworkflow.ReplayClaim, error) {
	if s == nil {
		return aiworkflow.ReplayClaim{}, ErrRepositoryUnavailable
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return aiworkflow.ReplayClaim{}, err
	}
	defer tx.Rollback()

	leaseUntil := s.now().Add(s.lease)
	result, err := tx.ExecContext(ctx, insertClaimSQL, incidentID, idempotencyKey, leaseUntil)
	if err != nil {
		return aiworkflow.ReplayClaim{}, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return aiworkflow.ReplayClaim{}, err
	}
	if inserted == 1 {
		if err := tx.Commit(); err != nil {
			return aiworkflow.ReplayClaim{}, err
		}
		return aiworkflow.ReplayClaim{Owned: true}, nil
	}

	var status string
	var payload []byte
	var existingLease time.Time
	if err := tx.QueryRowContext(ctx, selectClaimSQL, incidentID, idempotencyKey).Scan(&status, &payload, &existingLease); err != nil {
		return aiworkflow.ReplayClaim{}, err
	}
	switch status {
	case "done":
		var stored aiworkflow.Result
		if err := json.Unmarshal(payload, &stored); err != nil {
			return aiworkflow.ReplayClaim{}, fmt.Errorf("decode completed replay: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return aiworkflow.ReplayClaim{}, err
		}
		return aiworkflow.ReplayClaim{Replayed: true, Result: stored}, nil
	case "running":
		if existingLease.After(s.now()) {
			return aiworkflow.ReplayClaim{}, aiworkflow.ErrReplayClaimed
		}
		if _, err := tx.ExecContext(ctx, renewClaimSQL, incidentID, idempotencyKey, leaseUntil); err != nil {
			return aiworkflow.ReplayClaim{}, err
		}
		if err := tx.Commit(); err != nil {
			return aiworkflow.ReplayClaim{}, err
		}
		return aiworkflow.ReplayClaim{Owned: true}, nil
	default:
		return aiworkflow.ReplayClaim{}, fmt.Errorf("unexpected replay status %q", status)
	}
}

// Complete stores the completed result and marks the work item done.
func (s *Store) Complete(ctx context.Context, result aiworkflow.Result) error {
	if s == nil {
		return ErrRepositoryUnavailable
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode completed replay: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	outcome, err := tx.ExecContext(ctx, completeSQL, result.IncidentID, result.IdempotencyKey, payload)
	if err != nil {
		return err
	}
	updated, err := outcome.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return ErrClaimLost
	}
	return tx.Commit()
}

package incidents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	lockTransitionKeySQL = `SELECT pg_advisory_xact_lock(hashtextextended('incident-transition:' || $1::text, 0))`
	findTransitionSQL    = `SELECT t.incident_id::text, t.from_status, t.to_status, t.reason, t.actor_type, t.actor_id,
       t.occurred_at, t.audit_event_id::text, a.event_hash
FROM ops_incident_transitions t
JOIN ops_audit_events a ON a.event_id = t.audit_event_id
WHERE t.idempotency_key = $1`
	lockIncidentSQL = `SELECT status, updated_at, last_observed_at
FROM ops_incidents
WHERE id = $1
FOR UPDATE`
	findPreviousAuditHashSQL = `SELECT a.event_hash
FROM ops_incident_transitions t
JOIN ops_audit_events a ON a.event_id = t.audit_event_id
WHERE t.incident_id = $1
  AND a.entity_type = 'incident'
  AND a.entity_id = $1::text
  AND a.event_type = 'incident.status_transition'
ORDER BY a.sequence_id DESC
LIMIT 1`
	insertAuditEventSQL = `INSERT INTO ops_audit_events (
  event_id, event_type, actor_type, actor_id, entity_type, entity_id,
  occurred_at, details, previous_event_hash, event_hash
) VALUES ($1, 'incident.status_transition', $2, $3, 'incident', $4, $5, $6, NULLIF($7, ''), $8)`
	updateIncidentStatusSQL = `UPDATE ops_incidents
SET status = $3,
    acknowledged_at = CASE
      WHEN $3 = 'investigating' THEN COALESCE(acknowledged_at, $4)
      ELSE acknowledged_at
    END,
    resolved_at = CASE WHEN $3 = 'resolved' THEN $4 ELSE resolved_at END,
    updated_at = $4
WHERE id = $1 AND status = $2`
	insertTransitionSQL = `INSERT INTO ops_incident_transitions (
  incident_id, idempotency_key, from_status, to_status, reason,
  actor_type, actor_id, occurred_at, audit_event_id, previous_event_hash
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''))`
)

type PostgresStateMachine struct {
	db          DB
	maxAttempts int
}

func NewPostgresStateMachine(db DB) (*PostgresStateMachine, error) {
	if db == nil {
		return nil, ErrRepositoryUnavailable
	}
	return &PostgresStateMachine{db: db, maxAttempts: 3}, nil
}

func (machine *PostgresStateMachine) Transition(ctx context.Context, request TransitionRequest) (TransitionResult, error) {
	request.IncidentID = strings.TrimSpace(request.IncidentID)
	request.IdempotencyKey = strings.ToLower(strings.TrimSpace(request.IdempotencyKey))
	request.Actor.Type = strings.ToLower(strings.TrimSpace(request.Actor.Type))
	request.Reason = strings.TrimSpace(request.Reason)
	request.Actor.ID = strings.TrimSpace(request.Actor.ID)
	request.OccurredAt = request.OccurredAt.UTC()
	if !request.ObservedBefore.IsZero() {
		request.ObservedBefore = request.ObservedBefore.UTC()
	}
	if err := validateTransitionRequest(request); err != nil {
		return TransitionResult{}, err
	}
	var last error
	for attempt := 0; attempt < machine.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return TransitionResult{}, err
		}
		result, err := machine.transitionOnce(ctx, request)
		if err == nil {
			return result, nil
		}
		last = err
		if !isRetryable(err) {
			break
		}
	}
	if errors.Is(last, ErrInvalidTransition) || errors.Is(last, ErrTransitionConflict) || errors.Is(last, ErrIncidentNotFound) || errors.Is(last, ErrObservationConflict) {
		return TransitionResult{}, last
	}
	if errors.Is(last, context.Canceled) || errors.Is(last, context.DeadlineExceeded) {
		return TransitionResult{}, last
	}
	return TransitionResult{}, fmt.Errorf("%w: %v", ErrRepositoryUnavailable, last)
}

type storedTransition struct {
	IncidentID string
	From       IncidentStatus
	To         IncidentStatus
	Reason     string
	ActorType  string
	ActorID    string
	OccurredAt time.Time
	AuditID    string
	EventHash  string
}

func (machine *PostgresStateMachine) transitionOnce(ctx context.Context, request TransitionRequest) (TransitionResult, error) {
	tx, err := machine.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return TransitionResult{}, fmt.Errorf("%w: %v", ErrRepositoryUnavailable, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, lockTransitionKeySQL, request.IdempotencyKey); err != nil {
		return TransitionResult{}, err
	}
	stored, err := loadTransition(ctx, tx, request.IdempotencyKey)
	if err == nil {
		if !sameTransitionRequest(stored, request) {
			return TransitionResult{}, ErrTransitionConflict
		}
		if err = tx.Commit(); err != nil {
			return TransitionResult{}, err
		}
		committed = true
		return TransitionResult{IncidentID: stored.IncidentID, From: stored.From, To: stored.To, AuditEventID: stored.AuditID, EventHash: stored.EventHash}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return TransitionResult{}, err
	}

	var current IncidentStatus
	var updatedAt time.Time
	var lastObservedAt time.Time
	if err = tx.QueryRowContext(ctx, lockIncidentSQL, request.IncidentID).Scan(&current, &updatedAt, &lastObservedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TransitionResult{}, ErrIncidentNotFound
		}
		return TransitionResult{}, err
	}
	if !request.ObservedBefore.IsZero() && !lastObservedAt.Before(request.ObservedBefore) {
		return TransitionResult{}, ErrObservationConflict
	}
	if !AllowedTransition(current, request.To) || request.OccurredAt.Before(updatedAt) {
		return TransitionResult{}, ErrInvalidTransition
	}
	previousHash := ""
	if err = tx.QueryRowContext(ctx, findPreviousAuditHashSQL, request.IncidentID).Scan(&previousHash); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TransitionResult{}, err
	}
	eventID, details, eventHash, err := buildAuditEvent(request, current, previousHash)
	if err != nil {
		return TransitionResult{}, err
	}
	if _, err = tx.ExecContext(ctx, insertAuditEventSQL, eventID, request.Actor.Type, request.Actor.ID, request.IncidentID, request.OccurredAt, details, previousHash, eventHash); err != nil {
		return TransitionResult{}, err
	}
	result, err := tx.ExecContext(ctx, updateIncidentStatusSQL, request.IncidentID, current, request.To, request.OccurredAt)
	if err != nil {
		return TransitionResult{}, err
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr != nil || affected != 1 {
		return TransitionResult{}, ErrInvalidTransition
	}
	if _, err = tx.ExecContext(ctx, insertTransitionSQL, request.IncidentID, request.IdempotencyKey, current, request.To, request.Reason, request.Actor.Type, request.Actor.ID, request.OccurredAt, eventID, previousHash); err != nil {
		return TransitionResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return TransitionResult{}, err
	}
	committed = true
	return TransitionResult{IncidentID: request.IncidentID, From: current, To: request.To, AuditEventID: eventID, EventHash: eventHash, Applied: true}, nil
}

func loadTransition(ctx context.Context, tx Tx, key string) (storedTransition, error) {
	var stored storedTransition
	err := tx.QueryRowContext(ctx, findTransitionSQL, key).Scan(
		&stored.IncidentID, &stored.From, &stored.To, &stored.Reason,
		&stored.ActorType, &stored.ActorID, &stored.OccurredAt, &stored.AuditID, &stored.EventHash,
	)
	return stored, err
}

func sameTransitionRequest(stored storedTransition, request TransitionRequest) bool {
	return stored.IncidentID == request.IncidentID && stored.To == request.To &&
		stored.Reason == request.Reason && stored.ActorType == request.Actor.Type &&
		stored.ActorID == request.Actor.ID && stored.OccurredAt.Equal(request.OccurredAt)
}

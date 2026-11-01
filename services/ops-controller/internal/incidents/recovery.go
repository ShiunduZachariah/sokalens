package incidents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const recoverableIncidentsSQL = `SELECT id::text, status, last_observed_at
FROM ops_incidents
WHERE source = 'prometheus'
  AND fingerprint = $1
  AND status IN ('open', 'investigating', 'mitigated')
  AND last_observed_at < $2
ORDER BY last_observed_at, id
LIMIT 50`

type RecoveryTransitioner interface {
	Transition(context.Context, TransitionRequest) (TransitionResult, error)
}

type PostgresRecoveryStore struct {
	queries     Queryer
	transitions RecoveryTransitioner
}

func NewPostgresRecoveryStore(queries Queryer, transitions RecoveryTransitioner) (*PostgresRecoveryStore, error) {
	if queries == nil || transitions == nil {
		return nil, ErrRepositoryUnavailable
	}
	return &PostgresRecoveryStore{queries: queries, transitions: transitions}, nil
}

type recoveryCandidate struct {
	id           string
	status       IncidentStatus
	lastObserved time.Time
}

// ResolveHealthy closes only incidents that predate the sustained healthy
// window. Every change uses the audited state machine, which locks and checks
// last_observed_at again so a concurrent firing observation wins the race.
func (r *PostgresRecoveryStore) ResolveHealthy(ctx context.Context, fingerprint string, observedBefore, occurredAt time.Time) (int, error) {
	if r == nil || r.queries == nil || r.transitions == nil || !validDigest(fingerprint) || observedBefore.IsZero() || occurredAt.IsZero() || !observedBefore.Before(occurredAt) {
		return 0, ErrInvalidTransition
	}
	rows, err := r.queries.QueryContext(ctx, recoverableIncidentsSQL, fingerprint, observedBefore.UTC())
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrRepositoryUnavailable, err)
	}
	var candidates []recoveryCandidate
	for rows.Next() {
		var candidate recoveryCandidate
		var status string
		if err = rows.Scan(&candidate.id, &status, &candidate.lastObserved); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("%w: %v", ErrRepositoryUnavailable, err)
		}
		candidate.status = IncidentStatus(status)
		candidates = append(candidates, candidate)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		return 0, fmt.Errorf("%w: %v", ErrRepositoryUnavailable, errors.Join(err, closeErr))
	}

	resolved := 0
	for _, candidate := range candidates {
		stages := recoveryStages(candidate.status)
		for index, to := range stages {
			request := TransitionRequest{
				IncidentID:     candidate.id,
				IdempotencyKey: recoveryKey(candidate.id, to, candidate.lastObserved),
				To:             to,
				Reason:         "Prometheus condition healthy for at least fifteen minutes",
				Actor:          AuditActor{Type: "system", ID: "alert-evaluator"},
				OccurredAt:     occurredAt.UTC().Add(time.Duration(index) * time.Nanosecond),
				ObservedBefore: observedBefore.UTC(),
			}
			_, err = r.transitions.Transition(ctx, request)
			if errors.Is(err, ErrObservationConflict) || errors.Is(err, ErrInvalidTransition) || errors.Is(err, ErrIncidentNotFound) {
				break // another observation or operator changed this incident
			}
			if err != nil {
				return resolved, err
			}
			if to == StatusResolved {
				resolved++
			}
		}
	}
	return resolved, nil
}

func recoveryStages(status IncidentStatus) []IncidentStatus {
	switch status {
	case StatusOpen:
		return []IncidentStatus{StatusInvestigating, StatusMitigated, StatusResolved}
	case StatusInvestigating:
		return []IncidentStatus{StatusMitigated, StatusResolved}
	case StatusMitigated:
		return []IncidentStatus{StatusResolved}
	default:
		return nil
	}
}

func recoveryKey(id string, to IncidentStatus, lastObserved time.Time) string {
	sum := sha256.Sum256([]byte("prometheus-recovery\x00" + id + "\x00" + string(to) + "\x00" + lastObserved.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}

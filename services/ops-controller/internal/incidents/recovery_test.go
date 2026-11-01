package incidents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeRecoveryTransitions struct {
	requests []TransitionRequest
	failAt   int
}

func (f *fakeRecoveryTransitions) Transition(_ context.Context, request TransitionRequest) (TransitionResult, error) {
	f.requests = append(f.requests, request)
	if f.failAt == len(f.requests) {
		return TransitionResult{}, ErrObservationConflict
	}
	return TransitionResult{IncidentID: request.IncidentID, To: request.To, Applied: true}, nil
}

func TestRecoveryUsesAuditedTransitionsAndObservationGuard(t *testing.T) {
	now := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	cutoff := now.Add(-15 * time.Minute)
	last := cutoff.Add(-time.Minute)
	query := &queryDB{rows: []*queryRows{{values: [][]any{{"incident-open", "open", last}, {"incident-mitigated", "mitigated", last}}}}}
	transitions := &fakeRecoveryTransitions{}
	store, err := NewPostgresRecoveryStore(query, transitions)
	if err != nil {
		t.Fatal(err)
	}
	count, err := store.ResolveHealthy(context.Background(), strings.Repeat("a", 64), cutoff, now)
	if err != nil || count != 2 {
		t.Fatalf("resolved=%d err=%v", count, err)
	}
	if len(transitions.requests) != 4 {
		t.Fatalf("transitions=%d want four audited steps", len(transitions.requests))
	}
	for index, to := range []IncidentStatus{StatusInvestigating, StatusMitigated, StatusResolved, StatusResolved} {
		request := transitions.requests[index]
		if request.To != to || request.Actor.Type != "system" || request.Actor.ID != "alert-evaluator" || !request.ObservedBefore.Equal(cutoff) || !validDigest(request.IdempotencyKey) {
			t.Fatalf("transition %d = %+v", index, request)
		}
	}
	if len(query.queries) != 1 || query.queries[0] != recoverableIncidentsSQL || query.args[0][0] != strings.Repeat("a", 64) || !strings.Contains(recoverableIncidentsSQL, "source = 'prometheus'") {
		t.Fatalf("recovery query not scoped to Prometheus fingerprint: %v", query.queries)
	}
}

func TestRecoveryStopsWhenObservationRacesTransition(t *testing.T) {
	now := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	query := &queryDB{rows: []*queryRows{{values: [][]any{{"incident-open", "open", now.Add(-time.Hour)}}}}}
	transitions := &fakeRecoveryTransitions{failAt: 2}
	store, _ := NewPostgresRecoveryStore(query, transitions)
	count, err := store.ResolveHealthy(context.Background(), strings.Repeat("b", 64), now.Add(-15*time.Minute), now)
	if err != nil || count != 0 || len(transitions.requests) != 2 {
		t.Fatalf("resolved=%d err=%v transitions=%d", count, err, len(transitions.requests))
	}
}

func TestRecoveryReadFailureNeverTransitions(t *testing.T) {
	query := &queryDB{rowsErr: errors.New("database down")}
	transitions := &fakeRecoveryTransitions{}
	store, _ := NewPostgresRecoveryStore(query, transitions)
	now := time.Now().UTC()
	_, err := store.ResolveHealthy(context.Background(), strings.Repeat("c", 64), now.Add(-15*time.Minute), now)
	if !errors.Is(err, ErrRepositoryUnavailable) || len(transitions.requests) != 0 {
		t.Fatalf("err=%v transitions=%d", err, len(transitions.requests))
	}
}

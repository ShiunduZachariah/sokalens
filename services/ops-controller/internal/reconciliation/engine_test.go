package reconciliation

import (
	"context"
	"sync"
	"testing"
	"time"
)

type memoryRepository struct {
	mu      sync.Mutex
	runs    map[string]Run
	applied int
}

func (r *memoryRepository) Apply(_ context.Context, run Run) (Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stored, ok := r.runs[run.IdempotencyKey]; ok {
		stored.Applied = false
		return stored, nil
	}
	run.Applied = true
	r.runs[run.IdempotencyKey] = run
	r.applied++
	return run, nil
}

func requestAt(now time.Time, internal []InternalSnapshot, results []ProviderResult) Request {
	return Request{IdempotencyKey: "run-001", Provider: "billing", Environment: "test", StartedAt: now.Add(-time.Second), CompletedAt: now, Now: now, Internal: internal, ProviderResults: results}
}

func snapshot(id string, lifecycle Lifecycle, revision string, observed time.Time) ProviderResult {
	return ProviderResult{ProviderSubscriptionID: id, Snapshot: &ProviderSnapshot{ProviderSubscriptionID: id, Lifecycle: lifecycle, Revision: revision, ObservedAt: observed}}
}

func newEngine(t *testing.T) (*Engine, *memoryRepository) {
	t.Helper()
	repository := &memoryRepository{runs: make(map[string]Run)}
	engine, err := New(repository)
	if err != nil {
		t.Fatal(err)
	}
	return engine, repository
}

func TestPartialProviderFailureFreezesOnlyAffectedSubscription(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	engine, _ := newEngine(t)
	internal := []InternalSnapshot{
		{SubscriptionID: 1, ProviderSubscriptionID: "sub-ok", Status: "active", ProviderRevision: "r1", ProviderObservedAt: now.Add(-time.Minute), Grants: []Grant{{ID: 11, ReconciliationRevision: "r2"}}},
		{SubscriptionID: 2, ProviderSubscriptionID: "sub-failed", Status: "active", ProviderRevision: "r1", ProviderObservedAt: now.Add(-time.Minute), Grants: []Grant{{ID: 22, ReconciliationRevision: "r1"}}},
	}
	results := []ProviderResult{snapshot("sub-ok", Active, "r2", now), {ProviderSubscriptionID: "sub-failed", FailureCode: "provider_timeout"}}
	run, err := engine.Reconcile(context.Background(), requestAt(now, internal, results))
	if err != nil {
		t.Fatal(err)
	}
	if run.Result != "partial" || run.FailedCount != 1 {
		t.Fatalf("result=%s failed=%d", run.Result, run.FailedCount)
	}
	if len(run.Items[0].Corrections) != 0 {
		t.Fatalf("verified subscription received corrections: %+v", run.Items[0].Corrections)
	}
	failed := run.Items[1]
	if len(failed.Corrections) != 1 || failed.Corrections[0].Action != FreezeNewGrants || failed.Corrections[0].SubscriptionID != 2 {
		t.Fatalf("failed corrections=%+v", failed.Corrections)
	}
}

func TestScopeFailureFreezesOnlyNewGrantsAndDoesNotRevokeVerifiedAccess(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	engine, _ := newEngine(t)
	request := requestAt(now, []InternalSnapshot{{SubscriptionID: 1, ProviderSubscriptionID: "verified", Status: "active", Grants: []Grant{{ID: 1, ReconciliationRevision: "r1"}}}}, nil)
	request.ScopeFailureCode = "provider_unavailable"
	run, err := engine.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if run.Result != "failed" || countRunAction(run, FreezeNewGrants) != 1 || countRunAction(run, RevokeGrant) != 0 {
		t.Fatalf("scope-failure run=%+v", run)
	}
	if correction := run.Items[0].Corrections[0]; correction.SubscriptionID != 0 {
		t.Fatalf("scope freeze targeted subscription %d", correction.SubscriptionID)
	}
}

func TestDuplicateAndOutOfOrderSnapshotsAreDeterministic(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	engine, _ := newEngine(t)
	current := InternalSnapshot{SubscriptionID: 1, ProviderSubscriptionID: "sub", Status: "active", ProviderRevision: "r2", ProviderObservedAt: now, Grants: []Grant{{ID: 1, ReconciliationRevision: "r2"}}}
	old := snapshot("sub", Expiry, "r1", now.Add(-time.Hour))
	run, err := engine.Reconcile(context.Background(), requestAt(now, []InternalSnapshot{current}, []ProviderResult{old, old}))
	if err != nil {
		t.Fatal(err)
	}
	if item := run.Items[0]; item.Classification != "stale" || item.Result != Matched || len(item.Corrections) != 0 {
		t.Fatalf("stale item=%+v", item)
	}

	request := requestAt(now.Add(time.Minute), []InternalSnapshot{current}, []ProviderResult{snapshot("sub", Chargeback, "r3", now.Add(time.Minute)), old, snapshot("sub", Chargeback, "r3", now.Add(time.Minute))})
	request.IdempotencyKey = "run-002"
	run, err = engine.Reconcile(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if item := run.Items[0]; item.ProviderRevision != "r3" || item.Classification != string(Chargeback) || countAction(item.Corrections, RevokeGrant) != 1 {
		t.Fatalf("selected item=%+v", item)
	}
}

func TestAvoidsFalsePositivesForVerifiedAndUnknownSubscriptions(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	engine, _ := newEngine(t)
	current := InternalSnapshot{SubscriptionID: 7, ProviderSubscriptionID: "known", Status: "active", ProviderRevision: "same", ProviderObservedAt: now, Grants: []Grant{{ID: 70, ReconciliationRevision: "same"}}}
	run, err := engine.Reconcile(context.Background(), requestAt(now, []InternalSnapshot{current}, []ProviderResult{snapshot("known", Active, "same", now), snapshot("provider-only", Active, "new", now)}))
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Items) != 2 || run.CorrectionCount != 0 {
		t.Fatalf("items=%d corrections=%d", len(run.Items), run.CorrectionCount)
	}
	for _, item := range run.Items {
		if len(item.Corrections) != 0 {
			t.Fatalf("unexpected correction=%+v", item)
		}
	}
}

func TestLifecycleClassificationAndScopedActions(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	tests := []struct {
		name       string
		lifecycle  Lifecycle
		end, grace *time.Time
		target     string
		revoke     bool
	}{
		{"pending", PendingPayment, nil, nil, "incomplete", true},
		{"active", Active, nil, nil, "active", false},
		{"renewal", Renewal, nil, nil, "active", false},
		{"grace", Grace, nil, &future, "past_due", false},
		{"grace expired", Grace, nil, &past, "payment_failed", true},
		{"retry", Retry, nil, &future, "past_due", false},
		{"refund", Refund, nil, nil, "cancelled", true},
		{"chargeback", Chargeback, nil, nil, "suspended", true},
		{"cancel period end", Cancellation, &future, nil, "scheduled_to_cancel", false},
		{"cancel expired", Cancellation, &past, nil, "expired", true},
		{"expiry", Expiry, nil, nil, "expired", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine, _ := newEngine(t)
			current := InternalSnapshot{SubscriptionID: 1, ProviderSubscriptionID: "sub", Status: "wrong", ProviderObservedAt: past, Grants: []Grant{{ID: 9}}}
			result := snapshot("sub", test.lifecycle, "revision", now)
			result.Snapshot.CurrentPeriodEnd = test.end
			result.Snapshot.GraceEndsAt = test.grace
			run, err := engine.Reconcile(context.Background(), requestAt(now, []InternalSnapshot{current}, []ProviderResult{result}))
			if err != nil {
				t.Fatal(err)
			}
			item := run.Items[0]
			if item.Classification != string(test.lifecycle) || !hasCorrection(item.Corrections, CorrectSubscription, test.target) {
				t.Fatalf("item=%+v", item)
			}
			if (countAction(item.Corrections, RevokeGrant) == 1) != test.revoke {
				t.Fatalf("revoke actions=%+v", item.Corrections)
			}
		})
	}
}

func TestConcurrentReplayAppliesOnce(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	engine, repository := newEngine(t)
	request := requestAt(now, []InternalSnapshot{{SubscriptionID: 1, ProviderSubscriptionID: "sub", Status: "active"}}, []ProviderResult{snapshot("sub", Expiry, "r1", now)})
	const callers = 32
	var wg sync.WaitGroup
	wg.Add(callers)
	errors := make(chan error, callers)
	applied := make(chan bool, callers)
	for range callers {
		go func() {
			defer wg.Done()
			run, err := engine.Reconcile(context.Background(), request)
			errors <- err
			applied <- run.Applied
		}()
	}
	wg.Wait()
	close(errors)
	close(applied)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	appliedCount := 0
	for value := range applied {
		if value {
			appliedCount++
		}
	}
	if appliedCount != 1 || repository.applied != 1 {
		t.Fatalf("applied returns=%d repository=%d", appliedCount, repository.applied)
	}
}

func TestCorrectionIdentityIsStableAcrossScheduledRunKeys(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	internal := []InternalSnapshot{{SubscriptionID: 1, ProviderSubscriptionID: "sub", Status: "active", Grants: []Grant{{ID: 9}}}}
	results := []ProviderResult{snapshot("sub", Chargeback, "provider-r1", now)}
	engineA, _ := newEngine(t)
	requestA := requestAt(now, internal, results)
	requestA.IdempotencyKey = "scheduled-a"
	runA, err := engineA.Reconcile(context.Background(), requestA)
	if err != nil {
		t.Fatal(err)
	}
	engineB, _ := newEngine(t)
	requestB := requestAt(now.Add(time.Minute), internal, results)
	requestB.IdempotencyKey = "scheduled-b"
	requestB.StartedAt = requestA.StartedAt
	requestB.CompletedAt = requestA.CompletedAt
	requestB.Now = requestA.Now
	runB, err := engineB.Reconcile(context.Background(), requestB)
	if err != nil {
		t.Fatal(err)
	}
	if len(runA.Items[0].Corrections) != len(runB.Items[0].Corrections) {
		t.Fatal("correction counts differ")
	}
	for index := range runA.Items[0].Corrections {
		if runA.Items[0].Corrections[index].IdempotencyKey != runB.Items[0].Corrections[index].IdempotencyKey {
			t.Fatalf("correction %d keys differ", index)
		}
	}
}

func TestOutageRecoveryOutageUsesNewFreezeCycleIdentity(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	current := InternalSnapshot{SubscriptionID: 42, ProviderSubscriptionID: "sub-42", Status: "active"}

	firstOutage := requestAt(now, []InternalSnapshot{current}, []ProviderResult{{ProviderSubscriptionID: "sub-42", FailureCode: "provider_timeout"}})
	firstOutage.IdempotencyKey = "outage-cycle-1"
	firstEngine, _ := newEngine(t)
	firstRun, err := firstEngine.Reconcile(context.Background(), firstOutage)
	if err != nil {
		t.Fatal(err)
	}
	firstFreeze := firstRun.Items[0].Corrections[0]

	exactRetryEngine, _ := newEngine(t)
	exactRetry, err := exactRetryEngine.Reconcile(context.Background(), firstOutage)
	if err != nil {
		t.Fatal(err)
	}
	if retryKey := exactRetry.Items[0].Corrections[0].IdempotencyKey; retryKey != firstFreeze.IdempotencyKey {
		t.Fatalf("exact outage retry key = %s, want %s", retryKey, firstFreeze.IdempotencyKey)
	}

	recovered := current
	recovered.NewGrantsFrozen = true
	recoveryRequest := requestAt(now.Add(time.Minute), []InternalSnapshot{recovered}, []ProviderResult{snapshot("sub-42", Active, "provider-r1", now.Add(time.Minute))})
	recoveryRequest.IdempotencyKey = "recovery-cycle-1"
	recoveryEngine, _ := newEngine(t)
	recoveryRun, err := recoveryEngine.Reconcile(context.Background(), recoveryRequest)
	if err != nil {
		t.Fatal(err)
	}
	if countAction(recoveryRun.Items[0].Corrections, UnfreezeNewGrants) != 1 {
		t.Fatalf("recovery corrections = %+v, want one unfreeze", recoveryRun.Items[0].Corrections)
	}

	secondOutage := requestAt(now.Add(2*time.Minute), []InternalSnapshot{current}, []ProviderResult{{ProviderSubscriptionID: "sub-42", FailureCode: "provider_timeout"}})
	secondOutage.IdempotencyKey = "outage-cycle-2"
	secondEngine, _ := newEngine(t)
	secondRun, err := secondEngine.Reconcile(context.Background(), secondOutage)
	if err != nil {
		t.Fatal(err)
	}
	secondFreeze := secondRun.Items[0].Corrections[0]
	if secondFreeze.IdempotencyKey == firstFreeze.IdempotencyKey {
		t.Fatalf("second outage reused already-applied freeze key %s", secondFreeze.IdempotencyKey)
	}
}

func countAction(corrections []Correction, action Action) int {
	count := 0
	for _, correction := range corrections {
		if correction.Action == action {
			count++
		}
	}
	return count
}
func hasCorrection(corrections []Correction, action Action, target string) bool {
	for _, correction := range corrections {
		if correction.Action == action && correction.TargetStatus == target {
			return true
		}
	}
	return false
}

func countRunAction(run Run, action Action) int {
	count := 0
	for _, item := range run.Items {
		count += countAction(item.Corrections, action)
	}
	return count
}

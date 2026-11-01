package reconciliation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Engine struct{ repository Repository }

func New(repository Repository) (*Engine, error) {
	if repository == nil {
		return nil, ErrRepositoryUnavailable
	}
	return &Engine{repository: repository}, nil
}

func (e *Engine) Reconcile(ctx context.Context, request Request) (Run, error) {
	request.IdempotencyKey = strings.ToLower(strings.TrimSpace(request.IdempotencyKey))
	request.Provider = strings.ToLower(strings.TrimSpace(request.Provider))
	request.Environment = strings.ToLower(strings.TrimSpace(request.Environment))
	request.ScopeFailureCode = strings.ToLower(strings.TrimSpace(request.ScopeFailureCode))
	request.StartedAt = request.StartedAt.UTC()
	request.CompletedAt = request.CompletedAt.UTC()
	request.Now = request.Now.UTC()
	if err := validateRequest(request); err != nil {
		return Run{}, err
	}

	results := groupProviderResults(request.ProviderResults)
	internal := append([]InternalSnapshot(nil), request.Internal...)
	sort.Slice(internal, func(i, j int) bool { return internal[i].SubscriptionID < internal[j].SubscriptionID })
	run := Run{IdempotencyKey: request.IdempotencyKey, Provider: request.Provider, Environment: request.Environment, StartedAt: request.StartedAt, CompletedAt: request.CompletedAt}
	seenInternal := make(map[string]struct{}, len(internal))
	if request.ScopeFailureCode != "" {
		scope := Item{Classification: "provider_failure", Result: Failed, FailureCode: request.ScopeFailureCode}
		scope.Corrections = []Correction{correction(request, InternalSnapshot{}, nil, FreezeNewGrants, 0, "")}
		run.Items = append(run.Items, scope)
		addCounts(&run, scope)
	}
	for _, current := range internal {
		seenInternal[current.ProviderSubscriptionID] = struct{}{}
		item := Item{}
		if request.ScopeFailureCode != "" {
			item = Item{SubscriptionID: current.SubscriptionID, ProviderSubscriptionID: current.ProviderSubscriptionID, Classification: "provider_failure", Result: Failed, InternalRevision: current.ProviderRevision, FailureCode: request.ScopeFailureCode}
		} else {
			item = reconcileOne(request, current, results[current.ProviderSubscriptionID])
		}
		run.Items = append(run.Items, item)
		addCounts(&run, item)
	}
	// Unknown provider subscriptions are evidence only. They never grant,
	// revoke, or mutate an unrelated local subscription.
	for providerID, grouped := range results {
		if request.ScopeFailureCode != "" {
			break
		}
		if _, exists := seenInternal[providerID]; exists {
			continue
		}
		selected, failure := selectProviderResult(grouped)
		item := Item{ProviderSubscriptionID: providerID, Classification: "unknown", Result: Mismatch, FailureCode: failure}
		if selected != nil {
			item.ProviderRevision = selected.Revision
			observed := selected.ObservedAt.UTC()
			item.ProviderObservedAt = &observed
		}
		run.Items = append(run.Items, item)
		addCounts(&run, item)
	}
	sort.SliceStable(run.Items, func(i, j int) bool {
		if run.Items[i].SubscriptionID != run.Items[j].SubscriptionID {
			return run.Items[i].SubscriptionID < run.Items[j].SubscriptionID
		}
		return run.Items[i].ProviderSubscriptionID < run.Items[j].ProviderSubscriptionID
	})
	switch {
	case run.FailedCount == len(run.Items) && len(run.Items) > 0:
		run.Result = "failed"
	case run.FailedCount > 0:
		run.Result = "partial"
	default:
		run.Result = "completed"
	}
	return e.repository.Apply(ctx, run)
}

func validateRequest(r Request) error {
	if r.IdempotencyKey == "" || r.Provider == "" || r.Environment == "" || r.StartedAt.IsZero() || r.CompletedAt.Before(r.StartedAt) || r.Now.IsZero() {
		return ErrInvalidRequest
	}
	seenID, seenProvider := map[int64]struct{}{}, map[string]struct{}{}
	for _, s := range r.Internal {
		if s.SubscriptionID <= 0 || strings.TrimSpace(s.ProviderSubscriptionID) == "" {
			return ErrInvalidRequest
		}
		if _, ok := seenID[s.SubscriptionID]; ok {
			return ErrInvalidRequest
		}
		seenID[s.SubscriptionID] = struct{}{}
		if _, ok := seenProvider[s.ProviderSubscriptionID]; ok {
			return ErrInvalidRequest
		}
		seenProvider[s.ProviderSubscriptionID] = struct{}{}
	}
	for _, result := range r.ProviderResults {
		if strings.TrimSpace(result.ProviderSubscriptionID) == "" || (result.Snapshot == nil) == (result.FailureCode == "") {
			return ErrInvalidRequest
		}
		if result.Snapshot != nil && (result.Snapshot.ProviderSubscriptionID != result.ProviderSubscriptionID || result.Snapshot.ObservedAt.IsZero() || result.Snapshot.Revision == "" || !validLifecycle(result.Snapshot.Lifecycle)) {
			return ErrInvalidRequest
		}
	}
	return nil
}

func validLifecycle(l Lifecycle) bool {
	switch l {
	case PendingPayment, Active, Grace, Renewal, Refund, Chargeback, Cancellation, Retry, Expiry:
		return true
	default:
		return false
	}
}

func groupProviderResults(results []ProviderResult) map[string][]ProviderResult {
	grouped := make(map[string][]ProviderResult)
	for _, result := range results {
		grouped[result.ProviderSubscriptionID] = append(grouped[result.ProviderSubscriptionID], result)
	}
	return grouped
}

func selectProviderResult(results []ProviderResult) (*ProviderSnapshot, string) {
	var selected *ProviderSnapshot
	failure := "missing_snapshot"
	conflict := false
	for _, result := range results {
		if result.Snapshot == nil {
			if selected == nil {
				failure = result.FailureCode
			}
			continue
		}
		candidate := result.Snapshot
		if selected != nil && candidate.ObservedAt.Equal(selected.ObservedAt) && candidate.Revision != selected.Revision {
			conflict = true
		}
		if selected == nil || candidate.ObservedAt.After(selected.ObservedAt) || (candidate.ObservedAt.Equal(selected.ObservedAt) && candidate.Revision > selected.Revision) {
			selected = candidate
		}
	}
	if conflict {
		return nil, "snapshot_order_conflict"
	}
	if selected != nil {
		failure = ""
	}
	return selected, failure
}

type expectedState struct {
	status            string
	supported, cancel bool
	start, end, grace *time.Time
}

func expected(snapshot ProviderSnapshot, now time.Time) expectedState {
	switch snapshot.Lifecycle {
	case PendingPayment:
		return expectedState{status: "incomplete"}
	case Active, Renewal:
		return expectedState{status: "active", supported: true, start: snapshot.CurrentPeriodStart, end: snapshot.CurrentPeriodEnd}
	case Grace, Retry:
		supported := snapshot.GraceEndsAt != nil && now.Before(snapshot.GraceEndsAt.UTC())
		status := "payment_failed"
		if supported {
			status = "past_due"
		}
		return expectedState{status: status, supported: supported, start: snapshot.CurrentPeriodStart, end: snapshot.CurrentPeriodEnd, grace: snapshot.GraceEndsAt}
	case Refund:
		return expectedState{status: "cancelled"}
	case Chargeback:
		return expectedState{status: "suspended"}
	case Cancellation:
		if snapshot.CurrentPeriodEnd != nil && now.Before(snapshot.CurrentPeriodEnd.UTC()) {
			return expectedState{status: "scheduled_to_cancel", supported: true, cancel: true, start: snapshot.CurrentPeriodStart, end: snapshot.CurrentPeriodEnd}
		}
		return expectedState{status: "expired", cancel: true, start: snapshot.CurrentPeriodStart, end: snapshot.CurrentPeriodEnd}
	case Expiry:
		return expectedState{status: "expired", start: snapshot.CurrentPeriodStart, end: snapshot.CurrentPeriodEnd}
	default:
		return expectedState{status: "suspended"}
	}
}

func reconcileOne(request Request, current InternalSnapshot, results []ProviderResult) Item {
	snapshot, failure := selectProviderResult(results)
	item := Item{SubscriptionID: current.SubscriptionID, ProviderSubscriptionID: current.ProviderSubscriptionID, InternalRevision: current.ProviderRevision}
	if snapshot == nil {
		item.Classification, item.Result, item.FailureCode = "provider_failure", Failed, failure
		if !current.NewGrantsFrozen {
			item.Corrections = []Correction{correction(request, current, nil, FreezeNewGrants, 0, "")}
		}
		return item
	}
	observed := snapshot.ObservedAt.UTC()
	item.ProviderObservedAt = &observed
	item.ProviderRevision = snapshot.Revision
	if observed.Before(current.ProviderObservedAt.UTC()) {
		item.Classification, item.Result = "stale", Matched
		return item
	}
	if observed.Equal(current.ProviderObservedAt.UTC()) && current.ProviderRevision != "" && snapshot.Revision != current.ProviderRevision {
		item.Classification, item.Result, item.FailureCode = "provider_failure", Failed, "snapshot_order_conflict"
		if !current.NewGrantsFrozen {
			item.Corrections = []Correction{correction(request, current, nil, FreezeNewGrants, 0, "")}
		}
		return item
	}
	item.Classification = string(snapshot.Lifecycle)
	want := expected(*snapshot, request.Now)
	if current.Status != want.status || current.CancelAtPeriodEnd != want.cancel || !equalTime(current.CurrentPeriodStart, want.start) || !equalTime(current.CurrentPeriodEnd, want.end) || !equalTime(current.GraceEndsAt, want.grace) {
		item.Corrections = append(item.Corrections, correction(request, current, snapshot, CorrectSubscription, 0, want.status))
	}
	if current.NewGrantsFrozen {
		item.Corrections = append(item.Corrections, correction(request, current, snapshot, UnfreezeNewGrants, 0, ""))
	}
	grants := append([]Grant(nil), current.Grants...)
	sort.Slice(grants, func(i, j int) bool { return grants[i].ID < grants[j].ID })
	for _, grant := range grants {
		if grant.RevokedAt != nil {
			continue
		}
		if !want.supported {
			item.Corrections = append(item.Corrections, correction(request, current, snapshot, RevokeGrant, grant.ID, ""))
		} else if grant.ReconciliationRevision != snapshot.Revision {
			item.Corrections = append(item.Corrections, correction(request, current, snapshot, VerifyGrant, grant.ID, ""))
		}
	}
	item.Result = Matched
	if len(item.Corrections) > 0 {
		item.Result = Mismatch
	}
	return item
}

func correction(request Request, current InternalSnapshot, snapshot *ProviderSnapshot, action Action, grantID int64, target string) Correction {
	revision := "failure"
	var start, end, grace *time.Time
	cancel := false
	if snapshot != nil {
		revision, start, end, grace = snapshot.Revision, snapshot.CurrentPeriodStart, snapshot.CurrentPeriodEnd, snapshot.GraceEndsAt
		cancel = snapshot.Lifecycle == Cancellation
	}
	// Stateful freeze transitions need a cycle identity: an exact run retry must
	// deduplicate, while outage -> recovery -> outage must be allowed to freeze
	// the same scope again. Provider-state corrections remain stable across
	// scheduled runs when their complete authoritative target is identical.
	cycle := ""
	if action == FreezeNewGrants || action == UnfreezeNewGrants {
		cycle = request.IdempotencyKey
	}
	keyMaterial := fmt.Sprintf(
		"%s\x00%s\x00%d\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%t\x00%s",
		request.Provider, request.Environment, current.SubscriptionID,
		revision+":"+string(action), grantID, target,
		timeIdentity(start), timeIdentity(end), timeIdentity(grace), cancel, cycle,
	)
	hash := sha256.Sum256([]byte(keyMaterial))
	return Correction{IdempotencyKey: hex.EncodeToString(hash[:]), Action: action, SubscriptionID: current.SubscriptionID, GrantID: grantID, TargetStatus: target, EffectiveAt: request.CompletedAt, ProviderRevision: revision, PeriodStart: start, PeriodEnd: end, GraceEndsAt: grace, CancelAtPeriodEnd: cancel}
}

func timeIdentity(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func equalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.UTC().Equal(b.UTC())
}
func addCounts(run *Run, item Item) {
	switch item.Result {
	case Matched:
		run.MatchedCount++
	case Mismatch:
		run.MismatchCount++
	case Failed:
		run.FailedCount++
	}
	run.CorrectionCount += len(item.Corrections)
}

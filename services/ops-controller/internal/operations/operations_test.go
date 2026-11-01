package operations

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestObserveOnlyRejectsEveryTypedOperation(t *testing.T) {
	for _, kind := range KnownKinds() {
		t.Run(string(kind), func(t *testing.T) {
			result, err := (ObserveOnly{}).Execute(context.Background(), Request{
				Kind: kind, Target: "api", Scope: "staging", IdempotencyKey: "incident-1", Timeout: time.Minute,
			})
			if !errors.Is(err, ErrObserveOnly) || result.Accepted {
				t.Fatalf("result=%+v err=%v, want denied observe-only operation", result, err)
			}
		})
	}
}

// TestKnownKindsReturnsDefensiveCompleteEnumeration pins the exact set of
// mutation shapes that exist. The count assertion is deliberate: adding a kind
// widens what an authorized caller can reach, so it must be a conscious edit
// here and not a silent consequence of a change elsewhere.
//
// retry_job was added 2026-09-05 for the Telegram runbook surface. It only
// requeues a failed ops_jobs row, so it needs no privilege beyond the database
// connection the controller already holds.
func TestKnownKindsReturnsDefensiveCompleteEnumeration(t *testing.T) {
	kinds := KnownKinds()
	want := []Kind{RestartService, Rollback, ScaleWorker, FlushSafeCache, CheckBackup, RetryJob}
	if len(kinds) != len(want) {
		t.Fatalf("known operation kind count = %d, want %d", len(kinds), len(want))
	}
	for i, k := range want {
		if kinds[i] != k {
			t.Fatalf("kind %d = %q, want %q", i, kinds[i], k)
		}
	}
	kinds[0] = "shell"
	if KnownKinds()[0] != RestartService {
		t.Fatal("KnownKinds exposed mutable policy state")
	}
}

func TestRequestRejectsUntypedOrUnboundedInput(t *testing.T) {
	valid := Request{Kind: RestartService, Target: "api", Scope: "staging", IdempotencyKey: "incident-1", Timeout: time.Minute}
	tests := map[string]Request{
		"unknown kind":     func() Request { r := valid; r.Kind = "shell"; return r }(),
		"missing target":   func() Request { r := valid; r.Target = ""; return r }(),
		"missing scope":    func() Request { r := valid; r.Scope = ""; return r }(),
		"missing key":      func() Request { r := valid; r.IdempotencyKey = ""; return r }(),
		"timeout zero":     func() Request { r := valid; r.Timeout = 0; return r }(),
		"timeout too long": func() Request { r := valid; r.Timeout = 16 * time.Minute; return r }(),
	}
	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			if err := request.Validate(); err == nil {
				t.Fatal("Validate returned nil error")
			}
		})
	}
}

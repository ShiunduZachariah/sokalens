package quota

import (
	"context"
	"testing"
	"time"
)

func TestTokenBucketRefillsAndAllowsBurst(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	current := &now
	advance := func(d time.Duration) { *current = current.Add(d) }

	bucket := NewTokenBucket(10, func() time.Time { return *current })
	if !bucket.TryTake() {
		t.Fatal("expected first token to be available")
	}
	// Drain the remaining 9.
	for i := 0; i < 9; i++ {
		if !bucket.TryTake() {
			t.Fatalf("token %d should be available", i+2)
		}
	}
	if bucket.TryTake() {
		t.Fatal("bucket should be empty after 10 tokens")
	}
	// After 6 seconds, ~1 token refilled (rate = 10/60 = 1/6 per second).
	advance(6 * time.Second)
	if !bucket.TryTake() {
		t.Fatal("bucket should refill one token after 6s")
	}
}

func TestTokenBucketWaitBlocksThenAllows(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	current := &now
	bucket := NewTokenBucket(60, func() time.Time { return *current })
	// Consume everything.
	for i := 0; i < 60; i++ {
		bucket.TryTake()
	}
	done := make(chan error, 1)
	go func() {
		done <- bucket.Wait(context.Background())
	}()
	select {
	case err := <-done:
		t.Fatalf("Wait returned early: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	// Advance time 2s (2 tokens) — Wait should return.
	*current = current.Add(2 * time.Second)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after refill")
	}
}

func TestTokenBucketObserveReseedsFromHeaders(t *testing.T) {
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	current := &now
	bucket := NewTokenBucket(75, func() time.Time { return *current })
	for i := 0; i < 60; i++ {
		bucket.TryTake()
	}
	// Provider says 75 limit, 40 remaining -> reseed to 40.
	bucket.Observe(75, 40)
	for i := 0; i < 40; i++ {
		if !bucket.TryTake() {
			t.Fatalf("token %d should be available after Observe reseed", i+1)
		}
	}
	if bucket.TryTake() {
		t.Fatal("bucket should be empty at 40 consumed after Observe")
	}
	if estimate := bucket.WaitEstimate(); estimate <= 0 {
		t.Fatalf("WaitEstimate = %s, want > 0 when bucket empty", estimate)
	}
}

func TestDailyBudgetReserveAndObserve(t *testing.T) {
	now := time.Date(2026, 8, 18, 23, 55, 0, 0, time.UTC)
	current := &now
	budget := NewDailyBudget(100, 20, 100, func() time.Time { return *current })
	for i := 0; i < 79; i++ {
		budget.Spend(1)
	}
	// Near midnight the paced reserve has reached its safety floor of 15.
	if budget.CanSpend(7) {
		t.Fatal("CanSpend(7) should be false when it would break the safety floor")
	}
	if !budget.CanSpend(6) {
		t.Fatal("CanSpend(6) should be true when it lands on the safety floor")
	}
	// Authoritative header says 75 limit, 50 remaining.
	budget.Observe(75, 50)
	snap := budget.Snapshot()
	if snap.Limit != 75 || snap.Remaining != 50 {
		t.Fatalf("snapshot = %+v, want limit 75 remaining 50", snap)
	}
}

func TestDailyBudgetRollsAtDayBoundary(t *testing.T) {
	now := time.Date(2026, 8, 18, 23, 50, 0, 0, time.UTC)
	current := &now
	budget := NewDailyBudget(100, 10, 5, func() time.Time { return *current })
	if budget.CanSpend(1) {
		t.Fatal("should be blocked near end of day with remaining 5 <= reserve 10")
	}
	*current = current.Add(20 * time.Minute) // now Aug 19 00:10 UTC
	snap := budget.Snapshot()
	if snap.Remaining != 100 || snap.Consumed != 0 {
		t.Fatalf("new day snapshot = %+v, want remaining 100 consumed 0", snap)
	}
}

func TestDailyBudgetIgnoresStalePreviousDayHeaderAtUTCReset(t *testing.T) {
	now := time.Date(2026, 8, 22, 0, 0, 6, 0, time.UTC)
	budget := NewDailyBudget(7500, 1500, 7498, func() time.Time { return now })

	budget.Observe(7500, 51)
	if got := budget.Snapshot().Remaining; got != 7498 {
		t.Fatalf("remaining after stale reset-edge header = %d, want 7498", got)
	}

	// Once the provider publishes its fresh-day balance, that header is again
	// authoritative and normal accounting resumes.
	budget.Observe(7500, 7495)
	if got := budget.Snapshot().Remaining; got != 7495 {
		t.Fatalf("remaining after fresh header = %d, want 7495", got)
	}
}

func TestDailyBudgetTrustsLowHeaderOutsideUTCResetGrace(t *testing.T) {
	now := time.Date(2026, 8, 22, 0, 20, 0, 0, time.UTC)
	budget := NewDailyBudget(7500, 1500, 7498, func() time.Time { return now })

	budget.Observe(7500, 51)
	if got := budget.Snapshot().Remaining; got != 51 {
		t.Fatalf("remaining outside reset grace = %d, want authoritative 51", got)
	}
}

func TestDailyBudgetPacesSpendAndReleasesQuotaTowardMidnight(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	current := &now
	budget := NewDailyBudget(7500, 1500, 1200, func() time.Time { return *current })
	if budget.CanSpend(1) {
		t.Fatal("midday pacing should retain quota proportional to time remaining")
	}

	*current = time.Date(2026, 8, 18, 23, 50, 0, 0, time.UTC)
	if !budget.CanSpend(1000) {
		t.Fatal("late-day reserve should release quota that would otherwise expire")
	}
	if got, want := budget.Snapshot().Reserve, 30; got != want {
		t.Fatalf("late-day reserve = %d, want %d", got, want)
	}
}

func TestDailyBudgetAllowsOnlyTenPercentAdvanceBurstAtDayStart(t *testing.T) {
	now := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	budget := NewDailyBudget(7500, 1500, 6751, func() time.Time { return now })
	// Confirm the balance before asserting on pacing.
	//
	// This line is new, and it narrows what the test claims. The pacing
	// reserve is absolute for a budget whose balance a provider header has
	// confirmed -- which is the steady state, since every successful request
	// carries one. It is deliberately *not* absolute for a budget that has
	// never been confirmed, because enforcing it against a number we merely
	// reconstructed is what deadlocked production on 2026-09-21: the governor
	// refused the only action that could have told it the number was wrong.
	// See unconfirmedProbeAllowance and deadlock_test.go.
	budget.Observe(7500, 6751)
	if !budget.CanSpend(1) {
		t.Fatal("the final request inside the ten-percent advance buffer should be allowed")
	}
	budget.Spend(1)
	if budget.CanSpend(1) {
		t.Fatal("spend beyond the advance buffer should wait for linear quota release")
	}
}

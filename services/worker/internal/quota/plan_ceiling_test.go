package quota

import (
	"testing"
	"time"
)

// The ceiling is the one promise in this package that is not a heuristic: at
// most PROVIDER_DAILY_LIMIT requests leave this process per provider day, and
// nothing the provider says can raise it.
//
// It is tested separately from pacing because everything else here is
// negotiable. The reserve decays, the forecast is an estimate, probes
// deliberately ignore all of it. Those exist so a wrong belief cannot stall
// ingestion, and each is a path by which a request gets sent after pacing
// refused it. This is the floor underneath all of them.

func TestDailyCeilingRefusesPastThePackageSize(t *testing.T) {
	now := time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC)
	budget := NewDailyBudget(7500, 600, 7500, func() time.Time { return now })

	sent := 0
	for budget.CanSpend(1) {
		budget.RecordIssued(1)
		budget.Spend(1)
		sent++
		if sent > 20000 {
			t.Fatal("the budget never refused; the daily ceiling is not enforced")
		}
		// Walk the clock so the decaying reserve cannot be what stops us: the
		// ceiling has to be the binding constraint, not pacing.
		now = now.Add(2 * time.Second)
	}
	if sent > 7500 {
		t.Fatalf("sent %d requests against a 7,500 package", sent)
	}
	snap := budget.Snapshot()
	if snap.Issued > snap.PlanLimit {
		t.Fatalf("issued %d exceeds plan limit %d", snap.Issued, snap.PlanLimit)
	}
}

// The failure this is really about. The account's headers may legitimately
// advertise a larger package -- during a plan change, or because the provider
// is wrong -- and the configured ceiling is a billing decision, not a guess to
// be corrected by whatever the last response claimed.
func TestProviderHeaderCannotRaiseTheCeiling(t *testing.T) {
	now := time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC)
	budget := NewDailyBudget(7500, 600, 7500, func() time.Time { return now })

	// The provider insists this account is on the 75,000 package.
	budget.Observe(75000, 74000)
	if snap := budget.Snapshot(); snap.PlanLimit != 7500 {
		t.Fatalf("plan limit = %d after a 75,000 header; want 7,500", snap.PlanLimit)
	}

	sent := 0
	for budget.CanSpend(1) {
		budget.RecordIssued(1)
		// Keep re-asserting the generous header on every response, which is
		// exactly what would happen in production.
		budget.Observe(75000, 74000-sent)
		sent++
		if sent > 20000 {
			t.Fatal("a provider header raised the ceiling; the package is unprotected")
		}
		now = now.Add(2 * time.Second)
	}
	if sent > 7500 {
		t.Fatalf("sent %d requests after the provider advertised 75,000", sent)
	}
}

// A header may lower it, though. If the provider says the package is smaller
// than we configured, the provider is the one being billed and it is right.
func TestProviderHeaderMayLowerTheCeiling(t *testing.T) {
	now := time.Date(2026, 10, 16, 1, 0, 0, 0, time.UTC)
	budget := NewDailyBudget(7500, 600, 7500, func() time.Time { return now })
	budget.Observe(100, 100)
	if snap := budget.Snapshot(); snap.PlanLimit != 100 {
		t.Fatalf("plan limit = %d after a 100 header; want 100", snap.PlanLimit)
	}
}

// Probing is the deliberate escape hatch from a wrong balance, and it must not
// become an escape hatch from the package. Once our own count says the day is
// spent, there is nothing left to be wrong about.
func TestDailyCeilingStopsProbingOnceThePackageIsGenuinelySpent(t *testing.T) {
	now := time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	// initialRemaining 0: our own request log says the whole package is gone.
	budget := NewDailyBudget(7500, 600, 0, clock)
	budget.Observe(7500, 0)

	endOfDay := now.Truncate(24 * time.Hour).Add(24 * time.Hour)
	for now.Before(endOfDay.Add(-time.Minute)) {
		if budget.CanSpend(1) {
			t.Fatal("a spent package still let a request through")
		}
		now = now.Add(30 * time.Second)
	}
}

// The restart case. A process that reset its count on boot could spend the
// package once per restart, and restarts are the one event this deployment has
// had plenty of -- the 2026-09-21 incident began with one.
func TestDailyCeilingSurvivesARestartMidDay(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	// Restarted having already sent 7,000 of the package, as reconstructed
	// from our own request log.
	budget := NewDailyBudget(7500, 600, 500, func() time.Time { return now })

	sent := 0
	for budget.CanSpend(1) {
		budget.RecordIssued(1)
		budget.Spend(1)
		sent++
		if sent > 5000 {
			t.Fatal("a restart reset the daily ceiling")
		}
		now = now.Add(2 * time.Second)
	}
	if sent > 500 {
		t.Fatalf("sent %d more requests with 500 of the package left", sent)
	}
}

// And it has to let go at midnight, or one exhausted day becomes every day.
func TestDailyCeilingResetsWithTheProviderDay(t *testing.T) {
	now := time.Date(2026, 10, 16, 23, 59, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	budget := NewDailyBudget(7500, 600, 0, clock)
	if budget.CanSpend(1) {
		t.Fatal("a spent package should refuse before midnight")
	}
	now = now.Add(2 * time.Minute)
	if !budget.CanSpend(1) {
		t.Fatal("the ceiling did not reset with the provider day")
	}
	if snap := budget.Snapshot(); snap.Issued != 0 {
		t.Fatalf("issued = %d after the day rolled; want 0", snap.Issued)
	}
}

// The plan-change case. Shrinking the configured package from 75,000 to 7,500
// makes every day that already spent more than 7,500 arrive at the
// constructor with a negative remaining. Read as "unknown, assume a fresh
// day", each of those would be handed a full new allowance -- so the first day
// of the smaller plan would spend the most.
func TestOverspentDayIsNotReadAsAFreshOne(t *testing.T) {
	now := time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)
	// Our request log says 10,000 requests have gone out today, against a
	// package that is now 7,500: remaining computes to -2,500.
	budget := NewDailyBudget(7500, 600, 7500-10000, func() time.Time { return now })
	if budget.CanSpend(1) {
		t.Fatal("a day that overspent the package was granted a fresh allowance")
	}
	if snap := budget.Snapshot(); snap.Issued < snap.PlanLimit {
		t.Fatalf("issued = %d against plan limit %d; an overspent day must read as spent",
			snap.Issued, snap.PlanLimit)
	}
}

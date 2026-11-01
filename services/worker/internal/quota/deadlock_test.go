package quota

import (
	"testing"
	"time"
)

// The 2026-09-21 production outage, as a test.
//
// A worker restart seeded the daily budget from our own request log, which
// over-counts. It believed 6,656 of 75,000 remained when the provider's own
// accounting said 23,491. The afternoon pacing reserve was about 22,000, so
// every request was refused -- and the only thing that corrects `remaining` is
// a provider response header, which requires a request. All provider traffic
// stopped, live match polling included, and could not resume for hours.
//
// The shape of the bug is what matters more than the numbers: a governor that
// refuses the action that would tell it whether refusing was correct.

func afternoon() time.Time { return time.Date(2026, 9, 21, 14, 30, 0, 0, time.UTC) }

func TestAnUnconfirmedBudgetCanAlwaysConfirmItself(t *testing.T) {
	// Seeded the way the outage seeded it: badly, and far below the pacing
	// reserve. The budget must still let something through, because the
	// alternative is never finding out.
	budget := NewDailyBudget(75_000, 1_500, 6_656, afternoon)

	if budget.CanSpend(1) == false {
		t.Fatal("an unconfirmed budget refused every request; this is the deadlock")
	}
}

func TestTheProbeAllowanceIsBounded(t *testing.T) {
	// An escape hatch that never closes is not an escape hatch. If the
	// estimate happens to be right, this must not become a way to spend
	// through the pacing reserve all day.
	budget := NewDailyBudget(75_000, 1_500, 6_656, afternoon)

	allowed := 0
	for i := 0; i < 100; i++ {
		if budget.CanSpend(1) {
			allowed++
		}
	}
	if allowed != unconfirmedProbeAllowance {
		t.Fatalf("allowed %d probes, want exactly %d", allowed, unconfirmedProbeAllowance)
	}
}

func TestConfirmationClosesTheHatch(t *testing.T) {
	// Once a header has spoken, `remaining` is measured rather than guessed,
	// and the pacing reserve is entitled to be obeyed again.
	budget := NewDailyBudget(75_000, 1_500, 6_656, afternoon)
	budget.Observe(75_000, 6_656) // the provider agrees: we really are low

	if budget.CanSpend(1) {
		t.Fatal("a confirmed, genuinely exhausted budget still allowed spending")
	}
}

func TestProbingNeverBreaksTheHardReserve(t *testing.T) {
	// The hatch exists to escape a *pacing* reserve built on a guess. The hard
	// reserve protects against real exhaustion and is not negotiable.
	budget := NewDailyBudget(75_000, 1_500, 1_500, afternoon)

	if budget.CanSpend(1) {
		t.Fatal("probing spent into the hard reserve")
	}
}

func TestAConfirmedHeaderCorrectsABadSeed(t *testing.T) {
	// The recovery path the outage actually needed: one successful call, and
	// the truth replaces the reconstruction.
	budget := NewDailyBudget(75_000, 1_500, 6_656, afternoon)
	budget.Observe(75_000, 23_491) // what the provider really had

	if !budget.CanSpend(1) {
		t.Fatal("budget still refused after the provider reported ample quota")
	}
	if got := budget.Snapshot().Remaining; got != 23_491 {
		t.Fatalf("remaining = %d after a header, want the provider's 23,491", got)
	}
}

func TestANewDayReopensTheHatch(t *testing.T) {
	// Each provider day starts as an assumption again, so the allowance has to
	// come back with it -- otherwise the first restart after midnight is
	// exposed to exactly the same deadlock.
	day := afternoon()
	budget := NewDailyBudget(75_000, 1_500, 6_656, func() time.Time { return day })
	budget.Observe(75_000, 6_656)
	if budget.CanSpend(1) {
		t.Fatal("precondition: budget should be confirmed and refusing")
	}

	day = day.Add(24 * time.Hour) // cross into the next provider day
	if !budget.CanSpend(1) {
		t.Fatal("a fresh provider day did not restore the probe allowance")
	}
}

// The 2026-09-22 outage, as a test. Same shape, one layer deeper.
//
// The probe allowance above was supposed to have prevented this, and did not,
// because it stops at the first confirmation. Shortly after the 00:00 UTC
// reset the provider returned the previous day's remaining -- its own header
// lag -- Observe accepted 33 of 75,000 and set confirmed, and that switched
// off the only mechanism that could have corrected it.
//
// Every provider call was refused for the next fifteen hours while the
// provider's own dashboard showed the day at 0% used. Live scores froze and
// 882 matches sat in 'upcoming' with kickoff long past. A person comparing the
// site to FlashScore noticed. Nothing in the system did.
//
// So: a confirmed budget that is refusing must still, eventually, check.
func TestConfirmedBudgetRecoversFromAWrongRemaining(t *testing.T) {
	now := time.Date(2026, 9, 22, 6, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	budget := NewDailyBudget(75000, 1500, 75000, clock)
	// The provider's post-reset header lag, accepted as fact.
	budget.Observe(75000, 33)

	if budget.CanSpend(1) {
		t.Fatal("precondition: a budget that believes 33 remain should refuse")
	}
	// Still refusing a moment later: one refusal is not a deadlock.
	now = now.Add(30 * time.Second)
	if budget.CanSpend(1) {
		t.Fatal("a budget should not probe the instant it starts refusing")
	}

	// After the probe interval it must test its own belief, confirmed or not.
	now = now.Add(probeInterval)
	if !budget.CanSpend(1) {
		t.Fatal("a confirmed budget refused for longer than the probe interval " +
			"never re-checked -- this is the fifteen-hour stall")
	}

	// And that probe is what lets the truth back in.
	budget.Observe(75000, 74879)
	if !budget.CanSpend(1) {
		t.Fatal("budget did not recover after the provider reported the real remaining")
	}
}

// Probing must not become unbounded traffic when the refusal is correct.
func TestProbingIsBoundedWhenTheBudgetIsGenuinelyExhausted(t *testing.T) {
	now := time.Date(2026, 9, 22, 6, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	// Seeded as having spent nothing, then told by the provider that nothing
	// is left. That combination is the case this probe exists for: our own
	// count and the provider's claim disagree, and only a request can settle
	// it. Seeding it as having already sent the whole package would be a
	// different case entirely -- not a disputed belief but a spent day -- and
	// the daily ceiling stops that one outright. See
	// TestDailyCeilingStopsProbingOnceThePackageIsGenuinelySpent.
	budget := NewDailyBudget(75000, 1500, 75000, clock)
	budget.Observe(75000, 0)

	// One UTC day, asked far more often than it can probe, and stopping before
	// midnight so the day's reset does not restore the budget mid-measurement.
	// The bound that matters is the interval: probing is paced, not per-call.
	endOfDay := now.Truncate(24 * time.Hour).Add(24 * time.Hour)
	for now.Before(endOfDay.Add(-time.Minute)) {
		budget.CanSpend(1)
		now = now.Add(30 * time.Second)
	}

	probes, _ := budget.ProbeState()
	maxPerDay := int((24 * time.Hour) / probeInterval)
	if probes > maxPerDay {
		t.Fatalf("probed %d times in a day; the %s interval allows at most %d",
			probes, probeInterval, maxPerDay)
	}
	if probes == 0 {
		t.Fatal("an exhausted budget still needs to find out when the quota returns")
	}
	// The cost of never being stuck should be negligible against the limit.
	if probes*100 > 75000 {
		t.Fatalf("probing cost %d requests against a 75,000 limit", probes)
	}
}

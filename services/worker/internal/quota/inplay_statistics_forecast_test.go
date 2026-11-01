package quota

import (
	"context"
	"testing"
	"time"
)

func planWithStatisticsPoll(remaining int, demand DemandSnapshot, enabled bool) Decision {
	cfg := DefaultPlannerConfig()
	cfg.Remaining = func() int { return remaining }
	cfg.Now = func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }
	cfg.InPlayStatisticsPollEnabled = enabled
	return NewPlanner(cfg, stubDemand{demand: demand}).Plan(context.Background())
}

// A job that is not running must not reserve budget. The poll defaults to off,
// and a forecast that held quota back for it would throttle backfill and
// enrichment for nothing.
func TestInPlayStatisticsForecastIsZeroWhileThePollIsDisabled(t *testing.T) {
	decision := planWithStatisticsPoll(70000, DemandSnapshot{Live: 200, LiveWatch: 220, Upcoming: 400}, false)
	if decision.ProjectedInPlayStats != 0 {
		t.Fatalf("projected %d requests for a disabled poll, want 0", decision.ProjectedInPlayStats)
	}
}

// The reason this forecast exists: unlike the live poll, this one is not O(1)
// in match count. Twelve hours of a 200-match afternoon at twenty fixtures per
// request is most of a 7,500 day, and if the planner cannot see it the poll
// spends from the same allowance as live scores with neither knowing.
//
// The absolute figures below are for the default config, which is the 7,500
// plan, where the statistics cadence is clamped to five minutes. The clamp is
// the whole reason the numbers are affordable at all: at the unclamped 60s
// they would be 7,200 requests for this afternoon, which is the entire
// package.
func TestInPlayStatisticsForecastScalesWithConcurrentMatches(t *testing.T) {
	quiet := planWithStatisticsPoll(70000, DemandSnapshot{Live: 10, LiveWatch: 12, Upcoming: 400}, true)
	busy := planWithStatisticsPoll(70000, DemandSnapshot{Live: 200, LiveWatch: 220, Upcoming: 400}, true)

	if quiet.ProjectedInPlayStats <= 0 {
		t.Fatalf("ten live matches projected %d requests, want more than zero", quiet.ProjectedInPlayStats)
	}
	// Ten matches is one request per poll; 200 is ten. The forecast must grow
	// with the match count rather than being a flat allowance.
	if busy.ProjectedInPlayStats != quiet.ProjectedInPlayStats*10 {
		t.Fatalf("busy projection %d, want 10x the quiet projection %d",
			busy.ProjectedInPlayStats, quiet.ProjectedInPlayStats)
	}
	// Noon, so twelve hours remain: at the 7,500 plan's five-minute cadence
	// that is 12 * 12 polls, ten requests each.
	if want := 12 * 12 * 10; busy.ProjectedInPlayStats != want {
		t.Fatalf("projected %d requests, want %d (12h at 5min, 200 matches in 20s)",
			busy.ProjectedInPlayStats, want)
	}
}

// Three live matches still cost a whole request. A ratio would round that to
// nothing and the forecast would read zero through every weekday evening.
func TestAPartialBatchStillCostsAWholeRequest(t *testing.T) {
	decision := planWithStatisticsPoll(70000, DemandSnapshot{Live: 3, LiveWatch: 5, Upcoming: 100}, true)
	if want := 12 * 12; decision.ProjectedInPlayStats != want {
		t.Fatalf("projected %d requests for 3 matches, want %d (one request per poll)",
			decision.ProjectedInPlayStats, want)
	}
}

// Forecast from what is actually in play, not from the calendar.
//
// The live poll is forecast from LiveWatch because it is what discovers
// kickoff and costs one request regardless of coverage. This poll reads the
// live set from our own database, so it only ever covers matches already known
// to be live; using the calendar would reserve budget for matches it will not
// poll.
func TestInPlayStatisticsForecastIgnoresTheCalendarWindow(t *testing.T) {
	decision := planWithStatisticsPoll(70000, DemandSnapshot{Live: 0, LiveWatch: 150, Upcoming: 400}, true)
	if decision.ProjectedInPlayStats != 0 {
		t.Fatalf("projected %d requests with nothing in play, want 0", decision.ProjectedInPlayStats)
	}
	if decision.ProjectedLive <= 0 {
		t.Fatal("the live poll should still be forecast from the calendar window")
	}
}

// And it has to actually count against the day, not just be reported. A poll
// whose cost is visible but not deducted is a poll the planner cannot protect
// live scores from.
func TestInPlayStatisticsForecastReducesTheDiscretionaryBudget(t *testing.T) {
	demand := DemandSnapshot{Live: 200, LiveWatch: 220, Prematch0_2h: 20, Upcoming: 400}
	off := planWithStatisticsPoll(70000, demand, false)
	on := planWithStatisticsPoll(70000, demand, true)
	if on.SafeDiscretionary >= off.SafeDiscretionary {
		t.Fatalf("discretionary budget did not fall when the poll was enabled: %d -> %d",
			off.SafeDiscretionary, on.SafeDiscretionary)
	}
}

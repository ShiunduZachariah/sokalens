package quota

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// simulateDay runs a synthetic 24-hour provider day: the planner re-decides
// every 5 minutes, live matches are polled at the planner's live interval, and
// discretionary backfill is consumed only when the decision allows it. It
// asserts the daily budget is never exhausted and backfill behaviour follows
// the pressure classification. This is the deterministic scheduler simulation
// described in the quota-planner spec (section 24).
func simulateDay(t *testing.T, dailyLimit int, liveMatches int, quiet bool) (backfillRequests int, exhausted bool) {
	t.Helper()
	cfg := DefaultPlannerConfig()
	cfg.DailyLimit = dailyLimit
	cfg.DailyReserve = dailyLimit / 5
	cfg.MinuteLimit = 300
	// Simulate a live-heavy or quiet day.
	demand := DemandSnapshot{Live: liveMatches}
	if !quiet {
		demand.Prematch0_2h = 30
		demand.Prematch2_6h = 80
		demand.Prematch6_24h = 200
		demand.Tomorrow = 150
		demand.Upcoming = 900
	}

	now := time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)
	budget := NewDailyBudget(dailyLimit, cfg.DailyReserve, dailyLimit, func() time.Time { return now })

	planner := NewPlanner(cfg, stubDemand{demand: demand})
	// Remaining comes from the simulated budget.
	planner.cfg.Remaining = func() int { return budget.Snapshot().Remaining }
	planner.cfg.Now = func() time.Time { return now }

	var backfill int
	for hour := 0; hour < 24; hour++ {
		for minute := 0; minute < 60; minute += 5 {
			decision := planner.Plan(context.Background())
			// Live polling cost: one request per live poll interval.
			interval := decision.LivePollInterval
			if interval <= 0 {
				interval = 30 * time.Second
			}
			polls := int((5 * time.Minute) / interval)
			if polls < 1 {
				polls = 1
			}
			if liveMatches > 0 {
				budget.Spend(polls)
			}
			// Prematch demand consumption.
			if !quiet {
				budget.Spend(8)
			}
			// Discretionary backfill: consume up to the safe budget when allowed.
			if decision.Allowed[P4] && !decision.BackfillPaused && decision.SafeDiscretionary > 0 {
				spend := decision.SafeDiscretionary / 48
				if spend > 0 {
					budget.Spend(spend)
					backfill += spend
				}
			}
			if budget.Snapshot().Remaining <= 0 {
				exhausted = true
			}
			now = now.Add(5 * time.Minute)
		}
	}
	return backfill, exhausted
}

func TestSimulationQuietDayAllowsBackfill(t *testing.T) {
	backfill, exhausted := simulateDay(t, 7500, 0, true)
	if exhausted {
		t.Fatal("quiet day exhausted the daily budget")
	}
	if backfill <= 0 {
		t.Fatalf("quiet day should consume discretionary quota for backfill, got %d", backfill)
	}
}

func TestSimulationHeavyMatchdayDoesNotExhaustAndLimitsBackfill(t *testing.T) {
	backfill, exhausted := simulateDay(t, 7500, 25, false)
	if exhausted {
		t.Fatal("heavy matchday exhausted the daily budget")
	}
	// On a heavy live day the planner must protect P0/P1: backfill should be
	// materially smaller than on a quiet day.
	quietBackfill, _ := simulateDay(t, 7500, 0, true)
	if backfill >= quietBackfill {
		t.Fatalf("heavy day backfill (%d) should be less than quiet day (%d)", backfill, quietBackfill)
	}
}

func TestSimulationScalesByConfiguration(t *testing.T) {
	for _, limit := range []int{7500, 75000, 150000} {
		t.Run(fmt.Sprintf("limit-%d", limit), func(t *testing.T) {
			backfill, exhausted := simulateDay(t, limit, 25, false)
			if exhausted {
				t.Fatalf("limit %d exhausted", limit)
			}
			if backfill < 0 {
				t.Fatalf("limit %d negative backfill", limit)
			}
			// Larger limits should yield at least as much discretionary work.
			if limit > 7500 && backfill == 0 {
				t.Fatalf("limit %d should allow some backfill", limit)
			}
		})
	}
}

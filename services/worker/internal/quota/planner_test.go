package quota

import (
	"context"
	"testing"
	"time"
)

type stubDemand struct {
	demand DemandSnapshot
	err    error
}

func (s stubDemand) Demand(context.Context) (DemandSnapshot, error) {
	return s.demand, s.err
}

func planWith(remaining int, demand DemandSnapshot) Decision {
	cfg := DefaultPlannerConfig()
	cfg.Remaining = func() int { return remaining }
	cfg.Now = func() time.Time { return time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC) }
	p := NewPlanner(cfg, stubDemand{demand: demand})
	return p.Plan(context.Background())
}

func TestPlannerHealthyDayAllowsBackfill(t *testing.T) {
	// Few fixtures, plenty of quota: P4/P5 allowed.
	decision := planWith(70000, DemandSnapshot{Live: 2, Prematch0_2h: 10, Prematch2_6h: 40, Prematch6_24h: 120, Tomorrow: 90, Upcoming: 400})
	if decision.Pressure != PressureHealthy {
		t.Fatalf("pressure = %v, want healthy", decision.Pressure)
	}
	if !decision.Allowed[P4] || !decision.Allowed[P5] {
		t.Fatalf("backfill/enrichment should be allowed: %+v", decision.Allowed)
	}
	if decision.BackfillPaused {
		t.Fatal("backfill should not be paused")
	}
	if decision.SafeDiscretionary <= 0 {
		t.Fatalf("safe discretionary = %d, want > 0", decision.SafeDiscretionary)
	}
}

func TestPlannerHeavyMatchdayReservesForLive(t *testing.T) {
	// Heavy Saturday: many live + imminent matches, limited quota. 1,200
	// remaining of a 7,500 package -- the reserve plus the emergency reserve
	// is most of what is left, so discretionary work has to stop.
	decision := planWith(1200, DemandSnapshot{Live: 40, Prematch0_2h: 60, Prematch2_6h: 150, Prematch6_24h: 300, Tomorrow: 200, Upcoming: 1200})
	if decision.Allowed[P4] || decision.Allowed[P5] {
		t.Fatalf("backfill should be paused under pressure: %+v", decision.Allowed)
	}
	if !decision.Allowed[P0] || !decision.Allowed[P1] {
		t.Fatalf("live/imminent must stay allowed: %+v", decision.Allowed)
	}
	if decision.BackfillPaused != true {
		t.Fatal("backfill must be paused under pressure")
	}
}

func TestPlannerChampionsLeagueEveningReservesMorning(t *testing.T) {
	// Morning quiet but big evening demand: projected demand must preserve quota.
	cfg := DefaultPlannerConfig()
	cfg.Remaining = func() int { return 60000 }
	cfg.Now = func() time.Time { return time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC) }
	p := NewPlanner(cfg, stubDemand{demand: DemandSnapshot{
		Live: 5, Prematch0_2h: 20, Prematch2_6h: 60, Prematch6_24h: 320, Tomorrow: 0, Upcoming: 900,
	}})
	decision := p.Plan(context.Background())
	if decision.SafeDiscretionary <= 0 {
		t.Fatalf("must retain discretionary capacity for evening demand, got %d", decision.SafeDiscretionary)
	}
	if decision.Pressure == PressureCritical {
		t.Fatal("should not be critical in the morning")
	}
}

func TestPlannerQuotaExhaustedProtectsLive(t *testing.T) {
	decision := planWith(700, DemandSnapshot{Live: 10, Prematch0_2h: 30, Prematch2_6h: 20, Prematch6_24h: 50, Tomorrow: 40, Upcoming: 200})
	if decision.Pressure != PressureCritical {
		t.Fatalf("pressure = %v, want critical", decision.Pressure)
	}
	if !decision.Allowed[P0] || !decision.Allowed[P1] {
		t.Fatalf("P0/P1 must be allowed when critical: %+v", decision.Allowed)
	}
	if decision.Allowed[P2] || decision.Allowed[P3] || decision.Allowed[P4] || decision.Allowed[P5] {
		t.Fatalf("P2+ must be paused when critical: %+v", decision.Allowed)
	}
}

func TestPlannerLargeLimitsScaleByConfig(t *testing.T) {
	for _, limit := range []int{7500, 75000, 150000} {
		cfg := DefaultPlannerConfig()
		cfg.DailyLimit = limit
		cfg.DailyReserve = limit / 5
		cfg.Remaining = func() int { return limit }
		cfg.Now = func() time.Time { return time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC) }
		p := NewPlanner(cfg, stubDemand{demand: DemandSnapshot{Live: 5, Prematch0_2h: 30, Prematch2_6h: 60, Prematch6_24h: 200, Tomorrow: 150, Upcoming: 800}})
		decision := p.Plan(context.Background())
		if decision.DailyLimit != limit {
			t.Fatalf("limit = %d, want %d", decision.DailyLimit, limit)
		}
		if decision.SafeDiscretionary < 0 {
			t.Fatalf("limit %d: safe discretionary negative", limit)
		}
		if decision.EmergencyReserve != roundF(float64(limit)*cfg.EmergencyReserveFraction) {
			t.Fatalf("limit %d: reserve = %d", limit, decision.EmergencyReserve)
		}
	}
}

func TestPlannerUsesObservedProLimitAndSlowsBatchedPolls(t *testing.T) {
	cfg := DefaultPlannerConfig()
	cfg.InPlayStatisticsPollEnabled = true
	cfg.Remaining = func() int { return 6200 }
	cfg.Limit = func() int { return 7500 }
	cfg.Now = func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }
	decision := NewPlanner(cfg, stubDemand{demand: DemandSnapshot{Live: 100, LiveWatch: 100}}).Plan(context.Background())
	if decision.DailyLimit != 7500 || decision.EmergencyReserve != 900 {
		t.Fatalf("provider limit was not applied: limit=%d reserve=%d", decision.DailyLimit, decision.EmergencyReserve)
	}
	if decision.InPlayStatisticsPollInterval != 5*time.Minute || decision.ProjectedInPlayStats != 720 {
		t.Fatalf("batched statistics projection is wrong: interval=%s requests=%d", decision.InPlayStatisticsPollInterval, decision.ProjectedInPlayStats)
	}
	if decision.LivePollInterval < time.Minute {
		t.Fatalf("live poll is too frequent for Pro: %s", decision.LivePollInterval)
	}
}

func TestPlannerLivePollIntervalAdaptsToPressure(t *testing.T) {
	healthy := planWith(70000, DemandSnapshot{Live: 5, Prematch0_2h: 10})
	if healthy.LivePollInterval != DefaultPlannerConfig().LivePollIntervalHealthy {
		t.Fatalf("healthy poll interval = %s", healthy.LivePollInterval)
	}
	critical := planWith(2000, DemandSnapshot{Live: 5, Prematch0_2h: 10})
	if critical.LivePollInterval != DefaultPlannerConfig().LivePollIntervalPressure {
		t.Fatalf("pressure poll interval = %s", critical.LivePollInterval)
	}
}

func TestPlannerDemandQueryFailureIsConservative(t *testing.T) {
	cfg := DefaultPlannerConfig()
	cfg.Remaining = func() int { return 100 }
	p := NewPlanner(cfg, stubDemand{err: context.DeadlineExceeded})
	decision := p.Plan(context.Background())
	if decision.Pressure != PressureCritical {
		t.Fatalf("with zero demand info and low quota, want critical, got %v", decision.Pressure)
	}
}

func TestDailyReserveIsAbsoluteNotAShareOfThePlan(t *testing.T) {
	// 3.3 is explicit: the safety reserve is absolute, not proportional, and
	// both ways of making it proportional have now been tried. Carried up as
	// 20% it idles a fifth of a large plan. Left at 1,500 it became 2% of
	// 75,000 -- not a floor protecting P0 on a hot day, just a rounding error.
	// And carried back down unchanged from 75,000, the 4,000 that was right
	// there reserves 53% of a 7,500 package. 900 is 12%.
	//
	// So the assertion is on the band, not on a number anybody has to
	// remember: large enough to cover live polling to midnight, small enough
	// to leave a working platform.
	cfg := DefaultPlannerConfig()
	if cfg.DailyReserve != 900 {
		t.Fatalf("daily reserve = %d, want 900 absolute", cfg.DailyReserve)
	}
	if cfg.DailyReserve >= cfg.DailyLimit/5 {
		t.Fatalf("reserve %d is a fifth of the limit; 3.3 wants it absolute", cfg.DailyReserve)
	}
	if cfg.DailyReserve <= cfg.DailyLimit/40 {
		t.Fatalf("reserve %d is under 2.5%% of the limit; too thin to protect P0", cfg.DailyReserve)
	}
}

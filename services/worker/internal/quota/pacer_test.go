package quota

import (
	"context"
	"testing"
	"time"
)

func at(hour, minute int) time.Time {
	return time.Date(2026, 9, 21, hour, minute, 0, 0, time.UTC)
}

func TestMinutesLeftNeverReachesZero(t *testing.T) {
	// The denominator. At 23:59:30 the honest answer rounds to zero, and a
	// division by it would either panic or report an unbounded rate in the one
	// minute of the day least able to absorb a burst.
	if got := minutesUntilUTCMidnight(time.Date(2026, 9, 21, 23, 59, 30, 0, time.UTC)); got < 1 {
		t.Fatalf("minutes left = %d at 23:59:30, want at least 1", got)
	}
	if got := minutesUntilUTCMidnight(at(0, 0)); got != 1440 {
		t.Fatalf("minutes left = %d at midnight, want a full day", got)
	}
	if got := minutesUntilUTCMidnight(at(12, 0)); got != 720 {
		t.Fatalf("minutes left = %d at noon, want 720", got)
	}
}

func TestBallastSpendsTheSurplusAcrossTheWholeDay(t *testing.T) {
	// The defect this exists for: 12,000 requests spent of a 75,000 plan, the
	// remaining 63,000 destroyed at reset. A surplus divided by the minutes
	// left is a rate that reaches zero at reset rather than whenever the
	// backlog happens to empty.
	decision := Decision{SafeDiscretionary: 50_000}
	// 50,000 - 4,000 reserve = 46,000 over 720 minutes.
	got := ballastPerMinute(decision, 4000, at(12, 0))
	if want := 46_000 / 720; got != want {
		t.Fatalf("ballast = %d/min at noon, want %d", got, want)
	}
	// Spending at that rate for the rest of the day lands on the surplus,
	// which is the property the whole design rests on.
	if projected := got * 720; projected < 45_000 || projected > 46_000 {
		t.Fatalf("pacing to reset spends %d, want to land near the 46,000 surplus", projected)
	}
}

func TestBallastRisesOnlyWhenSurplusIsReal(t *testing.T) {
	// Same surplus, later in the day, means fewer minutes to spend it in, so
	// the per-minute rate rises. That is the intended shape -- but it must be
	// a consequence of genuine unspent quota, not a deadline panic. With the
	// surplus mostly gone, the late rate must be low despite few minutes left.
	rich := ballastPerMinute(Decision{SafeDiscretionary: 50_000}, 4000, at(18, 0))
	poor := ballastPerMinute(Decision{SafeDiscretionary: 4_500}, 4000, at(18, 0))
	if !(rich > poor) {
		t.Fatalf("rate did not track surplus: rich=%d poor=%d", rich, poor)
	}
	if poor > 1 {
		t.Fatalf("near-exhausted quota paced at %d/min; a deadline must not manufacture surplus", poor)
	}
}

func TestBallastIsThrottledNotStoppedNearReset(t *testing.T) {
	// Section 3.4. European late kickoffs run to ~23:00 UTC inside the same
	// quota day, so the last two hours are when ballast and live work actually
	// compete.
	before := ballastPerMinute(Decision{SafeDiscretionary: 50_000}, 4000, at(21, 30))
	after := ballastPerMinute(Decision{SafeDiscretionary: 50_000}, 4000, at(22, 30))
	if after >= before {
		t.Fatalf("late-day throttle did not apply: 21:30=%d 22:30=%d", before, after)
	}
	// Throttled, not stopped: quota unspent at midnight is destroyed, so
	// refusing to spend after 22:00 would guarantee waste on exactly the
	// nights carrying the most surplus.
	if after == 0 {
		t.Fatal("ballast stopped entirely after 22:00; the surplus would be destroyed at reset")
	}
}

func TestPausedBackfillIsNeverPaced(t *testing.T) {
	// The planner has already judged this surplus unsafe. Pacing against it
	// would spend the reserve the pause exists to protect.
	decision := Decision{SafeDiscretionary: 50_000, BackfillPaused: true}
	if got := ballastPerMinute(decision, 4000, at(12, 0)); got != 0 {
		t.Fatalf("paced %d/min while backfill is paused", got)
	}
}

func TestBallastHoldsTheReserveBack(t *testing.T) {
	// A surplus smaller than the reserve is not a surplus.
	if got := ballastPerMinute(Decision{SafeDiscretionary: 3_000}, 4000, at(12, 0)); got != 0 {
		t.Fatalf("paced %d/min with the surplus entirely inside the reserve", got)
	}
}

func TestASmallSurplusIsNotTruncatedToNothing(t *testing.T) {
	// Integer division of a real-but-small surplus rounds to zero, which would
	// strand it for the rest of the day rather than spending it slowly.
	got := ballastPerMinute(Decision{SafeDiscretionary: 4_100}, 4000, at(0, 1))
	if got != 1 {
		t.Fatalf("ballast = %d, want 1: a surplus below one request a minute is still a surplus", got)
	}
}

func TestPlanPublishesARateAndNotJustAPermission(t *testing.T) {
	// The end-to-end property. A healthy planner on a quiet calendar must
	// produce a positive rate: "allowed" without a target is what left 63,000
	// requests unspent.
	cfg := DefaultPlannerConfig()
	cfg.Now = func() time.Time { return at(9, 0) }
	cfg.Remaining = func() int { return 70_000 }
	decision := NewPlanner(cfg, nil).Plan(context.Background())

	if decision.Pressure != PressureHealthy {
		t.Fatalf("pressure = %v on an empty calendar with 70,000 left, want healthy", decision.Pressure)
	}
	if decision.BallastPerMinute <= 0 {
		t.Fatal("healthy planner published no ballast rate; the surplus would expire unspent")
	}
	if decision.MinutesLeftToday != 900 {
		t.Fatalf("minutes left = %d at 09:00, want 900", decision.MinutesLeftToday)
	}
	// And the rate must be able to absorb the surplus rather than dribbling.
	if spend := decision.BallastPerMinute * decision.MinutesLeftToday; spend < 40_000 {
		t.Fatalf("pacing to reset would spend only %d of a %d surplus", spend, decision.SafeDiscretionary)
	}
}

func TestCriticalPressureStillPacesNothing(t *testing.T) {
	// The brake must survive the accelerator. Under critical pressure the
	// planner pauses backfill, and the pacer must respect that even though it
	// is reading the same struct that carries the surplus.
	cfg := DefaultPlannerConfig()
	cfg.Now = func() time.Time { return at(20, 0) }
	cfg.Remaining = func() int { return 500 }
	decision := NewPlanner(cfg, nil).Plan(context.Background())

	if decision.Pressure != PressureCritical {
		t.Fatalf("pressure = %v with 500 requests left, want critical", decision.Pressure)
	}
	if decision.BallastPerMinute != 0 {
		t.Fatalf("paced %d/min under critical pressure", decision.BallastPerMinute)
	}
	if !decision.Allowed[P0] {
		t.Fatal("P0 must stay allowed under critical pressure")
	}
}

func TestBallastNeverExceedsTheMinuteLimit(t *testing.T) {
	// The daily budget and the minute limit are independent constraints, and
	// the section 3.3 division respects only the first. Late in the day the
	// denominator is small, so a surviving surplus divides into an enormous
	// per-minute rate: at 23:58 a 46,000 surplus computes to thousands of
	// requests against a 450/min limit.
	//
	// This platform has already paid for that mistake once. A body-encoded
	// rate-limit error with no Retry-After triggered a sixty-second global
	// pause and held real throughput to 36 requests a minute.
	decision := Decision{SafeDiscretionary: 50_000, MinuteLimit: 450}
	got := ballastPerMinute(decision, 4000, time.Date(2026, 9, 21, 23, 58, 0, 0, time.UTC))
	if got > 450 {
		t.Fatalf("paced %d/min against a 450/min limit; every excess request returns an error", got)
	}
	// And it must leave room for live work, which is bursty and cannot be
	// rescheduled -- a match happens when it happens.
	if got >= int(float64(450)*0.5) {
		t.Fatalf("ballast took %d of 450/min, leaving too little headroom for live work", got)
	}
}

func TestTheMinuteCapDoesNotSuppressOrdinaryPacing(t *testing.T) {
	// The cap is a ceiling for the pathological case, not a second throttle.
	// A normal mid-day rate is far below it and must pass through untouched.
	capped := ballastPerMinute(Decision{SafeDiscretionary: 50_000, MinuteLimit: 450}, 4000, at(12, 0))
	uncapped := 46_000 / 720
	if capped != uncapped {
		t.Fatalf("mid-day rate = %d, want the uncapped %d", capped, uncapped)
	}
}

func TestAnUnsetMinuteLimitStillCaps(t *testing.T) {
	// A zero limit is a missing config value, not permission to flood.
	got := ballastPerMinute(Decision{SafeDiscretionary: 50_000}, 4000, time.Date(2026, 9, 21, 23, 58, 0, 0, time.UTC))
	if got > 450 {
		t.Fatalf("paced %d/min with no configured limit; want the default cap", got)
	}
}

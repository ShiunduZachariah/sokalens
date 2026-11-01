package scheduler

import (
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/quota"
)

func TestTheStaticThrottleNoLongerSetsTheOperatingPoint(t *testing.T) {
	// The defect in one number. Backfill was capped at three jobs a cycle
	// whatever the surplus, and that constant is what held the platform to
	// 12,000 requests a day against a 75,000 plan. With a real surplus the
	// pacer must ask for substantially more than three.
	decision := quota.Decision{BallastPerMinute: 63}
	opts := QuotaOptions{CycleInterval: 30 * time.Second, MaxBackfillPerCycle: 25}
	if got := pacedBackfillCount(decision, opts); got <= 3 {
		t.Fatalf("paced %d jobs/cycle against a 63/min allowance; the old constant was 3", got)
	}
}

func TestPacedCountRespectsItsSafetyCeiling(t *testing.T) {
	// The ceiling is the operator's bound and the pacer may not cross it,
	// however much surplus it thinks it has.
	decision := quota.Decision{BallastPerMinute: 10_000}
	opts := QuotaOptions{CycleInterval: 30 * time.Second, MaxBackfillPerCycle: 25}
	if got := pacedBackfillCount(decision, opts); got != 25 {
		t.Fatalf("paced %d jobs/cycle, want the ceiling of 25", got)
	}
}

func TestNoAllowanceMeansNoBackfill(t *testing.T) {
	// A zero rate is the pacer saying the surplus is not real -- under
	// pressure, or paused. It must not be rounded up to "at least one".
	opts := QuotaOptions{CycleInterval: 30 * time.Second, MaxBackfillPerCycle: 25}
	if got := pacedBackfillCount(quota.Decision{BallastPerMinute: 0}, opts); got != 0 {
		t.Fatalf("paced %d jobs/cycle with no allowance", got)
	}
}

func TestOperatorZeroCeilingStopsBackfill(t *testing.T) {
	decision := quota.Decision{BallastPerMinute: 100}
	if got := pacedBackfillCount(decision, QuotaOptions{MaxBackfillPerCycle: 0}); got != 0 {
		t.Fatalf("zero operator ceiling must pause backfill, got %d jobs", got)
	}
}

func TestASmallAllowanceStillBuysOneJob(t *testing.T) {
	// Truncating a real allowance to zero would stall backfill on a quiet day
	// -- which is the day with the most quota to lose.
	opts := QuotaOptions{CycleInterval: 30 * time.Second, MaxBackfillPerCycle: 25}
	if got := pacedBackfillCount(quota.Decision{BallastPerMinute: 1}, opts); got != 1 {
		t.Fatalf("paced %d jobs/cycle on a small but real allowance, want 1", got)
	}
}

func TestPacedCountScalesWithTheCycleLength(t *testing.T) {
	// The rate is per minute; the schedule is per cycle. A longer cycle has to
	// carry proportionally more work or the allowance is silently under-spent.
	decision := quota.Decision{BallastPerMinute: 60}
	short := pacedBackfillCount(decision, QuotaOptions{CycleInterval: 30 * time.Second, MaxBackfillPerCycle: 200})
	long := pacedBackfillCount(decision, QuotaOptions{CycleInterval: 60 * time.Second, MaxBackfillPerCycle: 200})
	if long != short*2 {
		t.Fatalf("30s cycle = %d, 60s cycle = %d; want the longer cycle to carry double", short, long)
	}
}

func TestSpendEstimateErrsTowardsUnderSpending(t *testing.T) {
	// The cost estimate is one-directional. Over-estimating wastes quota;
	// under-estimating overshoots the daily budget and takes live coverage
	// down with it. A league-season backfill is about one call, so the
	// constant must sit at or above that.
	if requestsPerBackfillJob < 1 {
		t.Fatalf("requestsPerBackfillJob = %d; the pacer would overshoot the daily budget", requestsPerBackfillJob)
	}
}

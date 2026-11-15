package scheduler

import (
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/marketing"
)

// The marketing routine is woken only when it is switched on. Off is the
// default, and off must plan nothing at all.
func TestMarketingJobsArePlannedOnlyWhenEnabled(t *testing.T) {
	now := time.Date(2026, 11, 11, 6, 3, 0, 0, time.UTC)
	count := func(enabled bool) map[string]string {
		planner := mustPlanner(t, PlanOptions{ProviderMode: ProviderAPIFootball, ProviderInterval: 4 * time.Hour, PredictionInterval: 15 * time.Minute, Marketing: enabled})
		found := map[string]string{}
		for _, job := range planner.Plan(now) {
			if job.Job.Queue == marketing.Queue {
				found[job.Job.JobKey] = job.Job.IdempotencyKey
			}
		}
		return found
	}
	if off := count(false); len(off) != 0 {
		t.Fatalf("marketing jobs planned while off: %v", off)
	}
	on := count(true)
	if on[marketing.TickJobKey] != "schedule:marketing:tick:20261111T060000Z" || on[marketing.LiveJobKey] != "schedule:marketing:live:20261111T060200Z" {
		t.Fatalf("marketing jobs = %v", on)
	}
}

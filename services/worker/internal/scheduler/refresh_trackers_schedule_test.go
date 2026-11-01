package scheduler

import (
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/predictions"
)

// The tracker was invisible for want of this entry, and nothing failed.
//
// `refresh-trackers` existed as an engine command and the API read was wired,
// but no planner entry ever enqueued it. So prediction_tracker_state stayed
// empty, every pick came back with no tracker, and the field read as dead
// rather than as absent-until-judged. A client cannot tell those apart, and an
// integrator would reasonably conclude the feature does not work.
//
// A missing schedule entry is exactly the class of defect that cannot fail
// loudly: the job is not late, it does not exist. The only guard is asserting
// the plan contains it.
func TestThePlanSchedulesTheTrackerRefresh(t *testing.T) {
	planner := mustPlanner(t, PlanOptions{
		ProviderMode:          ProviderAPIFootball,
		EnableRealAPIFootball: true,
		ProviderInterval:      time.Hour,
		PredictionInterval:    15 * time.Minute,
	})
	now := time.Date(2026, 10, 1, 13, 37, 0, 0, time.UTC)

	var found *ScheduledJob
	for _, scheduled := range planner.Plan(now) {
		if scheduled.Job.JobKey == predictions.RefreshTrackersJobKey {
			job := scheduled
			found = &job
			break
		}
	}
	if found == nil {
		t.Fatal("the plan does not schedule predictions.trackers.refresh; tracker state will never be written")
	}
	// On the prediction cadence, not the daily one: a tracker refreshed
	// nightly would be wrong for the whole match it is meant to describe.
	// `predictions` is that cadence's label, shared with generate and settle.
	if found.Schedule != "predictions" {
		t.Fatalf("schedule = %q, want the prediction cadence", found.Schedule)
	}
	if found.Bucket == "" {
		t.Fatal("tracker refresh has no cadence bucket")
	}
	daily := planner.Plan(now)
	for _, scheduled := range daily {
		if scheduled.Job.JobKey == predictions.SnapshotAccuracyJobKey &&
			scheduled.Bucket == found.Bucket {
			t.Fatal("tracker refresh shares the daily accuracy bucket; it must run on the prediction cadence")
		}
	}
	if found.Job.IdempotencyKey == "" {
		t.Fatal("tracker refresh has no idempotency key, so a slot can enqueue it twice")
	}
}

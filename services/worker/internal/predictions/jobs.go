package predictions

import (
	"strings"
	"time"
)

const (
	GenerateUpcomingJobKey = "predictions.generate.upcoming"
	SettleFinishedJobKey   = "predictions.settle.finished"
	SnapshotAccuracyJobKey = "predictions.snapshot.accuracy"
	AggregatePruneJobKey   = "predictions.aggregate-prune-results"
	// FinaliseSnapshotsJobKey freezes the full-time in-play snapshot. Separate
	// from the in-play generation job because the live poll drops a fixture the
	// moment it ends, so the live path never sees the final whistle.
	FinaliseSnapshotsJobKey = "predictions.snapshots.finalise"
	// RefreshTrackersJobKey re-judges pre-match picks on live fixtures.
	//
	// A job of its own rather than part of the in-play pass, because the two
	// are about different rows: the in-play pass generates and publishes
	// in-play predictions, while this one only re-reads PRE-MATCH picks and
	// restates how they are doing. A fixture whose in-play generation is
	// suppressed -- stale data, extra time, no pre-match rates -- still has
	// pre-match picks a reader is watching, and folding the two together would
	// freeze their trackers for the rest of the match.
	RefreshTrackersJobKey = "predictions.trackers.refresh"
	DefaultRefreshWindow  = 15 * time.Minute
)

func IdempotencyKey(action string, now time.Time, window time.Duration) string {
	if window <= 0 {
		window = DefaultRefreshWindow
	}
	if now.IsZero() {
		now = time.Now()
	}
	bucket := now.UTC().Truncate(window).Format("20060102T150405Z")
	return strings.Join([]string{"schedule", "predictions", action, bucket}, ":")
}

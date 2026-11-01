package predictions

import (
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/recommendations"
)

func TestPredictionFollowUpsAreStableAndDeduplicated(t *testing.T) {
	now := time.Date(2026, time.August, 5, 13, 0, 0, 0, time.UTC)
	source := jobs.Job{ID: 42, IdempotencyKey: "schedule:predictions:generate:20260805T130000Z"}
	first := GenerationFollowUps(source, now)
	second := GenerationFollowUps(source, now.Add(time.Minute))
	if len(first) != 1 || first[0].JobKey != recommendations.GenerateCandidatesJobKey || !first[0].DeduplicateCompleted {
		t.Fatalf("generation follow-up=%+v", first)
	}
	if first[0].IdempotencyKey != second[0].IdempotencyKey {
		t.Fatalf("follow-up keys=%q and %q", first[0].IdempotencyKey, second[0].IdempotencyKey)
	}

	settlement := SettlementFollowUps(jobs.Job{ID: 43, IdempotencyKey: "settlement-slot"}, now)
	if len(settlement) != 1 || settlement[0].JobKey != SnapshotAccuracyJobKey || !settlement[0].DeduplicateCompleted {
		t.Fatalf("settlement follow-up=%+v", settlement)
	}
}

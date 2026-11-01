package maintenance

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

func TestRawPredictionDrainRequiresExplicitGate(t *testing.T) {
	store := &fakeRawStore{}
	job := RawPredictionJob{Store: store}
	if err := job.Run(context.Background(), jobs.Job{}); err != nil {
		t.Fatal(err)
	}
	if store.called {
		t.Fatal("disabled raw drain called the destructive store")
	}
	job.Policy.EnableDestructiveRetention = true
	if err := job.Run(context.Background(), jobs.Job{Payload: []byte(`{"batch_size":500,"max_batches":50,"enable_destructive_retention":false,"raw_prediction_retention_hours":1}`)}); err != nil {
		t.Fatal(err)
	}
	if !store.called {
		t.Fatal("enabled raw drain did not call the store")
	}
	if store.policy.BatchSize != 500 || store.policy.MaxBatches != 50 || !store.policy.EnableDestructiveRetention || store.policy.RawPredictionRetentionHours != 24 {
		t.Fatalf("unsafe or missing throughput override: %+v", store.policy)
	}
	if err := job.Run(context.Background(), jobs.Job{Payload: []byte(`{"max_batches":101}`)}); err == nil {
		t.Fatal("unbounded drain override accepted")
	}
}

type fakeRawStore struct {
	called bool
	policy Payload
}

func (s *fakeRawStore) PruneRaw(_ context.Context, policy Payload, _ time.Time) (Metrics, error) {
	s.called = true
	s.policy = policy
	return Metrics{}, nil
}

func TestRawPredictionDrainPreservesReferencesAndUsesBoundedIndexWalk(t *testing.T) {
	for _, fragment := range []string{
		"prediction.fixture_id=fixture.id", "JOIN LATERAL", "ORDER BY prediction.generated_at,prediction.id", "LIMIT $6",
		"fixture.finished_at < $1", "prediction_result_rollup_ledger", "model_evaluation_facts",
		"saved_predictions", "entity_favorites", "acca_slip_legs", "slip.status='active'",
	} {
		if !strings.Contains(rawPredictionDrainSQL, fragment) {
			t.Fatalf("drain is missing safety requirement %q", fragment)
		}
	}
	for _, forbidden := range []string{"row_number()", "PARTITION BY", "count(*) FROM engine_predictions"} {
		if strings.Contains(rawPredictionDrainSQL, forbidden) {
			t.Fatalf("drain must avoid full-table work: %q", forbidden)
		}
	}
}

package maintenance

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

func TestIncompleteSettledPredictionsDoNotBlockIndependentRetention(t *testing.T) {
	body, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "raw prediction retention blocked:") {
		t.Fatal("incomplete settled rows must be preserved and reported without blocking unrelated safe cleanup")
	}
}

func TestJobRetentionPreservesProviderAuditReferences(t *testing.T) {
	for _, table := range []string{"provider_request_logs", "provider_raw_payloads"} {
		if !strings.Contains(pruneJobsSQL, table) {
			t.Fatalf("pruneJobsSQL must preserve jobs referenced by %s", table)
		}
	}
}

type fakeStore struct {
	policy  Payload
	metrics Metrics
	err     error
}

func (f *fakeStore) Prune(_ context.Context, policy Payload, _ time.Time) (Metrics, error) {
	f.policy = policy
	return f.metrics, f.err
}

func TestJobAppliesBoundedRetentionDefaults(t *testing.T) {
	store := &fakeStore{metrics: Metrics{Jobs: 2, RecommendationEvents: 3}}
	job := Job{Store: store, Now: func() time.Time { return time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC) }}
	if err := job.Run(context.Background(), jobs.Job{ID: 9, Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if store.policy.JobRetentionDays != 30 || store.policy.EventRetentionDays != 90 || store.policy.BatchSize != 1000 || store.policy.MaxBatches != 10 {
		t.Fatalf("policy=%+v", store.policy)
	}
}

func TestJobPreservesConfiguredRetentionSafetySwitchesForEmptyPayload(t *testing.T) {
	store := &fakeStore{}
	job := Job{Store: store, Policy: Payload{EnableDestructiveRetention: true, DryRun: true}}
	if err := job.Run(context.Background(), jobs.Job{Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !store.policy.EnableDestructiveRetention || !store.policy.DryRun {
		t.Fatalf("configured policy was lost: %+v", store.policy)
	}
}

type fakeExecer struct {
	calls   int
	queries []string
	args    [][]any
	rows    []int64
}

func (f *fakeExecer) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	f.calls++
	f.queries = append(f.queries, query)
	f.args = append(f.args, args)
	if len(f.rows) > 0 {
		value := f.rows[0]
		f.rows = f.rows[1:]
		return rowCount(value), nil
	}
	return rowCount(0), nil
}

type rowCount int64

func (r rowCount) LastInsertId() (int64, error) { return 0, errors.New("unsupported") }
func (r rowCount) RowsAffected() (int64, error) { return int64(r), nil }

func TestRepositoryPrunesAllSupportedStoresInBoundedBatches(t *testing.T) {
	db := &fakeExecer{}
	metrics, err := NewRepository(db).Prune(context.Background(), Payload{BatchSize: 100, MaxBatches: 2}, time.Now())
	if err != nil {
		t.Fatalf("Prune returned error: %v", err)
	}
	if db.calls != 7 || metrics.Total() != 0 {
		t.Fatalf("calls=%d metrics=%+v", db.calls, metrics)
	}
}

func TestRetentionDefaultsBoundPredictionEvidenceAndProviderData(t *testing.T) {
	// ProviderRequestRetentionDays went 30 -> 1 on 2026-09-22.
	// provider_request_logs held 9,055,224 rows at 5,655 MB of a 12 GB
	// database, 6,263,581 of them under five days old -- it grows from volume,
	// not from a long tail, so thirty days was simply thirty days of volume.
	// Nobody reads these unless something went wrong; when it has, the payload
	// field widens the window for that run rather than all year.
	policy := (Payload{}).withDefaults()
	if policy.RawPredictionRetentionHours != 24 || policy.ModelEvidenceRetentionDays != 365 ||
		policy.AccuracyRetentionDays != 365 || policy.SavedReferenceRetentionHours != 6 ||
		policy.StatisticsMatchesPerTeam != 10 || policy.ProviderPayloadRetentionDays != 7 ||
		policy.ProviderRequestRetentionDays != 1 || policy.BatchSize != 1000 || policy.MaxBatches != 10 {
		t.Fatalf("unexpected bounded defaults: %+v", policy)
	}
}

func TestPredictionRetentionRequiresConfirmedCompletionAndDurableRollup(t *testing.T) {
	for _, fragment := range []string{
		"fixture.finished_at < $1",
		"prediction_result_rollup_ledger",
		"model_evaluation_facts",
		"saved_predictions",
		"entity_favorites",
		"prediction_results",
		"SKIP LOCKED",
		"DELETE FROM engine_predictions",
		"RETURNING prediction.id",
	} {
		if !strings.Contains(pruneRawPredictionsSQL, fragment) {
			t.Fatalf("raw prediction retention query missing %q", fragment)
		}
	}
}

func TestRawRetentionAllowsUnresultedVersionsButBlocksIncompleteSettledVersions(t *testing.T) {
	if !strings.Contains(pruneRawPredictionsSQL, "NOT EXISTS (SELECT 1 FROM prediction_results") {
		t.Fatal("raw retention must allow old versions that were never settled")
	}
	for _, fragment := range []string{"JOIN prediction_results", "NOT EXISTS (SELECT 1 FROM prediction_result_rollup_ledger", "NOT EXISTS (SELECT 1 FROM model_evaluation_facts"} {
		if !strings.Contains(rawPredictionBlockerCountSQL, fragment) {
			t.Fatalf("raw blocker query missing %q", fragment)
		}
	}
}

func TestModelEvidenceRetentionKeepsProofUntilRawPredictionIsDeleted(t *testing.T) {
	if !strings.Contains(pruneModelEvidenceSQL, "NOT EXISTS (SELECT 1 FROM engine_predictions") {
		t.Fatal("model evidence retention must preserve proof for raw prediction cleanup")
	}
}

func TestPredictionFavoriteExpiryUsesPublicIDAndConfirmedCompletion(t *testing.T) {
	for _, fragment := range []string{"favorite.entity_type='prediction'", "favorite.entity_id=prediction.public_id::text", "fixture.finished_at<$1", "SKIP LOCKED"} {
		if !strings.Contains(pruneSavedPredictionFavoritesSQL, fragment) {
			t.Fatalf("prediction favorite expiry query missing %q", fragment)
		}
	}
}

func TestLegacyDuplicateCleanupNormalizesThenDeletesOnlyUnreferencedSupersededCopies(t *testing.T) {
	for _, fragment := range []string{"engine_prediction_content_hash", "features_used", "FOR UPDATE SKIP LOCKED", "UPDATE engine_predictions", "RETURNING prediction.id"} {
		if !strings.Contains(normalizePredictionHashesSQL, fragment) {
			t.Fatalf("hash normalization query missing %q", fragment)
		}
	}
	for _, fragment := range []string{
		"PARTITION BY prediction.fixture_id,prediction.market,prediction.content_hash",
		"prediction.superseded_at IS NOT NULL",
		"ranked.duplicate_rank > 1",
		"prediction_results", "model_evaluation_facts", "prediction_result_rollup_ledger", "saved_predictions",
		"favorite.entity_type='prediction'", "favorite.entity_id=prediction.public_id::text",
		"FOR UPDATE OF prediction SKIP LOCKED", "DELETE FROM engine_predictions", "RETURNING prediction.id",
	} {
		if !strings.Contains(prunePredictionDuplicatesSQL, fragment) {
			t.Fatalf("duplicate cleanup query missing %q", fragment)
		}
	}
	for _, fragment := range []string{"prediction_results", "model_evaluation_facts", "prediction_result_rollup_ledger", "saved_predictions", "entity_favorites"} {
		if !strings.Contains(predictionDuplicateBlockerCountSQL, fragment) {
			t.Fatalf("duplicate blocker report missing %q", fragment)
		}
	}
	db := &fakeExecer{}
	if _, err := NewRepository(db).Prune(context.Background(), Payload{EnableDestructiveRetention: true, BatchSize: 100, MaxBatches: 1}, time.Now()); err != nil {
		t.Fatalf("live bounded retention: %v", err)
	}
	for _, query := range db.queries {
		if strings.Contains(query, "UPDATE engine_predictions AS prediction SET content_hash") ||
			strings.Contains(query, "DELETE FROM engine_predictions") ||
			strings.Contains(query, "PARTITION BY prediction.fixture_id") {
			t.Fatalf("daily maintenance must not scan the complete prediction table: %q", query)
		}
	}
}

func TestStatisticsRetentionKeepsLatestTenPerTeamAcrossCompetitions(t *testing.T) {
	for _, fragment := range []string{
		"PARTITION BY stats.team_id",
		"ORDER BY fixture.kickoff_at DESC",
		"ranked.history_rank > $2",
		"SKIP LOCKED",
	} {
		if !strings.Contains(pruneFixtureStatisticsSQL, fragment) {
			t.Fatalf("fixture statistics retention query missing %q", fragment)
		}
	}
	if strings.Contains(pruneFixtureStatisticsSQL, "league_id =") {
		t.Fatal("fixture statistics retention must not restrict history to the target competition")
	}
}

func TestProviderCleanupRunsInForeignKeyDependencyOrder(t *testing.T) {
	db := &fakeExecer{}
	_, err := NewRepository(db).Prune(context.Background(), Payload{EnableDestructiveRetention: true, DryRun: false, BatchSize: 100, MaxBatches: 1}, time.Now())
	if err != nil {
		t.Fatalf("Prune returned error: %v", err)
	}
	joined := strings.Join(db.queries, "\n-- next --\n")
	positions := []int{
		strings.Index(joined, "UPDATE provider_entity_maps"),
		strings.Index(joined, "DELETE FROM provider_raw_payloads"),
		strings.Index(joined, "DELETE FROM provider_request_logs"),
		strings.Index(joined, "DELETE FROM jobs"),
	}
	for i, position := range positions {
		if position < 0 {
			t.Fatalf("provider cleanup stage %d missing from queries", i)
		}
		if i > 0 && position <= positions[i-1] {
			t.Fatalf("provider cleanup is not dependency ordered: positions=%v", positions)
		}
	}
	if !strings.Contains(pruneProviderPayloadsSQL, "normalization_status = 'normalized'") ||
		!strings.Contains(pruneProviderPayloadsSQL, "normalized_at IS NOT NULL") {
		t.Fatal("provider payload cleanup must preserve live or unnormalized payloads")
	}
}

// countingExecer reports a full batch for one query and an empty one for every
// other, so a test can count how many batches a single table was granted.
type countingExecer struct {
	target    string
	batchSize int64
	hits      int
	maxHits   int
}

func (c *countingExecer) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	if !strings.Contains(query, c.target) {
		return rowCount(0), nil
	}
	c.hits++
	// Stop claiming there is more to delete once the test has seen enough, so
	// a regression cannot spin here until the budget expires.
	if c.hits >= c.maxHits {
		return rowCount(0), nil
	}
	return rowCount(c.batchSize), nil
}

// provider_request_logs takes roughly 1.25 million rows a day. The shared
// allowance is ten batches, so before this table had its own the drain ran at
// 10,000 rows a day against that -- the retention cutoff was unreachable and
// the table grew regardless of what the policy said.
func TestProviderRequestLogDrainIsNotCappedByTheSharedBatchAllowance(t *testing.T) {
	db := &countingExecer{target: "DELETE FROM provider_request_logs", batchSize: 1000, maxHits: 500}
	// Destructive retention must be on, or Prune returns before it ever reaches
	// provider_request_logs -- which is itself why the table has 9 million rows.
	policy := Payload{EnableDestructiveRetention: true}.withDefaults()
	if _, err := NewRepository(db).Prune(context.Background(), policy, time.Now()); err != nil {
		t.Fatalf("Prune returned error: %v", err)
	}
	if db.hits <= policy.MaxBatches {
		t.Fatalf("provider_request_logs got %d batches, no more than the shared allowance of %d -- "+
			"the drain cannot keep up with arrivals", db.hits, policy.MaxBatches)
	}
}

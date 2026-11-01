package maintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// How long the whole prune may spend deleting before it stops and leaves the
// rest for the next run.
//
// Retention is maintenance, not an emergency. Ten minutes drains roughly two
// days of provider_request_logs arrivals, so the table converges within a few
// runs even from a large backlog, while a run can never hold its lease long
// enough to look like a stuck worker.
const providerPruneBudget = 10 * time.Minute

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}
type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type Repository struct{ db execer }

func NewRepository(db execer) Repository { return Repository{db: db} }

func (r Repository) Prune(ctx context.Context, policy Payload, now time.Time) (Metrics, error) {
	if r.db == nil {
		return Metrics{}, errors.New("maintenance DB is nil")
	}
	policy = policy.withDefaults()
	metrics := Metrics{}
	legacy := []struct {
		name   string
		cutoff time.Time
		query  string
		count  *int64
	}{
		{"notification_outbox", now.AddDate(0, 0, -policy.NotificationRetentionDays), pruneNotificationOutboxSQL, &metrics.NotificationOutbox},
		{"recommendation_events", now.AddDate(0, 0, -policy.EventRetentionDays), pruneRecommendationEventsSQL, &metrics.RecommendationEvents},
		{"recommendation_candidates", now, pruneRecommendationCandidatesSQL, &metrics.RecommendationCandidates},
		{"recommendation_dismissals", now, pruneRecommendationDismissalsSQL, &metrics.RecommendationDismissals},
		{"auth_tokens", now.AddDate(0, 0, -7), pruneAuthTokensSQL, &metrics.AuthTokens},
		{"user_sessions", now.AddDate(0, 0, -policy.SessionRetentionDays), pruneUserSessionsSQL, &metrics.UserSessions},
	}
	for _, task := range legacy {
		deleted, err := r.pruneBatches(ctx, task.query, task.cutoff, policy.BatchSize, policy.MaxBatches)
		if err != nil {
			return metrics, fmt.Errorf("prune %s: %w", task.name, err)
		}
		*task.count = deleted
	}
	if !policy.EnableDestructiveRetention {
		deleted, err := r.pruneBatches(ctx, pruneJobsSQL, now.AddDate(0, 0, -policy.JobRetentionDays), policy.BatchSize, policy.MaxBatches)
		metrics.Jobs = deleted
		if err != nil {
			return metrics, fmt.Errorf("prune jobs: %w", err)
		}
		return metrics, nil
	}
	if policy.DryRun {
		candidates, err := r.reportCandidates(ctx, policy, now)
		if err != nil {
			return metrics, fmt.Errorf("report retention candidates: %w", err)
		}
		candidates.NotificationOutbox, candidates.RecommendationEvents = metrics.NotificationOutbox, metrics.RecommendationEvents
		candidates.RecommendationCandidates, candidates.RecommendationDismissals = metrics.RecommendationCandidates, metrics.RecommendationDismissals
		candidates.AuthTokens, candidates.UserSessions = metrics.AuthTokens, metrics.UserSessions
		if err := r.reportDuplicateCandidates(ctx, &candidates); err != nil {
			return candidates, fmt.Errorf("report duplicate candidates: %w", err)
		}
		if err := r.recordAudit(ctx, "bounded_retention_dry_run", candidates); err != nil {
			return candidates, fmt.Errorf("audit retention dry-run: %w", err)
		}
		return candidates, nil
	}
	preTasks := []struct {
		name  string
		query string
		count *int64
	}{
		{"saved_predictions", pruneSavedPredictionsSQL, &metrics.SavedPredictions},
		{"saved_prediction_favorites", pruneSavedPredictionFavoritesSQL, &metrics.SavedPredictionFavorites},
		{"saved_fixtures", pruneSavedFixturesSQL, &metrics.SavedFixtures},
	}
	savedCutoff := now.Add(-time.Duration(policy.SavedReferenceRetentionHours) * time.Hour)
	for _, task := range preTasks {
		deleted, err := r.pruneBatches(ctx, task.query, savedCutoff, policy.BatchSize, policy.MaxBatches)
		if err != nil {
			return metrics, fmt.Errorf("prune %s: %w", task.name, err)
		}
		*task.count = deleted
	}
	// The large raw prediction table is drained by an independent cursor-backed
	// job. Global duplicate ranking and hash normalization are intentionally
	// absent from this daily job: they scanned the whole table every batch.
	tasks := []struct {
		name   string
		cutoff time.Time
		query  string
		count  *int64
		// Zero means "use policy.MaxBatches". Only a table whose arrival rate
		// outruns the shared allowance needs its own; see below.
		maxBatches int
	}{
		{"model_evidence", now.AddDate(0, 0, -policy.ModelEvidenceRetentionDays), pruneModelEvidenceSQL, &metrics.ModelEvidence, 0},
		{"accuracy_aggregates", now.AddDate(0, 0, -policy.AccuracyRetentionDays), pruneAccuracyAggregatesSQL, &metrics.AccuracyAggregates, 0},
		{"provider_payload_references", now.AddDate(0, 0, -policy.ProviderPayloadRetentionDays), pruneProviderPayloadReferencesSQL, &metrics.ProviderPayloadReferences, 0},
		{"provider_payloads", now.AddDate(0, 0, -policy.ProviderPayloadRetentionDays), pruneProviderPayloadsSQL, &metrics.ProviderPayloads, 0},
		// provider_request_logs is the one table the shared allowance cannot
		// hold. It takes roughly 1.25 million rows a day -- 6,263,581 of its
		// 9,055,224 rows were under five days old -- while BatchSize 1000 x
		// MaxBatches 10 deletes 10,000 per run, and the job runs once a day.
		//
		// So the 30-day-to-1-day retention change did not on its own drain
		// anything: it moved the cutoff while the drain stayed far below the
		// arrival rate. The cutoff is only half a retention policy; the other
		// half is being able to reach it.
		//
		// It stays here, behind EnableDestructiveRetention and after the two
		// payload tasks, because the order is a foreign-key dependency: the
		// prune skips any request log a retained raw payload still points at,
		// so payloads must be considered first or those rows survive the run.
		// That ordering is asserted by
		// TestProviderCleanupRunsInForeignKeyDependencyOrder.
		{"provider_request_logs", now.AddDate(0, 0, -policy.ProviderRequestRetentionDays), pruneProviderRequestLogsSQL, &metrics.ProviderRequestLogs, policy.ProviderRequestMaxBatches},
		{"jobs", now.AddDate(0, 0, -policy.JobRetentionDays), pruneJobsSQL, &metrics.Jobs, 0},
	}
	for _, task := range tasks {
		maxBatches := task.maxBatches
		if maxBatches <= 0 {
			maxBatches = policy.MaxBatches
		}
		deleted, err := r.pruneBatchesWithin(ctx, task.query, task.cutoff, policy.BatchSize, maxBatches, providerPruneBudget)
		if err != nil {
			return metrics, fmt.Errorf("prune %s: %w", task.name, err)
		}
		*task.count = deleted
	}
	deleted, err := r.pruneStatisticsBatches(ctx, now.Add(-24*time.Hour), policy.StatisticsMatchesPerTeam, policy.BatchSize, policy.MaxBatches)
	if err != nil {
		return metrics, fmt.Errorf("prune fixture_statistics: %w", err)
	}
	metrics.FixtureStatistics = deleted
	if err := r.recordAudit(ctx, "bounded_retention", metrics); err != nil {
		return metrics, fmt.Errorf("audit retention cleanup: %w", err)
	}
	return metrics, nil
}

func (r Repository) recordAudit(ctx context.Context, operation string, metrics Metrics) error {
	details, err := json.Marshal(metrics)
	if err != nil {
		return err
	}
	examined := metrics.Total() + metrics.RawPredictionsBlocked + metrics.PredictionDuplicatesBlocked
	changed := metrics.Total()
	if operation == "bounded_retention_dry_run" {
		changed = 0
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO data_maintenance_audit (operation, criteria, rows_examined, rows_changed, details) VALUES ($1, 'bounded worker retention policy', $2, $3, $4::jsonb)`, operation, examined, changed, string(details))
	return err
}

func (r Repository) rawPredictionBlockers(ctx context.Context, cutoff time.Time) (int64, error) {
	db, ok := r.db.(queryRower)
	if !ok {
		return 0, nil
	}
	var blocked int64
	err := db.QueryRowContext(ctx, rawPredictionBlockerCountSQL, cutoff.UTC()).Scan(&blocked)
	return blocked, err
}

func (r Repository) predictionDuplicateBlockers(ctx context.Context) (int64, error) {
	db, ok := r.db.(queryRower)
	if !ok {
		return 0, nil
	}
	var blocked int64
	err := db.QueryRowContext(ctx, predictionDuplicateBlockerCountSQL).Scan(&blocked)
	return blocked, err
}

func (r Repository) reportDuplicateCandidates(ctx context.Context, metrics *Metrics) error {
	db, ok := r.db.(queryRower)
	if !ok {
		return errors.New("maintenance DB does not support duplicate reporting")
	}
	return db.QueryRowContext(ctx, predictionDuplicateCandidateCountsSQL).Scan(
		&metrics.PredictionHashesNormalized,
		&metrics.PredictionDuplicatesDeleted,
		&metrics.PredictionDuplicatesBlocked,
	)
}

func (r Repository) reportCandidates(ctx context.Context, policy Payload, now time.Time) (Metrics, error) {
	db, ok := r.db.(queryRower)
	if !ok {
		return Metrics{}, errors.New("maintenance DB does not support candidate reporting")
	}
	var m Metrics
	err := db.QueryRowContext(ctx, retentionCandidateCountsSQL,
		now.Add(-time.Duration(policy.SavedReferenceRetentionHours)*time.Hour), now.Add(-time.Duration(policy.RawPredictionRetentionHours)*time.Hour),
		now.AddDate(0, 0, -policy.ModelEvidenceRetentionDays), now.AddDate(0, 0, -policy.AccuracyRetentionDays),
		now.Add(-24*time.Hour), policy.StatisticsMatchesPerTeam, now.AddDate(0, 0, -policy.ProviderPayloadRetentionDays),
		now.AddDate(0, 0, -policy.ProviderRequestRetentionDays)).Scan(
		&m.SavedPredictions, &m.SavedPredictionFavorites, &m.SavedFixtures, &m.RawPredictions, &m.RawPredictionsBlocked, &m.ModelEvidence, &m.AccuracyAggregates,
		&m.FixtureStatistics, &m.ProviderPayloadReferences, &m.ProviderPayloads, &m.ProviderRequestLogs)
	return m, err
}

func (r Repository) pruneBatches(ctx context.Context, query string, cutoff time.Time, batchSize, maxBatches int) (int64, error) {
	return r.pruneBatchesWithin(ctx, query, cutoff, batchSize, maxBatches, 0)
}

// pruneBatchesWithin deletes in batches until the table is drained, the batch
// allowance runs out, or the wall-clock budget expires.
//
// The budget exists because raising the allowance high enough to keep up with
// provider_request_logs -- thousands of batches -- also makes a single run
// unbounded in time if the table is far behind. A run that drains what it can
// inside a fixed window and leaves the rest for tomorrow is self-correcting; a
// run that holds its lease for hours is an outage waiting for a bad day.
//
// A budget of zero means no time limit, which is right for every other table:
// they are bounded by their allowance long before wall-clock matters.
func (r Repository) pruneBatchesWithin(ctx context.Context, query string, cutoff time.Time, batchSize, maxBatches int, budget time.Duration) (int64, error) {
	var total int64
	deadline := time.Time{}
	if budget > 0 {
		deadline = time.Now().Add(budget)
	}
	for batch := 0; batch < maxBatches; batch++ {
		// Checked before the batch, not after, so the budget bounds when work
		// starts rather than only when it finishes.
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			break
		}
		result, err := r.db.ExecContext(ctx, query, cutoff.UTC(), batchSize)
		if err != nil {
			return total, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += rows
		if rows < int64(batchSize) {
			break
		}
	}
	return total, nil
}

func (r Repository) pruneLimitBatches(ctx context.Context, query string, batchSize, maxBatches int) (int64, error) {
	var total int64
	for batch := 0; batch < maxBatches; batch++ {
		result, err := r.db.ExecContext(ctx, query, batchSize)
		if err != nil {
			return total, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += rows
		if rows < int64(batchSize) {
			break
		}
	}
	return total, nil
}

func (r Repository) pruneStatisticsBatches(ctx context.Context, cutoff time.Time, keepCount, batchSize, maxBatches int) (int64, error) {
	var total int64
	for batch := 0; batch < maxBatches; batch++ {
		result, err := r.db.ExecContext(ctx, pruneFixtureStatisticsSQL, cutoff.UTC(), keepCount, batchSize)
		if err != nil {
			return total, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += rows
		if rows < int64(batchSize) {
			break
		}
	}
	return total, nil
}

const pruneJobsSQL = `WITH candidates AS (SELECT job.id FROM jobs AS job WHERE job.status IN ('completed','failed') AND COALESCE(job.completed_at,job.updated_at)<$1 AND NOT EXISTS (SELECT 1 FROM provider_request_logs request WHERE request.job_id=job.id) AND NOT EXISTS (SELECT 1 FROM provider_raw_payloads payload WHERE payload.job_id=job.id) ORDER BY job.id LIMIT $2 FOR UPDATE SKIP LOCKED) DELETE FROM jobs job USING candidates WHERE job.id=candidates.id RETURNING job.id`
const pruneSavedPredictionsSQL = `WITH candidates AS (SELECT saved.id FROM saved_predictions saved JOIN engine_predictions prediction ON prediction.id=saved.prediction_id JOIN fixtures fixture ON fixture.id=prediction.fixture_id WHERE fixture.finished_at IS NOT NULL AND fixture.finished_at<$1 ORDER BY saved.id LIMIT $2 FOR UPDATE OF saved SKIP LOCKED) DELETE FROM saved_predictions saved USING candidates WHERE saved.id=candidates.id RETURNING saved.id`
const pruneSavedPredictionFavoritesSQL = `WITH candidates AS (SELECT favorite.ctid FROM entity_favorites favorite JOIN engine_predictions prediction ON favorite.entity_type='prediction' AND favorite.entity_id=prediction.public_id::text JOIN fixtures fixture ON fixture.id=prediction.fixture_id WHERE fixture.finished_at IS NOT NULL AND fixture.finished_at<$1 LIMIT $2 FOR UPDATE OF favorite SKIP LOCKED) DELETE FROM entity_favorites favorite USING candidates WHERE favorite.ctid=candidates.ctid RETURNING favorite.ctid`
const pruneSavedFixturesSQL = `WITH candidates AS (SELECT favorite.ctid FROM entity_favorites favorite JOIN fixtures fixture ON favorite.entity_type='match' AND favorite.entity_id=fixture.id::text WHERE fixture.finished_at IS NOT NULL AND fixture.finished_at<$1 LIMIT $2 FOR UPDATE OF favorite SKIP LOCKED) DELETE FROM entity_favorites favorite USING candidates WHERE favorite.ctid=candidates.ctid RETURNING favorite.ctid`
const normalizePredictionHashesSQL = `WITH candidates AS (SELECT prediction.id FROM engine_predictions prediction WHERE prediction.content_hash IS DISTINCT FROM engine_prediction_content_hash(prediction.fixture_id,prediction.market,prediction.prediction,prediction.probability,prediction.confidence,prediction.confidence_tier,prediction.is_best_bet,prediction.lifecycle_stage,prediction.data_completeness,prediction.features_used,prediction.explanation,prediction.gate_tier,prediction.horizon_eligible,prediction.embargo_active,prediction.model_version) ORDER BY prediction.id LIMIT $1 FOR UPDATE SKIP LOCKED) UPDATE engine_predictions AS prediction SET content_hash=engine_prediction_content_hash(prediction.fixture_id,prediction.market,prediction.prediction,prediction.probability,prediction.confidence,prediction.confidence_tier,prediction.is_best_bet,prediction.lifecycle_stage,prediction.data_completeness,prediction.features_used,prediction.explanation,prediction.gate_tier,prediction.horizon_eligible,prediction.embargo_active,prediction.model_version),updated_at=CURRENT_TIMESTAMP FROM candidates WHERE prediction.id=candidates.id RETURNING prediction.id`
const prunePredictionDuplicatesSQL = `WITH ranked AS MATERIALIZED (SELECT prediction.id,row_number() OVER (PARTITION BY prediction.fixture_id,prediction.market,prediction.content_hash ORDER BY (prediction.superseded_at IS NULL) DESC,prediction.generated_at DESC,prediction.created_at DESC,prediction.id DESC) AS duplicate_rank FROM engine_predictions prediction), candidates AS (SELECT prediction.id FROM engine_predictions prediction JOIN ranked ON ranked.id=prediction.id WHERE prediction.superseded_at IS NOT NULL AND ranked.duplicate_rank > 1 AND NOT EXISTS(SELECT 1 FROM prediction_results result WHERE result.prediction_id=prediction.id) AND NOT EXISTS(SELECT 1 FROM model_evaluation_facts evidence WHERE evidence.prediction_id=prediction.id) AND NOT EXISTS(SELECT 1 FROM prediction_result_rollup_ledger ledger WHERE ledger.prediction_id=prediction.id) AND NOT EXISTS(SELECT 1 FROM saved_predictions saved WHERE saved.prediction_id=prediction.id) AND NOT EXISTS(SELECT 1 FROM entity_favorites favorite WHERE favorite.entity_type='prediction' AND favorite.entity_id=prediction.public_id::text) ORDER BY prediction.generated_at,prediction.id LIMIT $1 FOR UPDATE OF prediction SKIP LOCKED) DELETE FROM engine_predictions prediction USING candidates WHERE prediction.id=candidates.id RETURNING prediction.id`
const pruneRawPredictionsSQL = `WITH candidates AS (SELECT prediction.id FROM engine_predictions prediction JOIN fixtures fixture ON fixture.id=prediction.fixture_id WHERE fixture.finished_at IS NOT NULL AND fixture.finished_at < $1 AND (NOT EXISTS (SELECT 1 FROM prediction_results result WHERE result.prediction_id=prediction.id) OR (EXISTS (SELECT 1 FROM prediction_result_rollup_ledger ledger WHERE ledger.prediction_id=prediction.id) AND EXISTS (SELECT 1 FROM model_evaluation_facts evidence WHERE evidence.prediction_id=prediction.id))) AND NOT EXISTS (SELECT 1 FROM saved_predictions saved WHERE saved.prediction_id=prediction.id) AND NOT EXISTS (SELECT 1 FROM entity_favorites favorite WHERE favorite.entity_type='prediction' AND favorite.entity_id=prediction.public_id::text) ORDER BY prediction.generated_at,prediction.id LIMIT $2 FOR UPDATE OF prediction SKIP LOCKED) DELETE FROM engine_predictions AS prediction USING candidates WHERE prediction.id=candidates.id RETURNING prediction.id`
const pruneModelEvidenceSQL = `WITH candidates AS (SELECT evidence.prediction_id FROM model_evaluation_facts evidence WHERE evidence.settled_at<$1 AND NOT EXISTS (SELECT 1 FROM engine_predictions prediction WHERE prediction.id=evidence.prediction_id) ORDER BY evidence.settled_at,evidence.prediction_id LIMIT $2 FOR UPDATE SKIP LOCKED) DELETE FROM model_evaluation_facts evidence USING candidates WHERE evidence.prediction_id=candidates.prediction_id RETURNING evidence.prediction_id`
const pruneAccuracyAggregatesSQL = `WITH candidates AS (SELECT aggregate.ctid FROM prediction_accuracy_daily aggregate WHERE aggregate.aggregate_date<$1::date ORDER BY aggregate.aggregate_date LIMIT $2 FOR UPDATE SKIP LOCKED) DELETE FROM prediction_accuracy_daily aggregate USING candidates WHERE aggregate.ctid=candidates.ctid RETURNING aggregate.ctid`
const pruneFixtureStatisticsSQL = `WITH ranked AS MATERIALIZED (SELECT stats.fixture_id,stats.team_id,row_number() OVER (PARTITION BY stats.team_id ORDER BY fixture.kickoff_at DESC,stats.fixture_id DESC) AS history_rank FROM fixture_statistics stats JOIN fixtures fixture ON fixture.id=stats.fixture_id WHERE fixture.finished_at IS NOT NULL AND lower(fixture.status) IN ('ft','aet','pen','finished')), candidates AS (SELECT stats.ctid FROM fixture_statistics stats JOIN ranked ON ranked.fixture_id=stats.fixture_id AND ranked.team_id=stats.team_id JOIN fixtures fixture ON fixture.id=stats.fixture_id WHERE ranked.history_rank > $2 AND fixture.finished_at<$1 LIMIT $3 FOR UPDATE OF stats SKIP LOCKED) DELETE FROM fixture_statistics stats USING candidates WHERE stats.ctid=candidates.ctid RETURNING stats.ctid`
const pruneProviderPayloadReferencesSQL = `WITH candidates AS (SELECT mapping.ctid FROM provider_entity_maps mapping JOIN provider_raw_payloads payload ON payload.id=mapping.source_payload_id WHERE payload.normalization_status='normalized' AND payload.normalized_at IS NOT NULL AND payload.fetched_at<$1 LIMIT $2 FOR UPDATE OF mapping SKIP LOCKED) UPDATE provider_entity_maps AS mapping SET source_payload_id=NULL FROM candidates WHERE mapping.ctid=candidates.ctid`
const pruneProviderPayloadsSQL = `WITH candidates AS (SELECT payload.id FROM provider_raw_payloads payload WHERE payload.normalization_status = 'normalized' AND payload.normalized_at IS NOT NULL AND payload.fetched_at<$1 AND NOT EXISTS (SELECT 1 FROM provider_entity_maps mapping WHERE mapping.source_payload_id=payload.id) ORDER BY payload.id LIMIT $2 FOR UPDATE SKIP LOCKED) DELETE FROM provider_raw_payloads payload USING candidates WHERE payload.id=candidates.id RETURNING payload.id`
const pruneProviderRequestLogsSQL = `WITH candidates AS (SELECT request.id FROM provider_request_logs request WHERE request.requested_at<$1 AND NOT EXISTS (SELECT 1 FROM provider_raw_payloads payload WHERE payload.request_log_id=request.id) ORDER BY request.id LIMIT $2 FOR UPDATE SKIP LOCKED) DELETE FROM provider_request_logs request USING candidates WHERE request.id=candidates.id RETURNING request.id`

const rawPredictionBlockerCountSQL = `SELECT count(*) FROM engine_predictions prediction JOIN fixtures fixture ON fixture.id=prediction.fixture_id JOIN prediction_results result ON result.prediction_id=prediction.id WHERE fixture.finished_at<$1 AND (NOT EXISTS (SELECT 1 FROM prediction_result_rollup_ledger ledger WHERE ledger.prediction_id=prediction.id) OR NOT EXISTS (SELECT 1 FROM model_evaluation_facts evidence WHERE evidence.prediction_id=prediction.id))`
const predictionDuplicateBlockerCountSQL = `WITH ranked AS MATERIALIZED (SELECT prediction.id,row_number() OVER (PARTITION BY prediction.fixture_id,prediction.market,prediction.content_hash ORDER BY (prediction.superseded_at IS NULL) DESC,prediction.generated_at DESC,prediction.created_at DESC,prediction.id DESC) AS duplicate_rank FROM engine_predictions prediction) SELECT count(*) FROM engine_predictions prediction JOIN ranked ON ranked.id=prediction.id WHERE prediction.superseded_at IS NOT NULL AND ranked.duplicate_rank>1 AND (EXISTS(SELECT 1 FROM prediction_results result WHERE result.prediction_id=prediction.id) OR EXISTS(SELECT 1 FROM model_evaluation_facts evidence WHERE evidence.prediction_id=prediction.id) OR EXISTS(SELECT 1 FROM prediction_result_rollup_ledger ledger WHERE ledger.prediction_id=prediction.id) OR EXISTS(SELECT 1 FROM saved_predictions saved WHERE saved.prediction_id=prediction.id) OR EXISTS(SELECT 1 FROM entity_favorites favorite WHERE favorite.entity_type='prediction' AND favorite.entity_id=prediction.public_id::text))`
const predictionDuplicateCandidateCountsSQL = `WITH canonicalized AS MATERIALIZED (SELECT prediction.*,engine_prediction_content_hash(prediction.fixture_id,prediction.market,prediction.prediction,prediction.probability,prediction.confidence,prediction.confidence_tier,prediction.is_best_bet,prediction.lifecycle_stage,prediction.data_completeness,prediction.features_used,prediction.explanation,prediction.gate_tier,prediction.horizon_eligible,prediction.embargo_active,prediction.model_version) AS canonical_hash FROM engine_predictions prediction),ranked AS MATERIALIZED (SELECT canonicalized.*,row_number() OVER (PARTITION BY fixture_id,market,canonical_hash ORDER BY (superseded_at IS NULL) DESC,generated_at DESC,created_at DESC,id DESC) AS duplicate_rank FROM canonicalized) SELECT (SELECT count(*) FROM canonicalized WHERE content_hash IS DISTINCT FROM canonical_hash),(SELECT count(*) FROM ranked prediction WHERE prediction.superseded_at IS NOT NULL AND prediction.duplicate_rank>1 AND NOT EXISTS(SELECT 1 FROM prediction_results result WHERE result.prediction_id=prediction.id) AND NOT EXISTS(SELECT 1 FROM model_evaluation_facts evidence WHERE evidence.prediction_id=prediction.id) AND NOT EXISTS(SELECT 1 FROM prediction_result_rollup_ledger ledger WHERE ledger.prediction_id=prediction.id) AND NOT EXISTS(SELECT 1 FROM saved_predictions saved WHERE saved.prediction_id=prediction.id) AND NOT EXISTS(SELECT 1 FROM entity_favorites favorite WHERE favorite.entity_type='prediction' AND favorite.entity_id=prediction.public_id::text)),(SELECT count(*) FROM ranked prediction WHERE prediction.superseded_at IS NOT NULL AND prediction.duplicate_rank>1 AND (EXISTS(SELECT 1 FROM prediction_results result WHERE result.prediction_id=prediction.id) OR EXISTS(SELECT 1 FROM model_evaluation_facts evidence WHERE evidence.prediction_id=prediction.id) OR EXISTS(SELECT 1 FROM prediction_result_rollup_ledger ledger WHERE ledger.prediction_id=prediction.id) OR EXISTS(SELECT 1 FROM saved_predictions saved WHERE saved.prediction_id=prediction.id) OR EXISTS(SELECT 1 FROM entity_favorites favorite WHERE favorite.entity_type='prediction' AND favorite.entity_id=prediction.public_id::text)))`
const retentionCandidateCountsSQL = `WITH ranked_stats AS (SELECT stats.fixture_id,stats.team_id,row_number() OVER (PARTITION BY stats.team_id ORDER BY fixture.kickoff_at DESC,stats.fixture_id DESC) AS history_rank FROM fixture_statistics stats JOIN fixtures fixture ON fixture.id=stats.fixture_id WHERE fixture.finished_at IS NOT NULL AND lower(fixture.status) IN ('ft','aet','pen','finished')) SELECT (SELECT count(*) FROM saved_predictions saved JOIN engine_predictions prediction ON prediction.id=saved.prediction_id JOIN fixtures fixture ON fixture.id=prediction.fixture_id WHERE fixture.finished_at<$1),(SELECT count(*) FROM entity_favorites favorite JOIN engine_predictions prediction ON favorite.entity_type='prediction' AND favorite.entity_id=prediction.public_id::text JOIN fixtures fixture ON fixture.id=prediction.fixture_id WHERE fixture.finished_at<$1),(SELECT count(*) FROM entity_favorites favorite JOIN fixtures fixture ON favorite.entity_type='match' AND favorite.entity_id=fixture.id::text WHERE fixture.finished_at<$1),(SELECT count(*) FROM engine_predictions prediction JOIN fixtures fixture ON fixture.id=prediction.fixture_id WHERE fixture.finished_at<$2 AND (NOT EXISTS(SELECT 1 FROM prediction_results result WHERE result.prediction_id=prediction.id) OR (EXISTS(SELECT 1 FROM prediction_result_rollup_ledger ledger WHERE ledger.prediction_id=prediction.id) AND EXISTS(SELECT 1 FROM model_evaluation_facts evidence WHERE evidence.prediction_id=prediction.id))) AND NOT EXISTS(SELECT 1 FROM saved_predictions saved WHERE saved.prediction_id=prediction.id) AND NOT EXISTS(SELECT 1 FROM entity_favorites favorite WHERE favorite.entity_type='prediction' AND favorite.entity_id=prediction.public_id::text)),(SELECT count(*) FROM engine_predictions prediction JOIN fixtures fixture ON fixture.id=prediction.fixture_id JOIN prediction_results result ON result.prediction_id=prediction.id WHERE fixture.finished_at<$2 AND (NOT EXISTS(SELECT 1 FROM prediction_result_rollup_ledger ledger WHERE ledger.prediction_id=prediction.id) OR NOT EXISTS(SELECT 1 FROM model_evaluation_facts evidence WHERE evidence.prediction_id=prediction.id))),(SELECT count(*) FROM model_evaluation_facts WHERE settled_at<$3),(SELECT count(*) FROM prediction_accuracy_daily WHERE aggregate_date<$4::date),(SELECT count(*) FROM ranked_stats ranked JOIN fixtures fixture ON fixture.id=ranked.fixture_id WHERE ranked.history_rank>$6 AND fixture.finished_at<$5),(SELECT count(*) FROM provider_entity_maps mapping JOIN provider_raw_payloads payload ON payload.id=mapping.source_payload_id WHERE payload.normalization_status='normalized' AND payload.normalized_at IS NOT NULL AND payload.fetched_at<$7),(SELECT count(*) FROM provider_raw_payloads payload WHERE payload.normalization_status='normalized' AND payload.normalized_at IS NOT NULL AND payload.fetched_at<$7 AND NOT EXISTS(SELECT 1 FROM provider_entity_maps mapping WHERE mapping.source_payload_id=payload.id)),(SELECT count(*) FROM provider_request_logs request WHERE request.requested_at<$8 AND NOT EXISTS(SELECT 1 FROM provider_raw_payloads payload WHERE payload.request_log_id=request.id))`

const pruneNotificationOutboxSQL = `DELETE FROM notification_outbox WHERE id IN (SELECT id FROM notification_outbox WHERE status IN ('sent','suppressed','dead_letter') AND COALESCE(sent_at,available_at,created_at)<$1 ORDER BY id LIMIT $2)`
const pruneRecommendationEventsSQL = `DELETE FROM recommendation_events WHERE id IN (SELECT id FROM recommendation_events WHERE occurred_at<$1 ORDER BY id LIMIT $2)`
const pruneRecommendationCandidatesSQL = `DELETE FROM recommendation_candidates WHERE id IN (SELECT id FROM recommendation_candidates WHERE expires_at<$1 ORDER BY id LIMIT $2)`
const pruneRecommendationDismissalsSQL = `DELETE FROM recommendation_dismissals WHERE ctid IN (SELECT ctid FROM recommendation_dismissals WHERE expires_at IS NOT NULL AND expires_at<$1 LIMIT $2)`
const pruneAuthTokensSQL = `DELETE FROM auth_tokens WHERE id IN (SELECT id FROM auth_tokens WHERE expires_at<$1 OR (used_at IS NOT NULL AND used_at<$1) ORDER BY id LIMIT $2)`
const pruneUserSessionsSQL = `DELETE FROM user_sessions WHERE id IN (SELECT id FROM user_sessions WHERE expires_at<$1 OR (revoked_at IS NOT NULL AND revoked_at<$1) ORDER BY id LIMIT $2)`

package maintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

// RawPredictionStore is separate from the daily maintenance store so the
// potentially large historical drain has its own lease and safety switch.
type RawPredictionStore interface {
	PruneRaw(context.Context, Payload, time.Time) (Metrics, error)
}

type RawPredictionJob struct {
	Store  RawPredictionStore
	Now    func() time.Time
	Logger *slog.Logger
	Policy Payload
}

func (j RawPredictionJob) Run(ctx context.Context, source jobs.Job) error {
	if j.Store == nil {
		return errors.New("raw prediction retention store is required")
	}
	policy := j.Policy.withDefaults()
	if !policy.EnableDestructiveRetention {
		return nil
	}
	// A queued job may tune throughput, but cannot change the configured
	// destructive gate or shorten the confirmed-finish retention window.
	var override struct {
		BatchSize  *int `json:"batch_size"`
		MaxBatches *int `json:"max_batches"`
	}
	if len(source.Payload) > 0 {
		if err := json.Unmarshal(source.Payload, &override); err != nil {
			return fmt.Errorf("decode raw prediction drain payload: %w", err)
		}
	}
	if override.BatchSize != nil {
		if *override.BatchSize < 1 || *override.BatchSize > 5000 {
			return errors.New("raw prediction drain batch_size must be between 1 and 5000")
		}
		policy.BatchSize = *override.BatchSize
	}
	if override.MaxBatches != nil {
		if *override.MaxBatches < 1 || *override.MaxBatches > 100 {
			return errors.New("raw prediction drain max_batches must be between 1 and 100")
		}
		policy.MaxBatches = *override.MaxBatches
	}
	now := time.Now().UTC()
	if j.Now != nil {
		now = j.Now().UTC()
	}
	// Leave room for the worker lease to release the job cleanly.
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	metrics, err := j.Store.PruneRaw(ctx, policy, now)
	logger := j.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.InfoContext(ctx, "bounded raw prediction retention", "examined", metrics.RawPredictionsExamined,
		"deleted", metrics.RawPredictions, "blocked", metrics.RawPredictionsBlocked, "error", err)
	return err
}

type transactionBeginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

// PruneRaw scans finished fixtures and their predictions in index order. A
// durable keyset cursor makes each invocation resume without sorting or
// ranking the complete prediction table. The cursor and deletion commit in
// the same short transaction; skipped protected rows are revisited next pass.
func (r Repository) PruneRaw(ctx context.Context, policy Payload, now time.Time) (Metrics, error) {
	var metrics Metrics
	if !policy.EnableDestructiveRetention || policy.DryRun {
		return metrics, nil
	}
	db, ok := r.db.(transactionBeginner)
	if !ok {
		return metrics, errors.New("raw prediction drain requires transactional database")
	}
	policy = policy.withDefaults()
	cutoff := now.Add(-time.Duration(policy.RawPredictionRetentionHours) * time.Hour)
	for i := 0; i < policy.MaxBatches; i++ {
		if err := ctx.Err(); err != nil {
			return metrics, err
		}
		batch, complete, err := drainRawPredictionBatch(ctx, db, cutoff, policy.BatchSize)
		if err != nil {
			return metrics, err
		}
		metrics.RawPredictions += batch.deleted
		metrics.RawPredictionsExamined += batch.examined
		metrics.RawPredictionsBlocked += batch.examined - batch.deleted
		if complete {
			break
		}
	}
	if err := r.recordAudit(ctx, "raw_prediction_drain", metrics); err != nil {
		return metrics, fmt.Errorf("audit raw prediction drain: %w", err)
	}
	return metrics, nil
}

type rawBatch struct{ examined, deleted int64 }

func drainRawPredictionBatch(ctx context.Context, db transactionBeginner, cutoff time.Time, limit int) (rawBatch, bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return rawBatch{}, false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SET LOCAL statement_timeout = '20s'`); err != nil {
		return rawBatch{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout = '2s'`); err != nil {
		return rawBatch{}, false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO raw_prediction_drain_cursor (name) VALUES ('engine_predictions') ON CONFLICT (name) DO NOTHING`); err != nil {
		return rawBatch{}, false, err
	}
	var finishedAt, generatedAt sql.NullTime
	var fixtureID sql.NullInt64
	var predictionID sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT finished_at, fixture_id, generated_at, prediction_id::text FROM raw_prediction_drain_cursor WHERE name='engine_predictions' FOR UPDATE`).Scan(&finishedAt, &fixtureID, &generatedAt, &predictionID); err != nil {
		return rawBatch{}, false, err
	}
	var lastFinished sql.NullTime
	var lastFixture sql.NullInt64
	var lastGenerated sql.NullTime
	var lastID sql.NullString
	var batch rawBatch
	if err = tx.QueryRowContext(ctx, rawPredictionDrainSQL, cutoff, finishedAt, fixtureID, generatedAt, predictionID, limit).Scan(
		&batch.examined, &batch.deleted, &lastFinished, &lastFixture, &lastGenerated, &lastID); err != nil {
		return rawBatch{}, false, err
	}
	if batch.examined == 0 {
		// A complete pass is done. Reset so protected or late-arriving rows are
		// revisited on the next scheduled job without looping in this run.
		lastFinished, lastFixture = sql.NullTime{}, sql.NullInt64{}
		lastGenerated, lastID = sql.NullTime{}, sql.NullString{}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE raw_prediction_drain_cursor SET finished_at=$1,fixture_id=$2,generated_at=$3,prediction_id=$4::uuid,updated_at=now() WHERE name='engine_predictions'`, lastFinished, lastFixture, lastGenerated, lastID); err != nil {
		return rawBatch{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return rawBatch{}, false, err
	}
	return batch, batch.examined == 0, nil
}

const rawPredictionDrainSQL = `WITH scanned AS MATERIALIZED (
  SELECT fixture.finished_at, fixture.id AS fixture_id, prediction.generated_at, prediction.id AS prediction_id
  FROM fixtures fixture
  JOIN LATERAL (
    SELECT prediction.id, prediction.generated_at
    FROM engine_predictions prediction
    WHERE prediction.fixture_id=fixture.id
      AND ($4::timestamptz IS NULL OR fixture.id<>$3 OR
        (prediction.generated_at,prediction.id)>($4,$5::uuid))
    ORDER BY prediction.generated_at,prediction.id
    LIMIT $6
  ) prediction ON true
  WHERE fixture.finished_at < $1
    AND (fixture.finished_at,fixture.id) >=
      (COALESCE($2::timestamptz,'-infinity'::timestamptz), COALESCE($3::bigint,0))
  ORDER BY fixture.finished_at,fixture.id,prediction.generated_at,prediction.id
  LIMIT $6
), deleted AS (
  DELETE FROM engine_predictions prediction USING scanned
  WHERE prediction.id=scanned.prediction_id
    AND (NOT EXISTS (SELECT 1 FROM prediction_results result WHERE result.prediction_id=prediction.id)
      OR (EXISTS (SELECT 1 FROM prediction_result_rollup_ledger ledger WHERE ledger.prediction_id=prediction.id)
        AND EXISTS (SELECT 1 FROM model_evaluation_facts evidence WHERE evidence.prediction_id=prediction.id)))
    AND NOT EXISTS (SELECT 1 FROM saved_predictions saved WHERE saved.prediction_id=prediction.id)
    AND NOT EXISTS (SELECT 1 FROM entity_favorites favorite WHERE favorite.entity_type='prediction' AND favorite.entity_id=prediction.public_id::text)
    AND NOT EXISTS (SELECT 1 FROM acca_slip_legs leg JOIN acca_slips slip ON slip.id=leg.slip_id
      WHERE leg.prediction_source='engine' AND leg.prediction_id=prediction.id::text AND slip.status='active')
  RETURNING prediction.id
)
SELECT (SELECT count(*) FROM scanned), (SELECT count(*) FROM deleted),
  (SELECT finished_at FROM scanned ORDER BY finished_at DESC,fixture_id DESC,generated_at DESC,prediction_id DESC LIMIT 1),
  (SELECT fixture_id FROM scanned ORDER BY finished_at DESC,fixture_id DESC,generated_at DESC,prediction_id DESC LIMIT 1),
  (SELECT generated_at FROM scanned ORDER BY finished_at DESC,fixture_id DESC,generated_at DESC,prediction_id DESC LIMIT 1),
  (SELECT prediction_id::text FROM scanned ORDER BY finished_at DESC,fixture_id DESC,generated_at DESC,prediction_id DESC LIMIT 1)`

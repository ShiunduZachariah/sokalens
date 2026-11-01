-- In-play prediction lifecycle and audit (architecture v2 7.4).
--
-- Two changes: the lifecycle vocabulary gains the in-play stages, and the
-- prediction gains the anchor that says what was known when it was published.
--
-- data_cutoff_at and feature_version already exist on this table, so only the
-- three genuinely missing columns are added here. Adding a column that is
-- already present would fail the migration; re-deriving the two that exist
-- would be a second definition of the same thing.

-- The vocabulary only widens. Every value the old constraint permitted is
-- still permitted, so no existing row can violate the new one and no backfill
-- is needed.
ALTER TABLE engine_predictions DROP CONSTRAINT IF EXISTS engine_predictions_lifecycle_stage_check;
ALTER TABLE engine_predictions ADD CONSTRAINT engine_predictions_lifecycle_stage_check
  CHECK (lifecycle_stage IN (
    'EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED',
    'IN_PLAY_1H', 'HALF_TIME', 'IN_PLAY_2H', 'EXTRA_TIME', 'FINAL'
  ));

ALTER TABLE engine_predictions
  -- The audit anchor. It answers "what did we know when we published this
  -- minute-63 prediction?" Without it an in-play prediction is unfalsifiable,
  -- which would quietly undermine the accuracy page the product is judged on.
  --
  -- No foreign key: fixture_live_snapshots is range-partitioned and pruned on
  -- its own retention, so a reference would either block that pruning or
  -- cascade it into the prediction record. The anchor is allowed to outlive
  -- the snapshot it names; a dangling id still says which observation was
  -- used, which is more than nothing.
  ADD COLUMN IF NOT EXISTS fixture_live_snapshot_id BIGINT,
  -- The provider's elapsed minute, not a wall-clock derivation. Stored beside
  -- the prediction so a reader can see the state it was made in without
  -- joining to a snapshot that may since have been pruned.
  ADD COLUMN IF NOT EXISTS match_minute SMALLINT,
  ADD COLUMN IF NOT EXISTS period TEXT;

-- The in-play surfaces read "the newest prediction for a fixture in an in-play
-- stage". Without this that is a scan of a 1.6-million-row table per fixture.
-- Partial, because pre-match rows are the overwhelming majority and are never
-- the answer to this question.
CREATE INDEX IF NOT EXISTS idx_engine_predictions_in_play
  ON engine_predictions (fixture_id, generated_at DESC)
  WHERE lifecycle_stage IN ('IN_PLAY_1H', 'HALF_TIME', 'IN_PLAY_2H', 'EXTRA_TIME');

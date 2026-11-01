DROP INDEX IF EXISTS idx_engine_predictions_in_play;

-- Narrowing the vocabulary is the one part of this that is not simply
-- reversible: rows written while the in-play stages were legal would violate
-- the restored constraint and the ALTER would fail.
--
-- They are remapped rather than deleted. Deleting them would destroy published
-- predictions to satisfy a schema rollback, and failing would leave the
-- database stuck mid-down with the index already gone. LINEUP_CONFIRMED is the
-- closest surviving stage -- it is the last one before kickoff, so the row
-- stays readable and stays honest about being pre-in-play.
--
-- The remap is lossy and deliberately so: rolling this back means abandoning
-- in-play predictions, and the rollback should not pretend otherwise.
UPDATE engine_predictions
   SET lifecycle_stage = 'LINEUP_CONFIRMED'
 WHERE lifecycle_stage IN ('IN_PLAY_1H', 'HALF_TIME', 'IN_PLAY_2H', 'EXTRA_TIME', 'FINAL');

ALTER TABLE engine_predictions DROP CONSTRAINT IF EXISTS engine_predictions_lifecycle_stage_check;
ALTER TABLE engine_predictions ADD CONSTRAINT engine_predictions_lifecycle_stage_check
  CHECK (lifecycle_stage IN ('EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED'));

ALTER TABLE engine_predictions
  DROP COLUMN IF EXISTS fixture_live_snapshot_id,
  DROP COLUMN IF EXISTS match_minute,
  DROP COLUMN IF EXISTS period;

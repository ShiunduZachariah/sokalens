-- CI prebuilds this index concurrently before dbtool records the migration.
-- On an empty database the regular additive migration builds it directly.
CREATE INDEX IF NOT EXISTS idx_engine_predictions_results_btts_cover
 ON engine_predictions (id)
 INCLUDE (fixture_id,market,generated_at,prediction,confidence)
 WHERE market='btts_total_goals';

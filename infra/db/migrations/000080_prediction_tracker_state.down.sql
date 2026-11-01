-- Reverses 000080. The table is a projection recomputed from engine_predictions
-- and live match state, so dropping it loses nothing that cannot be rebuilt.
DROP INDEX IF EXISTS idx_prediction_tracker_state_computed;
DROP INDEX IF EXISTS idx_prediction_tracker_state_fixture;
DROP TABLE IF EXISTS prediction_tracker_state;

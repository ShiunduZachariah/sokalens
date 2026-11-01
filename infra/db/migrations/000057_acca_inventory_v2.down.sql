DROP VIEW IF EXISTS eligible_acca_predictions_v;
DROP INDEX IF EXISTS idx_acca_slip_legs_prediction;
DROP INDEX IF EXISTS idx_acca_slips_active_until;
DROP INDEX IF EXISTS idx_acca_slips_active_pool;
DROP TABLE IF EXISTS acca_slip_legs;
DROP TABLE IF EXISTS acca_slips;

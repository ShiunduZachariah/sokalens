-- Local/test expansion rollback only. Never discard production evaluation
-- evidence; disable writers and forward-fix instead once facts exist.
DROP INDEX IF EXISTS idx_drift_alerts_model_window;
DROP INDEX IF EXISTS idx_drift_alerts_unresolved;
DROP TABLE IF EXISTS drift_alerts;

DROP INDEX IF EXISTS idx_champion_challenger_market_window;
DROP TRIGGER IF EXISTS champion_challenger_evaluations_immutable ON champion_challenger_evaluations;
DROP TABLE IF EXISTS champion_challenger_evaluations;

DROP INDEX IF EXISTS idx_shadow_settlements_model_market_settled;
DROP TRIGGER IF EXISTS shadow_settlements_immutable ON shadow_settlements;
DROP TABLE IF EXISTS shadow_settlements;

DROP TRIGGER IF EXISTS model_evaluation_facts_immutable ON model_evaluation_facts;
DROP INDEX IF EXISTS idx_model_evaluation_facts_eligible_settled;
DROP INDEX IF EXISTS idx_model_evaluation_facts_model_settled;
DROP TABLE IF EXISTS model_evaluation_facts;
DROP FUNCTION IF EXISTS reject_model_evidence_mutation();

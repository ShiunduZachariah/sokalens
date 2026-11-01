-- 000044_ops_ai_budget_ledger.down.sql
DROP INDEX IF EXISTS idx_ops_ai_budget_reservations_active_lease;
DROP TABLE IF EXISTS ops_ai_budget_reservations;
DROP TABLE IF EXISTS ops_ai_budget_global;
DROP TABLE IF EXISTS ops_ai_budget_usage;

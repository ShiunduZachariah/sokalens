-- 000044_ops_ai_budget_ledger.up.sql
-- Durable AI budget accounting: per-incident committed usage, outstanding
-- reservations, and a single-row global concurrency counter. All mutations must
-- be guarded by SELECT ... FOR UPDATE so concurrent controllers cannot
-- double-spend the budget, and state survives process restart.
CREATE TABLE IF NOT EXISTS ops_ai_budget_usage (
  incident_id TEXT PRIMARY KEY,
  attempts INTEGER NOT NULL DEFAULT 0,
  tokens BIGINT NOT NULL DEFAULT 0,
  cost_micros BIGINT NOT NULL DEFAULT 0,
  active INTEGER NOT NULL DEFAULT 0,
  normal_active INTEGER NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ops_ai_budget_global (
  id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id = TRUE),
  global_active INTEGER NOT NULL DEFAULT 0,
  normal_active INTEGER NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO ops_ai_budget_global (id) VALUES (TRUE)
ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS ops_ai_budget_reservations (
  reservation_id UUID PRIMARY KEY,
  incident_id TEXT NOT NULL,
  priority TEXT NOT NULL CHECK (priority IN ('critical', 'normal')),
  reserved_attempts INTEGER NOT NULL,
  reserved_tokens BIGINT NOT NULL,
  reserved_cost_micros BIGINT NOT NULL,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'settled', 'released')),
  lease_until TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_ops_ai_budget_reservations_active_lease
  ON ops_ai_budget_reservations (lease_until) WHERE status = 'active';

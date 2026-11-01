-- 000045_ops_ai_replay_claim.up.sql
-- Atomic cross-controller AI workflow replay. The (incident_id, idempotency_key)
-- primary key makes Claim an atomic INSERT ... ON CONFLICT DO NOTHING so exactly
-- one controller runs a given work item and the rest read the completed result.
CREATE TABLE IF NOT EXISTS ops_ai_replay (
  incident_id TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running', 'done')),
  result JSONB,
  claimed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  lease_until TIMESTAMPTZ NOT NULL,
  completed_at TIMESTAMPTZ,
  PRIMARY KEY (incident_id, idempotency_key)
);

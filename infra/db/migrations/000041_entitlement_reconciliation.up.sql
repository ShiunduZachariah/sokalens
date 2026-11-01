-- Append-only entitlement reconciliation evidence and narrowly scoped grant
-- freezes. Provider snapshots remain authoritative; this schema performs no
-- network access and does not broaden controller authority.

ALTER TABLE user_entitlement_grants
  ADD COLUMN reconciliation_verified_at TIMESTAMPTZ,
  ADD COLUMN reconciliation_revision TEXT;

CREATE TABLE entitlement_reconciliation_runs (
  id BIGSERIAL PRIMARY KEY,
  idempotency_key TEXT NOT NULL UNIQUE,
  input_hash CHAR(64) NOT NULL,
  provider TEXT NOT NULL,
  environment TEXT NOT NULL,
  started_at TIMESTAMPTZ NOT NULL,
  completed_at TIMESTAMPTZ NOT NULL,
  result TEXT NOT NULL,
  item_count INTEGER NOT NULL,
  matched_count INTEGER NOT NULL,
  mismatch_count INTEGER NOT NULL,
  failed_count INTEGER NOT NULL,
  correction_count INTEGER NOT NULL,
  details JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (btrim(idempotency_key) <> ''),
  CHECK (input_hash ~ '^[0-9a-f]{64}$'),
  CHECK (btrim(provider) <> ''),
  CHECK (btrim(environment) <> ''),
  CHECK (completed_at >= started_at),
  CHECK (result IN ('completed', 'partial', 'failed')),
  CHECK (item_count >= 0 AND matched_count >= 0 AND mismatch_count >= 0 AND failed_count >= 0 AND correction_count >= 0),
  CHECK (item_count = matched_count + mismatch_count + failed_count)
);

CREATE TABLE entitlement_reconciliation_items (
  id BIGSERIAL PRIMARY KEY,
  run_id BIGINT NOT NULL REFERENCES entitlement_reconciliation_runs(id) ON DELETE RESTRICT,
  subscription_id BIGINT REFERENCES subscriptions(id) ON DELETE RESTRICT,
  provider_subscription_id TEXT,
  classification TEXT NOT NULL,
  result TEXT NOT NULL,
  internal_revision TEXT,
  provider_revision TEXT,
  provider_observed_at TIMESTAMPTZ,
  details JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (classification IN ('pending_payment','active','grace','renewal','refund','chargeback','cancellation','retry','expiry','provider_failure','stale','unknown')),
  CHECK (result IN ('matched','mismatch','failed')),
  CHECK (subscription_id IS NOT NULL OR classification IN ('provider_failure','unknown'))
);
CREATE INDEX idx_entitlement_reconciliation_items_subscription
  ON entitlement_reconciliation_items (subscription_id, created_at DESC, id DESC);

CREATE TABLE entitlement_reconciliation_corrections (
  id BIGSERIAL PRIMARY KEY,
  run_id BIGINT NOT NULL REFERENCES entitlement_reconciliation_runs(id) ON DELETE RESTRICT,
  item_id BIGINT NOT NULL REFERENCES entitlement_reconciliation_items(id) ON DELETE RESTRICT,
  idempotency_key TEXT NOT NULL,
  subscription_id BIGINT REFERENCES subscriptions(id) ON DELETE RESTRICT,
  grant_id BIGINT REFERENCES user_entitlement_grants(id) ON DELETE RESTRICT,
  action TEXT NOT NULL,
  target_status TEXT,
  effective_at TIMESTAMPTZ NOT NULL,
  provider_revision TEXT,
  applied BOOLEAN NOT NULL,
  details JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (action IN ('freeze_new_grants','verify_grant','correct_subscription','revoke_grant','unfreeze_new_grants')),
  CHECK ((action IN ('verify_grant','revoke_grant')) = (grant_id IS NOT NULL)),
  CHECK (subscription_id IS NOT NULL OR action IN ('freeze_new_grants','unfreeze_new_grants'))
);
CREATE UNIQUE INDEX uq_entitlement_reconciliation_corrections_run_key
  ON entitlement_reconciliation_corrections (run_id, idempotency_key);
CREATE INDEX idx_entitlement_reconciliation_corrections_subscription
  ON entitlement_reconciliation_corrections (subscription_id, created_at DESC, id DESC);

CREATE TABLE entitlement_reconciliation_effects (
  idempotency_key TEXT PRIMARY KEY,
  first_correction_id BIGINT NOT NULL UNIQUE REFERENCES entitlement_reconciliation_corrections(id) ON DELETE RESTRICT,
  applied_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE entitlement_grant_freezes (
  id BIGSERIAL PRIMARY KEY,
  provider TEXT NOT NULL,
  environment TEXT NOT NULL,
  subscription_id BIGINT REFERENCES subscriptions(id) ON DELETE RESTRICT,
  active BOOLEAN NOT NULL DEFAULT true,
  reason TEXT NOT NULL,
  last_correction_id BIGINT NOT NULL REFERENCES entitlement_reconciliation_corrections(id) ON DELETE RESTRICT,
  frozen_at TIMESTAMPTZ NOT NULL,
  released_at TIMESTAMPTZ,
  CHECK (btrim(provider) <> '' AND btrim(environment) <> ''),
  CHECK ((active AND released_at IS NULL) OR (NOT active AND released_at IS NOT NULL))
);
CREATE UNIQUE INDEX uq_entitlement_grant_freezes_scope
  ON entitlement_grant_freezes (provider, environment, subscription_id) NULLS NOT DISTINCT;

-- Existing grants remain readable during a provider outage. Grant creation
-- paths must consult this table before creating a new subscription grant.
COMMENT ON TABLE entitlement_grant_freezes IS
  'Fail-closed gate for NEW subscription grants only; existing verified grants are not revoked by provider availability failures.';

CREATE OR REPLACE FUNCTION reject_entitlement_reconciliation_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'entitlement reconciliation history is append-only';
END;
$$;

CREATE TRIGGER entitlement_reconciliation_runs_immutable
  BEFORE UPDATE OR DELETE ON entitlement_reconciliation_runs
  FOR EACH ROW EXECUTE FUNCTION reject_entitlement_reconciliation_mutation();
CREATE TRIGGER entitlement_reconciliation_items_immutable
  BEFORE UPDATE OR DELETE ON entitlement_reconciliation_items
  FOR EACH ROW EXECUTE FUNCTION reject_entitlement_reconciliation_mutation();
CREATE TRIGGER entitlement_reconciliation_corrections_immutable
  BEFORE UPDATE OR DELETE ON entitlement_reconciliation_corrections
  FOR EACH ROW EXECUTE FUNCTION reject_entitlement_reconciliation_mutation();
CREATE TRIGGER entitlement_reconciliation_effects_immutable
  BEFORE UPDATE OR DELETE ON entitlement_reconciliation_effects
  FOR EACH ROW EXECUTE FUNCTION reject_entitlement_reconciliation_mutation();

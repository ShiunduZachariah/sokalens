-- Advertising-provider routing is deliberately separate from `providers`,
-- which is the ingestion-provider catalogue (API-Football, TheSportsDB, ...).
-- This table contains browser-safe routing configuration only. Provider
-- secrets belong in the deployment secret store and must never be written to
-- public_config.
CREATE TABLE IF NOT EXISTS ad_providers (
  code               TEXT PRIMARY KEY,
  name               TEXT NOT NULL,
  adapter_key        TEXT NOT NULL,
  enabled            BOOLEAN NOT NULL DEFAULT FALSE,
  global_priority    INTEGER NOT NULL,
  traffic_weight     NUMERIC(10,4) NOT NULL DEFAULT 0,
  health_status      TEXT NOT NULL DEFAULT 'HEALTHY',
  last_health_check  TIMESTAMPTZ,
  failure_threshold  INTEGER NOT NULL DEFAULT 20,
  cooldown_seconds   INTEGER NOT NULL DEFAULT 300,
  request_timeout_ms INTEGER NOT NULL DEFAULT 1200,
  public_config      JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT ad_providers_code_valid
    CHECK (code ~ '^[a-z][a-z0-9_-]{1,63}$'),
  CONSTRAINT ad_providers_name_not_blank
    CHECK (btrim(name) <> ''),
  CONSTRAINT ad_providers_adapter_key_valid
    CHECK (adapter_key ~ '^[a-z][a-z0-9_-]{1,63}$'),
  CONSTRAINT ad_providers_global_priority_positive
    CHECK (global_priority > 0),
  CONSTRAINT ad_providers_traffic_weight_non_negative
    CHECK (traffic_weight >= 0),
  CONSTRAINT ad_providers_health_status_valid
    CHECK (health_status IN ('HEALTHY', 'DEGRADED', 'UNHEALTHY', 'DISABLED')),
  CONSTRAINT ad_providers_failure_threshold_positive
    CHECK (failure_threshold > 0),
  CONSTRAINT ad_providers_cooldown_seconds_positive
    CHECK (cooldown_seconds > 0),
  CONSTRAINT ad_providers_request_timeout_bounded
    CHECK (request_timeout_ms BETWEEN 500 AND 5000),
  CONSTRAINT ad_providers_public_config_object
    CHECK (jsonb_typeof(public_config) = 'object')
);

CREATE INDEX IF NOT EXISTS idx_ad_providers_routing
  ON ad_providers (enabled, global_priority, code);

-- Append-only observations used to calculate global provider health. Session
-- routing itself remains ephemeral in Redis; only a one-way session hash is
-- retained here so operators can distinguish one-user failures from a global
-- outage without storing the session identifier.
CREATE TABLE IF NOT EXISTS ad_provider_outcomes (
  id             BIGSERIAL PRIMARY KEY,
  provider_code  TEXT NOT NULL REFERENCES ad_providers(code) ON DELETE RESTRICT,
  placement_key  TEXT NOT NULL,
  session_hash   CHAR(64) NOT NULL,
  attempt_hash   CHAR(64) NOT NULL UNIQUE,
  outcome        TEXT NOT NULL,
  failure_reason TEXT,
  duration_ms    INTEGER NOT NULL,
  occurred_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT ad_provider_outcomes_placement_not_blank
    CHECK (btrim(placement_key) <> '' AND length(placement_key) <= 128),
  CONSTRAINT ad_provider_outcomes_session_hash_valid
    CHECK (session_hash ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ad_provider_outcomes_attempt_hash_valid
    CHECK (attempt_hash ~ '^[0-9a-f]{64}$'),
  CONSTRAINT ad_provider_outcomes_outcome_valid
    CHECK (outcome IN ('success', 'failure')),
  CONSTRAINT ad_provider_outcomes_failure_reason_valid
    CHECK (failure_reason IS NULL OR failure_reason IN (
      'NO_FILL',
      'SCRIPT_ERROR',
      'TIMEOUT',
      'NETWORK_ERROR',
      'BLOCKED',
      'INVALID_ZONE',
      'PROVIDER_UNAVAILABLE'
    )),
  CONSTRAINT ad_provider_outcomes_failure_reason_matches_outcome
    CHECK (
      (outcome = 'success' AND failure_reason IS NULL)
      OR (outcome = 'failure' AND failure_reason IS NOT NULL)
    ),
  CONSTRAINT ad_provider_outcomes_duration_bounded
    CHECK (duration_ms BETWEEN 0 AND 60000)
);

CREATE INDEX IF NOT EXISTS idx_ad_provider_outcomes_provider_recent
  ON ad_provider_outcomes (provider_code, occurred_at DESC);

CREATE INDEX IF NOT EXISTS idx_ad_provider_outcomes_provider_failures
  ON ad_provider_outcomes (provider_code, occurred_at DESC)
  WHERE outcome = 'failure';

CREATE INDEX IF NOT EXISTS idx_ad_provider_outcomes_session_recent
  ON ad_provider_outcomes (session_hash, occurred_at DESC);

CREATE INDEX IF NOT EXISTS idx_ad_provider_outcomes_placement_recent
  ON ad_provider_outcomes (placement_key, occurred_at DESC);

CREATE INDEX IF NOT EXISTS idx_ad_provider_outcomes_occurred_at
  ON ad_provider_outcomes (occurred_at);

-- Disabled-by-default catalogue rows make adapter availability explicit while
-- guaranteeing a migration can never begin serving ads. Operators must add
-- reviewed public_config and deliberately enable a provider afterward.
INSERT INTO ad_providers (
  code,
  name,
  adapter_key,
  enabled,
  global_priority,
  traffic_weight,
  health_status,
  public_config
) VALUES
  ('adsense', 'Google AdSense', 'adsense', FALSE, 10, 100, 'DISABLED', '{}'::jsonb),
  ('adsterra', 'Adsterra', 'adsterra', FALSE, 20, 0, 'DISABLED', '{}'::jsonb),
  ('monetag', 'Monetag', 'monetag', FALSE, 30, 0, 'DISABLED', '{}'::jsonb)
ON CONFLICT (code) DO NOTHING;

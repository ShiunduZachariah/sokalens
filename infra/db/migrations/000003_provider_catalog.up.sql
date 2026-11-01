CREATE TABLE IF NOT EXISTS providers (
  provider TEXT PRIMARY KEY,
  display_name TEXT NOT NULL,
  base_url TEXT NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO providers (
  provider,
  display_name,
  base_url
) VALUES (
  'api-football',
  'API-Football',
  'https://v3.football.api-sports.io'
) ON CONFLICT (provider) DO UPDATE SET
  display_name = EXCLUDED.display_name,
  base_url = EXCLUDED.base_url,
  updated_at = now();

UPDATE provider_rate_limits
SET
  daily_limit = 100,
  daily_remaining = LEAST(daily_remaining, 100),
  minute_limit = 10,
  minute_remaining = LEAST(minute_remaining, 10),
  daily_reserve = 20,
  minute_reserve = 2,
  updated_at = now()
WHERE provider = 'api-football';

CREATE TABLE IF NOT EXISTS provider_endpoint_specs (
  provider TEXT NOT NULL REFERENCES providers(provider) ON DELETE CASCADE,
  endpoint_key TEXT NOT NULL,
  endpoint_group TEXT NOT NULL,
  method TEXT NOT NULL DEFAULT 'GET',
  path_template TEXT NOT NULL,
  params_schema JSONB NOT NULL DEFAULT '{}',
  quota_cost INTEGER NOT NULL DEFAULT 1 CHECK (quota_cost > 0),
  default_queue TEXT NOT NULL DEFAULT 'provider',
  default_priority INTEGER NOT NULL DEFAULT 100,
  min_refresh_interval INTERVAL,
  stale_after INTERVAL,
  raw_storage_allowed BOOLEAN NOT NULL DEFAULT FALSE,
  public_display_allowed BOOLEAN NOT NULL DEFAULT FALSE,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  notes TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (provider, endpoint_key)
);

CREATE INDEX IF NOT EXISTS idx_provider_endpoint_specs_enabled
  ON provider_endpoint_specs (provider, enabled);

INSERT INTO provider_endpoint_specs (
  provider,
  endpoint_key,
  endpoint_group,
  path_template,
  params_schema,
  default_queue,
  default_priority,
  notes
) VALUES
  ('api-football', 'status', 'operations', 'status', '{}', 'provider', 10, 'One-shot provider account/quota health validation.'),
  ('api-football', 'countries', 'reference', 'countries', '{}', 'provider-reference', 20, 'Reference country list; do not assume provider country codes map directly to ISO alpha-2.'),
  ('api-football', 'leagues', 'reference', 'leagues', '{"optional":["id","name","country","code","season","team","type","current","search","last"]}', 'provider-reference', 30, 'League/season catalog and coverage metadata.'),
  ('api-football', 'teams', 'reference', 'teams', '{"optional":["id","name","league","season","country","code","venue","search"]}', 'provider-reference', 40, 'Team catalog for provider-to-canonical mapping.'),
  ('api-football', 'fixtures', 'fixtures', 'fixtures', '{"optional":["id","ids","live","date","league","season","team","last","next","from","to","round","status","venue","timezone"]}', 'provider-fixtures', 50, 'Fixture schedule, status, teams, score blocks, and venue.'),
  ('api-football', 'fixtures.events', 'fixtures', 'fixtures/events', '{"required":["fixture"],"optional":["team","player","type"]}', 'provider-fixtures', 60, 'Goals, cards, substitutions, and VAR event stream.'),
  ('api-football', 'fixtures.statistics', 'fixtures', 'fixtures/statistics', '{"required":["fixture"],"optional":["team","type"]}', 'provider-fixtures', 60, 'Team match statistics such as shots, corners, cards, and fouls where covered.'),
  ('api-football', 'standings', 'performance', 'standings', '{"required":["league","season"],"optional":["team"]}', 'provider-performance', 70, 'League table and home/away performance records.'),
  ('api-football', 'odds.pre_match', 'odds', 'odds', '{"optional":["fixture","league","season","date","timezone","page","bookmaker","bet"]}', 'provider-odds', 80, 'Pre-match odds. Store and display only after license/commercial review.'),
  ('api-football', 'odds.live', 'odds', 'odds/live', '{"optional":["fixture","league","bet"]}', 'provider-odds', 90, 'In-play odds. Disabled for launch display unless explicitly reviewed.'),
  ('api-football', 'predictions.provider', 'benchmark', 'predictions', '{"required":["fixture"]}', 'provider-benchmark', 100, 'Provider predictions are benchmark/reference data only, not our public prediction source of truth.')
ON CONFLICT (provider, endpoint_key) DO UPDATE SET
  endpoint_group = EXCLUDED.endpoint_group,
  path_template = EXCLUDED.path_template,
  params_schema = EXCLUDED.params_schema,
  default_queue = EXCLUDED.default_queue,
  default_priority = EXCLUDED.default_priority,
  notes = EXCLUDED.notes,
  updated_at = now();

CREATE TABLE IF NOT EXISTS provider_feature_sources (
  provider TEXT NOT NULL,
  feature_key TEXT NOT NULL,
  endpoint_key TEXT NOT NULL,
  notes TEXT,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (provider, feature_key, endpoint_key),
  FOREIGN KEY (provider, endpoint_key) REFERENCES provider_endpoint_specs(provider, endpoint_key) ON DELETE CASCADE
);

INSERT INTO provider_feature_sources (
  provider,
  feature_key,
  endpoint_key,
  notes
) VALUES
  ('api-football', 'corners', 'fixtures.statistics', 'Use when fixture statistics include corner values.'),
  ('api-football', 'cards', 'fixtures.events', 'Cards settle from fixture events where available.'),
  ('api-football', 'fouls', 'fixtures.statistics', 'Use null-safe handling because some leagues do not provide foul counts.'),
  ('api-football', 'throw_ins', 'fixtures.statistics', 'Coverage must be validated before this market is exposed.'),
  ('api-football', 'team_over_under_goals', 'odds.pre_match', 'Odds source only; model labels come from normalized fixture scores.'),
  ('api-football', 'half_time_full_time', 'fixtures', 'Fixtures provide halftime and fulltime score blocks after settlement.'),
  ('api-football', 'over_under_goals', 'odds.pre_match', 'Validate 0.5, 1.5, 2.5, 3.5, 4.5, and 5.5 coverage per league/bookmaker.'),
  ('api-football', 'multi_match_builder', 'odds.pre_match', 'Bundle recommendations depend on supported underlying markets only.')
ON CONFLICT (provider, feature_key, endpoint_key) DO UPDATE SET
  notes = EXCLUDED.notes,
  updated_at = now();

CREATE TABLE IF NOT EXISTS provider_raw_payloads (
  id BIGSERIAL PRIMARY KEY,
  request_log_id BIGINT UNIQUE REFERENCES provider_request_logs(id),
  job_id BIGINT REFERENCES jobs(id),
  provider TEXT NOT NULL REFERENCES providers(provider),
  endpoint_key TEXT NOT NULL,
  params_hash TEXT,
  status_code INTEGER NOT NULL,
  response_hash TEXT NOT NULL,
  payload JSONB NOT NULL,
  headers JSONB NOT NULL DEFAULT '{}',
  fetched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  stale_after TIMESTAMPTZ,
  storage_policy TEXT NOT NULL DEFAULT 'metadata_only',
  normalized_at TIMESTAMPTZ,
  normalization_status TEXT NOT NULL DEFAULT 'pending',
  normalization_error TEXT,
  UNIQUE (provider, endpoint_key, params_hash, response_hash)
);

CREATE INDEX IF NOT EXISTS idx_provider_raw_payloads_recent
  ON provider_raw_payloads (provider, endpoint_key, params_hash, fetched_at DESC);

CREATE TABLE IF NOT EXISTS provider_entity_maps (
  id BIGSERIAL PRIMARY KEY,
  provider TEXT NOT NULL REFERENCES providers(provider),
  entity_type TEXT NOT NULL,
  provider_entity_id TEXT NOT NULL,
  provider_entity_key TEXT,
  canonical_entity_type TEXT,
  canonical_entity_id TEXT,
  match_status TEXT NOT NULL DEFAULT 'unresolved',
  confidence NUMERIC(5,4) CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
  source_payload_id BIGINT REFERENCES provider_raw_payloads(id),
  metadata JSONB NOT NULL DEFAULT '{}',
  first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  retired_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_entity_maps_active_external_id
  ON provider_entity_maps (provider, entity_type, provider_entity_id)
  WHERE retired_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_provider_entity_maps_canonical
  ON provider_entity_maps (canonical_entity_type, canonical_entity_id);

CREATE INDEX IF NOT EXISTS idx_provider_entity_maps_unresolved
  ON provider_entity_maps (provider, entity_type, first_seen_at)
  WHERE canonical_entity_id IS NULL AND retired_at IS NULL;

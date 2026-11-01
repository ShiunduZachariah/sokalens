INSERT INTO countries (code, name, commercial_focus)
VALUES
  ('KE', 'Kenya', true),
  ('GB', 'United Kingdom', false),
  ('US', 'United States', false)
ON CONFLICT (code) DO NOTHING;

INSERT INTO sports (slug, name)
VALUES ('football', 'Football')
ON CONFLICT (slug) DO NOTHING;

INSERT INTO leagues (sport_id, country_code, slug, name, priority_score)
SELECT s.id, 'KE', 'kenyan-premier-league', 'Kenyan Premier League', 90
FROM sports s
WHERE s.slug = 'football'
ON CONFLICT (slug) DO NOTHING;

INSERT INTO teams (country_code, slug, name)
VALUES
  ('KE', 'gor-mahia', 'Gor Mahia'),
  ('KE', 'afc-leopards', 'AFC Leopards')
ON CONFLICT (slug) DO NOTHING;

INSERT INTO matches (league_id, home_team_id, away_team_id, kickoff_at, status)
SELECT l.id, home.id, away.id, '2026-05-01T15:00:00Z', 'scheduled'
FROM leagues l
JOIN teams home ON home.slug = 'gor-mahia'
JOIN teams away ON away.slug = 'afc-leopards'
WHERE l.slug = 'kenyan-premier-league'
  AND NOT EXISTS (
    SELECT 1
    FROM matches m
    WHERE m.league_id = l.id
      AND m.home_team_id = home.id
      AND m.away_team_id = away.id
      AND m.kickoff_at = '2026-05-01T15:00:00Z'
  );

INSERT INTO odds_markets (market_key, name, phase)
VALUES
  ('1x2', '1X2', 'core'),
  ('double_chance', 'Double Chance', 'core'),
  ('btts', 'Both Teams To Score', 'core'),
  ('ou_0_5', 'Over/Under 0.5 Goals', 'core'),
  ('ou_1_5', 'Over/Under 1.5 Goals', 'core'),
  ('ou_2_5', 'Over/Under 2.5 Goals', 'core'),
  ('ou_3_5', 'Over/Under 3.5 Goals', 'core'),
  ('ou_4_5', 'Over/Under 4.5 Goals', 'core'),
  ('ou_5_5', 'Over/Under 5.5 Goals', 'core'),
  ('team_ou_goals', 'Team Over/Under Goals', 'advanced'),
  ('ht_ft', 'Half-Time/Full-Time', 'advanced'),
  ('corners', 'Corners', 'advanced'),
  ('cards', 'Cards', 'advanced'),
  ('fouls', 'Fouls', 'advanced'),
  ('throw_ins', 'Throw-ins', 'advanced'),
  ('multi_match_builder', 'Multi-Match Builder', 'planned')
ON CONFLICT (market_key) DO NOTHING;

INSERT INTO prediction_market_support (
  market_key,
  support_status,
  tier,
  settlement_source,
  minimum_data_notes,
  enabled
)
VALUES
  ('1x2', 'enabled', 'core', 'final_score', 'Settled from final home/draw/away result.', true),
  ('double_chance', 'enabled', 'core', 'final_score', 'Derived from auditable 1X2 probabilities and settled from final result.', true),
  ('btts', 'enabled', 'core', 'final_score', 'Settled from whether both teams scored in final result.', true),
  ('ou_2_5', 'enabled', 'core', 'final_score', 'Settled from total goals against the 2.5 threshold.', true),
  ('ou_0_5', 'enabled', 'core', 'final_score', 'Settled from total goals against the 0.5 threshold.', true),
  ('ou_1_5', 'enabled', 'core', 'final_score', 'Settled from total goals against the 1.5 threshold.', true),
  ('ou_3_5', 'enabled', 'core', 'final_score', 'Settled from total goals against the 3.5 threshold.', true),
  ('ou_4_5', 'enabled', 'core', 'final_score', 'Settled from total goals against the 4.5 threshold.', true),
  ('ou_5_5', 'enabled', 'core', 'final_score', 'Settled from total goals against the 5.5 threshold.', true),
  ('team_ou_goals', 'enabled', 'advanced', 'final_score', 'Settled from team goals against a 1.5 threshold.', true),
  ('ht_ft', 'enabled', 'advanced', 'final_score', 'Settled from half-time and full-time score evidence.', true),
  ('corners', 'enabled', 'advanced', 'statistics', 'Settled from home and away corners totals.', true),
  ('cards', 'enabled', 'advanced', 'fixture_events', 'Settled from home and away cards totals.', true),
  ('fouls', 'enabled', 'advanced', 'statistics', 'Settled from home and away fouls totals.', true),
  ('throw_ins', 'enabled', 'advanced', 'statistics', 'Settled from home and away throw-in totals.', true),
  ('multi_match_builder', 'planned', 'planned', 'not_supported', 'Depends on published, settled, auditable underlying markets.', false)
ON CONFLICT (market_key) DO UPDATE
SET support_status = EXCLUDED.support_status,
    tier = EXCLUDED.tier,
    settlement_source = EXCLUDED.settlement_source,
    minimum_data_notes = EXCLUDED.minimum_data_notes,
    enabled = EXCLUDED.enabled,
    updated_at = now();

INSERT INTO prediction_models (model_key, version)
VALUES ('foundation-baseline', '0.1.0')
ON CONFLICT DO NOTHING;

INSERT INTO odds_snapshots (match_id, market_id, provider, payload, captured_at)
SELECT m.id, market.id, 'foundation_seed', '{"home":2.10,"draw":3.20,"away":3.60}'::jsonb, now()
FROM matches m
JOIN leagues l ON l.id = m.league_id
JOIN odds_markets market ON market.market_key = '1x2'
WHERE l.slug = 'kenyan-premier-league'
  AND m.kickoff_at = '2026-05-01T15:00:00Z'
  AND NOT EXISTS (
    SELECT 1
    FROM odds_snapshots existing
    WHERE existing.match_id = m.id
      AND existing.market_id = market.id
      AND existing.provider = 'foundation_seed'
  );

INSERT INTO predictions (match_id, market_id, model_id, status, selection, confidence, published_version, published_at)
SELECT m.id, market.id, model.id, 'published', 'home', 53.04, 1, now()
FROM matches m
JOIN leagues l ON l.id = m.league_id
JOIN odds_markets market ON market.market_key = '1x2'
JOIN prediction_models model ON model.model_key = 'foundation-baseline'
WHERE l.slug = 'kenyan-premier-league'
  AND m.kickoff_at = '2026-05-01T15:00:00Z'
  AND NOT EXISTS (
    SELECT 1
    FROM predictions existing
    WHERE existing.match_id = m.id
      AND existing.market_id = market.id
      AND existing.model_id = model.id
      AND existing.published_version = 1
  );

INSERT INTO prediction_versions (
  prediction_id,
  version,
  model_id,
  selection,
  confidence,
  probabilities,
  input_snapshot_hash,
  input_snapshot,
  publication_status,
  published_at
)
SELECT
  p.id,
  p.published_version,
  p.model_id,
  p.selection,
  p.confidence,
  '{"home":0.5304,"draw":0.2376,"away":0.2320}'::jsonb,
  'foundation-seed-1x2-gor-afc',
  '{"source":"foundation_seed","market":"1x2","notes":"static seed for migration and API smoke validation"}'::jsonb,
  'published',
  p.published_at
FROM predictions p
JOIN matches m ON m.id = p.match_id
JOIN leagues l ON l.id = m.league_id
JOIN odds_markets market ON market.id = p.market_id
WHERE l.slug = 'kenyan-premier-league'
  AND market.market_key = '1x2'
  AND NOT EXISTS (
    SELECT 1
    FROM prediction_versions existing
    WHERE existing.prediction_id = p.id
      AND existing.version = p.published_version
  );

INSERT INTO prediction_feature_snapshots (
  match_id,
  market_id,
  model_id,
  feature_key,
  snapshot_hash,
  payload
)
SELECT
  m.id,
  market.id,
  model.id,
  'foundation-baseline-inputs',
  'foundation-feature-gor-afc-1x2-v1',
  '{
    "home":{"attack_rating":0.72,"defense_rating":0.66,"form_rating":0.70},
    "away":{"attack_rating":0.54,"defense_rating":0.50,"form_rating":0.48},
    "source":"foundation_seed"
  }'::jsonb
FROM matches m
JOIN leagues l ON l.id = m.league_id
JOIN odds_markets market ON market.market_key = '1x2'
JOIN prediction_models model ON model.model_key = 'foundation-baseline'
WHERE l.slug = 'kenyan-premier-league'
  AND m.kickoff_at = '2026-05-01T15:00:00Z'
ON CONFLICT (match_id, market_id, model_id, snapshot_hash) DO NOTHING;

INSERT INTO prediction_outcomes (prediction_version_id, result, evidence)
SELECT pv.id, 'pending', '{"source":"foundation_seed","reason":"fixture not finished"}'::jsonb
FROM prediction_versions pv
WHERE pv.input_snapshot_hash = 'foundation-seed-1x2-gor-afc'
  AND NOT EXISTS (
    SELECT 1
    FROM prediction_outcomes existing
    WHERE existing.prediction_version_id = pv.id
  );

INSERT INTO users (email, password_hash, role)
VALUES
  ('fan@example.com', NULL, 'user'),
  ('reader@example.com', NULL, 'user')
ON CONFLICT (email) DO NOTHING;

INSERT INTO feature_flags (feature_key, enabled, default_action, description)
VALUES
  ('public_content', true, 'allow', 'Public sports content is globally available by default.'),
  ('predictions', true, 'allow', 'Predictions are globally available by default.'),
  ('odds_display', true, 'allow', 'Odds context is globally available unless restricted or blocked by provider terms.'),
  ('bookmaker_ctas', false, 'deny', 'Bookmaker CTAs remain off until approved.'),
  ('affiliate_links', false, 'deny', 'Affiliate links remain off until approved.'),
  ('paid_promotions', false, 'deny', 'Paid promotions remain off until approved.'),
  ('gambling_ads', false, 'deny', 'Gambling ads remain off until approved.'),
  ('subscriptions', false, 'deny', 'Subscriptions remain disabled until a billing adapter, prices, and webhook credentials are configured.'),
  ('push_promotions', false, 'deny', 'Commercial push promotions remain off until approved.'),
  ('account_deletion', true, 'allow', 'Verified users may request account deletion through the authenticated API.'),
  ('notification_opt_out', true, 'allow', 'Notification opt-out is enforced by the API and notification workers.')
ON CONFLICT (feature_key) DO UPDATE
SET enabled = EXCLUDED.enabled,
    default_action = EXCLUDED.default_action,
    description = EXCLUDED.description;

INSERT INTO app_settings (key, value)
VALUES
  ('platform.phase', '{"current":"foundation","next":"provider_ingestion"}'::jsonb),
  ('privacy.deleted_account_retention_days', '90'::jsonb),
  ('mobile.terminology', '{"match_analysis":"Match Analysis","match_insights":"Match Insights","analysis_watchlist":"Analysis Watchlist","analysis_alerts":"Analysis Alerts","multi_match_builder":"Multi-Match Builder","price_comparison":"Price Comparison","confidence":"Confidence","form_guide":"Form Guide","match_outlook":"Match Outlook","model_signal":"Model Signal"}'::jsonb),
  ('multi_match_builder.constraints', '{"min_matches":2,"max_matches":6,"filters":["league","competition","team","match_start_time","match_end_time","min_price","max_price","minimum_confidence_score","market_type","risk_level"]}'::jsonb)
ON CONFLICT (key) DO NOTHING;

INSERT INTO subscription_entitlements (entitlement_key, tier, name, description, mobile_label, web_label)
VALUES
  ('free_entity_favorites', 'free', 'Entity Favorites', 'Teams, leagues, competitions, and matches can be favorited by basic users.', 'Favorites', 'Favorites'),
  ('free_basic_match_notifications', 'free', 'Basic Match Notifications', 'Starting soon, live start, goals, cards, final result, and lineup notifications where available.', 'Match Alerts', 'Match Alerts'),
  ('pro_no_ads', 'pro', 'No Ads', 'Suppress paid ad placements for active Pro users.', 'No Ads', 'No Ads'),
  ('pro_deeper_analysis', 'pro', 'Deeper Analysis', 'Fuller analysis where model quality and data coverage support it.', 'Deeper Analysis', 'Deeper Prediction Analysis'),
  ('pro_insight_watchlist', 'pro', 'Insight Watchlist', 'Save analysis or prediction insight items using the shared backend watchlist concept.', 'Analysis Watchlist', 'Prediction Watchlist'),
  ('pro_insight_alerts', 'pro', 'Insight Alerts', 'Alerts for new or updated insight items using neutral backend alert concepts.', 'Analysis Alerts', 'Prediction Alerts'),
  ('pro_market_alerts', 'pro', 'Market-Specific Insight Alerts', 'Alerts for selected markets and signals.', 'Market Alerts', 'Market Alerts'),
  ('pro_confidence_alerts', 'pro', 'Confidence Threshold Alerts', 'Alerts when confidence crosses a selected threshold.', 'Confidence Alerts', 'Confidence Alerts'),
  ('pro_advanced_multi_match_builder', 'pro', 'Advanced Multi-Match Builder', 'Advanced filters for 2-6 match bundles.', 'Advanced Multi-Match Builder', 'Advanced Multi-Match Builder'),
  ('pro_price_movement_alerts', 'pro', 'Price Movement Alerts', 'Alerts for meaningful price movement where data coverage supports it.', 'Price Alerts', 'Odds Movement Alerts'),
  ('pro_analysis_history', 'pro', 'Analysis History', 'History for published analysis or prediction items.', 'Analysis History', 'Prediction History'),
  ('pro_early_analysis_notifications', 'pro', 'Early Analysis Notifications', 'Early notifications where available and quality gates support them.', 'Early Analysis Alerts', 'Early Prediction Alerts')
ON CONFLICT (entitlement_key) DO NOTHING;

INSERT INTO ad_campaigns (campaign_key, name, status, placement_key)
VALUES ('house-foundation', 'Foundation House Campaign', 'draft', 'home_top')
ON CONFLICT (campaign_key) DO NOTHING;

INSERT INTO jobs (
  job_key,
  status,
  payload,
  queue,
  priority,
  run_after,
  max_attempts,
  idempotency_key
)
SELECT
  'provider.api-football.countries',
  'queued',
  '{"endpoint":"countries"}'::jsonb,
  'provider-reference',
  20,
  now(),
  3,
  'api-football:countries:{}'
WHERE NOT EXISTS (
  SELECT 1
  FROM jobs
  WHERE idempotency_key = 'api-football:countries:{}'
    AND status IN ('queued', 'running', 'completed')
);

INSERT INTO jobs (
  job_key,
  status,
  payload,
  queue,
  priority,
  run_after,
  max_attempts,
  idempotency_key
)
SELECT
  'provider.api-football.leagues',
  'queued',
  '{"endpoint":"leagues","current":true}'::jsonb,
  'provider-reference',
  25,
  now(),
  3,
  'api-football:leagues:{"current":true}'
WHERE NOT EXISTS (
  SELECT 1
  FROM jobs
  WHERE idempotency_key = 'api-football:leagues:{"current":true}'
    AND status IN ('queued', 'running', 'completed')
);

INSERT INTO jobs (
  job_key,
  status,
  payload,
  queue,
  priority,
  run_after,
  max_attempts,
  idempotency_key
)
SELECT
  'provider.api-football.fixtures',
  'queued',
  '{"endpoint":"fixtures","date":"today"}'::jsonb,
  'provider-fixtures',
  30,
  now(),
  3,
  'api-football:fixtures:{"date":"today"}'
WHERE NOT EXISTS (
  SELECT 1
  FROM jobs
  WHERE idempotency_key = 'api-football:fixtures:{"date":"today"}'
    AND status IN ('queued', 'running', 'completed')
);

INSERT INTO jobs (
  job_key,
  status,
  payload,
  queue,
  priority,
  run_after,
  max_attempts,
  idempotency_key
)
SELECT
  'predictions.generate.upcoming',
  'queued',
  '{"batch_size":100}'::jsonb,
  'predictions',
  40,
  now(),
  3,
  'predictions:generate:upcoming:foundation'
WHERE NOT EXISTS (
  SELECT 1
  FROM jobs
  WHERE idempotency_key = 'predictions:generate:upcoming:foundation'
    AND status IN ('queued', 'running', 'completed')
);

INSERT INTO jobs (
  job_key,
  status,
  payload,
  queue,
  priority,
  run_after,
  max_attempts,
  idempotency_key
)
SELECT
  'predictions.settle.finished',
  'queued',
  '{"batch_size":100}'::jsonb,
  'predictions',
  60,
  now(),
  3,
  'predictions:settle:finished:foundation'
WHERE NOT EXISTS (
  SELECT 1
  FROM jobs
  WHERE idempotency_key = 'predictions:settle:finished:foundation'
    AND status IN ('queued', 'running', 'completed')
);

-- Register the already-running v1 engine without inventing historical
-- training/artifact precision. This catalogue is the set of market families
-- emitted by python-engine-v1 today; new families require an explicit model
-- registration rather than inheriting v1 by accident.
CREATE OR REPLACE FUNCTION engine_prediction_market_family(market_name TEXT)
RETURNS TEXT
LANGUAGE SQL
IMMUTABLE
STRICT
PARALLEL SAFE
AS $function$
  SELECT CASE
    WHEN market_name IN ('1x2', 'draw_no_bet', 'half_time_result') THEN '1x2'
    WHEN market_name = 'double_chance' THEN 'double_chance'
    WHEN market_name IN ('btts', 'btts_total_goals', 'btts_match_result') THEN 'btts'
    WHEN market_name IN ('total_goals', 'highest_scoring_half', 'odd_even_total_goals', 'result_total_goals') THEN 'total_goals'
    WHEN market_name = 'team_to_score_first' THEN 'team_goals'
    WHEN market_name = 'correct_score' THEN 'correct_score'
    WHEN market_name IN ('total_corners', 'team_corners_home', 'team_corners_away') THEN 'corners'
    WHEN market_name = 'total_cards' THEN 'cards'
    WHEN market_name = 'total_fouls' THEN 'fouls'
    WHEN market_name IN ('total_shots', 'shots_on_target') THEN 'shots'
    WHEN market_name = 'possession_winner' THEN 'possession'
  END
$function$;

INSERT INTO market_metadata (
  market_family, display_name, supported_lifecycle_stages, settlement_version,
  calibration_required, active, metadata
)
VALUES
  ('1x2', 'Match result family', ARRAY['EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED'], 'v1', true, true, '{"bootstrap":"eng-003","market_mapping":"explicit"}'),
  ('double_chance', 'Double chance', ARRAY['EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED'], 'v1', true, true, '{"bootstrap":"eng-003","market_mapping":"explicit"}'),
  ('btts', 'Both teams to score family', ARRAY['EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED'], 'v1', true, true, '{"bootstrap":"eng-003","market_mapping":"explicit"}'),
  ('total_goals', 'Total goals family', ARRAY['EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED'], 'v1', true, true, '{"bootstrap":"eng-003","market_mapping":"explicit"}'),
  ('team_goals', 'Team goals family', ARRAY['EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED'], 'v1', true, true, '{"bootstrap":"eng-003","market_mapping":"explicit"}'),
  ('correct_score', 'Correct score', ARRAY['EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED'], 'v1', true, true, '{"bootstrap":"eng-003","market_mapping":"explicit"}'),
  ('corners', 'Corners family', ARRAY['EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED'], 'v1', true, true, '{"bootstrap":"eng-003","market_mapping":"explicit"}'),
  ('cards', 'Cards family', ARRAY['EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED'], 'v1', true, true, '{"bootstrap":"eng-003","market_mapping":"explicit"}'),
  ('fouls', 'Fouls family', ARRAY['EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED'], 'v1', true, true, '{"bootstrap":"eng-003","market_mapping":"explicit"}'),
  ('shots', 'Shots family', ARRAY['EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED'], 'v1', true, true, '{"bootstrap":"eng-003","market_mapping":"explicit"}'),
  ('possession', 'Possession', ARRAY['EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED'], 'v1', true, true, '{"bootstrap":"eng-003","market_mapping":"explicit"}')
ON CONFLICT (market_family) DO NOTHING;

DO $bootstrap$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM market_metadata
    WHERE active = true
      AND market_family NOT IN (
        '1x2', 'double_chance', 'btts', 'total_goals', 'team_goals',
        'correct_score', 'corners', 'cards', 'fouls', 'shots', 'possession'
      )
  ) THEN
    RAISE EXCEPTION
      'active market family has no reviewed python-engine-v1 mapping; register it explicitly before ENG-003';
  END IF;
END
$bootstrap$;

INSERT INTO model_registry (
  id, model_key, model_version, market_family, runtime, feature_version,
  hyperparameters, code_commit, status, historical_metadata_approximate,
  audit_metadata
)
SELECT
  (
    substr(md5('sokalens:python-engine-v1:' || market_family), 1, 8) || '-' ||
    substr(md5('sokalens:python-engine-v1:' || market_family), 9, 4) || '-' ||
    substr(md5('sokalens:python-engine-v1:' || market_family), 13, 4) || '-' ||
    substr(md5('sokalens:python-engine-v1:' || market_family), 17, 4) || '-' ||
    substr(md5('sokalens:python-engine-v1:' || market_family), 21, 12)
  )::uuid,
  'python-engine-v1',
  'python-engine-v1',
  market_family,
  'python-offline',
  'python-engine-v1',
  '{"configuration_metadata":{"status":"historical_configuration_unavailable","approximate":true}}'::jsonb,
  'historical-commit-unavailable',
  'production',
  true,
  jsonb_build_object(
    'bootstrap', 'eng-003',
    'identity_source', 'running_engine_model_version',
    'historical_fields_approximate', true,
    'unknown_fields', jsonb_build_array(
      'artifact_uri', 'artifact_hash', 'configuration_hash', 'training_start',
      'training_end', 'training_cutoff_at', 'training_dataset_hash', 'code_commit',
      'promoted_at'
    )
  )
FROM market_metadata
WHERE active = true
ON CONFLICT (model_key, model_version, market_family) DO NOTHING;

DO $champion_check$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM market_metadata metadata
    LEFT JOIN model_registry registry
      ON registry.market_family = metadata.market_family
     AND registry.model_key = 'python-engine-v1'
     AND registry.model_version = 'python-engine-v1'
     AND registry.status = 'production'
    WHERE metadata.active = true
    GROUP BY metadata.market_family
    HAVING count(registry.id) <> 1
  ) THEN
    RAISE EXCEPTION
      'every active market family must have exactly one production python-engine-v1 champion';
  END IF;
END
$champion_check$;

-- Link only exact v1 identities and reviewed market aliases. Rows with any
-- other model version or market stay NULL for explicit review. Batches bound
-- row locks while allowing an idempotent rerun after interruption.
DO $backfill$
DECLARE
  affected_rows INTEGER;
BEGIN
  LOOP
    WITH targets AS (
      SELECT ep.id
      FROM engine_predictions ep
      WHERE ep.model_registry_id IS NULL
        AND ep.model_version = 'python-engine-v1'
        AND engine_prediction_market_family(ep.market) IS NOT NULL
      ORDER BY ep.id
      LIMIT 1000
      FOR UPDATE SKIP LOCKED
    ), mapped AS (
      SELECT
        ep.id,
        engine_prediction_market_family(ep.market) AS market_family
      FROM engine_predictions ep
      JOIN targets ON targets.id = ep.id
    )
    UPDATE engine_predictions ep
    SET model_registry_id = registry.id
    FROM mapped
    JOIN model_registry registry
      ON registry.model_key = 'python-engine-v1'
     AND registry.model_version = 'python-engine-v1'
     AND registry.market_family = mapped.market_family
     AND registry.status = 'production'
    WHERE ep.id = mapped.id
      AND ep.model_registry_id IS NULL;

    GET DIAGNOSTICS affected_rows = ROW_COUNT;
    EXIT WHEN affected_rows = 0;
  END LOOP;
END
$backfill$;

-- Preserve settled v1 evidence that predates the online dual-writer. Only
-- structurally complete settlements are admitted. Missing/contradictory
-- historical data remains absent instead of being reconstructed.
DO $fact_backfill$
DECLARE
  affected_rows INTEGER;
BEGIN
  LOOP
    WITH targets AS (
      SELECT results.prediction_id
      FROM prediction_results results
      JOIN engine_predictions prediction ON prediction.id = results.prediction_id
      JOIN model_registry registry ON registry.id = prediction.model_registry_id
      LEFT JOIN model_evaluation_facts facts ON facts.prediction_id = results.prediction_id
      WHERE facts.prediction_id IS NULL
        AND prediction.model_version = 'python-engine-v1'
        AND registry.model_key = 'python-engine-v1'
        AND registry.model_version = 'python-engine-v1'
        AND registry.audit_metadata ->> 'bootstrap' = 'eng-003'
        AND results.settlement_status IN ('settled', 'void', 'unsettled_data_unavailable')
        AND results.settled_at >= prediction.generated_at
        AND (
          (results.settlement_status = 'settled' AND results.actual IS NOT NULL AND results.is_correct IS NOT NULL)
          OR results.settlement_status IN ('void', 'unsettled_data_unavailable')
        )
      ORDER BY results.prediction_id
      LIMIT 1000
      FOR UPDATE OF results SKIP LOCKED
    )
    INSERT INTO model_evaluation_facts (
      prediction_id, model_registry_id, fixture_id, market, selection,
      lifecycle_stage, prediction_timestamp, data_cutoff_at, feature_version,
      raw_probability, calibrated_probability, confidence, final_outcome,
      settlement_classification, settled_at, odds_snapshot_id,
      evaluation_eligible, exclusion_reason, evaluation_contract_version
    )
    SELECT
      prediction.id,
      prediction.model_registry_id,
      prediction.fixture_id,
      prediction.market,
      prediction.prediction,
      prediction.lifecycle_stage,
      prediction.generated_at,
      prediction.data_cutoff_at,
      prediction.feature_version,
      COALESCE(prediction.raw_probability, prediction.probability),
      prediction.calibrated_probability,
      prediction.confidence,
      COALESCE(results.actual, 'data_unavailable'),
      CASE
        WHEN results.settlement_status = 'void' THEN 'void'
        WHEN results.settlement_status = 'unsettled_data_unavailable' THEN 'excluded'
        WHEN results.is_correct THEN 'correct'
        ELSE 'incorrect'
      END,
      results.settled_at,
      prediction.odds_snapshot_id,
      results.settlement_status = 'settled',
      CASE
        WHEN results.settlement_status = 'void' THEN 'settlement_void'
        WHEN results.settlement_status = 'unsettled_data_unavailable' THEN 'unsettled_data_unavailable'
      END,
      'model-evaluation-v1-bootstrap-approximate'
    FROM targets
    JOIN prediction_results results ON results.prediction_id = targets.prediction_id
    JOIN engine_predictions prediction ON prediction.id = targets.prediction_id
    ON CONFLICT (prediction_id) DO NOTHING;

    GET DIAGNOSTICS affected_rows = ROW_COUNT;
    EXIT WHEN affected_rows = 0;
  END LOOP;
END
$fact_backfill$;

DO $unlinked_report$
DECLARE
  unsupported_v1_markets BIGINT;
  other_model_versions BIGINT;
BEGIN
  SELECT count(*) INTO unsupported_v1_markets
  FROM engine_predictions
  WHERE model_registry_id IS NULL
    AND model_version = 'python-engine-v1'
    AND engine_prediction_market_family(market) IS NULL;

  SELECT count(*) INTO other_model_versions
  FROM engine_predictions
  WHERE model_registry_id IS NULL
    AND model_version <> 'python-engine-v1';

  RAISE NOTICE
    'ENG-003 left % unsupported python-engine-v1 markets and % other model-version rows unlinked for review',
    unsupported_v1_markets, other_model_versions;
END
$unlinked_report$;

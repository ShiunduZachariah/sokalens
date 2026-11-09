-- python-engine-v2: Model A fits team ratings on the whole league. Every published
-- market moves, so its forecasts need their own identity in the registry and the
-- accuracy ledger. Evidence: docs/prediction-engine/model-a-expected-goals.md.
-- The "v2" counts champion versions; it is not the architecture v2 specification.
UPDATE model_registry
SET status = 'retired',
    retired_at = now()
WHERE model_key = 'python-engine-v1'
  AND model_version = 'python-engine-v1'
  AND status = 'production';

-- The conflict clause restores these rows on an up/down/up cycle. Any other
-- production model in a family breaks the one-champion index and fails the deploy.
INSERT INTO model_registry (
  id, model_key, model_version, market_family, runtime, feature_version,
  validation_window, hyperparameters, code_commit, status, promoted_at,
  audit_metadata
)
SELECT
  (
    substr(md5('sokalens:python-engine-v2:' || market_family), 1, 8) || '-' ||
    substr(md5('sokalens:python-engine-v2:' || market_family), 9, 4) || '-' ||
    substr(md5('sokalens:python-engine-v2:' || market_family), 13, 4) || '-' ||
    substr(md5('sokalens:python-engine-v2:' || market_family), 17, 4) || '-' ||
    substr(md5('sokalens:python-engine-v2:' || market_family), 21, 12)
  )::uuid,
  'python-engine-v2',
  'python-engine-v2',
  market_family,
  'python-offline',
  'python-engine-v2',
  '{"tuning":{"start":"2019-07-01","end":"2022-06-30"},"holdout":{"start":"2022-07-01","fixtures":48591,"leagues":39}}'::jsonb,
  '{"team_strength_sample":"whole_league_two_seasons","decay":0.98,"shrinkage_matches":12,"goal_rate":"fitted_league_rate"}'::jsonb,
  'see-pull-request-360',
  'production',
  now(),
  '{"supersedes":"python-engine-v1","evidence":"docs/prediction-engine/model-a-expected-goals.md"}'::jsonb
FROM model_registry
WHERE model_key = 'python-engine-v1'
  AND model_version = 'python-engine-v1'
ON CONFLICT (model_key, model_version, market_family) DO UPDATE
SET status = 'production',
    promoted_at = now(),
    retired_at = NULL;

DO $champion_check$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM market_metadata metadata
    LEFT JOIN model_registry registry
      ON registry.market_family = metadata.market_family
     AND registry.model_key = 'python-engine-v2'
     AND registry.model_version = 'python-engine-v2'
     AND registry.status = 'production'
    WHERE metadata.active = true
    GROUP BY metadata.market_family
    HAVING count(registry.id) <> 1
  ) THEN
    RAISE EXCEPTION
      'every active market family must have exactly one production python-engine-v2 champion';
  END IF;
END
$champion_check$;

-- Migrations run before the new containers start, and an automatic rollback
-- restores the old image without reversing them. In both cases the old engine
-- looks up the production row for python-engine-v1, finds none and writes NULL,
-- and settlement skips a prediction with no model. This links such a row to the
-- exact model that wrote it. Unknown versions and markets stay NULL for review.
CREATE FUNCTION link_engine_prediction_to_its_model()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $function$
BEGIN
  IF NEW.model_registry_id IS NULL THEN
    SELECT registry.id INTO NEW.model_registry_id
    FROM model_registry AS registry
    WHERE registry.model_key = NEW.model_version
      AND registry.model_version = NEW.model_version
      AND registry.market_family = engine_prediction_market_family(NEW.market);
  END IF;
  RETURN NEW;
END
$function$;

CREATE TRIGGER engine_predictions_link_model
BEFORE INSERT ON engine_predictions
FOR EACH ROW EXECUTE FUNCTION link_engine_prediction_to_its_model();

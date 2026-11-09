-- python-engine-v3: published markets now use Models B and C where the holdout shows
-- them beating the champion, and double chance and draw no bet follow the final 1X2 on
-- every path. Evidence: docs/prediction-engine/promoted-markets.md.
--
-- The owner took this decision on 2026-11-05 ahead of the section 21.4 shadow minimum of
-- 500 settled forecasts. Live shadow had none settled, so the evidence is the 2022-07-01
-- holdout, and the audit metadata says so.
UPDATE model_registry
SET status = 'retired',
    retired_at = now()
WHERE model_key = 'python-engine-v2'
  AND model_version = 'python-engine-v2'
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
    substr(md5('sokalens:python-engine-v3:' || market_family), 1, 8) || '-' ||
    substr(md5('sokalens:python-engine-v3:' || market_family), 9, 4) || '-' ||
    substr(md5('sokalens:python-engine-v3:' || market_family), 13, 4) || '-' ||
    substr(md5('sokalens:python-engine-v3:' || market_family), 17, 4) || '-' ||
    substr(md5('sokalens:python-engine-v3:' || market_family), 21, 12)
  )::uuid,
  'python-engine-v3',
  'python-engine-v3',
  market_family,
  'python-offline',
  'python-engine-v3',
  '{"tuning":{"start":"2021-07-01","end":"2022-06-30"},"holdout":{"start":"2022-07-01","fixtures":48591}}'::jsonb,
  '{"champion":["1x2","double_chance","draw_no_bet","odd_even_total_goals","team_to_score_first","possession_winner"],"champion_b_c_mix":["total_goals","btts","btts_match_result","btts_total_goals","result_total_goals","correct_score"],"model_b":["half_time_result","highest_scoring_half"],"model_c":["total_corners","team_corners_home","team_corners_away","total_cards","total_fouls","total_shots","shots_on_target"]}'::jsonb,
  'see-pull-request',
  'production',
  now(),
  '{"supersedes":"python-engine-v2","evidence":"docs/prediction-engine/promoted-markets.md","decided_by":"owner","decided_on":"2026-11-05","section_21_4_shadow_minimum_met":false,"settled_shadow_forecasts_at_decision":0,"calibrated":false}'::jsonb
FROM model_registry
WHERE model_key = 'python-engine-v2'
  AND model_version = 'python-engine-v2'
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
     AND registry.model_key = 'python-engine-v3'
     AND registry.model_version = 'python-engine-v3'
     AND registry.status = 'production'
    WHERE metadata.active = true
    GROUP BY metadata.market_family
    HAVING count(registry.id) <> 1
  ) THEN
    RAISE EXCEPTION
      'every active market family must have exactly one production python-engine-v3 champion';
  END IF;
END
$champion_check$;

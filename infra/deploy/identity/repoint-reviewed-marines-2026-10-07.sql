-- Restore the provider map after the old TheSportsDB history writer observed
-- the reviewed alias. Run with ON_ERROR_STOP=1 in an explicit transaction,
-- first with ROLLBACK and then with COMMIT after the worker fix is deployed.
-- The alias rows, matches, fixtures, scores, and last_seen_at are untouched.
DO $reviewed_repoint$
DECLARE
  provider_map provider_entity_maps%ROWTYPE;
  updated_count integer;
BEGIN
  PERFORM pg_advisory_xact_lock(20261006, 87);
  IF NOT EXISTS (
    SELECT 1 FROM fixture_identity_aliases
    WHERE old_id = 7816414 AND canonical_id = 1803076
      AND operation_id = 'reviewed-2026-10-07-marines-2020102' AND revoked_at IS NULL
  ) OR NOT EXISTS (
    SELECT 1 FROM team_identity_aliases
    WHERE old_id = 6743190 AND canonical_id = 3861947
      AND operation_id = 'reviewed-2026-10-07-marines-2020102' AND revoked_at IS NULL
  ) THEN
    RAISE EXCEPTION 'reviewed Marines alias is absent or changed';
  END IF;
  IF (SELECT count(*) FROM matches WHERE id IN (7816414, 1803076)
        AND league_id = 1213 AND kickoff_at = '2024-05-11 13:00:00+00'::timestamptz
        AND home_team_id = 3400946 AND status = 'finished'
        AND home_score = 3 AND away_score = 1) <> 2
     OR (SELECT count(*) FROM fixtures WHERE id IN (7816414, 1803076)
        AND league_id = 1213 AND kickoff_at = '2024-05-11 13:00:00+00'::timestamptz
        AND home_team_id = 3400946 AND status = 'finished'
        AND home_goals_ft = 3 AND away_goals_ft = 1) <> 2
     OR (SELECT count(*) FROM matches WHERE away_team_id = 6743190) <> 1
     OR (SELECT count(*) FROM fixtures WHERE away_team_id = 6743190) <> 1 THEN
    RAISE EXCEPTION 'reviewed Marines sporting or team references changed';
  END IF;
  IF (SELECT away_team_id FROM matches WHERE id = 7816414) IS DISTINCT FROM 6743190
     OR (SELECT away_team_id FROM matches WHERE id = 1803076) IS DISTINCT FROM 3861947
     OR (SELECT away_team_id FROM fixtures WHERE id = 7816414) IS DISTINCT FROM 6743190
     OR (SELECT away_team_id FROM fixtures WHERE id = 1803076) IS DISTINCT FROM 3861947 THEN
    RAISE EXCEPTION 'reviewed Marines away-team identity changed';
  END IF;
  SELECT * INTO provider_map FROM provider_entity_maps
  WHERE provider = 'thesportsdb' AND entity_type = 'match'
    AND provider_entity_id = '2020102' AND retired_at IS NULL FOR UPDATE;
  IF provider_map.id IS NULL OR provider_map.canonical_entity_id NOT IN ('7816414', '1803076') THEN
    RAISE EXCEPTION 'TheSportsDB fixture map changed to an unreviewed target';
  END IF;
  IF provider_map.canonical_entity_id = '1803076' THEN
    RETURN;
  END IF;
  UPDATE provider_entity_maps SET canonical_entity_id = '1803076'
  WHERE id = provider_map.id AND canonical_entity_id = '7816414';
  GET DIAGNOSTICS updated_count = ROW_COUNT;
  IF updated_count <> 1 THEN
    RAISE EXCEPTION 'reviewed provider map repoint did not update exactly one row';
  END IF;
  INSERT INTO audit_logs (action, entity_type, entity_id, reason, before_value, after_value)
  VALUES ('reviewed_identity_alias.repoint_provider', 'fixture', '7816414',
    'TheSportsDB history writer replayed the old mapped fixture after reviewed alias activation',
    jsonb_build_object('provider_map', to_jsonb(provider_map)),
    jsonb_build_object('canonical_fixture_id', 1803076,
      'alias_operation_id', 'reviewed-2026-10-07-marines-2020102'));
END;
$reviewed_repoint$;

SELECT canonical_entity_id FROM provider_entity_maps
WHERE provider = 'thesportsdb' AND entity_type = 'match'
  AND provider_entity_id = '2020102' AND retired_at IS NULL;

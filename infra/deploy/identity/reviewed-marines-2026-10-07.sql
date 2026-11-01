-- Reviewed, single-pair historical identity operation. Run through psql with
-- ON_ERROR_STOP=1 inside an explicit transaction; verify the SELECT below
-- before COMMIT. The same SQL can be exercised with ROLLBACK in QA.
--
-- 2024-05-11 Sunrise 3-1 Marines, National Soccer League. The older
-- API-Football-backed row 1803076 and the later TheSportsDB row 7816414
-- have identical league, kickoff, home identity, final score and names.
-- The latter has no publication, result, event, odds, statistics, favorite or
-- notification references. Its one-off Marines team 6743190 has no other
-- match. All rows remain stored and the alias can be revoked with a new audit
-- operation; this script never changes sporting status or prediction history.
DO $reviewed_alias$
DECLARE
  old_match matches%ROWTYPE;
  canonical_match matches%ROWTYPE;
  old_fixture fixtures%ROWTYPE;
  canonical_fixture fixtures%ROWTYPE;
  old_team teams%ROWTYPE;
  canonical_team teams%ROWTYPE;
  provider_map provider_entity_maps%ROWTYPE;
  ref record;
  ref_count bigint;
  updated_count integer;
  operation text := 'reviewed-2026-10-07-marines-2020102';
  reason text := 'same National Soccer League fixture, exact kickoff/opponent/3-1 score; duplicate TheSportsDB team has one match and no publications';
BEGIN
  PERFORM pg_advisory_xact_lock(20261006, 87);
  PERFORM id FROM matches WHERE id IN (1803076, 7816414) ORDER BY id FOR UPDATE;
  PERFORM id FROM teams WHERE id IN (3861947, 6743190) ORDER BY id FOR UPDATE;
  SELECT * INTO old_match FROM matches WHERE id = 7816414;
  SELECT * INTO canonical_match FROM matches WHERE id = 1803076;
  SELECT * INTO old_fixture FROM fixtures WHERE id = 7816414;
  SELECT * INTO canonical_fixture FROM fixtures WHERE id = 1803076;
  SELECT * INTO old_team FROM teams WHERE id = 6743190;
  SELECT * INTO canonical_team FROM teams WHERE id = 3861947;

  IF old_match.id IS NULL OR canonical_match.id IS NULL OR old_fixture.id IS NULL
     OR canonical_fixture.id IS NULL OR old_team.id IS NULL OR canonical_team.id IS NULL THEN
    RAISE EXCEPTION 'reviewed Marines identity rows missing';
  END IF;
  IF old_team.name IS DISTINCT FROM 'Marines' OR canonical_team.name IS DISTINCT FROM 'Marines'
     OR old_team.slug IS DISTINCT FROM 'team-marines' OR canonical_team.slug IS DISTINCT FROM 'team-rwanda-marines'
     OR old_team.logo_url IS NOT NULL THEN
    RAISE EXCEPTION 'Marines team identity changed since review';
  END IF;
  IF old_match.league_id IS DISTINCT FROM 1213 OR canonical_match.league_id IS DISTINCT FROM 1213
     OR old_match.home_team_id IS DISTINCT FROM 3400946 OR canonical_match.home_team_id IS DISTINCT FROM 3400946
     OR old_match.away_team_id IS DISTINCT FROM 6743190 OR canonical_match.away_team_id IS DISTINCT FROM 3861947
     OR old_match.kickoff_at IS DISTINCT FROM '2024-05-11 13:00:00+00'::timestamptz
     OR canonical_match.kickoff_at IS DISTINCT FROM old_match.kickoff_at
     OR old_match.status IS DISTINCT FROM 'finished' OR canonical_match.status IS DISTINCT FROM 'finished'
     OR old_match.home_score IS DISTINCT FROM 3 OR canonical_match.home_score IS DISTINCT FROM 3
     OR old_match.away_score IS DISTINCT FROM 1 OR canonical_match.away_score IS DISTINCT FROM 1
     OR old_fixture.league_id IS DISTINCT FROM 1213 OR canonical_fixture.league_id IS DISTINCT FROM 1213
     OR old_fixture.home_team_id IS DISTINCT FROM 3400946 OR canonical_fixture.home_team_id IS DISTINCT FROM 3400946
     OR old_fixture.away_team_id IS DISTINCT FROM 6743190 OR canonical_fixture.away_team_id IS DISTINCT FROM 3861947
     OR old_fixture.kickoff_at IS DISTINCT FROM old_match.kickoff_at
     OR canonical_fixture.kickoff_at IS DISTINCT FROM canonical_match.kickoff_at
     OR old_fixture.status IS DISTINCT FROM 'finished' OR canonical_fixture.status IS DISTINCT FROM 'finished'
     OR old_fixture.season_id IS DISTINCT FROM 2024 OR canonical_fixture.season_id IS DISTINCT FROM 2024
     OR old_fixture.home_goals_ft IS DISTINCT FROM 3 OR canonical_fixture.home_goals_ft IS DISTINCT FROM 3
     OR old_fixture.away_goals_ft IS DISTINCT FROM 1 OR canonical_fixture.away_goals_ft IS DISTINCT FROM 1
     OR old_fixture.source IS DISTINCT FROM 'thesportsdb' OR canonical_fixture.source IS DISTINCT FROM 'thesportsdb' THEN
    RAISE EXCEPTION 'Marines fixture identity or score changed since review';
  END IF;

  -- Check every physical fixture/match reference, including tables added
  -- after this review. The old row may remain in matches/fixtures themselves,
  -- but must not have a publication or user-owned child reference.
  FOR ref IN
    SELECT DISTINCT c.relname AS table_name, a.attname AS column_name
    FROM pg_class c
    JOIN pg_namespace ns ON ns.oid = c.relnamespace
    JOIN pg_attribute a ON a.attrelid = c.oid
    WHERE ns.nspname = 'public' AND c.relkind IN ('r', 'p')
      AND NOT c.relispartition AND NOT a.attisdropped
      AND a.attname IN ('fixture_id', 'match_id')
  LOOP
    EXECUTE format('SELECT count(*) FROM public.%I WHERE %I = $1', ref.table_name, ref.column_name)
      INTO ref_count USING 7816414::bigint;
    IF ref_count <> 0 THEN
      RAISE EXCEPTION 'old fixture has % references in %.%', ref_count, ref.table_name, ref.column_name;
    END IF;
  END LOOP;
  FOR ref IN
    SELECT DISTINCT c.relname AS table_name, a.attname AS column_name
    FROM pg_class c
    JOIN pg_namespace ns ON ns.oid = c.relnamespace
    JOIN pg_attribute a ON a.attrelid = c.oid
    WHERE ns.nspname = 'public' AND c.relkind IN ('r', 'p')
      AND NOT c.relispartition AND NOT a.attisdropped
      AND a.attname IN ('team_id', 'home_team_id', 'away_team_id')
  LOOP
    EXECUTE format('SELECT count(*) FROM public.%I WHERE %I = $1', ref.table_name, ref.column_name)
      INTO ref_count USING 6743190::bigint;
    IF (ref.table_name, ref.column_name) IN (('matches', 'away_team_id'), ('fixtures', 'away_team_id')) THEN
      IF ref_count <> 1 THEN
        RAISE EXCEPTION 'old team has % references in %.%, expected one', ref_count, ref.table_name, ref.column_name;
      END IF;
    ELSIF ref_count <> 0 THEN
      RAISE EXCEPTION 'old team has % unreviewed references in %.%', ref_count, ref.table_name, ref.column_name;
    END IF;
  END LOOP;
  IF EXISTS (SELECT 1 FROM entity_favorites WHERE entity_id IN ('6743190','7816414'))
     OR EXISTS (SELECT 1 FROM notifications WHERE entity_id IN ('6743190','7816414')) THEN
    RAISE EXCEPTION 'old identity gained a user reference';
  END IF;

  SELECT * INTO provider_map FROM provider_entity_maps
  WHERE provider = 'thesportsdb' AND entity_type = 'match'
    AND provider_entity_id = '2020102' AND retired_at IS NULL FOR UPDATE;
  IF provider_map.id IS NULL OR provider_map.canonical_entity_id IS DISTINCT FROM '7816414'
     OR EXISTS (SELECT 1 FROM provider_entity_maps WHERE canonical_entity_id IN ('6743190','7816414') AND id <> provider_map.id AND retired_at IS NULL) THEN
    RAISE EXCEPTION 'provider mapping changed since review';
  END IF;

  IF EXISTS (SELECT 1 FROM team_identity_aliases WHERE old_id = 6743190)
     OR EXISTS (SELECT 1 FROM fixture_identity_aliases WHERE old_id = 7816414) THEN
    RAISE EXCEPTION 'identity pair was already reviewed; do not replay this operation';
  END IF;
  INSERT INTO team_identity_aliases (old_id, canonical_id, operation_id, reason, evidence)
  VALUES (6743190, 3861947, operation, reason,
    jsonb_build_object('old_team', to_jsonb(old_team), 'canonical_team', to_jsonb(canonical_team),
      'fixture_pair', jsonb_build_array(7816414, 1803076), 'reviewed_at', now()));
  INSERT INTO fixture_identity_aliases (old_id, canonical_id, operation_id, reason, evidence)
  VALUES (7816414, 1803076, operation, reason,
    jsonb_build_object('old_match', to_jsonb(old_match), 'canonical_match', to_jsonb(canonical_match),
      'old_fixture', to_jsonb(old_fixture), 'canonical_fixture', to_jsonb(canonical_fixture),
      'provider_map_before', to_jsonb(provider_map), 'reviewed_at', now()));
  -- Preserve last_seen_at: reviewing identity is not a provider observation.
  UPDATE provider_entity_maps SET canonical_entity_id = '1803076'
  WHERE id = provider_map.id AND canonical_entity_id = '7816414';
  GET DIAGNOSTICS updated_count = ROW_COUNT;
  IF updated_count <> 1 THEN
    RAISE EXCEPTION 'provider map repoint did not update exactly one row';
  END IF;
  INSERT INTO audit_logs (action, entity_type, entity_id, reason, before_value, after_value)
  VALUES ('reviewed_identity_alias.activate', 'fixture', '7816414', reason,
    jsonb_build_object('old_match', to_jsonb(old_match), 'provider_map', to_jsonb(provider_map)),
    jsonb_build_object('operation_id', operation, 'canonical_fixture_id', 1803076,
      'canonical_team_id', 3861947));
END;
$reviewed_alias$;

SELECT operation_id, old_id, canonical_id FROM team_identity_aliases WHERE old_id = 6743190
UNION ALL SELECT operation_id, old_id, canonical_id FROM fixture_identity_aliases WHERE old_id = 7816414;

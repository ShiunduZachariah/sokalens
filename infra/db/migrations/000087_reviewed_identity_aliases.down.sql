-- A rollback must not erase an activated identity decision or its provenance.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM fixture_identity_aliases)
     OR EXISTS (SELECT 1 FROM team_identity_aliases) THEN
    RAISE EXCEPTION 'reviewed identity aliases contain history; revoke and archive them before schema rollback';
  END IF;
END;
$$;

DROP TABLE IF EXISTS fixture_identity_aliases;
DROP TABLE IF EXISTS team_identity_aliases;
DROP FUNCTION IF EXISTS guard_reviewed_identity_alias();

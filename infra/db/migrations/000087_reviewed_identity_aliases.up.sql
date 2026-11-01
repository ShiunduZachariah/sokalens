-- An identity decision is separate from the sporting lifecycle and from the
-- immutable publications attached to either original row. No aliases are
-- seeded here: activation requires a reviewed, fixture-specific operation.
CREATE TABLE fixture_identity_aliases (
  old_id BIGINT PRIMARY KEY REFERENCES fixtures(id) ON DELETE RESTRICT,
  canonical_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE RESTRICT,
  operation_id TEXT NOT NULL,
  reason TEXT NOT NULL,
  evidence JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_at TIMESTAMPTZ,
  revocation_operation_id TEXT,
  revocation_reason TEXT,
  CONSTRAINT fixture_identity_alias_distinct CHECK (old_id <> canonical_id),
  CONSTRAINT fixture_identity_alias_operation CHECK (length(btrim(operation_id)) > 0),
  CONSTRAINT fixture_identity_alias_reason CHECK (length(btrim(reason)) > 0),
  CONSTRAINT fixture_identity_alias_evidence CHECK (jsonb_typeof(evidence) = 'object' AND evidence <> '{}'::jsonb),
  CONSTRAINT fixture_identity_alias_revocation CHECK (
    (revoked_at IS NULL AND revocation_operation_id IS NULL AND revocation_reason IS NULL)
    OR (revoked_at IS NOT NULL AND revocation_operation_id IS NOT NULL AND revocation_reason IS NOT NULL
      AND length(btrim(revocation_operation_id)) > 0 AND length(btrim(revocation_reason)) > 0)
  )
);

CREATE INDEX fixture_identity_aliases_canonical_idx ON fixture_identity_aliases (canonical_id);

CREATE TABLE team_identity_aliases (
  old_id BIGINT PRIMARY KEY REFERENCES teams(id) ON DELETE RESTRICT,
  canonical_id BIGINT NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
  operation_id TEXT NOT NULL,
  reason TEXT NOT NULL,
  evidence JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_at TIMESTAMPTZ,
  revocation_operation_id TEXT,
  revocation_reason TEXT,
  CONSTRAINT team_identity_alias_distinct CHECK (old_id <> canonical_id),
  CONSTRAINT team_identity_alias_operation CHECK (length(btrim(operation_id)) > 0),
  CONSTRAINT team_identity_alias_reason CHECK (length(btrim(reason)) > 0),
  CONSTRAINT team_identity_alias_evidence CHECK (jsonb_typeof(evidence) = 'object' AND evidence <> '{}'::jsonb),
  CONSTRAINT team_identity_alias_revocation CHECK (
    (revoked_at IS NULL AND revocation_operation_id IS NULL AND revocation_reason IS NULL)
    OR (revoked_at IS NOT NULL AND revocation_operation_id IS NOT NULL AND revocation_reason IS NOT NULL
      AND length(btrim(revocation_operation_id)) > 0 AND length(btrim(revocation_reason)) > 0)
  )
);

CREATE INDEX team_identity_aliases_canonical_idx ON team_identity_aliases (canonical_id);

-- The advisory lock serializes identity decisions across both alias tables.
-- It also rejects chains, cycles and replacing a target used by an earlier
-- active decision. An UPDATE can only revoke an active alias, recording a new
-- operation; identities and original evidence are never rewritten.
CREATE FUNCTION guard_reviewed_identity_alias() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
  alias_table regclass := TG_RELID;
  conflicting BOOLEAN;
BEGIN
  IF TG_OP = 'DELETE' THEN
    RAISE EXCEPTION 'reviewed identity aliases cannot be deleted; revoke with an audited operation';
  END IF;
  IF TG_OP = 'UPDATE' THEN
    PERFORM pg_advisory_xact_lock(20261006, 87);
    IF OLD.revoked_at IS NOT NULL
       OR NEW.revoked_at IS NULL
       OR NEW.old_id IS DISTINCT FROM OLD.old_id
       OR NEW.canonical_id IS DISTINCT FROM OLD.canonical_id
       OR NEW.operation_id IS DISTINCT FROM OLD.operation_id
       OR NEW.reason IS DISTINCT FROM OLD.reason
       OR NEW.evidence IS DISTINCT FROM OLD.evidence
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
      RAISE EXCEPTION 'reviewed identity alias may only be revoked, not rewritten';
    END IF;
    RETURN NEW;
  END IF;
  PERFORM pg_advisory_xact_lock(20261006, 87);
  IF NEW.revoked_at IS NOT NULL THEN
    RAISE EXCEPTION 'reviewed identity alias must be activated before it can be revoked';
  END IF;
  EXECUTE format('SELECT EXISTS (SELECT 1 FROM %s WHERE revoked_at IS NULL AND (old_id = $1 OR canonical_id = $2))', alias_table)
    INTO conflicting USING NEW.canonical_id, NEW.old_id;
  IF conflicting THEN
    RAISE EXCEPTION 'identity alias target/source already participates in an alias chain';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER guard_fixture_identity_alias BEFORE INSERT OR UPDATE OR DELETE ON fixture_identity_aliases
FOR EACH ROW EXECUTE FUNCTION guard_reviewed_identity_alias();
CREATE TRIGGER guard_team_identity_alias BEFORE INSERT OR UPDATE OR DELETE ON team_identity_aliases
FOR EACH ROW EXECUTE FUNCTION guard_reviewed_identity_alias();

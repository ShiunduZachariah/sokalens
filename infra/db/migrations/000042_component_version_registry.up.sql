-- Immutable, digest-pinned component version evidence. Mutable heads contain
-- only transactional pointers to immutable artifacts; every pointer change is
-- captured by an append-only transition row.

CREATE TABLE ops_component_versions (
  id BIGSERIAL PRIMARY KEY,
  component TEXT NOT NULL,
  environment TEXT NOT NULL,
  source_sha TEXT NOT NULL,
  image_digest TEXT NOT NULL,
  manifest_digest TEXT NOT NULL,
  registered_at TIMESTAMPTZ NOT NULL,
  retain_until TIMESTAMPTZ NOT NULL,
  retention_reason TEXT NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}',
  UNIQUE (id, component, environment),
  UNIQUE (component, environment, source_sha, image_digest, manifest_digest),
  CHECK (component ~ '^[a-z0-9][a-z0-9._-]{0,127}$'),
  CHECK (environment ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
  CHECK (source_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
  CHECK (image_digest ~ '^sha256:[0-9a-f]{64}$'),
  CHECK (manifest_digest ~ '^sha256:[0-9a-f]{64}$'),
  CHECK (lower(image_digest) NOT LIKE '%latest%'),
  CHECK (btrim(retention_reason) <> ''),
  CHECK (retain_until >= registered_at),
  CHECK (jsonb_typeof(metadata) = 'object')
);

CREATE TABLE ops_component_version_heads (
  component TEXT NOT NULL,
  environment TEXT NOT NULL,
  revision BIGINT NOT NULL DEFAULT 0,
  current_version_id BIGINT,
  previous_version_id BIGINT,
  last_known_good_version_id BIGINT,
  candidate_version_id BIGINT,
  updated_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (component, environment),
  FOREIGN KEY (current_version_id, component, environment)
    REFERENCES ops_component_versions (id, component, environment) ON DELETE RESTRICT,
  FOREIGN KEY (previous_version_id, component, environment)
    REFERENCES ops_component_versions (id, component, environment) ON DELETE RESTRICT,
  FOREIGN KEY (last_known_good_version_id, component, environment)
    REFERENCES ops_component_versions (id, component, environment) ON DELETE RESTRICT,
  FOREIGN KEY (candidate_version_id, component, environment)
    REFERENCES ops_component_versions (id, component, environment) ON DELETE RESTRICT,
  CHECK (component ~ '^[a-z0-9][a-z0-9._-]{0,127}$'),
  CHECK (environment ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
  CHECK (revision >= 0),
  CHECK (candidate_version_id IS NULL OR candidate_version_id IS DISTINCT FROM current_version_id)
);

CREATE TABLE ops_component_version_transitions (
  id BIGSERIAL PRIMARY KEY,
  component TEXT NOT NULL,
  environment TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  input_hash CHAR(64) NOT NULL,
  action TEXT NOT NULL,
  expected_revision BIGINT NOT NULL,
  resulting_revision BIGINT NOT NULL,
  before_current_id BIGINT,
  before_previous_id BIGINT,
  before_lkg_id BIGINT,
  before_candidate_id BIGINT,
  after_current_id BIGINT,
  after_previous_id BIGINT,
  after_lkg_id BIGINT,
  after_candidate_id BIGINT,
  actor TEXT NOT NULL,
  reason TEXT NOT NULL,
  occurred_at TIMESTAMPTZ NOT NULL,
  metadata JSONB NOT NULL DEFAULT '{}',
  UNIQUE (component, environment, idempotency_key),
  UNIQUE (component, environment, resulting_revision),
  FOREIGN KEY (before_current_id, component, environment) REFERENCES ops_component_versions (id, component, environment) ON DELETE RESTRICT,
  FOREIGN KEY (before_previous_id, component, environment) REFERENCES ops_component_versions (id, component, environment) ON DELETE RESTRICT,
  FOREIGN KEY (before_lkg_id, component, environment) REFERENCES ops_component_versions (id, component, environment) ON DELETE RESTRICT,
  FOREIGN KEY (before_candidate_id, component, environment) REFERENCES ops_component_versions (id, component, environment) ON DELETE RESTRICT,
  FOREIGN KEY (after_current_id, component, environment) REFERENCES ops_component_versions (id, component, environment) ON DELETE RESTRICT,
  FOREIGN KEY (after_previous_id, component, environment) REFERENCES ops_component_versions (id, component, environment) ON DELETE RESTRICT,
  FOREIGN KEY (after_lkg_id, component, environment) REFERENCES ops_component_versions (id, component, environment) ON DELETE RESTRICT,
  FOREIGN KEY (after_candidate_id, component, environment) REFERENCES ops_component_versions (id, component, environment) ON DELETE RESTRICT,
  CHECK (component ~ '^[a-z0-9][a-z0-9._-]{0,127}$'),
  CHECK (environment ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
  CHECK (btrim(idempotency_key) <> ''),
  CHECK (input_hash ~ '^[0-9a-f]{64}$'),
  CHECK (action IN ('set_candidate', 'promote_candidate', 'mark_lkg', 'rollback_to_lkg')),
  CHECK (expected_revision >= 0 AND resulting_revision = expected_revision + 1),
  CHECK (btrim(actor) <> '' AND btrim(reason) <> ''),
  CHECK (after_candidate_id IS NULL OR after_candidate_id IS DISTINCT FROM after_current_id),
  CHECK (jsonb_typeof(metadata) = 'object')
);
CREATE INDEX idx_ops_component_version_transitions_scope_time
  ON ops_component_version_transitions (component, environment, occurred_at DESC, id DESC);

CREATE OR REPLACE FUNCTION reject_component_version_evidence_mutation()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'component version evidence is immutable';
END;
$$;

CREATE TRIGGER ops_component_versions_immutable
  BEFORE UPDATE OR DELETE ON ops_component_versions
  FOR EACH ROW EXECUTE FUNCTION reject_component_version_evidence_mutation();
CREATE TRIGGER ops_component_version_transitions_immutable
  BEFORE UPDATE OR DELETE ON ops_component_version_transitions
  FOR EACH ROW EXECUTE FUNCTION reject_component_version_evidence_mutation();
CREATE TRIGGER ops_component_version_heads_no_delete
  BEFORE DELETE ON ops_component_version_heads
  FOR EACH ROW EXECUTE FUNCTION reject_component_version_evidence_mutation();

-- Fail closed if any writer changes a head without append-only evidence in the
-- same transaction. Deferred evaluation lets the repository update the head
-- and append its transition in either statement order while preserving atomicity.
CREATE OR REPLACE FUNCTION require_component_version_transition()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.revision <> 1 OR NOT EXISTS (
      SELECT 1 FROM ops_component_version_transitions audit_entry
      WHERE audit_entry.component = NEW.component
        AND audit_entry.environment = NEW.environment
        AND audit_entry.expected_revision = 0
        AND audit_entry.resulting_revision = NEW.revision
        AND audit_entry.before_current_id IS NULL
        AND audit_entry.before_previous_id IS NULL
        AND audit_entry.before_lkg_id IS NULL
        AND audit_entry.before_candidate_id IS NULL
        AND audit_entry.after_current_id IS NOT DISTINCT FROM NEW.current_version_id
        AND audit_entry.after_previous_id IS NOT DISTINCT FROM NEW.previous_version_id
        AND audit_entry.after_lkg_id IS NOT DISTINCT FROM NEW.last_known_good_version_id
        AND audit_entry.after_candidate_id IS NOT DISTINCT FROM NEW.candidate_version_id
        AND audit_entry.occurred_at = NEW.updated_at
    ) THEN
      RAISE EXCEPTION 'component version head creation requires matching transition evidence';
    END IF;
  ELSIF NEW.revision <> OLD.revision + 1
     OR NEW.updated_at <= OLD.updated_at
     OR NOT EXISTS (
      SELECT 1 FROM ops_component_version_transitions audit_entry
      WHERE audit_entry.component = NEW.component
        AND audit_entry.environment = NEW.environment
        AND audit_entry.expected_revision = OLD.revision
        AND audit_entry.resulting_revision = NEW.revision
        AND audit_entry.before_current_id IS NOT DISTINCT FROM OLD.current_version_id
        AND audit_entry.before_previous_id IS NOT DISTINCT FROM OLD.previous_version_id
        AND audit_entry.before_lkg_id IS NOT DISTINCT FROM OLD.last_known_good_version_id
        AND audit_entry.before_candidate_id IS NOT DISTINCT FROM OLD.candidate_version_id
        AND audit_entry.after_current_id IS NOT DISTINCT FROM NEW.current_version_id
        AND audit_entry.after_previous_id IS NOT DISTINCT FROM NEW.previous_version_id
        AND audit_entry.after_lkg_id IS NOT DISTINCT FROM NEW.last_known_good_version_id
        AND audit_entry.after_candidate_id IS NOT DISTINCT FROM NEW.candidate_version_id
        AND audit_entry.occurred_at = NEW.updated_at
    ) THEN
      RAISE EXCEPTION 'component version head mutation requires fresh matching transition evidence';
  END IF;
  RETURN NEW;
END;
$$;

CREATE CONSTRAINT TRIGGER ops_component_version_heads_require_transition
  AFTER INSERT OR UPDATE ON ops_component_version_heads
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION require_component_version_transition();

COMMENT ON TABLE ops_component_versions IS
  'Immutable digest-pinned deployable artifacts. Floating tags such as latest are structurally unavailable.';
COMMENT ON TABLE ops_component_version_heads IS
  'Transactional current, previous, last-known-good and candidate pointers scoped by explicit component and environment.';
COMMENT ON TABLE ops_component_version_transitions IS
  'Append-only audit evidence for every version pointer transition, including exact before and after states.';

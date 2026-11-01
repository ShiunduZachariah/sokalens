-- Durable idempotency ledger for incident lifecycle transitions. The incident
-- mutation, audit append, and this record are committed in one transaction.
CREATE TABLE ops_incident_transitions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  incident_id UUID NOT NULL REFERENCES ops_incidents(id) ON DELETE RESTRICT,
  idempotency_key TEXT NOT NULL UNIQUE,
  from_status TEXT NOT NULL,
  to_status TEXT NOT NULL,
  reason TEXT NOT NULL,
  actor_type TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  occurred_at TIMESTAMPTZ NOT NULL,
  audit_event_id UUID NOT NULL UNIQUE REFERENCES ops_audit_events(event_id) ON DELETE RESTRICT,
  previous_event_hash TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (length(idempotency_key) = 64),
  CHECK (from_status IN ('open', 'investigating', 'mitigated', 'resolved', 'suppressed')),
  CHECK (to_status IN ('open', 'investigating', 'mitigated', 'resolved', 'suppressed')),
  CHECK (from_status <> to_status),
  CHECK (
    (from_status = 'open' AND to_status IN ('investigating', 'suppressed')) OR
    (from_status = 'investigating' AND to_status IN ('mitigated', 'suppressed')) OR
    (from_status = 'mitigated' AND to_status IN ('investigating', 'resolved')) OR
    (from_status = 'suppressed' AND to_status = 'open')
  ),
  CHECK (btrim(reason) <> ''),
  CHECK (actor_type IN ('system', 'user', 'admin', 'worker', 'ai')),
  CHECK (btrim(actor_id) <> ''),
  CHECK (previous_event_hash IS NULL OR length(previous_event_hash) = 64)
);

-- Chain uniqueness is incident-scoped: legacy and unrelated audit events may
-- have their own roots, while one incident cannot have two roots or forks.
CREATE UNIQUE INDEX idx_ops_incident_transition_chain
  ON ops_incident_transitions (incident_id, (COALESCE(previous_event_hash, '__root__')));

CREATE INDEX idx_ops_incident_transitions_incident
  ON ops_incident_transitions (incident_id, occurred_at DESC, id DESC);

CREATE TRIGGER ops_incident_transitions_immutable
BEFORE UPDATE OR DELETE ON ops_incident_transitions
FOR EACH ROW EXECUTE FUNCTION reject_ops_immutable_mutation();

COMMENT ON TABLE ops_incident_transitions IS
  'Immutable idempotency ledger for transactional incident state changes and their audit events.';

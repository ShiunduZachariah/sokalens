-- Immutable observation identities make incident correlation restart-durable.
-- An observation can be delivered repeatedly, but contributes to an incident's
-- occurrence count exactly once.
CREATE TABLE ops_incident_observations (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  incident_id UUID NOT NULL REFERENCES ops_incidents(id) ON DELETE RESTRICT,
  source TEXT NOT NULL,
  source_event_id TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  observed_at TIMESTAMPTZ NOT NULL,
  received_at TIMESTAMPTZ NOT NULL,
  evidence JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (source, source_event_id),
  UNIQUE (idempotency_key),
  CHECK (btrim(source) <> ''),
  CHECK (btrim(source_event_id) <> ''),
  CHECK (length(idempotency_key) = 64),
  CHECK (jsonb_typeof(evidence) = 'object')
);

CREATE INDEX idx_ops_incident_observations_incident
  ON ops_incident_observations (incident_id, observed_at DESC);

-- Every pre-existing incident already represents at least its originating
-- source event. Record that identity before enabling immutable writes so a
-- delivery retry after this migration cannot increment it again.
INSERT INTO ops_incident_observations (
  incident_id,
  source,
  source_event_id,
  idempotency_key,
  observed_at,
  received_at,
  evidence,
  created_at
)
SELECT
  id,
  source,
  source_event_id,
  encode(digest(jsonb_build_array('ops-incident-observation-v1', source, source_event_id)::text, 'sha256'), 'hex'),
  first_observed_at,
  created_at,
  jsonb_build_object('classification', 'legacy-incident-bootstrap'),
  created_at
FROM ops_incidents;

CREATE TRIGGER ops_incident_observations_immutable
BEFORE UPDATE OR DELETE ON ops_incident_observations
FOR EACH ROW EXECUTE FUNCTION reject_ops_immutable_mutation();

COMMENT ON TABLE ops_incident_observations IS
  'Immutable restart-durable alert identities; unique source events and idempotency keys contribute to incident counts once.';

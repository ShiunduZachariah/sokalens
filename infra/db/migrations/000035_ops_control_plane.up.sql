-- Durable operations-control state. Redis may accelerate claims, but these
-- PostgreSQL rows remain authoritative.
CREATE TABLE ops_incidents (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  source TEXT NOT NULL,
  source_event_id TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  title TEXT NOT NULL,
  summary TEXT NOT NULL,
  severity TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'open',
  evidence JSONB NOT NULL DEFAULT '{}',
  first_observed_at TIMESTAMPTZ NOT NULL,
  last_observed_at TIMESTAMPTZ NOT NULL,
  acknowledged_at TIMESTAMPTZ,
  resolved_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (source, source_event_id),
  CHECK (btrim(source) <> ''),
  CHECK (btrim(source_event_id) <> ''),
  CHECK (btrim(fingerprint) <> ''),
  CHECK (btrim(title) <> ''),
  CHECK (severity IN ('info', 'warning', 'critical')),
  CHECK (status IN ('open', 'investigating', 'mitigated', 'resolved', 'suppressed')),
  CHECK (last_observed_at >= first_observed_at),
  CHECK (acknowledged_at IS NULL OR acknowledged_at >= first_observed_at),
  CHECK (resolved_at IS NULL OR resolved_at >= first_observed_at)
);

CREATE INDEX idx_ops_incidents_active
  ON ops_incidents (severity, last_observed_at DESC)
  WHERE status IN ('open', 'investigating', 'mitigated');
CREATE INDEX idx_ops_incidents_fingerprint
  ON ops_incidents (fingerprint, last_observed_at DESC);

CREATE TABLE ops_jobs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  incident_id UUID REFERENCES ops_incidents(id) ON DELETE SET NULL,
  queue_name TEXT NOT NULL DEFAULT 'default',
  job_type TEXT NOT NULL,
  idempotency_key TEXT NOT NULL UNIQUE,
  priority INTEGER NOT NULL DEFAULT 100,
  payload JSONB NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  attempts INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 5,
  lease_owner TEXT,
  lease_token UUID,
  lease_expires_at TIMESTAMPTZ,
  last_error_code TEXT,
  last_error_message TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  started_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (btrim(queue_name) <> ''),
  CHECK (btrim(job_type) <> ''),
  CHECK (btrim(idempotency_key) <> ''),
  CHECK (priority BETWEEN 0 AND 1000),
  CHECK (status IN ('pending', 'leased', 'retry', 'succeeded', 'failed', 'dead_letter', 'cancelled')),
  CHECK (attempts >= 0 AND max_attempts > 0 AND attempts <= max_attempts),
  CHECK ((status = 'leased' AND lease_owner IS NOT NULL AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL) OR
         (status <> 'leased')),
  CHECK (completed_at IS NULL OR started_at IS NULL OR completed_at >= started_at)
);

CREATE INDEX idx_ops_jobs_claim
  ON ops_jobs (queue_name, priority DESC, available_at, created_at)
  WHERE status IN ('pending', 'retry');
CREATE INDEX idx_ops_jobs_expired_leases
  ON ops_jobs (lease_expires_at)
  WHERE status = 'leased';
CREATE INDEX idx_ops_jobs_incident
  ON ops_jobs (incident_id, created_at DESC);

CREATE TABLE ops_authority_grants (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  scope TEXT NOT NULL,
  resource_pattern TEXT NOT NULL,
  autonomy_mode TEXT NOT NULL,
  allowed_operations TEXT[] NOT NULL DEFAULT '{}',
  granted_by TEXT NOT NULL,
  reason TEXT NOT NULL,
  effective_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  revoked_by TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (btrim(scope) <> ''),
  CHECK (btrim(resource_pattern) <> ''),
  CHECK (autonomy_mode IN ('observe', 'assist', 'auto_rollback', 'auto_heal', 'auto_patch')),
  CHECK (allowed_operations <@ ARRAY[
    'restart_service', 'rollback_release', 'scale_worker', 'flush_safe_cache',
    'check_backup', 'requeue_job', 'recover_lease'
  ]::TEXT[]),
  CHECK (
    (autonomy_mode IN ('observe', 'assist') AND cardinality(allowed_operations) = 0) OR
    (autonomy_mode = 'auto_rollback' AND
      allowed_operations <@ ARRAY['rollback_release']::TEXT[]) OR
    autonomy_mode IN ('auto_heal', 'auto_patch')
  ),
  CHECK (btrim(granted_by) <> ''),
  CHECK (btrim(reason) <> ''),
  CHECK (expires_at IS NULL OR expires_at > effective_at),
  CHECK ((revoked_at IS NULL AND revoked_by IS NULL) OR
         (revoked_at IS NOT NULL AND revoked_by IS NOT NULL AND btrim(revoked_by) <> ''))
);

CREATE UNIQUE INDEX idx_ops_authority_one_active_scope
  ON ops_authority_grants (scope, resource_pattern)
  WHERE revoked_at IS NULL;
CREATE INDEX idx_ops_authority_expiry
  ON ops_authority_grants (expires_at)
  WHERE revoked_at IS NULL AND expires_at IS NOT NULL;

CREATE TABLE ops_actions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  incident_id UUID REFERENCES ops_incidents(id) ON DELETE SET NULL,
  job_id UUID REFERENCES ops_jobs(id) ON DELETE SET NULL,
  authority_grant_id UUID REFERENCES ops_authority_grants(id) ON DELETE RESTRICT,
  idempotency_key TEXT NOT NULL UNIQUE,
  operation_type TEXT NOT NULL,
  target_scope TEXT NOT NULL,
  target_resource TEXT NOT NULL,
  requested_mode TEXT NOT NULL,
  parameters JSONB NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'proposed',
  requested_by TEXT NOT NULL,
  authorized_by TEXT,
  requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  authorized_at TIMESTAMPTZ,
  started_at TIMESTAMPTZ,
  completed_at TIMESTAMPTZ,
  result JSONB,
  error_code TEXT,
  error_message TEXT,
  CHECK (btrim(idempotency_key) <> ''),
  CHECK (operation_type IN (
    'restart_service', 'rollback_release', 'scale_worker', 'flush_safe_cache',
    'check_backup', 'requeue_job', 'recover_lease'
  )),
  CHECK (btrim(target_scope) <> ''),
  CHECK (btrim(target_resource) <> ''),
  CHECK (requested_mode IN ('observe', 'assist', 'auto_rollback', 'auto_heal', 'auto_patch')),
  CHECK (status IN ('proposed', 'authorized', 'running', 'succeeded', 'failed', 'denied', 'cancelled')),
  CHECK (
    requested_mode NOT IN ('observe', 'assist') OR
    status IN ('proposed', 'denied', 'cancelled')
  ),
  CHECK (requested_mode <> 'auto_rollback' OR operation_type = 'rollback_release'),
  CHECK (btrim(requested_by) <> ''),
  CHECK (authorized_at IS NULL OR authorized_at >= requested_at),
  CHECK (started_at IS NULL OR started_at >= requested_at),
  CHECK (completed_at IS NULL OR started_at IS NULL OR completed_at >= started_at)
);

CREATE INDEX idx_ops_actions_incident
  ON ops_actions (incident_id, requested_at DESC);
CREATE INDEX idx_ops_actions_status
  ON ops_actions (status, requested_at)
  WHERE status IN ('proposed', 'authorized', 'running');

CREATE TABLE ops_deployments (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  deployment_key TEXT NOT NULL UNIQUE,
  deployment_type TEXT NOT NULL,
  environment TEXT NOT NULL,
  service TEXT NOT NULL,
  release_manifest_hash TEXT NOT NULL,
  source_commit TEXT NOT NULL,
  artifact_digest TEXT NOT NULL,
  model_registry_id UUID REFERENCES model_registry(id) ON DELETE RESTRICT,
  previous_deployment_id UUID REFERENCES ops_deployments(id) ON DELETE RESTRICT,
  status TEXT NOT NULL,
  initiated_by TEXT NOT NULL,
  started_at TIMESTAMPTZ NOT NULL,
  completed_at TIMESTAMPTZ,
  metadata JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (btrim(deployment_key) <> ''),
  CHECK (deployment_type IN ('software', 'model')),
  CHECK (btrim(environment) <> ''),
  CHECK (btrim(service) <> ''),
  CHECK (length(release_manifest_hash) = 64),
  CHECK (btrim(source_commit) <> ''),
  CHECK (btrim(artifact_digest) <> ''),
  CHECK ((deployment_type = 'model' AND model_registry_id IS NOT NULL) OR
         (deployment_type = 'software' AND model_registry_id IS NULL)),
  CHECK (status IN ('pending', 'running', 'succeeded', 'failed', 'rolled_back', 'cancelled')),
  CHECK (btrim(initiated_by) <> ''),
  CHECK (completed_at IS NULL OR completed_at >= started_at)
);

CREATE INDEX idx_ops_deployments_environment_service
  ON ops_deployments (environment, service, started_at DESC);

CREATE TABLE ops_audit_events (
  sequence_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  event_id UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
  event_type TEXT NOT NULL,
  actor_type TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  entity_type TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  details JSONB NOT NULL DEFAULT '{}',
  previous_event_hash TEXT,
  event_hash TEXT NOT NULL UNIQUE,
  CHECK (btrim(event_type) <> ''),
  CHECK (actor_type IN ('system', 'user', 'admin', 'worker', 'ai')),
  CHECK (btrim(actor_id) <> ''),
  CHECK (btrim(entity_type) <> ''),
  CHECK (btrim(entity_id) <> ''),
  CHECK (previous_event_hash IS NULL OR length(previous_event_hash) = 64),
  CHECK (length(event_hash) = 64)
);

CREATE INDEX idx_ops_audit_entity
  ON ops_audit_events (entity_type, entity_id, sequence_id DESC);
CREATE INDEX idx_ops_audit_occurred
  ON ops_audit_events (occurred_at DESC, sequence_id DESC);

CREATE OR REPLACE FUNCTION reject_ops_immutable_mutation()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $function$
BEGIN
  RAISE EXCEPTION '% rows are immutable', TG_TABLE_NAME;
END
$function$;

CREATE TRIGGER ops_audit_events_immutable
BEFORE UPDATE OR DELETE ON ops_audit_events
FOR EACH ROW EXECUTE FUNCTION reject_ops_immutable_mutation();

COMMENT ON TABLE ops_jobs IS
  'Authoritative durable operations queue; Redis is optional claim acceleration only.';
COMMENT ON TABLE ops_audit_events IS
  'Append-only hash-chain-ready audit evidence. Writers compute event_hash from canonical event data.';
COMMENT ON TABLE ops_deployments IS
  'Deployment lifecycle state. Every transition must emit a separate immutable ops_audit_events row.';

-- Local/test expansion rollback only. Preserve production operations audit
-- evidence and forward-fix after the controller starts writing durable state.
DROP TRIGGER IF EXISTS ops_audit_events_immutable ON ops_audit_events;
DROP FUNCTION IF EXISTS reject_ops_immutable_mutation();

DROP INDEX IF EXISTS idx_ops_audit_occurred;
DROP INDEX IF EXISTS idx_ops_audit_entity;
DROP TABLE IF EXISTS ops_audit_events;

DROP INDEX IF EXISTS idx_ops_deployments_environment_service;
DROP TABLE IF EXISTS ops_deployments;

DROP INDEX IF EXISTS idx_ops_actions_status;
DROP INDEX IF EXISTS idx_ops_actions_incident;
DROP TABLE IF EXISTS ops_actions;

DROP INDEX IF EXISTS idx_ops_authority_expiry;
DROP INDEX IF EXISTS idx_ops_authority_one_active_scope;
DROP TABLE IF EXISTS ops_authority_grants;

DROP INDEX IF EXISTS idx_ops_jobs_incident;
DROP INDEX IF EXISTS idx_ops_jobs_expired_leases;
DROP INDEX IF EXISTS idx_ops_jobs_claim;
DROP TABLE IF EXISTS ops_jobs;

DROP INDEX IF EXISTS idx_ops_incidents_fingerprint;
DROP INDEX IF EXISTS idx_ops_incidents_active;
DROP TABLE IF EXISTS ops_incidents;

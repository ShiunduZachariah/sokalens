-- Local/test expansion rollback only. Production transition and audit evidence
-- is append-only and must be preserved with a forward fix.
DROP TRIGGER IF EXISTS ops_incident_transitions_immutable ON ops_incident_transitions;
DROP INDEX IF EXISTS idx_ops_incident_transitions_incident;
DROP INDEX IF EXISTS idx_ops_incident_transition_chain;
DROP TABLE IF EXISTS ops_incident_transitions;

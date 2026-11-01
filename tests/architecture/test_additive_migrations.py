import ast
from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[2]
MIGRATIONS = ROOT / "infra" / "db" / "migrations"


class AdditiveMigrationContractTests(unittest.TestCase):
    def test_new_migrations_have_reversible_pairs(self) -> None:
        for version in ("000033", "000034", "000035", "000036", "000037", "000038", "000039", "000040", "000041", "000042", "000043", "000044", "000045", "000050", "000051", "000052", "000053", "000054", "000056", "000057", "000058", "000059", "000060", "000061", "000066"):
            self.assertEqual(len(list(MIGRATIONS.glob(f"{version}_*.up.sql"))), 1)
            self.assertEqual(len(list(MIGRATIONS.glob(f"{version}_*.down.sql"))), 1)

    def test_match_live_state_migration_is_additive_only(self) -> None:
        """000065 runs against a live database during a fixture window.

        A destructive statement in the up migration would take match data with
        it, and the down migration is the only place a DROP belongs.
        """
        up = (MIGRATIONS / "000065_match_live_state_freshness.up.sql").read_text()
        for forbidden in ("DROP TABLE", "DROP COLUMN", "TRUNCATE", "DELETE FROM", "ALTER COLUMN"):
            self.assertNotIn(forbidden, up.upper(), f"000065 up is not additive: {forbidden}")
        # The added column must tolerate every existing row, which means no
        # NOT NULL without a default. Nothing backfills it: an invented
        # confirmation time would read as authoritative.
        self.assertIn("ADD COLUMN IF NOT EXISTS live_state_at TIMESTAMPTZ", up)
        self.assertNotIn("NOT NULL", up.upper().split("COMMENT ON")[0])

        down = (MIGRATIONS / "000065_match_live_state_freshness.down.sql").read_text()
        self.assertIn("DROP COLUMN IF EXISTS live_state_at", down)

    def test_telegram_bridge_migrations_are_additive_only(self) -> None:
        """The ops bridge may only add. It runs against a live database, so a
        destructive statement here would take production data with it."""
        for name in ("000059_ops_telegram_bridge", "000060_ops_notification_outbox"):
            sql = (MIGRATIONS / f"{name}.up.sql").read_text()
            for forbidden in ("DROP TABLE", "DROP COLUMN", "TRUNCATE", "DELETE FROM", "ALTER COLUMN"):
                self.assertNotIn(forbidden, sql.upper(), f"{name} is not additive: {forbidden}")
            # Every column added to an existing table must tolerate existing
            # rows. Comments are skipped: they discuss ADD COLUMN, they do not
            # execute it.
            for line in sql.splitlines():
                statement = line.strip()
                if statement.startswith("--"):
                    continue
                if "ADD COLUMN" in statement.upper():
                    self.assertIn("IF NOT EXISTS", statement.upper(), f"{name}: {statement}")

            down = (MIGRATIONS / f"{name}.down.sql").read_text()
            self.assertNotIn("TRUNCATE", down.upper())

    def test_data_backfill_requests_are_bounded_auditable_and_provider_agnostic(self) -> None:
        sql = (MIGRATIONS / "000050_data_backfill_requests.up.sql").read_text()
        self.assertIn("CREATE TABLE data_backfill_requests", sql)
        self.assertIn("target_fixture_id BIGINT NOT NULL REFERENCES fixtures(id)", sql)
        self.assertIn("status IN ('pending', 'in_progress', 'completed', 'confirmed_unavailable')", sql)
        self.assertIn("feature_group IN ('results', 'rich_statistics')", sql)
        self.assertIn("provider_attempts JSONB", sql)
        self.assertIn("attempts >= 0", sql)
        self.assertIn("expires_at", sql)
        self.assertIn("UNIQUE (target_fixture_id, team_id, competition_id, feature_group)", sql)

        down = (MIGRATIONS / "000050_data_backfill_requests.down.sql").read_text()
        self.assertIn("DROP TABLE IF EXISTS data_backfill_requests", down)

    def test_thesportsdb_provider_is_registered_before_history_maps_are_written(self) -> None:
        sql = (MIGRATIONS / "000051_thesportsdb_provider.up.sql").read_text()
        down = (MIGRATIONS / "000051_thesportsdb_provider.down.sql").read_text()

        self.assertIn("INSERT INTO providers", sql)
        self.assertIn("'thesportsdb'", sql)
        self.assertIn("ON CONFLICT (provider) DO UPDATE", sql)
        self.assertIn("NOT EXISTS", down)

    def test_governance_expansion_does_not_rewrite_legacy_predictions(self) -> None:
        sql = (MIGRATIONS / "000033_model_governance_core.up.sql").read_text()
        self.assertIn("ALTER TABLE engine_predictions", sql)
        self.assertNotIn("UPDATE engine_predictions", sql)
        self.assertNotIn("DELETE FROM engine_predictions", sql)
        self.assertIn("NOT VALID", sql)
        self.assertIn("idx_model_registry_one_production_per_market", sql)
        self.assertIn("model_registry_identity_immutable", sql)

    def test_evaluation_fact_retention_is_restrictive_and_immutable(self) -> None:
        sql = (MIGRATIONS / "000034_model_evaluation_ledger.up.sql").read_text()
        self.assertIn("prediction_id UUID PRIMARY KEY REFERENCES engine_predictions(id) ON DELETE RESTRICT", sql)
        self.assertIn("model_evaluation_facts_immutable", sql)
        self.assertIn("shadow_settlements_immutable", sql)
        self.assertIn("champion_challenger_evaluations_immutable", sql)
        self.assertNotIn("ON DELETE CASCADE", sql)

    def test_bounded_prediction_retention_contract_is_reversible_and_governed(self) -> None:
        sql = (MIGRATIONS / "000056_bounded_prediction_retention.up.sql").read_text()
        down = (MIGRATIONS / "000056_bounded_prediction_retention.down.sql").read_text()

        self.assertIn("ADD COLUMN IF NOT EXISTS finished_at TIMESTAMPTZ", sql)
        self.assertIn("CREATE VIEW current_engine_predictions", sql)
        self.assertIn("WHERE superseded_at IS NULL", sql)
        self.assertIn("CREATE VIEW retention_candidates", sql)
        self.assertIn("p_features_used - 'as_of_timestamp'", sql)
        self.assertIn("DROP CONSTRAINT IF EXISTS model_evaluation_facts_prediction_id_fkey", sql)
        self.assertIn("BEFORE UPDATE ON model_evaluation_facts", sql)
        self.assertNotIn("BEFORE UPDATE OR DELETE ON model_evaluation_facts", sql)
        self.assertIn("ON DELETE CASCADE", sql)
        self.assertNotIn("DELETE FROM engine_predictions", sql)
        self.assertNotIn("DELETE FROM model_evaluation_facts", sql)

        self.assertIn("DROP VIEW IF EXISTS retention_candidates", down)
        self.assertIn("DROP VIEW IF EXISTS current_engine_predictions", down)
        self.assertIn("BEFORE UPDATE OR DELETE ON model_evaluation_facts", down)
        self.assertIn("ON DELETE RESTRICT", down)
        self.assertIn("DROP COLUMN IF EXISTS finished_at", down)

        verifier = (ROOT / "scripts" / "architecture" / "verify_additive_migrations.py").read_text()
        self.assertIn('"000056"', verifier)

    def test_ops_state_is_postgres_authoritative_and_audited(self) -> None:
        sql = (MIGRATIONS / "000035_ops_control_plane.up.sql").read_text()
        for table in (
            "ops_incidents",
            "ops_jobs",
            "ops_authority_grants",
            "ops_actions",
            "ops_deployments",
            "ops_audit_events",
        ):
            self.assertIn(f"CREATE TABLE {table}", sql)
        self.assertIn("ops_audit_events_immutable", sql)
        self.assertIn("idempotency_key TEXT NOT NULL UNIQUE", sql)
        self.assertNotIn("ops_deployments_immutable", sql)
        self.assertIn("cardinality(allowed_operations) = 0", sql)
        self.assertIn("requested_mode NOT IN ('observe', 'assist')", sql)

    def test_v1_champion_bootstrap_is_explicit_bounded_and_fail_closed(self) -> None:
        sql = (MIGRATIONS / "000036_v1_champion_bootstrap.up.sql").read_text()
        self.assertIn("historical_metadata_approximate", sql)
        self.assertIn("'historical_fields_approximate', true", sql)
        self.assertIn('"configuration_metadata"', sql)
        self.assertIn("WHERE active = true", sql)
        self.assertIn("LIMIT 1000", sql)
        self.assertIn("FOR UPDATE SKIP LOCKED", sql)
        self.assertIn("ep.model_version = 'python-engine-v1'", sql)
        self.assertIn("ep.model_registry_id IS NULL", sql)
        self.assertIn("CREATE OR REPLACE FUNCTION engine_prediction_market_family", sql)
        self.assertNotIn("engine_predictions_v1_registry_required", sql)
        self.assertIn("active market family has no reviewed python-engine-v1 mapping", sql)
        self.assertIn("every active market family must have exactly one production python-engine-v1 champion", sql)
        self.assertIn("model-evaluation-v1-bootstrap-approximate", sql)
        self.assertIn("results.settlement_status IN ('settled', 'void', 'unsettled_data_unavailable')", sql)
        self.assertIn("WHEN results.settlement_status = 'unsettled_data_unavailable' THEN 'excluded'", sql)
        self.assertIn("COALESCE(prediction.raw_probability, prediction.probability)", sql)
        self.assertIn("ON CONFLICT (prediction_id) DO NOTHING", sql)
        self.assertIn("left % unsupported python-engine-v1 markets", sql)
        self.assertNotIn("ELSE '1x2'", sql)

        down = (MIGRATIONS / "000036_v1_champion_bootstrap.down.sql").read_text()
        self.assertIn("audit_metadata ->> 'bootstrap' = 'eng-003'", down)
        self.assertNotIn("DELETE FROM model_registry", down)
        self.assertNotIn("DELETE FROM model_evaluation_facts", down)

    def test_every_v1_pipeline_market_has_an_explicit_bootstrap_mapping(self) -> None:
        emitted: set[str] = set()
        for relative in (
            "services/prediction-engine/prediction_engine/pipeline.py",
            "services/prediction-engine/prediction_engine/shot_possession_markets.py",
        ):
            tree = ast.parse((ROOT / relative).read_text())
            for node in ast.walk(tree):
                if isinstance(node, ast.Call):
                    for keyword in node.keywords:
                        if keyword.arg == "market_key" and isinstance(keyword.value, ast.Constant):
                            emitted.add(keyword.value.value)
                if isinstance(node, ast.Return) and isinstance(node.value, ast.Tuple):
                    first = node.value.elts[0] if node.value.elts else None
                    if isinstance(first, ast.Constant) and first.value in {"total_goals", "result_total_goals", "possession_winner"}:
                        emitted.add(first.value)

        pipeline = ast.parse(
            (ROOT / "services/prediction-engine/prediction_engine/pipeline.py").read_text()
        )
        model_a = next(
            node for node in pipeline.body
            if isinstance(node, ast.FunctionDef) and node.name == "_model_a_prediction_candidates"
        )
        for node in ast.walk(model_a):
            if isinstance(node, ast.Tuple) and len(node.elts) >= 2:
                first, second = node.elts[:2]
                if isinstance(first, ast.Constant) and isinstance(second, (ast.Subscript, ast.Call)):
                    emitted.add(first.value)
                if len(node.elts) == 3 and isinstance(first, ast.Constant):
                    emitted.add(first.value)

        tier_d = next(
            node for node in pipeline.body
            if isinstance(node, ast.FunctionDef) and node.name == "_tier_d_prediction_candidates"
        )
        for node in ast.walk(tier_d):
            if isinstance(node, ast.Tuple):
                values = [item.value for item in node.elts if isinstance(item, ast.Constant)]
                emitted.update(value for value in values if value in {"btts_total_goals", "btts_match_result"})

        self.assertEqual(emitted, {
            "1x2", "double_chance", "draw_no_bet", "btts", "total_goals",
            "half_time_result", "highest_scoring_half", "team_to_score_first",
            "odd_even_total_goals", "correct_score", "total_corners",
            "team_corners_home", "team_corners_away", "total_cards", "total_fouls",
            "total_shots", "shots_on_target", "possession_winner", "btts_total_goals",
            "btts_match_result", "result_total_goals",
        })
        sql = (MIGRATIONS / "000036_v1_champion_bootstrap.up.sql").read_text()
        for market in emitted:
            self.assertIn(f"'{market}'", sql, market)

    def test_lifecycle_stability_is_additive_bounded_and_unambiguous(self) -> None:
        sql = (MIGRATIONS / "000037_prediction_lifecycle_stability.up.sql").read_text()
        for column in (
            "previous_probability",
            "probability_delta",
            "max_lifecycle_probability_shift",
            "prediction_direction_changed",
            "lifecycle_stability_score",
        ):
            self.assertIn(f"ADD COLUMN {column}", sql)
        self.assertIn("engine_predictions_lifecycle_previous_delta_pair", sql)
        self.assertIn("engine_predictions_lifecycle_max_covers_delta", sql)
        self.assertIn("engine_predictions_lifecycle_first_version_neutral", sql)
        self.assertIn("idx_engine_predictions_lifecycle_stability", sql)
        self.assertIn("distinct from legacy v2 stability_score", sql)
        self.assertNotIn("UPDATE engine_predictions", sql)
        self.assertNotIn("DELETE FROM engine_predictions", sql)

        down = (MIGRATIONS / "000037_prediction_lifecycle_stability.down.sql").read_text()
        self.assertIn("DROP COLUMN IF EXISTS lifecycle_stability_score", down)

    def test_incident_observations_are_restart_durable_and_immutable(self) -> None:
        sql = (MIGRATIONS / "000038_ops_incident_observations.up.sql").read_text()
        self.assertIn("CREATE TABLE ops_incident_observations", sql)
        self.assertIn("REFERENCES ops_incidents(id) ON DELETE RESTRICT", sql)
        self.assertIn("UNIQUE (source, source_event_id)", sql)
        self.assertIn("UNIQUE (idempotency_key)", sql)
        self.assertIn("ops_incident_observations_immutable", sql)
        self.assertIn("BEFORE UPDATE OR DELETE", sql)
        self.assertNotIn("ON DELETE CASCADE", sql)

        down = (MIGRATIONS / "000038_ops_incident_observations.down.sql").read_text()
        self.assertIn("DROP TABLE IF EXISTS ops_incident_observations", down)

    def test_incident_transitions_are_transactional_idempotent_and_incident_scoped(self) -> None:
        sql = (MIGRATIONS / "000039_ops_incident_transitions.up.sql").read_text()
        self.assertIn("CREATE TABLE ops_incident_transitions", sql)
        self.assertIn("idempotency_key TEXT NOT NULL UNIQUE", sql)
        self.assertIn("REFERENCES ops_incidents(id) ON DELETE RESTRICT", sql)
        self.assertIn("REFERENCES ops_audit_events(event_id) ON DELETE RESTRICT", sql)
        self.assertIn("previous_event_hash TEXT", sql)
        self.assertIn("incident_id, (COALESCE(previous_event_hash, '__root__'))", sql)
        self.assertIn("ops_incident_transitions_immutable", sql)
        self.assertNotIn("ON ops_audit_events ((COALESCE(previous_event_hash", sql)

        down = (MIGRATIONS / "000039_ops_incident_transitions.down.sql").read_text()
        self.assertIn("DROP TABLE IF EXISTS ops_incident_transitions", down)

    def test_ops_jobs_add_legacy_safe_lease_retry_and_dead_letter_contracts(self) -> None:
        sql = (MIGRATIONS / "000040_ops_durable_jobs.up.sql").read_text()
        self.assertIn("ADD COLUMN last_error_class", sql)
        self.assertIn("ADD COLUMN dead_lettered_at", sql)
        self.assertIn("ops_jobs_lease_shape_check", sql)
        self.assertIn("ops_jobs_terminal_time_check", sql)
        self.assertIn("ops_jobs_dead_letter_time_check", sql)
        self.assertGreaterEqual(sql.count("NOT VALID"), 4)
        self.assertIn("priority DESC, available_at, created_at, id", sql)
        self.assertIn("Semantic P0/P1/P2", sql)
        self.assertNotIn("UPDATE ops_jobs", sql)
        self.assertNotIn("DELETE FROM ops_jobs", sql)

        down = (MIGRATIONS / "000040_ops_durable_jobs.down.sql").read_text()
        self.assertIn("DROP COLUMN IF EXISTS dead_lettered_at", down)
        self.assertIn("DROP COLUMN IF EXISTS last_error_class", down)
        self.assertIn("CREATE INDEX idx_ops_jobs_claim", down)

    def test_entitlement_reconciliation_is_scoped_deduplicated_and_append_only(self) -> None:
        sql = (MIGRATIONS / "000041_entitlement_reconciliation.up.sql").read_text()
        for table in (
            "entitlement_reconciliation_runs",
            "entitlement_reconciliation_items",
            "entitlement_reconciliation_corrections",
            "entitlement_reconciliation_effects",
            "entitlement_grant_freezes",
        ):
            self.assertIn(f"CREATE TABLE {table}", sql)
        self.assertIn("idempotency_key TEXT NOT NULL UNIQUE", sql)
        self.assertIn("uq_entitlement_grant_freezes_scope", sql)
        self.assertIn("NULLS NOT DISTINCT", sql)
        self.assertIn("BEFORE UPDATE OR DELETE", sql)
        self.assertIn("entitlement_reconciliation_effects_immutable", sql)
        self.assertIn("append-only", sql)
        self.assertIn("NEW subscription grants only", sql)
        self.assertNotIn("ON DELETE CASCADE", sql)

        down = (MIGRATIONS / "000041_entitlement_reconciliation.down.sql").read_text()
        self.assertIn("DROP TABLE IF EXISTS entitlement_reconciliation_runs", down)
        self.assertIn("DROP COLUMN IF EXISTS reconciliation_verified_at", down)

    def test_component_version_registry_is_digest_pinned_audited_and_immutable(self) -> None:
        sql = (MIGRATIONS / "000042_component_version_registry.up.sql").read_text()
        for table in (
            "ops_component_versions",
            "ops_component_version_heads",
            "ops_component_version_transitions",
        ):
            self.assertIn(f"CREATE TABLE {table}", sql)
        self.assertIn("image_digest ~ '^sha256:[0-9a-f]{64}$'", sql)
        self.assertIn("manifest_digest ~ '^sha256:[0-9a-f]{64}$'", sql)
        self.assertIn("current_version_id", sql)
        self.assertIn("previous_version_id", sql)
        self.assertIn("last_known_good_version_id", sql)
        self.assertIn("candidate_version_id", sql)
        self.assertIn("component version evidence is immutable", sql)
        self.assertIn("DEFERRABLE INITIALLY DEFERRED", sql)
        self.assertIn("UNIQUE (component, environment, resulting_revision)", sql)
        self.assertIn("NEW.revision <> OLD.revision + 1", sql)
        self.assertIn("audit_entry.expected_revision = OLD.revision", sql)
        self.assertIn("audit_entry.before_current_id IS NOT DISTINCT FROM OLD.current_version_id", sql)
        self.assertIn("component version head mutation requires fresh matching transition evidence", sql)
        self.assertNotIn("ON DELETE CASCADE", sql)

        down = (MIGRATIONS / "000042_component_version_registry.down.sql").read_text()
        self.assertIn("DROP TABLE IF EXISTS ops_component_version_transitions", down)
        self.assertIn("DROP TABLE IF EXISTS ops_component_versions", down)



if __name__ == "__main__":
    unittest.main()

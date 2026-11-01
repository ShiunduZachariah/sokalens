#!/usr/bin/env python3
"""Verify additive governance and retention migrations on disposable PostgreSQL.

The verifier intentionally creates and destroys its own Docker container. It
never accepts a database URL, preventing accidental execution against shared or
production databases.
"""

from __future__ import annotations

import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import uuid


ROOT = Path(__file__).resolve().parents[2]
MIGRATIONS = ROOT / "infra" / "db" / "migrations"
# Versions verified as additive expansions on top of the baseline schema.
# 000059/000060 belong here rather than in the baseline because they
# reference ops_jobs, which 000035 creates and which is itself an
# expansion — a bridge migration in the baseline would run before the
# operations control plane exists. 000061 belongs here for the same reason:
# it indexes acca_slip_legs, which 000057 creates. 000064 tunes the same ACCA
# tables and must therefore run after 000057 as part of the expansion.
# 000065-000070 are registered so their down migrations are exercised, not
# merely their ups. 000065 drops a column and 000066 drops partitioned tables;
# a rollback nobody has run is not a rollback plan. 000076 belongs here for the
# same reason as 000059/000061: it alters seo_urls, which 000073 creates and
# which is itself an expansion, so in the baseline it would run before the
# table exists. 000077 is registered for the 000065/000066 reason: its down
# migration drops prediction_result_revisions and narrows three CHECK
# constraints back to their pre-in-play form, and a narrowing rollback that
# nobody has run is not a rollback plan.
# 000082 adds disabled advertising providers and its audit ledger; verify both paths.
# QA recorded now-withdrawn ad migrations 000083/000084; keep 000085 unique.
# Historical correction and reviewed aliases depend on later result/identity
# tables, so exercise 000086/000087 as expansions in both migration directions.
NEW_VERSIONS = ("000033", "000034", "000035", "000036", "000037", "000038", "000039", "000040", "000041", "000042", "000043", "000044", "000045", "000056", "000057", "000058", "000059", "000060", "000061", "000064", "000065", "000066", "000067", "000068", "000069", "000070", "000071", "000072", "000073", "000076", "000077", "000078", "000079", "000080", "000081", "000082", "000085", "000086", "000087")


def run(*args: str, env: dict[str, str] | None = None, capture: bool = False) -> str:
    completed = subprocess.run(
        args,
        cwd=ROOT,
        env=env,
        check=True,
        text=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.PIPE if capture else None,
    )
    return completed.stdout.strip() if capture else ""


def psql(container: str, sql: str, *, expect_failure: bool = False) -> str:
    command = [
        "docker", "exec", container, "psql", "-X", "-v", "ON_ERROR_STOP=1",
        "-U", "sports", "-d", "migration_verify", "-qAt", "-c", sql,
    ]
    completed = subprocess.run(command, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if expect_failure:
        if completed.returncode == 0:
            raise AssertionError(f"statement unexpectedly succeeded: {sql}")
        return completed.stderr.strip()
    if completed.returncode != 0:
        raise RuntimeError(completed.stderr.strip())
    return completed.stdout.strip()


def dbtool(database_url: str, mode: str, migration_dir: Path) -> None:
    env = os.environ.copy()
    env["DATABASE_URL"] = database_url
    env["APP_ENV"] = "test"
    env["APP_SECRET"] = "disposable-migration-verifier-secret"
    run(
        "go", "run", "./services/api/cmd/dbtool", "-env-file", "", "-mode", mode,
        "-migrations", str(migration_dir), "-timeout", "5m", env=env,
    )


def legacy_hash_sql(prediction_id: str) -> str:
    return f"""
SELECT md5(ROW(
  id, fixture_id, market, prediction, probability, confidence,
  confidence_tier, is_best_bet, lifecycle_stage, data_completeness,
  features_used, explanation, generated_at, superseded_at, gate_tier,
  horizon_eligible, embargo_active, model_version, created_at, public_id,
  content_hash, last_evaluated_at, updated_at
)::text)
FROM engine_predictions WHERE id = '{prediction_id}'::uuid
"""


def main() -> None:
    container = f"sokalens-migration-verify-{uuid.uuid4().hex[:10]}"
    with tempfile.TemporaryDirectory(prefix="sokalens-migrations-") as temp:
        temp_root = Path(temp)
        baseline = temp_root / "baseline"
        expansion = temp_root / "expansion"
        baseline.mkdir()
        expansion.mkdir()
        for source in MIGRATIONS.glob("*.sql"):
            version = source.name.split("_", 1)[0]
            shutil.copy2(source, (expansion if version in NEW_VERSIONS else baseline) / source.name)

        try:
            run(
                "docker", "run", "--detach", "--rm", "--name", container,
                "--publish", "127.0.0.1::5432",
                "--env", "POSTGRES_DB=migration_verify",
                "--env", "POSTGRES_USER=sports",
                "--env", "POSTGRES_PASSWORD=sports",
                "postgres:18-alpine",
            )
            port = run("docker", "port", container, "5432/tcp", capture=True).rsplit(":", 1)[1]

            # Wait on the published port, not on the container's unix socket.
            #
            # `pg_isready` with no -h talks to the socket, and the official
            # image serves that socket during initdb -- before the real server
            # restarts and begins listening on TCP. So the socket answers while
            # the published port still refuses or accepts-then-closes, and the
            # very next line connects over TCP and fails with
            # "failed to receive message: unexpected EOF".
            #
            # A warm CI runner has the image cached and usually wins the race;
            # a cold machine loses it. Checking what the caller will actually
            # use removes the race rather than making it rarer.
            for _ in range(90):
                try:
                    with socket.create_connection(("127.0.0.1", int(port)), timeout=2):
                        pass
                except OSError:
                    time.sleep(1)
                    continue
                ready = subprocess.run(
                    ["docker", "exec", container, "pg_isready",
                     "-h", "127.0.0.1", "-p", "5432", "-U", "sports", "-d", "migration_verify"],
                    stdout=subprocess.DEVNULL,
                    stderr=subprocess.DEVNULL,
                )
                if ready.returncode == 0:
                    break
                time.sleep(1)
            else:
                raise TimeoutError("disposable PostgreSQL did not begin accepting TCP connections")
            database_url = f"postgres://sports:sports@127.0.0.1:{port}/migration_verify?sslmode=disable"
            dbtool(database_url, "up", baseline)
            env = os.environ.copy()
            env.update(DATABASE_URL=database_url, APP_ENV="test", APP_SECRET="disposable-migration-verifier-secret")
            run(
                "go", "run", "./services/api/cmd/dbtool", "-env-file", "", "-mode", "seed",
                "-seed", "infra/db/seeds/001_foundation_seed.sql", "-timeout", "5m", env=env,
            )

            psql(container, """
INSERT INTO fixtures (
  id, league_id, season_id, home_team_id, away_team_id, kickoff_at, status,
  source_match_id, source
)
SELECT id, league_id, EXTRACT(YEAR FROM kickoff_at AT TIME ZONE 'UTC')::integer,
       home_team_id, away_team_id, kickoff_at, status, id, 'migration-verifier'
FROM matches ORDER BY id LIMIT 1
ON CONFLICT (id) DO NOTHING
""")
            prediction_id = psql(container, """
INSERT INTO engine_predictions (
  fixture_id, market, prediction, probability, confidence, confidence_tier,
  is_best_bet, lifecycle_stage, data_completeness, features_used, explanation,
  gate_tier, horizon_eligible, embargo_active, model_version
)
SELECT id, '1x2', 'Home', 0.61, 0.61, 'medium', false, 'PRE_MATCH',
       0.80, '{"source":"legacy-verifier"}'::jsonb, 'legacy row', 'FULL',
       true, false, 'legacy-v1'
FROM fixtures ORDER BY id LIMIT 1
RETURNING id
""").splitlines()[0]
            known_v1_prediction_id = psql(container, """
INSERT INTO engine_predictions (
  fixture_id, market, prediction, probability, confidence, confidence_tier,
  is_best_bet, lifecycle_stage, data_completeness, features_used, explanation,
  gate_tier, horizon_eligible, embargo_active, model_version
)
SELECT id, 'btts_total_goals', 'yes_over_2.5', 0.57, 0.57, 'low', false,
       'PRE_MATCH', 0.80, '{"source":"known-v1-verifier"}'::jsonb,
       'known v1 row', 'FULL', true, false, 'python-engine-v1'
FROM fixtures ORDER BY id LIMIT 1
RETURNING id
""").splitlines()[0]
            psql(container, f"""
INSERT INTO prediction_results (
  prediction_id, fixture_id, market, predicted, actual, is_correct,
  settlement_status, probability, confidence, confidence_tier,
  lifecycle_stage_at_settlement, league_id, settled_at
)
SELECT prediction.id, prediction.fixture_id, prediction.market, prediction.prediction,
       'yes_over_2.5', true, 'settled', prediction.probability,
       prediction.confidence, prediction.confidence_tier, prediction.lifecycle_stage,
       fixtures.league_id, prediction.generated_at + interval '2 hours'
FROM engine_predictions prediction
JOIN fixtures ON fixtures.id = prediction.fixture_id
WHERE prediction.id = '{known_v1_prediction_id}'::uuid
""")
            unavailable_v1_prediction_id = psql(container, """
INSERT INTO engine_predictions (
  fixture_id, market, prediction, probability, confidence, confidence_tier,
  is_best_bet, lifecycle_stage, data_completeness, features_used, explanation,
  gate_tier, horizon_eligible, embargo_active, model_version
)
SELECT id, 'possession_winner', 'home_more', 0.55, 0.55, 'low', false,
       'PRE_MATCH', 0.75, '{"source":"unavailable-v1-verifier"}'::jsonb,
       'unavailable v1 row', 'FULL', true, false, 'python-engine-v1'
FROM fixtures ORDER BY id LIMIT 1
RETURNING id
""").splitlines()[0]
            psql(container, f"""
INSERT INTO prediction_results (
  prediction_id, fixture_id, market, predicted, actual, is_correct,
  settlement_status, probability, confidence, confidence_tier,
  lifecycle_stage_at_settlement, league_id, settled_at
)
SELECT prediction.id, prediction.fixture_id, prediction.market, prediction.prediction,
       NULL, NULL, 'unsettled_data_unavailable', prediction.probability,
       prediction.confidence, prediction.confidence_tier, prediction.lifecycle_stage,
       fixtures.league_id, prediction.generated_at + interval '2 hours'
FROM engine_predictions prediction
JOIN fixtures ON fixtures.id = prediction.fixture_id
WHERE prediction.id = '{unavailable_v1_prediction_id}'::uuid
""")
            before_hash = psql(container, legacy_hash_sql(prediction_id))

            dbtool(database_url, "up", expansion)
            after_hash = psql(container, legacy_hash_sql(prediction_id))
            if before_hash != after_hash:
                raise AssertionError("legacy engine_predictions payload changed during additive migration")
            if psql(container, """
SELECT count(*) || ':' || count(*) FILTER (WHERE enabled)::text || ':' ||
       count(*) FILTER (WHERE health_status = 'DISABLED')::text
FROM ad_providers
WHERE code IN ('adsense', 'adsterra', 'monetag')
""") != "3:0:3":
                raise AssertionError("ad provider catalogue was not seeded disabled by default")
            if psql(container, "SELECT count(*) FROM ad_providers WHERE public_config <> '{}'::jsonb") != "0":
                raise AssertionError("ad provider seed unexpectedly contains public configuration")
            psql(container, """
INSERT INTO ad_provider_outcomes (
  provider_code, placement_key, session_hash, attempt_hash, outcome, duration_ms
) VALUES ('adsense', 'migration_verify', repeat('a', 64), repeat('1', 64), 'success', 100)
""")
            psql(container, """
INSERT INTO ad_provider_outcomes (
  provider_code, placement_key, session_hash, attempt_hash, outcome, failure_reason, duration_ms
) VALUES ('adsense', 'migration_verify', repeat('b', 64), repeat('2', 64), 'failure', 'TIMEOUT', 1200)
""")
            psql(container, """
INSERT INTO ad_provider_outcomes (
  provider_code, placement_key, session_hash, attempt_hash, outcome, failure_reason, duration_ms
) VALUES ('adsense', 'migration_verify', repeat('c', 64), repeat('3', 64), 'success', 'TIMEOUT', 100)
""", expect_failure=True)
            psql(container, """
INSERT INTO ad_provider_outcomes (
  provider_code, placement_key, session_hash, attempt_hash, outcome, failure_reason, duration_ms
) VALUES ('adsense', 'migration_verify', repeat('d', 64), repeat('4', 64), 'failure', 'UNKNOWN', 100)
""", expect_failure=True)
            psql(container, """
INSERT INTO ad_provider_outcomes (
  provider_code, placement_key, session_hash, attempt_hash, outcome, duration_ms
) VALUES ('adsense', 'migration_verify_duplicate', repeat('e', 64), repeat('1', 64), 'success', 101)
""", expect_failure=True)
            psql(container, """
INSERT INTO ad_providers (
  code, name, adapter_key, global_priority, request_timeout_ms
) VALUES ('invalid-adapter', 'Invalid adapter', '../unknown', 40, 1200)
""", expect_failure=True)
            psql(container, """
INSERT INTO ad_providers (
  code, name, adapter_key, global_priority, request_timeout_ms
) VALUES ('invalid-timeout', 'Invalid timeout', 'adsense', 40, 499)
""", expect_failure=True)
            if psql(container, "SELECT finished_at FROM fixtures LIMIT 0") != "":
                raise AssertionError("fixtures.finished_at contract query returned unexpected rows")
            if psql(container, "SELECT * FROM current_engine_predictions LIMIT 0") != "":
                raise AssertionError("current prediction view contract query returned unexpected rows")
            if psql(container, "SELECT * FROM retention_candidates LIMIT 0") != "":
                raise AssertionError("retention candidate view contract query returned unexpected rows")
            if psql(container, """
SELECT engine_prediction_content_hash(
  1, '1x2', 'home', 0.61, 0.61, 'medium', false, 'PRE_MATCH', 0.80,
  '{"source":"verifier","as_of_timestamp":"2026-08-30T10:00:00Z"}'::jsonb,
  'same', 'FULL', true, false, 'python-engine-v1'
) = engine_prediction_content_hash(
  1, '1x2', 'home', 0.61, 0.61, 'medium', false, 'PRE_MATCH', 0.80,
  '{"source":"verifier","as_of_timestamp":"2026-08-30T10:15:00Z"}'::jsonb,
  'same', 'FULL', true, false, 'python-engine-v1'
)
""") != "t":
                raise AssertionError("clock-only feature changes altered the semantic prediction hash")
            psql(container, "UPDATE model_evaluation_facts SET confidence=confidence WHERE true", expect_failure=True)
            psql(container, f"BEGIN; DELETE FROM model_evaluation_facts WHERE prediction_id='{known_v1_prediction_id}'::uuid; ROLLBACK")
            if psql(container, """
SELECT count(*)
FROM market_metadata metadata
JOIN model_registry registry USING (market_family)
WHERE metadata.active = true AND registry.status = 'production'
""") != "11":
                raise AssertionError("ENG-003 did not create exactly one champion for every active market family")
            if psql(container, f"""
SELECT registry.market_family
FROM engine_predictions prediction
JOIN model_registry registry ON registry.id = prediction.model_registry_id
WHERE prediction.id = '{known_v1_prediction_id}'::uuid
""") != "btts":
                raise AssertionError("known python-engine-v1 market was not linked to its reviewed family")
            if psql(container, f"""
SELECT model_registry_id IS NULL
FROM engine_predictions
WHERE id = '{prediction_id}'::uuid
""") != "t":
                raise AssertionError("unknown legacy model version was mapped to a v1 champion")
            if psql(container, f"""
SELECT previous_probability IS NULL AND probability_delta IS NULL AND
       max_lifecycle_probability_shift IS NULL AND
       prediction_direction_changed IS NULL AND lifecycle_stability_score IS NULL
FROM engine_predictions WHERE id = '{prediction_id}'::uuid
""") != "t":
                raise AssertionError("ENG-008 inferred unsafe lifecycle evidence for a legacy row")
            psql(container, f"""
INSERT INTO engine_predictions (
  fixture_id, market, prediction, probability, confidence, confidence_tier,
  is_best_bet, lifecycle_stage, data_completeness, features_used, explanation,
  gate_tier, horizon_eligible, embargo_active, model_version,
  max_lifecycle_probability_shift, prediction_direction_changed,
  lifecycle_stability_score
)
SELECT fixture_id, 'double_chance', 'home_or_draw', 0.65, 0.65, 'medium', false,
       'EARLY', 0.8, '{{"source":"eng-008-verifier"}}'::jsonb,
       'neutral first version', 'FULL', true, false, 'python-engine-v1',
       0, false, 0.5
FROM engine_predictions WHERE id = '{known_v1_prediction_id}'::uuid
""")
            psql(container, f"""
INSERT INTO engine_predictions (
  fixture_id, market, prediction, probability, confidence, confidence_tier,
  is_best_bet, lifecycle_stage, data_completeness, features_used, explanation,
  gate_tier, horizon_eligible, embargo_active, model_version,
  previous_probability, probability_delta, max_lifecycle_probability_shift,
  prediction_direction_changed, lifecycle_stability_score
)
SELECT fixture_id, 'draw_no_bet', 'home', 0.70, 0.65, 'medium', false,
       'PRE_MATCH', 0.8, '{{"source":"eng-008-invalid"}}'::jsonb,
       'invalid shift', 'FULL', true, false, 'python-engine-v1',
       0.60, 0.10, 0.05, false, 0.5
FROM engine_predictions WHERE id = '{known_v1_prediction_id}'::uuid
""", expect_failure=True)
            if psql(container, f"""
SELECT count(*) || ':' || min(settlement_classification) || ':' || min(evaluation_eligible::text)
FROM model_evaluation_facts
WHERE prediction_id = '{known_v1_prediction_id}'::uuid
""") != "1:correct:true":
                raise AssertionError("existing safe v1 settlement did not receive one eligible immutable fact")
            if psql(container, f"""
SELECT final_outcome || ':' || settlement_classification || ':' ||
       evaluation_eligible::text || ':' || exclusion_reason
FROM model_evaluation_facts
WHERE prediction_id = '{unavailable_v1_prediction_id}'::uuid
""") != "data_unavailable:excluded:false:unsettled_data_unavailable":
                raise AssertionError("explicit unavailable settlement was not preserved as an excluded fact")

            # The data migration itself is safe to rerun even when the migration
            # runner has already recorded the version.
            psql(container, (MIGRATIONS / "000036_v1_champion_bootstrap.up.sql").read_text())
            if psql(container, "SELECT count(*) FROM model_registry WHERE status = 'production'") != "11":
                raise AssertionError("ENG-003 rerun created duplicate production champions")
            if psql(container, f"SELECT count(*) FROM model_evaluation_facts WHERE prediction_id = '{known_v1_prediction_id}'::uuid") != "1":
                raise AssertionError("ENG-003 rerun duplicated the historical evaluation fact")
            if psql(container, f"SELECT count(*) FROM model_evaluation_facts WHERE prediction_id = '{unavailable_v1_prediction_id}'::uuid") != "1":
                raise AssertionError("ENG-003 rerun duplicated the unavailable evaluation fact")

            model_id = psql(container, """
INSERT INTO model_registry (
  model_key, model_version, market_family, runtime, feature_version, code_commit, status
) VALUES ('verify-model', 'v1', 'verifier-market', 'python-offline', 'legacy-v1', 'verify-commit', 'production')
RETURNING id
""").splitlines()[0]
            psql(container, """
INSERT INTO model_registry (
  model_key, model_version, market_family, runtime, feature_version, code_commit, status
) VALUES ('second-production', 'v1', 'verifier-market', 'python-offline', 'legacy-v1', 'verify-commit', 'production')
""", expect_failure=True)
            psql(container, f"UPDATE model_registry SET model_version = 'rewritten' WHERE id = '{model_id}'::uuid", expect_failure=True)
            psql(container, f"""
INSERT INTO model_evaluation_facts (
  prediction_id, model_registry_id, fixture_id, market, selection, lifecycle_stage,
  prediction_timestamp, raw_probability, confidence, final_outcome,
  settlement_classification, settled_at, evaluation_eligible,
  evaluation_contract_version
)
SELECT id, '{model_id}'::uuid, fixture_id, market, prediction, lifecycle_stage,
       generated_at, probability, confidence, 'Home', 'correct', generated_at + interval '2 hours',
       true, 'v1'
FROM engine_predictions WHERE id = '{prediction_id}'::uuid
""")
            psql(container, f"BEGIN; DELETE FROM engine_predictions WHERE id = '{prediction_id}'::uuid; ROLLBACK")
            if psql(container, f"SELECT count(*) FROM model_evaluation_facts WHERE prediction_id = '{prediction_id}'::uuid") != "1":
                raise AssertionError("governed raw-parent deletion removed compact model evidence")
            psql(container, f"UPDATE model_evaluation_facts SET confidence = 0.5 WHERE prediction_id = '{prediction_id}'::uuid", expect_failure=True)
            psql(container, """
INSERT INTO ops_authority_grants (
  scope, resource_pattern, autonomy_mode, allowed_operations, granted_by, reason
) VALUES ('services', 'worker-*', 'observe', ARRAY['restart_service'], 'verifier', 'must fail')
""", expect_failure=True)
            psql(container, """
INSERT INTO ops_actions (
  idempotency_key, operation_type, target_scope, target_resource, requested_mode,
  status, requested_by
) VALUES ('observe-execution', 'restart_service', 'services', 'worker-1', 'observe',
          'authorized', 'verifier')
""", expect_failure=True)
            audit_id = psql(container, """
INSERT INTO ops_audit_events (
  event_type, actor_type, actor_id, entity_type, entity_id, event_hash
) VALUES ('verification', 'system', 'migration-verifier', 'migration', '000035', repeat('a', 64))
RETURNING event_id
""").splitlines()[0]
            psql(container, f"DELETE FROM ops_audit_events WHERE event_id = '{audit_id}'::uuid", expect_failure=True)

            incident_id = psql(container, """
INSERT INTO ops_incidents (
  source, source_event_id, fingerprint, title, summary, severity,
  first_observed_at, last_observed_at
) VALUES (
  'migration-verifier', 'event-1', repeat('b', 64), 'Verifier alert',
  'Durable observation contract', 'warning', now(), now()
)
RETURNING id
""").splitlines()[0]
            observation_id = psql(container, f"""
INSERT INTO ops_incident_observations (
  incident_id, source, source_event_id, idempotency_key,
  observed_at, received_at, evidence
) VALUES (
  '{incident_id}'::uuid, 'migration-verifier', 'event-1', repeat('c', 64),
  now(), now(), '{{"classification":"ops-incident-v1"}}'::jsonb
)
RETURNING id
""").splitlines()[0]
            psql(container, f"""
INSERT INTO ops_incident_observations (
  incident_id, source, source_event_id, idempotency_key,
  observed_at, received_at
) VALUES (
  '{incident_id}'::uuid, 'migration-verifier', 'event-1', repeat('d', 64),
  now(), now()
)
""", expect_failure=True)
            psql(container, f"""
INSERT INTO ops_incident_observations (
  incident_id, source, source_event_id, idempotency_key,
  observed_at, received_at
) VALUES (
  '{incident_id}'::uuid, 'migration-verifier', 'event-2', repeat('c', 64),
  now(), now()
)
""", expect_failure=True)
            psql(container, f"UPDATE ops_incident_observations SET evidence = '{{}}'::jsonb WHERE id = '{observation_id}'::uuid", expect_failure=True)
            psql(container, f"DELETE FROM ops_incident_observations WHERE id = '{observation_id}'::uuid", expect_failure=True)

            second_incident_id = psql(container, """
INSERT INTO ops_incidents (
  source, source_event_id, fingerprint, title, summary, severity,
  first_observed_at, last_observed_at
) VALUES (
  'migration-verifier', 'event-2', repeat('e', 64), 'Second verifier alert',
  'Independent audit root contract', 'info', now(), now()
)
RETURNING id
""").splitlines()[0]
            first_transition_id = psql(container, f"""
WITH audit AS (
  INSERT INTO ops_audit_events (
    event_type, actor_type, actor_id, entity_type, entity_id,
    details, event_hash
  ) VALUES (
    'incident.status_transition', 'system', 'migration-verifier',
    'incident', '{incident_id}', '{{"from_status":"open","to_status":"investigating"}}'::jsonb,
    repeat('f', 64)
  ) RETURNING event_id
)
INSERT INTO ops_incident_transitions (
  incident_id, idempotency_key, from_status, to_status, reason,
  actor_type, actor_id, occurred_at, audit_event_id, previous_event_hash
)
SELECT '{incident_id}'::uuid, repeat('1', 64), 'open', 'investigating',
       'verify first root', 'system', 'migration-verifier', now(), event_id, NULL
FROM audit
RETURNING id
""").splitlines()[0]
            psql(container, f"""
WITH audit AS (
  INSERT INTO ops_audit_events (
    event_type, actor_type, actor_id, entity_type, entity_id,
    details, event_hash
  ) VALUES (
    'incident.status_transition', 'system', 'migration-verifier',
    'incident', '{second_incident_id}', '{{"from_status":"open","to_status":"suppressed"}}'::jsonb,
    repeat('2', 64)
  ) RETURNING event_id
)
INSERT INTO ops_incident_transitions (
  incident_id, idempotency_key, from_status, to_status, reason,
  actor_type, actor_id, occurred_at, audit_event_id, previous_event_hash
)
SELECT '{second_incident_id}'::uuid, repeat('3', 64), 'open', 'suppressed',
       'verify independent root', 'system', 'migration-verifier', now(), event_id, NULL
FROM audit
""")
            psql(container, f"""
WITH audit AS (
  INSERT INTO ops_audit_events (
    event_type, actor_type, actor_id, entity_type, entity_id,
    details, event_hash
  ) VALUES (
    'incident.status_transition', 'system', 'migration-verifier',
    'incident', '{incident_id}', '{{"from_status":"open","to_status":"suppressed"}}'::jsonb,
    repeat('4', 64)
  ) RETURNING event_id
)
INSERT INTO ops_incident_transitions (
  incident_id, idempotency_key, from_status, to_status, reason,
  actor_type, actor_id, occurred_at, audit_event_id, previous_event_hash
)
SELECT '{incident_id}'::uuid, repeat('5', 64), 'open', 'suppressed',
       'must reject second root', 'system', 'migration-verifier', now(), event_id, NULL
FROM audit
""", expect_failure=True)
            psql(container, f"""
WITH audit AS (
  INSERT INTO ops_audit_events (
    event_type, actor_type, actor_id, entity_type, entity_id,
    details, previous_event_hash, event_hash
  ) VALUES (
    'incident.status_transition', 'system', 'migration-verifier',
    'incident', '{incident_id}', '{{"from_status":"investigating","to_status":"mitigated"}}'::jsonb,
    repeat('f', 64), repeat('6', 64)
  ) RETURNING event_id
)
INSERT INTO ops_incident_transitions (
  incident_id, idempotency_key, from_status, to_status, reason,
  actor_type, actor_id, occurred_at, audit_event_id, previous_event_hash
)
SELECT '{incident_id}'::uuid, repeat('7', 64), 'investigating', 'mitigated',
       'verify successor', 'system', 'migration-verifier', now(), event_id, repeat('f', 64)
FROM audit
""")
            psql(container, f"""
WITH audit AS (
  INSERT INTO ops_audit_events (
    event_type, actor_type, actor_id, entity_type, entity_id,
    details, previous_event_hash, event_hash
  ) VALUES (
    'incident.status_transition', 'system', 'migration-verifier',
    'incident', '{incident_id}', '{{"from_status":"investigating","to_status":"suppressed"}}'::jsonb,
    repeat('f', 64), repeat('8', 64)
  ) RETURNING event_id
)
INSERT INTO ops_incident_transitions (
  incident_id, idempotency_key, from_status, to_status, reason,
  actor_type, actor_id, occurred_at, audit_event_id, previous_event_hash
)
SELECT '{incident_id}'::uuid, repeat('9', 64), 'investigating', 'suppressed',
       'must reject fork', 'system', 'migration-verifier', now(), event_id, repeat('f', 64)
FROM audit
""", expect_failure=True)
            psql(container, f"UPDATE ops_incident_transitions SET reason = 'rewritten' WHERE id = '{first_transition_id}'::uuid", expect_failure=True)
            psql(container, f"DELETE FROM ops_incident_transitions WHERE id = '{first_transition_id}'::uuid", expect_failure=True)

            psql(container, """
INSERT INTO ops_jobs (
  queue_name, job_type, idempotency_key, priority, payload, status,
  available_at, max_attempts, created_at, updated_at
) VALUES
  ('critical', 'verify_job', repeat('a', 63) || '1', 900, '{"service":"api"}'::jsonb,
   'pending', now(), 3, now() - interval '2 seconds', now()),
  ('critical', 'verify_job', repeat('a', 63) || '2', 600, '{"service":"worker"}'::jsonb,
   'pending', now(), 3, now() - interval '1 second', now())
""")
            psql(container, """
INSERT INTO ops_jobs (
  queue_name, job_type, idempotency_key, priority, payload, status,
  available_at, max_attempts, lease_owner, lease_token, lease_expires_at
) VALUES (
  'critical', 'invalid_lease', repeat('b', 64), 900, '{}'::jsonb, 'pending',
  now(), 3, 'worker', gen_random_uuid(), now() + interval '1 minute'
)
""", expect_failure=True)
            psql(container, """
INSERT INTO ops_jobs (
  queue_name, job_type, idempotency_key, priority, payload, status,
  available_at, max_attempts, completed_at
) VALUES (
  'critical', 'invalid_dead_letter', repeat('c', 64), 900, '{}'::jsonb,
  'dead_letter', now(), 3, now()
)
""", expect_failure=True)

            lock_command = [
                "docker", "exec", container, "psql", "-X", "-v", "ON_ERROR_STOP=1",
                "-U", "sports", "-d", "migration_verify", "-qAt", "-c",
                """BEGIN;
SELECT id FROM ops_jobs
WHERE queue_name = 'critical' AND status = 'pending'
ORDER BY priority DESC, available_at, created_at, id
FOR UPDATE LIMIT 1;
SELECT pg_advisory_xact_lock(40040);
SELECT pg_sleep(10);
COMMIT;""",
            ]
            lock_process = subprocess.Popen(
                lock_command, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            for _ in range(20):
                if psql(container, "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND objid = 40040 AND granted") == "1":
                    break
                time.sleep(0.1)
            else:
                lock_process.kill()
                raise AssertionError("priority lock session did not reach the concurrent claim gate")
            skipped_claim = psql(container, """
WITH candidate AS (
  SELECT id FROM ops_jobs
  WHERE queue_name = 'critical' AND status = 'pending'
  ORDER BY priority DESC, available_at, created_at, id
  FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE ops_jobs job
SET status = 'leased', attempts = attempts + 1, lease_owner = 'worker-b',
    lease_token = gen_random_uuid(), lease_expires_at = now() + interval '1 minute',
    started_at = now(), updated_at = now()
FROM candidate WHERE job.id = candidate.id
RETURNING job.priority
""")
            lock_stdout, lock_stderr = lock_process.communicate(timeout=15)
            if lock_process.returncode != 0:
                raise RuntimeError(lock_stderr.strip())
            if skipped_claim != "600":
                raise AssertionError(f"SKIP LOCKED claim did not take independent P1 work: {skipped_claim!r}")
            if not lock_stdout.strip():
                raise AssertionError("priority lock session did not select P0 work")
            highest_claim = psql(container, """
WITH candidate AS (
  SELECT id FROM ops_jobs
  WHERE queue_name = 'critical' AND status = 'pending'
  ORDER BY priority DESC, available_at, created_at, id
  FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE ops_jobs job
SET status = 'leased', attempts = attempts + 1, lease_owner = 'worker-a',
    lease_token = gen_random_uuid(), lease_expires_at = now() + interval '1 minute',
    started_at = now(), updated_at = now()
FROM candidate WHERE job.id = candidate.id
RETURNING job.priority
""")
            if highest_claim != "900":
                raise AssertionError(f"priority mapping/order did not claim P0 first: {highest_claim!r}")

            reconciliation_run_id = psql(container, """
INSERT INTO entitlement_reconciliation_runs (
  idempotency_key,input_hash,provider,environment,started_at,completed_at,
  result,item_count,matched_count,mismatch_count,failed_count,correction_count
) VALUES (
  'migration-verifier-run',repeat('a',64),'mock-billing','test',now()-interval '1 second',now(),
  'failed',1,0,0,1,1
) RETURNING id
""")
            reconciliation_item_id = psql(container, f"""
INSERT INTO entitlement_reconciliation_items (
  run_id,classification,result,details
) VALUES (
  {reconciliation_run_id},'provider_failure','failed','{{"failure_code":"provider_timeout"}}'::jsonb
) RETURNING id
""")
            reconciliation_correction_id = psql(container, f"""
INSERT INTO entitlement_reconciliation_corrections (
  run_id,item_id,idempotency_key,action,effective_at,applied,details
) VALUES (
  {reconciliation_run_id},{reconciliation_item_id},repeat('b',64),'freeze_new_grants',now(),true,'{{}}'::jsonb
) RETURNING id
""")
            psql(container, f"""
INSERT INTO entitlement_reconciliation_effects (idempotency_key,first_correction_id,applied_at)
VALUES (repeat('b',64),{reconciliation_correction_id},now())
""")
            psql(container, f"""
INSERT INTO entitlement_grant_freezes (
  provider,environment,subscription_id,active,reason,last_correction_id,frozen_at
) VALUES ('mock-billing','test',NULL,true,'provider_reconciliation_failure',{reconciliation_correction_id},now())
""")
            psql(container, f"UPDATE entitlement_reconciliation_runs SET result='completed' WHERE id={reconciliation_run_id}", expect_failure=True)
            psql(container, f"DELETE FROM entitlement_reconciliation_corrections WHERE id={reconciliation_correction_id}", expect_failure=True)
            psql(container, "DELETE FROM entitlement_reconciliation_effects WHERE idempotency_key=repeat('b',64)", expect_failure=True)
            psql(container, f"""
INSERT INTO entitlement_reconciliation_effects (idempotency_key,first_correction_id,applied_at)
VALUES (repeat('b',64),{reconciliation_correction_id},now())
""", expect_failure=True)
            psql(container, f"""
INSERT INTO entitlement_grant_freezes (
  provider,environment,subscription_id,active,reason,last_correction_id,frozen_at
) VALUES ('mock-billing','test',NULL,true,'duplicate_scope',{reconciliation_correction_id},now())
""", expect_failure=True)

            psql(container, """
BEGIN;
WITH artifact AS (
  INSERT INTO ops_component_versions (
    component,environment,source_sha,image_digest,manifest_digest,
    registered_at,retain_until,retention_reason
  ) VALUES (
    'ops-controller','test',repeat('c',40),'sha256:' || repeat('d',64),
    'sha256:' || repeat('e',64),TIMESTAMPTZ '2026-08-16 00:00:00+00',
    TIMESTAMPTZ '2027-08-16 00:00:00+00','migration verifier retention'
  ) RETURNING id
), transition AS (
  INSERT INTO ops_component_version_transitions (
    component,environment,idempotency_key,input_hash,action,
    expected_revision,resulting_revision,after_candidate_id,actor,reason,occurred_at
  ) SELECT 'ops-controller','test','verifier-set-candidate',repeat('f',64),
    'set_candidate',0,1,id,'migration-verifier','verify audited head creation',
    TIMESTAMPTZ '2026-08-16 00:00:01+00'
  FROM artifact
)
INSERT INTO ops_component_version_heads (
  component,environment,revision,candidate_version_id,updated_at
) SELECT 'ops-controller','test',1,id,TIMESTAMPTZ '2026-08-16 00:00:01+00'
FROM artifact;
COMMIT;
""")
            if psql(container, "SELECT revision FROM ops_component_version_heads WHERE component='ops-controller' AND environment='test'") != "1":
                raise AssertionError("audited component version head was not created")
            psql(container, "UPDATE ops_component_version_heads SET revision=2 WHERE component='ops-controller' AND environment='test'", expect_failure=True)
            psql(container, """
BEGIN;
WITH artifact AS (
  INSERT INTO ops_component_versions (
    component,environment,source_sha,image_digest,manifest_digest,
    registered_at,retain_until,retention_reason
  ) VALUES (
    'ops-controller','test',repeat('1',40),'sha256:' || repeat('2',64),
    'sha256:' || repeat('3',64),TIMESTAMPTZ '2026-08-16 00:00:02+00',
    TIMESTAMPTZ '2027-08-16 00:00:02+00','migration verifier retention'
  ) RETURNING id
)
INSERT INTO ops_component_version_transitions (
  component,environment,idempotency_key,input_hash,action,
  expected_revision,resulting_revision,before_candidate_id,after_candidate_id,
  actor,reason,occurred_at
) SELECT 'ops-controller','test','verifier-replace-candidate',repeat('4',64),
  'set_candidate',1,2,
  (SELECT candidate_version_id FROM ops_component_version_heads
   WHERE component='ops-controller' AND environment='test'),
  id,'migration-verifier','verify audited head update',
  TIMESTAMPTZ '2026-08-16 00:00:03+00'
FROM artifact;
UPDATE ops_component_version_heads
SET revision=2,
    candidate_version_id=(SELECT id FROM ops_component_versions WHERE source_sha=repeat('1',40)),
    updated_at=TIMESTAMPTZ '2026-08-16 00:00:03+00'
WHERE component='ops-controller' AND environment='test';
COMMIT;
""")
            if psql(container, "SELECT revision FROM ops_component_version_heads WHERE component='ops-controller' AND environment='test'") != "2":
                raise AssertionError("second audited component version head was not created")
            psql(container, """
UPDATE ops_component_version_heads
SET revision=1,
    candidate_version_id=(SELECT id FROM ops_component_versions WHERE source_sha=repeat('c',40)),
    updated_at=TIMESTAMPTZ '2026-08-16 00:00:01+00'
WHERE component='ops-controller' AND environment='test'
""", expect_failure=True)
            psql(container, "UPDATE ops_component_versions SET retention_reason='rewritten' WHERE component='ops-controller' AND environment='test'", expect_failure=True)
            psql(container, "DELETE FROM ops_component_version_transitions WHERE component='ops-controller' AND environment='test'", expect_failure=True)

            dbtool(database_url, "down", expansion)
            rollback_hash = psql(container, legacy_hash_sql(prediction_id))
            if before_hash != rollback_hash:
                raise AssertionError("legacy engine_predictions payload changed during rollback")
            if psql(container, "SELECT to_regclass('public.model_registry') IS NULL") != "t":
                raise AssertionError("expansion tables remained after rollback")
            if psql(container, "SELECT to_regclass('public.ad_providers') IS NULL") != "t":
                raise AssertionError("advertising provider tables remained after rollback")
            if psql(container, "SELECT to_regclass('public.inplay_publication_state') IS NULL") != "t":
                raise AssertionError("in-play publication state remained after rollback")
            if psql(container, "SELECT to_regclass('public.fixture_identity_aliases') IS NULL") != "t":
                raise AssertionError("fixture identity aliases remained after rollback")
            if psql(container, "SELECT to_regclass('public.effective_model_evaluation_facts') IS NULL") != "t":
                raise AssertionError("effective model evidence view remained after rollback")

            dbtool(database_url, "up", expansion)
            if psql(container, """
SELECT count(*) FROM schema_migrations
WHERE version LIKE '000033_%'
   OR version LIKE '000034_%'
   OR version LIKE '000035_%'
   OR version LIKE '000036_%'
   OR version LIKE '000037_%'
   OR version LIKE '000038_%'
   OR version LIKE '000039_%'
   OR version LIKE '000040_%'
   OR version LIKE '000041_%'
   OR version LIKE '000042_%'
   OR version LIKE '000043_%'
   OR version LIKE '000044_%'
   OR version LIKE '000045_%'
   OR version LIKE '000056_%'
   OR version LIKE '000057_%'
""") != "15":
                raise AssertionError("up/down/up did not restore all expansion migration records")
            if psql(container, "SELECT count(*) FROM schema_migrations WHERE version = '000082_ad_provider_routing'") != "1":
                raise AssertionError("up/down/up did not restore ad provider routing migration record")
            if psql(container, "SELECT count(*) FROM schema_migrations WHERE version = '000085_inplay_publication_state'") != "1":
                raise AssertionError("up/down/up did not restore in-play publication migration record")
            if psql(container, "SELECT to_regclass('public.inplay_publication_state') IS NOT NULL") != "t":
                raise AssertionError("in-play publication state was not restored")
            if psql(container, "SELECT count(*) FROM schema_migrations WHERE version = '000086_historical_result_correction'") != "1":
                raise AssertionError("up/down/up did not restore historical correction migration record")
            if psql(container, "SELECT count(*) FROM schema_migrations WHERE version = '000087_reviewed_identity_aliases'") != "1":
                raise AssertionError("up/down/up did not restore reviewed identity alias migration record")
            if psql(container, "SELECT to_regclass('public.effective_model_evaluation_facts') IS NOT NULL") != "t":
                raise AssertionError("effective model evidence view was not restored")
            if psql(container, "SELECT to_regclass('public.fixture_identity_aliases') IS NOT NULL") != "t":
                raise AssertionError("fixture identity alias table was not restored")
            final_hash = psql(container, legacy_hash_sql(prediction_id))
            if before_hash != final_hash:
                raise AssertionError("legacy engine_predictions payload changed after up/down/up")
            print("PASS: additive up/down/up, v1 champion bootstrap, legacy preservation, incident-chain integrity, durable concurrent job claims, entitlement reconciliation deduplication, component version audit integrity, bounded retention, and immutability")
        finally:
            subprocess.run(["docker", "rm", "--force", container], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == "__main__":
    main()

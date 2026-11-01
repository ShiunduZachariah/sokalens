from __future__ import annotations

import json
import os
import unittest

from prediction_engine.db_repository import _UPSERT_HISTORY_BACKFILL_SQL


class BackfillLifecyclePostgresTests(unittest.TestCase):
    def test_expired_future_request_reopens_without_reopening_played_or_completed(self) -> None:
        url = os.getenv("PREDICTION_ENGINE_TEST_DATABASE_URL")
        if not url:
            self.skipTest("set PREDICTION_ENGINE_TEST_DATABASE_URL to a disposable PostgreSQL database")
        import psycopg

        with psycopg.connect(url) as connection:
            # pg_temp resolves ahead of public already, but only for the names
            # created below. CI points this at a migrated database, so a table
            # named by _UPSERT_HISTORY_BACKFILL_SQL and not created here would
            # resolve to the real, empty public table and the statement would
            # return nothing instead of failing. Restricting the path makes the
            # shadowing structural; the assertion keeps it honest, because SET
            # is per-session.
            connection.execute("SET search_path TO pg_temp")
            search_path = connection.execute("SELECT current_setting('search_path')").fetchone()[0]
            self.assertEqual(
                search_path,
                "pg_temp",
                "public is reachable from this session, so an unqualified name could resolve to a real table",
            )
            for statement in (
                """CREATE TEMP TABLE fixtures (
                    id BIGINT PRIMARY KEY, kickoff_at TIMESTAMPTZ NOT NULL)""",
                """CREATE TEMP TABLE data_backfill_requests (
                    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
                    target_fixture_id BIGINT NOT NULL,
                    team_id BIGINT NOT NULL,
                    competition_id BIGINT NOT NULL,
                    feature_group TEXT NOT NULL,
                    matches_needed INTEGER NOT NULL,
                    status TEXT NOT NULL,
                    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 12),
                    requested_at TIMESTAMPTZ NOT NULL DEFAULT now(),
                    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
                    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
                    expires_at TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '24 hours'),
                    completed_at TIMESTAMPTZ,
                    last_error TEXT,
                    UNIQUE (target_fixture_id, team_id, competition_id, feature_group),
                    CHECK (
                        (status IN ('completed', 'confirmed_unavailable', 'expired')
                         AND completed_at IS NOT NULL)
                        OR (status IN ('pending', 'in_progress') AND completed_at IS NULL)
                    ))""",
                """CREATE TEMP TABLE jobs (
                    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
                    job_key TEXT NOT NULL, status TEXT NOT NULL,
                    payload JSONB NOT NULL, queue TEXT NOT NULL, priority INTEGER NOT NULL,
                    run_after TIMESTAMPTZ NOT NULL, idempotency_key TEXT,
                    max_attempts INTEGER NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT now())""",
                """CREATE UNIQUE INDEX jobs_active_key ON jobs (idempotency_key)
                    WHERE idempotency_key IS NOT NULL AND status IN ('queued', 'running')""",
                """INSERT INTO fixtures (id, kickoff_at) VALUES
                    (100, now() + interval '2 days'),
                    (200, now() - interval '1 day'),
                    (300, now() + interval '2 days')""",
                """INSERT INTO data_backfill_requests
                    (target_fixture_id, team_id, competition_id, feature_group, matches_needed,
                     status, attempts, requested_at, expires_at, completed_at, last_error)
                    VALUES
                    (100, 10, 39, 'results', 5, 'expired', 12,
                     now() - interval '3 days', now() - interval '1 day',
                     now() - interval '1 day', 'provider returned status 500'),
                    (200, 20, 39, 'results', 5, 'expired', 1,
                     now() - interval '3 days', now() - interval '1 day',
                     now() - interval '1 day', 'REQUEST_EXPIRED'),
                    (300, 30, 39, 'results', 5, 'completed', 1,
                     now() - interval '3 days', now() - interval '1 day',
                     now() - interval '1 day', NULL)""",
            ):
                connection.execute(statement)

            requests = [
                dict(target_fixture_id=fixture_id, team_id=team_id,
                     competition_id=39, feature_group="results", matches_needed=5)
                for fixture_id, team_id in ((100, 10), (200, 20), (300, 30))
            ]
            connection.execute(
                _UPSERT_HISTORY_BACKFILL_SQL,
                {"requests": json.dumps(requests)},
            ).fetchall()
            rows = connection.execute(
                """SELECT target_fixture_id, status, attempts,
                          completed_at IS NULL, expires_at > now(), last_error
                   FROM data_backfill_requests ORDER BY target_fixture_id"""
            ).fetchall()
            self.assertEqual(rows[0][:5], (100, "pending", 0, True, True))
            self.assertIn("status 500", rows[0][5])
            self.assertEqual(rows[1][:4], (200, "expired", 1, False))
            self.assertEqual(rows[2][:4], (300, "completed", 1, False))
            self.assertEqual(
                connection.execute("SELECT count(*) FROM jobs WHERE status = 'queued'").fetchone()[0],
                1,
            )


if __name__ == "__main__":
    unittest.main()

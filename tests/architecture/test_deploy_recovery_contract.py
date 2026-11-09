import os
from pathlib import Path
import re
import subprocess
import tempfile
import time
import unittest


ROOT = Path(__file__).resolve().parents[2]


class DeployRecoveryContractTests(unittest.TestCase):
    def test_frontend_build_caches_are_isolated_by_application(self) -> None:
        """Turbopack artifacts reference each application's dependency graph."""
        cache_ids = []
        for app in ("web", "admin"):
            source = (ROOT / "apps" / app / "Dockerfile").read_text(encoding="utf-8")
            mounts = re.findall(r"--mount=([^\s]+)", source)
            next_caches = [
                dict(part.split("=", 1) for part in mount.split(","))
                for mount in mounts
                if "target=/app/.next/cache" in mount
            ]
            self.assertEqual(len(next_caches), 1, app)
            mount = next_caches[0]
            self.assertEqual(mount.get("type"), "cache")
            # Docker defaults the id to target; identical /app paths collide.
            cache_ids.append(mount.get("id", mount["target"]))
        self.assertEqual(len(set(cache_ids)), len(cache_ids),
                         "web and admin must not share Turbopack dependency caches")

    def test_backup_is_restore_verified_and_does_not_require_r2(self) -> None:
        source = (ROOT / "infra/deploy/backup.sh").read_text(encoding="utf-8")
        for required in (
            "pg_dump",
            "--format=custom",
            "sha256sum",
            "pg_restore --list",
            "createdb",
            "pg_restore",
            "dropdb",
            "schema_migrations",
        ):
            self.assertIn(required, source)
        self.assertNotIn("R2_ENDPOINT:?", source)

    def test_the_faster_recovery_point_still_checks_everything(self) -> None:
        source = (ROOT / "infra/deploy/backup.sh").read_text(encoding="utf-8")
        # Parallel restore cannot read a pipe, so a dump read from stdin would make
        # the --jobs flag fail or quietly stop being parallel.
        self.assertIn('--file="$CONTAINER_DUMP"', source)
        self.assertIn('--jobs="$RESTORE_JOBS"', source)
        self.assertIn('--exit-on-error "$CONTAINER_DUMP"', source)
        self.assertNotIn('> "$DUMP_FILE"', source)
        self.assertNotIn('< "$DUMP_FILE"', source)
        # The verification is the point of this step: a faster one may not skip it.
        for check in ("sha256sum --check", "pg_restore --list", "critical_counts", "RESTORE_MIGRATIONS"):
            self.assertIn(check, source)
        # Only the throwaway restore may give up durability.
        self.assertEqual(source.count("synchronous_commit=off"), 1)
        self.assertLess(source.index("synchronous_commit=off"), source.index('-d "$RESTORE_DB" --jobs'))
        # The file inside the container is the same size as the dump, on the same disk.
        self.assertIn('docker exec "$CONTAINER" rm -f "$CONTAINER_DUMP"', source)
        self.assertIn("rm -f /tmp/sokalens-*.dump", source)

    def test_the_dump_keeps_every_table(self) -> None:
        source = (ROOT / "infra/deploy/backup.sh").read_text(encoding="utf-8")
        # Tempting, because the provider logs are large. But provider_entity_maps
        # references provider_raw_payloads, which references provider_request_logs, so
        # leaving their rows out fails the restore check on real data. A test database
        # with no such links would not show it.
        self.assertNotIn("--exclude-table", source)
        self.assertNotIn("--exclude-schema", source)

    def _only_adds(self, sql: str) -> bool:
        source = (ROOT / "infra/deploy/ci-deploy.sh").read_text(encoding="utf-8")
        start = source.index("migration_only_adds() {")
        end = source.index("\n}\n", start) + 3
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "m.up.sql"
            path.write_text(sql, encoding="utf-8")
            script = Path(directory) / "check.sh"
            script.write_text(
                "set -u\n" + source[start:end] + 'migration_only_adds "$1"\n', encoding="utf-8", newline="\n"
            )
            result = subprocess.run(["bash", script.as_posix(), path.as_posix()], capture_output=True, text=True)
        self.assertIn(result.returncode, (0, 1), result.stderr)
        return result.returncode == 0

    def test_additive_migrations_skip_the_recovery_point(self) -> None:
        for sql in (
            "CREATE TABLE t (id BIGINT PRIMARY KEY, note TEXT);",
            "CREATE TABLE t (id BIGINT REFERENCES u (id) ON DELETE CASCADE ON UPDATE RESTRICT);",
            "-- why this exists\nCREATE TABLE IF NOT EXISTS t (id BIGINT);\nCREATE INDEX idx ON t (id);",
            "CREATE UNIQUE INDEX CONCURRENTLY idx ON t (id);",
            "ALTER TABLE t ADD COLUMN note TEXT;",
            "alter table if exists t add column note text;\nCOMMENT ON TABLE t IS 'kept';",
            "BEGIN;\nCREATE TABLE t (id BIGINT);\nCOMMIT;",
            "CREATE TRIGGER g BEFORE UPDATE OR DELETE ON t FOR EACH ROW EXECUTE FUNCTION f();",
            "INSERT INTO t (name) VALUES ('seed') ON CONFLICT (name) DO NOTHING;",
        ):
            self.assertTrue(self._only_adds(sql), sql)

    def test_anything_else_takes_the_recovery_point(self) -> None:
        for sql in (
            "DROP TABLE t;",
            "ALTER TABLE t DROP COLUMN c;",
            "ALTER TABLE t ADD COLUMN a INT, DROP COLUMN b;",
            "ALTER TABLE t ALTER COLUMN c TYPE TEXT;",
            "ALTER TABLE t RENAME COLUMN a TO b;",
            "UPDATE t SET a = 1;",
            "DELETE FROM t;",
            "TRUNCATE t;",
            "INSERT INTO t VALUES (1);",
            "INSERT INTO t (a) VALUES (1) ON CONFLICT (a) DO UPDATE SET a = 2;",
            "CREATE TABLE t (id INT); UPDATE u SET a = 1;",
            "CREATE FUNCTION f() RETURNS INT AS $$ BEGIN RETURN 1; END $$ LANGUAGE plpgsql;",
            "DO $$ BEGIN PERFORM 1; END $$;",
            "/* block comment */ CREATE TABLE t (id INT);",
            "SELECT something_unexpected();",
        ):
            self.assertFalse(self._only_adds(sql), sql)

    def test_real_migrations_are_classified_as_expected(self) -> None:
        directory = ROOT / "infra/db/migrations"
        for name in ("000089_raw_prediction_drain_cursor", "000092_challenger_market_forecasts"):
            self.assertTrue(self._only_adds((directory / f"{name}.up.sql").read_text(encoding="utf-8")), name)
        self.assertFalse(self._only_adds((directory / "000091_champion_python_engine_v2.up.sql").read_text(encoding="utf-8")))

    def test_backup_clears_what_a_killed_deploy_left_behind(self) -> None:
        source = (ROOT / "infra/deploy/backup.sh").read_text(encoding="utf-8")
        waits = source.index("stale_restore_sessions)")
        drops = source.index('dropdb --if-exists -U "$DB_USER" "$stale_restore"')
        dumps = source.index("pg_dump -U")
        self.assertLess(waits, drops)
        self.assertLess(drops, dumps)
        # Anything looser could reach the production database itself.
        self.assertEqual(source.count("LIKE 'sokalens\\_restore\\_%'"), 2)

    def test_deploy_budget_leaves_room_for_the_recovery_point(self) -> None:
        source = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        job = source[source.index("\n  deploy:\n"):]
        budget = re.search(r"timeout-minutes: (\d+)", job)
        self.assertIsNotNone(budget)
        # A dump and a restore check have taken over an hour between them.
        self.assertGreaterEqual(int(budget.group(1)), 120)

    def test_deploy_has_lkg_health_rollback_and_bounded_gc(self) -> None:
        source = (ROOT / "infra/deploy/ci-deploy.sh").read_text(encoding="utf-8")
        for required in (
            "RELEASE_SHA",
            "lkg",
            "rollback_release",
            "wait_for_container",
            "/readyz",
            "docker builder prune",
            "--keep-storage",
            "BUILD_CACHE_KEEP:-3GB",
            "prune_old_lkg_images",
            "LKG_IMAGES_KEEP:-3",
            "grep '^sokalens-lkg-'",
            "sort -u || true",
            "{{.Repository}}:{{.Tag}}",
            "PREDICTION_SCHEDULE_INTERVAL",
        ):
            self.assertIn(required, source)
        for forbidden in (
            "-mode down",
            "VACUUM FULL",
            "FLUSHALL",
            "docker volume rm",
            "docker builder prune -af",
            "until=24h",
            "until=168h",
            "--filter until",
        ):
            self.assertNotIn(forbidden, source)

        self.assertIn("BACKUP_DIR=/home/deploy/backups/sokalens", source)
        self.assertNotIn("BACKUP_DIR=/var/backups", source)

    def test_production_defaults_use_fifteen_minute_prediction_cadence(self) -> None:
        compose = (ROOT / "infra/docker/docker-compose.yml").read_text(encoding="utf-8")
        template = (ROOT / "infra/deploy/.env.prod.example").read_text(encoding="utf-8")
        deploy = (ROOT / "infra/deploy/ci-deploy.sh").read_text(encoding="utf-8")
        self.assertIn("PREDICTION_SCHEDULE_INTERVAL:-15m", compose)
        self.assertIn("PREDICTION_SCHEDULE_INTERVAL=15m", template)
        self.assertIn("ENABLE_DESTRUCTIVE_RETENTION:-false", compose)
        self.assertIn("RETENTION_DRY_RUN:-true", compose)
        self.assertIn("ENABLE_DESTRUCTIVE_RETENTION=false", template)
        self.assertIn("RETENTION_DRY_RUN=true", template)
        self.assertIn('ensure_env_value ENABLE_DESTRUCTIVE_RETENTION false', deploy)
        self.assertIn('ensure_env_value RETENTION_DRY_RUN true', deploy)


    def test_acca_odds_floor_is_compared_at_storage_precision(self) -> None:
        """combined_fair_odds is numeric(4dp); the floor must be rounded to match.

        A slip composed exactly at the floor stores as 1.8371 while
        power(1.5, 3/2.0) is 1.8371173070873836. An unrounded comparison
        rejects it by 1.7e-05 -- a slip that meets the standard as written,
        failed on representation alone. One such slip held the readiness gate's
        invalid count at 1 permanently, and it requires zero, so every frontend
        deploy failed after a 40-minute wait.

        All three call sites must agree, or the generator, the public listing
        and the deploy gate disagree about the same slip.
        """
        for relative in (
            "infra/deploy/acca-readiness.sh",
            "services/worker/internal/acca/repository.go",
            "services/api/internal/acca/standards.go",
        ):
            source = (ROOT / relative).read_text(encoding="utf-8")
            self.assertIn("round(power(1.5", source, f"{relative} compares against an unrounded floor")
            # Every predicate must use the rounded form; a stray unrounded one
            # would reintroduce the boundary rejection in just that path. Match
            # the predicate specifically -- prose in comments may name the
            # unrounded expression while explaining why it is wrong.
            predicates = re.findall(r"power\(1\.5, %?s\.leg_count", source)
            rounded = re.findall(r"round\(power\(1\.5, %?s\.leg_count", source)
            self.assertEqual(
                len(predicates),
                len(rounded),
                f"{relative} still has an unrounded floor comparison",
            )

if __name__ == "__main__":
    unittest.main()


class BackupRetentionContractTests(unittest.TestCase):
    def test_only_the_newest_recovery_point_is_kept_locally(self) -> None:
        """Fourteen days of dumps was 26GB of unusable history.

        Fixtures, odds and predictions stream in continuously, so a dump from
        last Tuesday describes a world that no longer exists: restoring it into
        production would throw away everything since, which means it would never
        actually be used. What it did do was take one dump per deploy -- several
        a day, 741MB each and growing from 161MB a fortnight earlier -- and keep
        every one of them for a fortnight.

        The local copy exists to undo the mistake you just made, while you still
        remember making it. One is enough. History belongs off-site in R2.
        """
        source = (ROOT / "infra/deploy/backup.sh").read_text(encoding="utf-8")

        self.assertNotIn("-mtime +14", source, "the fortnight of stale dumps is back")
        self.assertIn("KEEP_LOCAL_DUMPS", source)

        # Pruning must happen after verification and upload, so a failed run
        # leaves the previous recovery point standing rather than no recovery
        # point at all.
        self.assertLess(
            source.index("pg_restore --list"),
            source.index("KEEP_LOCAL_DUMPS"),
            "dumps are pruned before the new one is verified",
        )
        self.assertLess(source.index("R2_ENDPOINT"), source.index("KEEP_LOCAL_DUMPS"))

    def test_deploy_takes_a_recovery_point_only_for_pending_migrations(self) -> None:
        """A pre-migration recovery point protects the irreversible boundary."""
        source = (ROOT / "infra/deploy/ci-deploy.sh").read_text(encoding="utf-8")

        self.assertIn("pending_migration_count", source)
        self.assertIn('if [ "$BACKUP_PENDING_MIGRATIONS" -eq 0 ]', source)
        self.assertIn("migration_only_adds", source)
        self.assertIn("pending_are_readable", source)
        self.assertIn('BACKUP_DIR="$BACKUP_DIR" bash "$NEW_DIR/infra/deploy/backup.sh"', source)
        self.assertIn("backup: skipped, no pending migrations", source)
        self.assertNotIn("BACKUP_MIN_INTERVAL_HOURS", source)

    def test_the_build_cache_is_bounded_by_size_rather_than_age(self) -> None:
        """Pruning by age is what made deploys start cold.

        A layer's age says nothing about whether the next build wants it. The
        ceiling keeps the most recently used layers and discards the rest, so
        every deploy trims to a bounded size without throwing away the base and
        dependency layers a rebuild reuses.
        """
        source = (ROOT / "infra/deploy/ci-deploy.sh").read_text(encoding="utf-8")

        self.assertIn("docker builder prune -f --keep-storage", source)
        self.assertNotIn("--filter until", source, "age-based pruning makes builds cold")


FAKE_DOCKER = r"""#!/bin/bash
# Stands in for docker. The live database and a restored one answer differently, so a
# restore check that compared with the live database after a migration would fail.
echo "$*" >> "$FAKE_STATE/docker.log"
case "$*" in
  cp\ *)
    case "$2" in
      sports-prediction-platform-postgres-1:*) printf 'dump' > "$3" ;;
      *) cp "$2" "$FAKE_STATE/restored-file" ;;
    esac
    ;;
  *"pg_restore"*"--list"*) ;;
  *"pg_restore"*) [ ! -f "$FAKE_STATE/restore-fails" ] || exit 1 ;;
  *"pg_stat_activity"*) echo 0 ;;
  *"SELECT datname"*) ;;
  *"SELECT version FROM schema_migrations"*)
    case "$*" in
      *"-d sokalens_restore_"*) echo 000092 ;;
      *) cat "$FAKE_STATE/live-migrations" ;;
    esac
    ;;
  *"SELECT 'schema_migrations'"*)
    printf 'engine_predictions|1000\nfixtures|1000\nschema_migrations|92\n'
    ;;
esac
exit 0
"""


class RestoreCheckAfterDeployTests(unittest.TestCase):
    def setUp(self) -> None:
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.backups = self.root / "backups"
        self.state = self.root / "state"
        self.state.mkdir()
        bin_directory = self.root / "bin"
        bin_directory.mkdir()
        (bin_directory / "docker").write_text(FAKE_DOCKER, encoding="utf-8", newline="\n")
        (bin_directory / "docker").chmod(0o755)
        (self.state / "live-migrations").write_text("000092\n", encoding="utf-8", newline="\n")
        self.environment = {
            **os.environ,
            "PATH": f"{bin_directory}{os.pathsep}{os.environ['PATH']}",
            "BACKUP_DIR": self.backups.as_posix(),
            "FAKE_STATE": self.state.as_posix(),
            "KEEP_LOCAL_DUMPS": "1",
        }

    def _run(self, *arguments: str) -> subprocess.CompletedProcess[str]:
        return subprocess.run(
            ["bash", (ROOT / "infra/deploy/backup.sh").as_posix(), *arguments],
            capture_output=True,
            text=True,
            env=self.environment,
            cwd=self.root,
        )

    def _restores(self) -> int:
        log = (self.state / "docker.log").read_text(encoding="utf-8")
        return sum(1 for line in log.splitlines() if "--exit-on-error" in line)

    def _dumps(self) -> list[Path]:
        return sorted(self.backups.glob("sokalens-*.dump"))

    def _migrate_live_database(self) -> None:
        (self.state / "live-migrations").write_text("000092\n000093\n", encoding="utf-8", newline="\n")

    def test_a_deploy_dumps_without_restoring_and_leaves_it_pending(self) -> None:
        result = self._run("dump")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self._restores(), 0)
        self.assertEqual(len(self._dumps()), 1)
        self.assertEqual(
            (self.backups / "restore-check-pending").read_text(encoding="utf-8").strip(),
            self._dumps()[0].as_posix(),
        )

    def test_the_check_after_a_migration_compares_with_the_database_as_dumped(self) -> None:
        self.assertEqual(self._run("dump").returncode, 0)
        self._migrate_live_database()
        result = self._run("verify-pending")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self._restores(), 1)
        self.assertFalse((self.backups / "restore-check-pending").exists())
        # What gets restored is the file that would be used.
        self.assertEqual((self.state / "restored-file").read_text(encoding="utf-8"), "dump")

    def test_nothing_waiting_takes_no_time(self) -> None:
        result = self._run("verify-pending")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("no recovery point is waiting", result.stdout)
        self.assertFalse((self.state / "docker.log").exists())

    def test_a_failed_check_is_red_and_keeps_the_previous_recovery_point(self) -> None:
        self.assertEqual(self._run("dump").returncode, 0)
        self.assertEqual(self._run("verify-pending").returncode, 0)
        proven = self._dumps()[0]

        time.sleep(1.1)  # Dump names and retention order come from the clock.
        self.assertEqual(self._run("dump").returncode, 0)
        (self.state / "restore-fails").touch()
        self.assertNotEqual(self._run("verify-pending").returncode, 0)
        self.assertTrue(proven.exists(), "the last proven recovery point was deleted")
        self.assertTrue((self.backups / "restore-check-pending").exists())

    def test_the_dump_after_an_unproven_one_proves_itself_before_the_migration(self) -> None:
        self.assertEqual(self._run("dump").returncode, 0)
        first = self._dumps()[0]
        time.sleep(1.1)
        (self.state / "restore-fails").touch()
        # The check still fails, so the deploy stops here, before migrating.
        self.assertNotEqual(self._run("dump").returncode, 0)
        self.assertEqual(self._restores(), 1)
        self.assertTrue((self.backups / "restore-check-pending").exists())

        (self.state / "restore-fails").unlink()
        time.sleep(1.1)
        result = self._run("dump")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("proven before the migration", result.stdout)
        self.assertEqual(self._restores(), 2)
        self.assertFalse((self.backups / "restore-check-pending").exists())
        self.assertFalse(first.exists())
        self.assertEqual(len(self._dumps()), 1)

    def test_a_tampered_dump_fails_before_it_is_restored(self) -> None:
        self.assertEqual(self._run("dump").returncode, 0)
        self._dumps()[0].write_text("changed", encoding="utf-8")
        self.assertNotEqual(self._run("verify-pending").returncode, 0)
        self.assertEqual(self._restores(), 0)
        self.assertTrue((self.backups / "restore-check-pending").exists())

    def test_the_plain_path_still_proves_its_dump(self) -> None:
        self.assertEqual(self._run("dump").returncode, 0)
        time.sleep(1.1)
        result = self._run()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self._restores(), 1)
        self.assertFalse((self.backups / "restore-check-pending").exists())
        self.assertEqual(len(self._dumps()), 1)

    def test_an_unknown_mode_does_nothing(self) -> None:
        self.assertEqual(self._run("restore-everything").returncode, 2)
        self.assertFalse((self.state / "docker.log").exists())

    def test_the_deploy_dumps_and_a_later_job_proves_it(self) -> None:
        deploy = (ROOT / "infra/deploy/ci-deploy.sh").read_text(encoding="utf-8")
        self.assertIn('bash "$NEW_DIR/infra/deploy/backup.sh" dump', deploy)
        self.assertLess(deploy.index('backup.sh" dump'), deploy.index("-mode up -migrations"))

        workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        job = workflow[workflow.index("\n  verify-recovery-point:\n"):]
        self.assertIn("needs: [deploy]", job)
        # A deploy that failed after dumping needs the answer most.
        self.assertIn("always() &&", job)
        self.assertIn("needs.deploy.result != 'skipped'", job)
        self.assertIn("github.ref == 'refs/heads/main'", job)
        self.assertIn("verify-pending", job)
        self.assertNotIn("continue-on-error", job)

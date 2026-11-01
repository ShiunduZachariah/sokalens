from pathlib import Path
import re
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
        self.assertIn('if [ "$BACKUP_PENDING_MIGRATIONS" -gt 0 ]', source)
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

from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[2]
OPS = ROOT / "services" / "ops-controller" / "internal"


class OpsQueuePolicyContractTests(unittest.TestCase):
    def test_observe_policy_has_no_executor_reachability(self) -> None:
        policy = (OPS / "policy" / "observe.go").read_text()
        self.assertIn("type ObserveOnlyReachability struct {\n\taudit ImmutableAuditSink", policy)
        self.assertIn("_ operations.Executor", policy)
        self.assertNotIn(".Execute(", policy)
        self.assertNotIn("exec.Command", policy)
        self.assertNotIn("os/exec", policy)

    def test_durable_jobs_do_not_authorize_operations(self) -> None:
        source = "\n".join(
            path.read_text()
            for path in sorted((OPS / "jobs").glob("*.go"))
            if not path.name.endswith("_test.go")
        )
        self.assertIn("FOR UPDATE SKIP LOCKED", source)
        self.assertIn('client.Do(ctx, "PUBLISH"', source)
        self.assertNotIn("ops_actions", source)
        self.assertNotIn("ops_deployments", source)
        self.assertNotIn("operations.Executor", source)
        self.assertNotIn("os/exec", source)

    def test_scheduler_is_bounded_and_operation_agnostic(self) -> None:
        scheduler = (OPS / "scheduler" / "scheduler.go").read_text()
        for contract in (
            "Concurrency",
            "P0Reserved",
            "P1Reserved",
            "AdmissionCapacity",
            "activeService",
            "AgingInterval",
        ):
            self.assertIn(contract, scheduler)
        self.assertNotIn("/internal/operations\"", scheduler)
        self.assertNotIn("os/exec", scheduler)

    def test_canary_policy_is_side_effect_free_and_fail_closed(self) -> None:
        source = "\n".join(
            path.read_text()
            for path in sorted((OPS / "canary").glob("*.go"))
            if not path.name.endswith("_test.go")
        )
        for contract in ("5, 25, 100", "AutomaticRollback", "UnauthorizedAdminAccess", "UnverifiedEntitlementGrant"):
            self.assertIn(contract, source)
        self.assertNotIn("operations.Executor", source)
        self.assertNotIn("os/exec", source)
        self.assertNotIn("net/http", source)

    def test_reconciliation_has_no_provider_network_or_broad_mutation_path(self) -> None:
        source = "\n".join(
            path.read_text()
            for path in sorted((OPS / "reconciliation").glob("*.go"))
            if not path.name.endswith("_test.go")
        )
        self.assertIn("ProviderResults", source)
        self.assertIn("[]ProviderResult", source)
        self.assertIn("provider=$2 AND environment=$3 AND provider_subscription_id=$11", source)
        self.assertIn("source_type='subscription' AND source_id=$2::text", source)
        self.assertIn("sql.LevelSerializable", source)
        self.assertNotIn("net/http", source)
        self.assertNotIn("os/exec", source)


if __name__ == "__main__":
    unittest.main()

"""The production deploy compares with what production last ran.

These tests execute the real step scripts from ci.yml in a throwaway git
repository, so they fail if the workflow's behaviour changes, not just its text.
paths-filter cannot run here, so its filters are read from ci.yml and applied to
the same two-dot diff the action makes when given a commit SHA as its base.
"""

from pathlib import Path
import os
import re
import stat
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
DEPLOY_SCRIPT = (ROOT / "infra/deploy/ci-deploy.sh").read_text(encoding="utf-8")

FULL_SET = "api worker web admin ops-controller prometheus node-exporter cadvisor grafana loki promtail"
GATE_RESULTS = (
    "DETECT", "API", "SECURITY", "SEO", "SECURITY_AUDIT", "SECURITY_AUDIT_GO",
    "SECRET_SCAN", "WORKER", "OPS", "WEB", "MOBILE", "CONTRACTS", "INFRA_DOCS",
)
DEPLOY_SURFACES = ("api", "worker", "web", "admin", "infra", "proxy", "contracts", "security", "ops")
MAIN_PUSH_ONLY = "if: github.event_name == 'push' && github.ref == 'refs/heads/main'"


def step_script(step_id: str) -> str:
    """Return the dedented `run: |` body of the step with this id."""
    lines = WORKFLOW.splitlines()
    start = lines.index(next(line for line in lines if line.strip() == f"id: {step_id}"))
    run_line = next(i for i in range(start, len(lines)) if lines[i].strip() == "run: |")
    run_indent = len(lines[run_line]) - len(lines[run_line].lstrip())
    body = []
    for line in lines[run_line + 1:]:
        if line.strip() and len(line) - len(line.lstrip()) <= run_indent:
            break
        body.append(line[run_indent + 2:])
    return "\n".join(body) + "\n"


def detect_filters() -> dict[str, list[str]]:
    lines = WORKFLOW.splitlines()
    start = next(i for i, line in enumerate(lines) if line.strip() == "filters: |")
    indent = len(lines[start]) - len(lines[start].lstrip())
    filters: dict[str, list[str]] = {}
    current = ""
    for line in lines[start + 1:]:
        if line.strip() and len(line) - len(line.lstrip()) <= indent:
            break
        stripped = line.strip()
        if stripped.startswith("- "):
            filters[current].append(stripped[2:].strip("'"))
        elif stripped.endswith(":") and not stripped.startswith("#"):
            current = stripped[:-1]
            filters[current] = []
    return filters


def glob_matches(pattern: str, path: str) -> bool:
    expression = re.escape(pattern).replace(r"\*\*", ".*").replace(r"\*", "[^/]*")
    return re.fullmatch(expression, path) is not None


def surfaces_for(paths: list[str]) -> dict[str, bool]:
    """Apply ci.yml's filters the way predicate-quantifier some-with-excludes does."""
    result = {}
    for name, patterns in detect_filters().items():
        wanted = [p for p in patterns if not p.startswith("!")]
        excluded = [p[1:] for p in patterns if p.startswith("!")]
        result[name] = any(
            any(glob_matches(p, path) for p in wanted)
            and not any(glob_matches(p, path) for p in excluded)
            for path in paths
        )
    return result


class Repository:
    """A shallow checkout of a scratch origin, shaped like the runner's."""

    def __init__(self, directory: Path) -> None:
        self.directory = directory
        self.origin = directory / "origin"
        self.checkout = directory / "checkout"
        self.stub = directory / "stub"
        self.stub.mkdir()
        self.env = {
            **os.environ,
            "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_AUTHOR_NAME": "test",
            "GIT_AUTHOR_EMAIL": "test@example.com",
            "GIT_COMMITTER_NAME": "test",
            "GIT_COMMITTER_EMAIL": "test@example.com",
            "PATH": f"{self.stub}{os.pathsep}{os.environ['PATH']}",
        }
        self.origin.mkdir()
        self.git(self.origin, "init", "-q", "-b", "main")
        # GitHub serves any reachable commit; a local origin refuses by default.
        self.git(self.origin, "config", "uploadpack.allowAnySHA1InWant", "true")

    def git(self, cwd: Path, *args: str) -> str:
        return subprocess.run(
            ["git", *args], cwd=cwd, env=self.env, check=True, text=True,
            capture_output=True,
        ).stdout.strip()

    def commit(self, *paths: str) -> str:
        for path in paths:
            target = self.origin / path
            target.parent.mkdir(parents=True, exist_ok=True)
            with target.open("a", encoding="utf-8") as handle:
                handle.write("change\n")
        self.git(self.origin, "add", "-A")
        self.git(self.origin, "commit", "-q", "-m", "change")
        return self.git(self.origin, "rev-parse", "HEAD")

    def clone(self) -> None:
        self.git(self.directory, "clone", "-q", "--depth=1", self.origin.as_uri(), "checkout")

    def run_step(self, step_id: str, **env: str) -> tuple[subprocess.CompletedProcess, dict[str, str]]:
        output = self.directory / f"{step_id}.output"
        output.write_text("", encoding="utf-8")
        temp = self.directory / "runner-temp"
        temp.mkdir(exist_ok=True)
        result = subprocess.run(
            ["bash", "-e", "-c", step_script(step_id)],
            cwd=self.checkout, text=True, capture_output=True,
            env={**self.env, "GITHUB_OUTPUT": str(output), "RUNNER_TEMP": str(temp), **env},
        )
        pairs = (
            line.split("=", 1)
            for line in output.read_text(encoding="utf-8").splitlines()
            if "=" in line
        )
        return result, dict(pairs)

    def stub_ssh(self, stdout: str = "", status: int = 0) -> None:
        stub = self.stub / "ssh"
        stub.write_text(f"#!/usr/bin/env bash\nprintf '%s\\n' '{stdout}'\nexit {status}\n", encoding="utf-8")
        stub.chmod(stub.stat().st_mode | stat.S_IXUSR)

    def resolve_baseline(self, marker: str = "", ssh_status: int = 0, outcome: str = "success") -> dict[str, str]:
        self.stub_ssh(marker, ssh_status)
        result, outputs = self.run_step("baseline", SSH_OUTCOME=outcome)
        if result.returncode != 0:
            raise AssertionError(result.stdout + result.stderr)
        return outputs

    def surfaces(self, baseline: dict[str, str]) -> dict[str, bool]:
        """What detect's outputs would be for this baseline."""
        if baseline.get("full") == "true":
            return dict.fromkeys(detect_filters(), True)
        # paths-filter fetches a SHA base itself, and only then makes this diff.
        self.git(self.checkout, "fetch", "-q", "--depth=1", "--no-tags", "origin", baseline["base"])
        diff = self.git(self.checkout, "diff", "--no-renames", "--name-only", baseline["base"], "HEAD")
        return surfaces_for(diff.splitlines())

    def run_gates(self, baseline: dict[str, str], **results: str):
        surfaces = self.surfaces(baseline)
        env = {name: "success" for name in GATE_RESULTS}
        env.update(results)
        env.update({f"SURFACE_{name.upper()}": str(surfaces[name]).lower() for name in DEPLOY_SURFACES})
        env["DEPLOY_BASELINE"] = baseline.get("base", "")
        return self.run_step("gates", **env)

    def services(self, baseline: dict[str, str]) -> str:
        result, outputs = self.run_gates(baseline)
        if result.returncode != 0:
            raise AssertionError(result.stdout + result.stderr)
        return outputs.get("services", "")


class DeployBaselineBehaviourTests(unittest.TestCase):
    def setUp(self) -> None:
        scratch = tempfile.TemporaryDirectory(ignore_cleanup_errors=True)
        self.addCleanup(scratch.cleanup)
        self.repo = Repository(Path(scratch.name))
        self.deployed = self.repo.commit("README.md", "services/api/main.go", "apps/web/page.tsx")

    def test_narrow_commit_after_a_failed_deploy_rebuilds_the_full_set(self) -> None:
        """The 2026-11-04 failure: a migration and worker change, killed mid-deploy."""
        for narrow_path in (".github/workflows/ci.yml", "infra/deploy/ci-deploy.sh"):
            with self.subTest(narrow_path=narrow_path):
                failed = self.repo.commit("infra/db/migrations/000091_retire.up.sql", "services/worker/main.go")
                self.repo.commit(narrow_path)
                if self.repo.checkout.exists():
                    self.repo.git(self.repo.checkout, "fetch", "-q", "--depth=1", "origin", "main")
                    self.repo.git(self.repo.checkout, "reset", "-q", "--hard", "FETCH_HEAD")
                else:
                    self.repo.clone()

                self.assertEqual(self.repo.services({"base": failed}), "proxy-only",
                                 "comparing with the previous push should reproduce the old behaviour")

                baseline = self.repo.resolve_baseline(self.deployed)
                self.assertEqual(baseline, {"base": self.deployed, "full": "false"})
                self.assertEqual(self.repo.services(baseline), FULL_SET)

    def test_the_gates_of_the_failed_commit_run_again(self) -> None:
        """Widening the deploy alone would ship services whose tests were skipped."""
        self.repo.commit("services/worker/main.go", "apps/web/page.tsx")
        self.repo.commit(".github/workflows/ci.yml")
        self.repo.clone()
        surfaces = self.repo.surfaces(self.repo.resolve_baseline(self.deployed))
        self.assertTrue(surfaces["worker"], "the worker gate would be skipped")
        self.assertTrue(surfaces["web"], "the web gate would be skipped")

    def test_failed_commit_narrower_than_infra_rebuilds_exactly_its_services(self) -> None:
        self.repo.commit("services/worker/main.go")
        self.repo.commit("infra/deploy/ci-deploy.sh")
        self.repo.clone()
        self.assertEqual(self.repo.services(self.repo.resolve_baseline(self.deployed)).split(), ["worker"])

    def test_a_good_deploy_keeps_the_next_narrow_commit_narrow(self) -> None:
        latest_good = self.repo.commit("services/worker/main.go")
        self.repo.commit(".github/workflows/ci.yml")
        self.repo.clone()
        self.assertEqual(self.repo.services(self.repo.resolve_baseline(latest_good)), "proxy-only")

    def test_documentation_after_a_good_deploy_deploys_nothing(self) -> None:
        latest_good = self.repo.commit("services/worker/main.go")
        self.repo.commit("docs/notes.md")
        self.repo.clone()
        self.assertEqual(self.repo.services(self.repo.resolve_baseline(latest_good)), "")

    def test_toolchain_shortcut_sees_everything_since_the_baseline(self) -> None:
        """A toolchain-only push must not hide a web change that never deployed."""
        self.repo.commit("apps/web/page.tsx")
        self.repo.commit("go.work")
        self.repo.clone()
        self.assertIn("web", self.repo.services(self.repo.resolve_baseline(self.deployed)).split())

    def test_toolchain_only_release_stays_narrow(self) -> None:
        self.repo.commit("go.work", "services/api/go.mod")
        self.repo.clone()
        self.assertEqual(self.repo.services(self.repo.resolve_baseline(self.deployed)), "api worker ops-controller")

    def test_unknown_baseline_runs_every_gate_and_rebuilds_every_service(self) -> None:
        self.repo.commit(".github/workflows/ci.yml")
        self.repo.clone()
        cases = {
            "first deploy, nothing recorded": {"marker": ""},
            "marker is not a SHA": {"marker": "unknown-release"},
            "marker tries to inject an output": {"marker": "x\nbase=" + self.deployed},
            "commit is not in the repository": {"marker": "0" * 40},
            "host unreachable": {"marker": self.deployed, "ssh_status": 255},
            "ssh not configured": {"marker": self.deployed, "outcome": "failure"},
        }
        for name, kwargs in cases.items():
            with self.subTest(name):
                baseline = self.repo.resolve_baseline(**kwargs)
                self.assertEqual(baseline, {"full": "true"})
                self.assertTrue(all(self.repo.surfaces(baseline).values()))
                self.assertEqual(self.repo.services(baseline), FULL_SET)

    def test_deploy_key_is_gone_before_the_third_party_action_runs(self) -> None:
        self.repo.commit(".github/workflows/ci.yml")
        self.repo.clone()
        key_directory = self.repo.directory / "runner-temp" / "vps-ssh"
        key_directory.mkdir(parents=True)
        (key_directory / "id_deploy").write_text("key", encoding="utf-8")
        self.repo.resolve_baseline(self.deployed)
        self.assertFalse(key_directory.exists())

    def test_a_failed_gate_still_blocks_a_wider_deploy(self) -> None:
        self.repo.commit("services/worker/main.go")
        self.repo.commit(".github/workflows/ci.yml")
        self.repo.clone()
        baseline = self.repo.resolve_baseline(self.deployed)
        for gate in ("WORKER", "SECRET_SCAN", "DETECT"):
            with self.subTest(gate):
                result, outputs = self.repo.run_gates(baseline, **{gate: "failure"})
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("services", outputs)


class DeployBaselineWiringTests(unittest.TestCase):
    def test_every_detect_surface_is_widened_when_the_baseline_is_unknown(self) -> None:
        for name in detect_filters():
            self.assertIn(
                f"{name}: ${{{{ steps.baseline.outputs.full == 'true' || steps.filter.outputs.{name} }}}}",
                WORKFLOW,
                f"{name} would stay narrow, and its gate could be skipped, with no baseline",
            )

    def test_the_filter_and_the_deploy_share_one_baseline(self) -> None:
        self.assertIn("base: ${{ steps.baseline.outputs.base }}", WORKFLOW)
        self.assertIn("deploy_baseline: ${{ steps.baseline.outputs.base }}", WORKFLOW)
        self.assertIn("DEPLOY_BASELINE: ${{ needs.detect.outputs.deploy_baseline }}", WORKFLOW)
        self.assertLess(WORKFLOW.index("id: baseline\n"), WORKFLOW.index("dorny/paths-filter"))
        self.assertNotIn("HEAD^", WORKFLOW, "a diff against the previous commit has come back")

    def test_only_main_pushes_touch_the_production_host_from_detect(self) -> None:
        detect = WORKFLOW[WORKFLOW.index("  detect:\n"):WORKFLOW.index("dorny/paths-filter")]
        self.assertEqual(detect.count(MAIN_PUSH_ONLY), 2)
        for step in ("id: baseline-ssh", "id: baseline\n"):
            self.assertIn(MAIN_PUSH_ONLY, detect[detect.index(step):detect.index(step) + 120])

    def test_the_connection_stays_in_secrets(self) -> None:
        script = step_script("baseline")
        self.assertRegex(script, r"\bvps \"cat ")
        self.assertIsNone(re.search(r"\b\d{1,3}(\.\d{1,3}){3}\b", script))
        self.assertNotIn("@", script)

    def test_workflow_reads_the_file_the_deploy_writes(self) -> None:
        root = re.search(r'^LKG_ROOT="([^"]+)"$', DEPLOY_SCRIPT, re.M).group(1)
        marker = re.search(r'^LAST_DEPLOYED_MARKER="\$LKG_ROOT/([^"]+)"$', DEPLOY_SCRIPT, re.M).group(1)
        self.assertIn(f"MARKER={root}/{marker}\n", step_script("baseline"))
        self.assertNotEqual(root, re.search(r'^DEPLOY_DIR="([^"]+)"$', DEPLOY_SCRIPT, re.M).group(1))

    def test_only_a_completed_deploy_advances_the_marker(self) -> None:
        call = "record_last_deployed ||"
        code = [line for line in DEPLOY_SCRIPT.splitlines() if not line.lstrip().startswith("#")]
        uses = [line for line in code if "record_last_deployed" in line]
        self.assertEqual(len(uses), 2, f"expected one definition and one call, found {uses}")
        self.assertLess(DEPLOY_SCRIPT.index('record_release_state "$backend_sha" "$frontend_sha"\n'),
                        DEPLOY_SCRIPT.index("DEPLOY_SUCCEEDED=1\n"))
        self.assertLess(DEPLOY_SCRIPT.index("DEPLOY_SUCCEEDED=1\n"), DEPLOY_SCRIPT.index(call))


if __name__ == "__main__":
    unittest.main()

from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[2]
WORKFLOWS = ROOT / ".github" / "workflows"


class WorkflowRunnerContractTests(unittest.TestCase):
    def test_no_workflow_is_pinned_to_a_github_hosted_runner(self) -> None:
        """A hardcoded `ubuntu-latest` cannot run once the minutes are gone.

        The account's 2,000 included minutes are exhausted, so GitHub-hosted
        jobs do not fail -- they never start, with "recent account payments
        have failed or your spending limit needs to be increased". CI moved to
        self-hosted runners, but three workflows kept the literal and so went
        permanently red: auto-merge on every push to main, and, less visibly,
        the IndexNow refresh, which meant search engines stopped being told
        about new fixtures at all.

        Going through the variable keeps `ubuntu-latest` as the fallback, so a
        fork or a restored balance still works without editing anything.
        """
        offenders = []
        for workflow in sorted(WORKFLOWS.glob("*.yml")):
            for number, line in enumerate(workflow.read_text(encoding="utf-8").splitlines(), 1):
                if re.match(r"\s*runs-on:\s*ubuntu-latest\s*$", line):
                    offenders.append(f"{workflow.name}:{number}")

        self.assertEqual(
            offenders,
            [],
            "these jobs cannot start while the hosted minutes are exhausted; "
            "use runs-on: ${{ vars.CI_RUNNER || 'ubuntu-latest' }}: " + ", ".join(offenders),
        )

    def test_no_workflow_carries_vps_connection_details(self) -> None:
        """The VPS address, SSH port and user come from secrets, never the file.

        Workflow files are readable by anyone who can read the repository and
        their commands are echoed into run logs. The vps-ssh action reads the
        connection details from secrets, which GitHub masks, and pins the host
        key from VPS_KNOWN_HOSTS instead of trusting whatever answers first.
        """
        forbidden = [
            # Loopback is the CI job's own PostgreSQL service container.
            (re.compile(r"\b(?!127\.)\d{1,3}(?:\.\d{1,3}){3}\b"), "an IPv4 address"),
            (re.compile(r"\bssh(?:-keyscan)?\b[^\n]*\s-p\s*\d+"), "an inline SSH port"),
            (re.compile(r"\bscp\b[^\n]*\s-P\s*\d+"), "an inline SCP port"),
            (re.compile(r"StrictHostKeyChecking=(?:no|accept-new)"), "trust-on-first-use host keys"),
            (re.compile(r"\bssh-keyscan\b"), "host-key discovery at run time"),
        ]
        offenders = []
        for workflow in sorted(WORKFLOWS.glob("*.yml")):
            for number, line in enumerate(workflow.read_text(encoding="utf-8").splitlines(), 1):
                for pattern, what in forbidden:
                    if pattern.search(line):
                        offenders.append(f"{workflow.name}:{number} ({what})")

        self.assertEqual(
            offenders,
            [],
            "use the ./.github/actions/vps-ssh action and `ssh vps`: " + ", ".join(offenders),
        )


if __name__ == "__main__":
    unittest.main()

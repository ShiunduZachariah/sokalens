#!/usr/bin/env bash
# A one-shot, immutable PR rollout. Timer dispatch is exact; CI activation is
# sequential and waits for tests, normal merge rules and restore verification.
set -euo pipefail
umask 077
repo="${1:?repository required}"
feature_pr="${2:?feature PR number required}"
feature_sha="${3:?reviewed feature SHA required}"
expected_qa="${4:?expected QA base required}"
expected_main="${5:?expected production base required}"
release_at="${6:?UTC start required}"
[[ "$feature_pr" =~ ^[0-9]+$ ]] || exit 2
for sha in "$feature_sha" "$expected_qa" "$expected_main"; do [[ "$sha" =~ ^[0-9a-f]{40}$ ]] || exit 2; done
python3 - "$release_at" <<'TIME'
import datetime,sys
planned=datetime.datetime.fromisoformat(sys.argv[1].replace('Z','+00:00'))
now=datetime.datetime.now(datetime.timezone.utc)
if not 0 <= (now-planned).total_seconds() <= 600:
 raise SystemExit('outside the authorized start window; no activation')
TIME
checkout="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$checkout"
[[ "$(git rev-parse HEAD)" == "$feature_sha" ]]
[[ -z "$(git status --porcelain)" ]]
state_dir="${BACKEND_WINDOW_STATE_DIR:-$HOME/.local/state/sokalens/backend-window/${release_at//:/-}}"
mkdir -p "$state_dir"
production_sha=""
succeeded=0
finish() {
 status=$?
 trap - EXIT
 if (( status != 0 && succeeded == 0 )) && [[ -n "$production_sha" ]]; then
  bash "$checkout/infra/deploy/rollback-backend-release.sh" "$production_sha" || echo "ERROR: automatic rollback needs operator recovery" >&2
 fi
 printf '%s status=%s qa=%s production=%s\n' "$(date -u +%FT%TZ)" "$status" "${qa_sha:-not-started}" "${production_sha:-not-started}" > "$state_dir/result"
 exit "$status"
}
trap finish EXIT
assert_checks() {
 gh pr view "$1" -R "$repo" --json headRefOid,statusCheckRollup,reviewDecision | python3 -c '
import json,sys
p=json.load(sys.stdin)
if p["headRefOid"]!=sys.argv[1]: raise SystemExit("PR identity changed")
if p.get("reviewDecision")=="REVIEW_REQUIRED": raise SystemExit("required owner review is missing")
checks=p.get("statusCheckRollup",[])
if not any(x.get("name")=="CI gates" and x.get("conclusion")=="SUCCESS" for x in checks): raise SystemExit("CI gates are not green")
for x in checks:
 if x.get("status")!="COMPLETED" or x.get("conclusion") not in ("SUCCESS","SKIPPED","NEUTRAL"): raise SystemExit("an additional check is pending or failed")
' "$2"
}
wait_run() {
 local workflow="$1" branch="$2" sha="$3" event="${4:-push}" after_id="${5:-0}" run_id=""
 for _ in $(seq 1 60); do
  run_id="$(gh run list -R "$repo" --workflow "$workflow" --branch "$branch" --event "$event" --limit 30 --json databaseId,headSha,createdAt | python3 -c 'import json,sys; r=[x for x in json.load(sys.stdin) if x["headSha"]==sys.argv[1] and x["databaseId"]>int(sys.argv[2])]; print(r[0]["databaseId"] if r else "")' "$sha" "$after_id")"
  [[ -z "$run_id" ]] || break
  sleep 5
 done
 [[ -n "$run_id" ]] || { echo "expected workflow never started: $workflow" >&2; return 1; }
 printf '%s run=%s sha=%s\n' "$workflow" "$run_id" "$sha"
 gh run watch "$run_id" -R "$repo" --exit-status --interval 15
}
[[ "$(gh api "repos/$repo/git/ref/heads/qa" --jq .object.sha)" == "$expected_qa" ]] || { echo "QA base changed; re-review required" >&2; exit 1; }
[[ "$(gh api "repos/$repo/git/ref/heads/main" --jq .object.sha)" == "$expected_main" ]] || { echo "main base changed; re-review required" >&2; exit 1; }
assert_checks "$feature_pr" "$feature_sha"
[[ "${OPERATOR_MERGE_AUTHORIZED:-false}" == true ]] || { echo 'owner merge authorization is required by AGENTS.md; rollout held' >&2; exit 1; }
# Normal merges only: no admin bypass, direct protected-branch pushes or
# weakened gate. GitHub refuses this if its required approvals are missing.
gh pr merge "$feature_pr" -R "$repo" --squash --match-head-commit "$feature_sha"
qa_sha="$(gh pr view "$feature_pr" -R "$repo" --json mergeCommit --jq .mergeCommit.oid)"
[[ "$qa_sha" =~ ^[0-9a-f]{40}$ ]]
wait_run qa-deploy.yml qa "$qa_sha"
wait_run ci.yml qa "$qa_sha"
[[ "$(gh api "repos/$repo/git/ref/heads/qa" --jq .object.sha)" == "$qa_sha" ]]
[[ "$(gh api "repos/$repo/git/ref/heads/main" --jq .object.sha)" == "$expected_main" ]]
cat > "$state_dir/promotion.md" <<BODY
Promote the bounded backend maintenance release validated in QA at commit $qa_sha.

QA deployment and branch CI passed. This release indexes queue monitoring,
bounds worker polling and operational history, reconciles PostgreSQL cache/query
statistics settings, and adds guarded expiry of unresolved raw prediction details.
Both deployment paths include pre/post checks and rollback. New destructive
expiry is enabled only by the separate restore-verified maintenance workflow.
BODY
promotion_url="$(gh pr create -R "$repo" --base main --head qa --title 'perf: promote validated bounded backend maintenance' --body-file "$state_dir/promotion.md")"
promotion_pr="${promotion_url##*/}"
for _ in $(seq 1 600); do
 if gh pr view "$promotion_pr" -R "$repo" --json statusCheckRollup --jq '.statusCheckRollup[].name' | grep -qx 'CI gates'; then break; fi
 sleep 5
done
gh pr checks "$promotion_pr" -R "$repo" --watch --interval 15
assert_checks "$promotion_pr" "$qa_sha"
gh pr merge "$promotion_pr" -R "$repo" --merge --match-head-commit "$qa_sha"
production_sha="$(gh pr view "$promotion_pr" -R "$repo" --json mergeCommit --jq .mergeCommit.oid)"
[[ "$production_sha" =~ ^[0-9a-f]{40}$ ]]
wait_run ci.yml main "$production_sha"
# The workflow now exists on default main. Each dispatch validates the exact
# deployed source identity and obtains its own fresh restore-verified backup.
for target in qa production; do
 sha="$qa_sha"
 [[ "$target" != production ]] || sha="$production_sha"
 latest_run="$(gh run list -R "$repo" --workflow backend-maintenance.yml --branch main --event workflow_dispatch --limit 1 --json databaseId --jq '.[0].databaseId // 0')"
 gh workflow run backend-maintenance.yml -R "$repo" --ref main -f "target=$target" -f "expected_sha=$sha"
 wait_run backend-maintenance.yml main "$production_sha" workflow_dispatch "$latest_run"
done
succeeded=1
echo "QA and production rollout and bounded maintenance passed"

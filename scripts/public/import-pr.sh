#!/usr/bin/env bash
# Bring a pull request from the public snapshot repo into this one.
#
#   bash scripts/public/import-pr.sh <public-pr-number>
#
# The public repo (ShiunduZachariah/sokalens) is a one-commit-per-refresh
# snapshot of main; it shares files with this repo but not history. So a
# contribution arrives as patches: this fetches the PR, turns its commits into
# patches against the public commit it was based on, and applies them with
# `git am` to a new branch cut from origin/qa. Authorship and messages are kept.
#
# It never pushes. Read the diff before you do: once the branch is here, CI runs
# it on the self-hosted runner, which is the production host. The fork routing
# in ci.yml only covers forks of THIS repo, not imported patches.
set -euo pipefail

pr="${1:?usage: import-pr.sh <public-pr-number>}"
case "$pr" in ''|*[!0-9]*) echo "PR number must be numeric" >&2; exit 2 ;; esac
public="${PUBLIC_REMOTE:-https://github.com/ShiunduZachariah/sokalens.git}"
base_branch="${IMPORT_BASE:-origin/qa}"
branch="contrib/public-$pr"

if [ -n "$(git status --porcelain)" ]; then
  echo "working tree is not clean; commit or set your changes aside first" >&2
  exit 1
fi
if git rev-parse --verify --quiet "refs/heads/$branch" >/dev/null; then
  echo "branch $branch already exists" >&2
  exit 1
fi

git fetch --quiet origin
git fetch --quiet --no-tags "$public" "+refs/heads/main:refs/public/main" "+refs/pull/$pr/head:refs/public/pr-$pr"

head="refs/public/pr-$pr"
base="$(git merge-base refs/public/main "$head")" || {
  echo "PR $pr shares no commit with public main; ask the contributor to rebase" >&2
  exit 1
}
count="$(git rev-list --count "$base..$head")"
if [ "$count" -eq 0 ]; then
  echo "PR $pr has no commits beyond public main" >&2
  exit 1
fi

echo "PR $pr: $count commit(s) on top of public ${base:0:8}"
git --no-pager log --format='  %h %an: %s' "$base..$head"
echo
git --no-pager diff --stat "$base" "$head"

# Paths whose contents run on CI or at build time. Not a reason to refuse,
# a reason to read every line.
sensitive="$(git diff --name-only "$base" "$head" | grep -E '^(\.github/|\.githooks/|scripts/|infra/|Taskfile\.yml$)|(^|/)(Dockerfile[^/]*|package\.json|package-lock\.json|go\.mod|go\.sum|requirements[^/]*\.txt|pyproject\.toml|Makefile)$' || true)"
if [ -n "$sensitive" ]; then
  echo
  echo "REVIEW CAREFULLY -- these run in CI or at build time:"
  sed 's/^/  /' <<<"$sensitive"
fi

patches="$(mktemp)"
trap 'rm -f "$patches"' EXIT
git format-patch --stdout "$base..$head" > "$patches"

git switch --quiet -c "$branch" "$base_branch"
if ! git am --3way --quiet "$patches"; then
  echo
  echo "patches did not apply cleanly onto $base_branch."
  echo "resolve, then 'git am --continue' (or 'git am --abort' and delete $branch)."
  exit 1
fi

echo
echo "applied onto $branch from $base_branch. Next:"
echo "  git diff $base_branch...$branch      # read all of it"
echo "  git push -u origin $branch && gh pr create --base qa"
echo "Credit the contributor in the PR, and close public PR $pr once it ships."

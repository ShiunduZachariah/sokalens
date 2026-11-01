#!/usr/bin/env bash
# Publish the current origin/main to the public snapshot repo as one commit.
#
#   bash scripts/public/refresh.sh [--credit "#12 by @name"]... [--push]
#
# The public repo never receives this repo's history: that history holds old
# server details, personal addresses and third-party data. Each refresh is a
# single new commit whose tree is origin/main's and whose parent is the public
# main it replaces, so contributors' clones fast-forward and nothing is
# force-pushed.
#
# Without --push it builds the commit and prints what would be published.
set -euo pipefail

public="${PUBLIC_REMOTE:-https://github.com/ShiunduZachariah/sokalens.git}"
source_ref="${REFRESH_SOURCE:-origin/main}"
# The public author is always the GitHub no-reply address, never a personal one.
author_name="${PUBLIC_AUTHOR_NAME:-Shiundu Zachariah}"
author_email="${PUBLIC_AUTHOR_EMAIL:-72136327+ShiunduZachariah@users.noreply.github.com}"

push=0
credits=()
while [ $# -gt 0 ]; do
  case "$1" in
    --push) push=1 ;;
    --credit) credits+=("${2:?--credit needs a value}"); shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

case "$author_email" in
  *@users.noreply.github.com) ;;
  *) echo "PUBLIC_AUTHOR_EMAIL must be a GitHub no-reply address" >&2; exit 1 ;;
esac

git fetch --quiet origin
git fetch --quiet --no-tags "$public" "+refs/heads/main:refs/public/main"

tree="$(git rev-parse "$source_ref^{tree}")"
parent="$(git rev-parse refs/public/main)"
if [ "$tree" = "$(git rev-parse "$parent^{tree}")" ]; then
  echo "public main already matches $source_ref; nothing to publish"
  exit 0
fi

# Last line of defence against publishing what the history rewrite avoided.
leaks=()
while IFS= read -r path; do leaks+=("$path"); done < <(
  git ls-tree -r --name-only "$tree" |
    grep -E '(^|/)\.env($|\.(local|prod|qa|vps)[^/]*$)|(^|/)id_(rsa|ed25519|ecdsa)$|\.pem$|^Model-Training-Data/' |
    grep -vE '\.example$' || true)
if [ -n "${VPS_HOST:-}" ] && git grep -q -F "$VPS_HOST" "$tree" -- .; then
  leaks+=("the VPS host address appears in tracked files")
fi
if [ ${#leaks[@]} -gt 0 ]; then
  echo "refusing to publish:" >&2
  printf '  %s\n' "${leaks[@]}" >&2
  exit 1
fi

source_sha="$(git rev-parse --short "$source_ref")"
message="Update SokaLens to ${source_sha}"
if [ ${#credits[@]} -gt 0 ]; then
  message+=$'\n\nIncludes community contributions:'
  for credit in "${credits[@]}"; do message+=$'\n- '"$credit"; done
fi

commit="$(printf '%s\n' "$message" |
  GIT_AUTHOR_NAME="$author_name" GIT_AUTHOR_EMAIL="$author_email" \
  GIT_COMMITTER_NAME="$author_name" GIT_COMMITTER_EMAIL="$author_email" \
  git commit-tree "$tree" -p "$parent")"

echo "public ${parent:0:8} -> ${commit:0:8} (tree of $source_ref ${source_sha})"
git --no-pager diff --stat "$parent" "$commit" | tail -1
if [ "$push" -eq 1 ]; then
  git push "$public" "$commit:refs/heads/main"
else
  echo "dry run; re-run with --push to publish"
fi

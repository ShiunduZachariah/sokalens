#!/usr/bin/env bash
# A recovered checkout is usable for secret scanning only with the exact commit
# and its complete history. Never turn a failed fetch into a partial scan.
set -euo pipefail
expected="${1:?expected immutable commit required}"
[[ "$expected" =~ ^[0-9a-f]{40}$ ]] || { echo 'invalid expected commit' >&2; exit 1; }
[[ "$(git rev-parse HEAD)" == "$expected" ]] || { echo 'secret scan checkout identity differs from the CI commit' >&2; exit 1; }
[[ "$(git rev-parse --is-shallow-repository)" == false ]] || { echo 'secret scan requires full history; shallow checkout refused' >&2; exit 1; }
# Check credential NAMES only. Never print an authorization header to CI logs.
if git config --local --name-only --get-regexp '^http\..*\.extraheader$' >/dev/null; then
  echo 'secret scan checkout retained an HTTP authorization header' >&2
  exit 1
fi
git fsck --connectivity-only --no-reflogs --no-dangling
printf 'secret scan checkout verified: exact commit and complete history; credentials removed\n'

#!/usr/bin/env bash
# Run `npm audit` for one workspace, retrying ONLY transport failures.
#
# This does not weaken the gate. There are two distinct outcomes that npm
# reports with the same exit code 1:
#
#   a) the audit ran and found vulnerabilities at or above the threshold
#   b) the audit never ran because npm's advisory service did not answer
#
# (a) is a real finding and must fail the build immediately -- retrying it
# would be exactly the "retry until green" antipattern. (b) is missing
# information, and an unavailable audit is NOT a clean audit, so it is retried
# a bounded number of times and then FAILS anyway.
#
# Usage: npm-audit-with-retry.sh <prefix> [audit-level]
set -euo pipefail

prefix="${1:?usage: npm-audit-with-retry.sh <prefix> [audit-level]}"
level="${2:-high}"
attempts="${NPM_AUDIT_ATTEMPTS:-5}"
base_delay="${NPM_AUDIT_BASE_DELAY:-15}"

# Signatures of the advisory endpoint being unavailable rather than reporting a
# finding. Matched case-insensitively against combined stdout+stderr.
transport_pattern='audit endpoint returned an error|Service Unavailable|503|network timeout|ETIMEDOUT|ECONNRESET|ENOTFOUND|EAI_AGAIN|socket hang up|429 Too Many Requests'

for (( attempt = 1; attempt <= attempts; attempt++ )); do
  output=""
  status=0
  output="$(npm audit --prefix "$prefix" --omit=dev --audit-level="$level" 2>&1)" || status=$?

  if (( status == 0 )); then
    printf '%s\n' "$output"
    echo "npm audit [$prefix]: clean on attempt $attempt/$attempts"
    exit 0
  fi

  if ! grep -qiE "$transport_pattern" <<<"$output"; then
    # The audit ran and reported something. Never retry a real finding.
    printf '%s\n' "$output"
    echo "ERROR: npm audit [$prefix] reported findings at level '$level'." >&2
    exit "$status"
  fi

  echo "npm audit [$prefix]: advisory service unavailable (attempt $attempt/$attempts)" >&2
  printf '%s\n' "$output" | tail -5 >&2

  if (( attempt < attempts )); then
    delay=$(( base_delay * attempt ))
    echo "retrying in ${delay}s" >&2
    sleep "$delay"
  fi
done

# Bounded retries exhausted. Fail closed: we still do not know whether these
# dependencies are vulnerable, and that is not the same as knowing they are not.
echo "ERROR: npm audit [$prefix] could not complete after $attempts attempts." >&2
echo "The advisory service was unavailable throughout. This is NOT a pass: the" >&2
echo "audit never ran, so the dependency set is unverified. Re-run once" >&2
echo "https://registry.npmjs.org/-/npm/v1/security/advisories/bulk responds." >&2
exit 1

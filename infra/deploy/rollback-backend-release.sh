#!/usr/bin/env bash
# Emergency rollback after a CI check outside ci-deploy.sh fails.
set -euo pipefail
expected="${1:?exact failed release SHA required}"
[[ "$expected" =~ ^[0-9a-f]{40}$ ]] || exit 2
mkdir -p "$HOME/.cache"
exec 9>"$HOME/.cache/sokalens-production-release.lock"
flock -w 600 9
current="$HOME/sokalens"
if [[ "$(cat "$current/.release-source-sha" 2>/dev/null || true)" != "$expected" ]]; then
 echo "release is already restored or has changed; no rollback applied"
 exit 0
fi
state="$HOME/sokalens-lkg/releases/$expected.env"
[[ -r "$state" ]] || { echo "no captured rollback state for the expected release" >&2; exit 1; }
[[ -d "$HOME/sokalens-old" ]] || { echo "prior source is unavailable" >&2; exit 1; }
# Load function definitions only; never execute the deployment entrypoint.
source <(sed '/^\[ -f "\$TARBALL" \]/,$d' "$current/infra/deploy/ci-deploy.sh")
trap - EXIT
source "$state"
BACKEND_READY=0
ROLLBACK_ACTIVE=0
rollback_release
if [[ "${LKG_PREVIOUS_DEPLOYED_SHA:-}" =~ ^[0-9a-f]{40}$ ]]; then
 printf '%s\n' "$LKG_PREVIOUS_DEPLOYED_SHA" > "$LAST_DEPLOYED_MARKER"
fi
cleanup_secret_files
echo "post-deployment failure restored the prior application and PostgreSQL runtime"

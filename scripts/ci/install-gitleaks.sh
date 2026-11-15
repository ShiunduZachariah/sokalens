#!/usr/bin/env bash
# Install the pinned gitleaks release for the secret-scan job.
#
# The release archive comes from GitHub's release CDN, which has answered 504
# for minutes at a time. The previous inline install retried three times two
# seconds apart, so a single bad minute failed the secret scan -- and, on main,
# blocked the deploy -- three times in one release. Each of those was a
# non-answer, not a finding: the scanner never ran.
#
# Three things fix that without weakening the gate:
#
#   1. The archive is pinned by SHA-256. Every copy is checked against it,
#      whether it was just downloaded or came from the cache, so nothing but
#      the exact release ever runs.
#   2. A verified archive is kept in the runner's tool cache, which persists
#      between jobs on the self-hosted runner. After the first download, the
#      scan no longer depends on GitHub's CDN at all.
#   3. A download that is needed is retried with backoff for about two and a
#      half minutes, not six seconds.
#
# If no verified archive can be had, this FAILS. A scanner that was not
# installed proves nothing about secrets, and that is not the same as a pass.
#
# Usage: GITLEAKS_VERSION=x.y.z GITLEAKS_SHA256=<hex> install-gitleaks.sh <bin-dir>
set -euo pipefail

bin_dir="${1:?usage: install-gitleaks.sh <bin-dir>}"
version="${GITLEAKS_VERSION:?GITLEAKS_VERSION is required}"
sha256="${GITLEAKS_SHA256:?GITLEAKS_SHA256 is required: pin the archive, do not trust whatever downloads}"
attempts="${GITLEAKS_DOWNLOAD_ATTEMPTS:-6}"
base_delay="${GITLEAKS_DOWNLOAD_BASE_DELAY:-10}"
url="${GITLEAKS_URL:-https://github.com/gitleaks/gitleaks/releases/download/v${version}/gitleaks_${version}_linux_x64.tar.gz}"
# RUNNER_TOOL_CACHE survives between jobs on a self-hosted runner and is
# writable without sudo. On a hosted runner it is discarded with the VM, which
# costs one download and nothing else.
cache_root="${GITLEAKS_CACHE_DIR:-${RUNNER_TOOL_CACHE:-}}"

verified() {
  [[ -f "$1" ]] && printf '%s  %s\n' "$sha256" "$1" | sha256sum --check --status
}

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
archive=""

cached=""
if [[ -n "$cache_root" ]]; then
  cached="$cache_root/gitleaks/$version/gitleaks_${version}_linux_x64.tar.gz"
  if verified "$cached"; then
    archive="$cached"
    echo "gitleaks $version: using the verified archive in the runner's tool cache"
  elif [[ -e "$cached" ]]; then
    # A truncated write or a changed pin. Never run it; fetch a fresh copy.
    echo "::warning::cached gitleaks archive failed its checksum and was discarded" >&2
    rm -f "$cached"
  fi
fi

if [[ -z "$archive" ]]; then
  download="$work/gitleaks.tar.gz"
  for (( attempt = 1; attempt <= attempts; attempt++ )); do
    # --fail: without it a 504 page is written to the archive and the failure
    # surfaces later as a confusing gzip error. Retries are this loop's job.
    if curl -sS --fail --location --connect-timeout 20 --max-time 120 \
        -o "$download" "$url"; then
      if verified "$download"; then
        archive="$download"
        echo "gitleaks $version: downloaded and verified on attempt $attempt/$attempts"
        break
      fi
      # A 200 with the wrong bytes: a truncated body or an interfering proxy.
      # It is never installed. Retrying costs nothing, because no retry can
      # make an unverified file pass.
      echo "gitleaks $version: download did not match the pinned SHA-256 (attempt $attempt/$attempts)" >&2
    else
      echo "gitleaks $version: download failed (attempt $attempt/$attempts)" >&2
    fi
    rm -f "$download"
    if (( attempt < attempts )); then
      delay=$(( base_delay * attempt ))
      echo "retrying in ${delay}s" >&2
      sleep "$delay"
    fi
  done
fi

if [[ -z "$archive" ]]; then
  echo "::error::gitleaks $version could not be installed after $attempts attempts. The secret scan did NOT run, so this is not a pass. Re-run the job once $url responds." >&2
  exit 1
fi

if [[ -n "$cached" && "$archive" != "$cached" ]]; then
  # Best effort. A cache that cannot be written costs a download next time,
  # never this run. Copy then rename, so a reader never sees half a file.
  if mkdir -p "$(dirname "$cached")" 2>/dev/null &&
      cp "$archive" "$cached.partial.$$" 2>/dev/null &&
      mv -f "$cached.partial.$$" "$cached" 2>/dev/null; then
    echo "gitleaks $version: archive cached for later runs"
  else
    rm -f "$cached.partial.$$" 2>/dev/null || true
    echo "::warning::could not cache the gitleaks archive; the next run will download it again" >&2
  fi
fi

mkdir -p "$bin_dir"
tar -xzf "$archive" -C "$bin_dir" gitleaks
chmod 0755 "$bin_dir/gitleaks"
"$bin_dir/gitleaks" version

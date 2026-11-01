#!/usr/bin/env bash
# Drop the edge's cached HTML after a release.
#
# WHY THIS EXISTS. A cached document names the exact JS chunk hashes of the
# build that produced it. A deploy replaces those files, so HTML cached before
# the release points at chunks that no longer resolve: the page still renders
# from SSR, but nothing hydrates and every interaction is dead.
#
# Without a purge the only defence is keeping the stale window short, and that
# is what `stale-while-revalidate=60` in the Caddyfile is doing -- it was an
# hour and was cut to a minute for exactly this. That bound is paid for on
# every request by every visitor, because a short window means a low hit rate,
# and a miss is a full round trip to this VPS from wherever the reader is.
#
# Purging on release removes the reason for the bound. It does not raise any
# TTL by itself; it makes raising them safe.
#
# WHAT IT PURGES. It asks for HTML by prefix and settles for everything, see
# below.
#
# Purge-by-prefix is not available on every Cloudflare plan, and it is not
# available on this one -- measured against the live zone on 2026-09-22, the
# API refused the prefix call and the fallback ran. So in practice this purges
# everything on every release.
#
# That is cheaper than it sounds, and the reason is worth keeping: assets under
# /_next/static are content-addressed, so a new build references new filenames.
# The old build's assets are unreferenced the moment the release lands, and the
# new build's would be a cache miss whether we purged or not. What the full
# purge actually discards is a set of files nothing will ask for again.
#
# The prefix attempt is kept first because it is the right call if the plan
# ever changes, and because it costs one request to find out.
#
# Either way this exits 0 and says what happened: a deploy must not fail
# because a cache could not be cleared.
set -euo pipefail

ENV_FILE="${ENV_FILE:-/home/deploy/sokalens/.env.prod}"
[ -r "$ENV_FILE" ] || { echo "WARN: cannot read $ENV_FILE; skipping purge" >&2; exit 0; }

# .env.prod has CRLF endings; a value read naively carries a trailing \r into
# the request and curl rejects the URL. Same helper as cloudflare-cache-rule.sh.
read_env() {
  grep "^$1=" "$ENV_FILE" | head -1 | cut -d= -f2- | tr -d '\r' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//'
}

CF_TOKEN="$(read_env CLOUDFLARE_API_TOKEN)"
CF_ZONE="$(read_env CLOUDFLARE_ZONE_ID)"
if [ -z "$CF_TOKEN" ] || [ -z "$CF_ZONE" ]; then
  echo "WARN: Cloudflare credentials absent; skipping purge" >&2
  exit 0
fi

purge() {
  curl -sS --max-time 30 -X POST \
    -H "Authorization: Bearer $CF_TOKEN" \
    -H "Content-Type: application/json" \
    --data "$1" \
    "https://api.cloudflare.com/client/v4/zones/$CF_ZONE/purge_cache"
}

succeeded() {
  printf '%s' "$1" | grep -q '"success":[[:space:]]*true'
}

# Every HTML surface Caddy marks cacheable, as prefixes. The bare host covers
# the home page; the rest cover the templated families. Locale prefixes are
# listed because /es/teams/x is a different cache entry from /teams/x.
PREFIXES='["https://sokalens.com/","https://sokalens.com/teams/","https://sokalens.com/leagues","https://sokalens.com/predictions/","https://sokalens.com/best-bets","https://sokalens.com/football","https://sokalens.com/acca-generator","https://sokalens.com/multi-match-builder","https://sokalens.com/es/","https://sokalens.com/fr/","https://sokalens.com/pt/","https://sokalens.com/ar/","https://sokalens.com/de/","https://sokalens.com/it/"]'

echo "==> Purging cached HTML at the edge"
response="$(purge "{\"prefixes\":$PREFIXES}" || true)"

if succeeded "$response"; then
  echo "purged HTML by prefix"
  exit 0
fi

echo "prefix purge unavailable on this plan; falling back to a full purge" >&2
response="$(purge '{"purge_everything":true}' || true)"

if succeeded "$response"; then
  echo "purged everything"
  exit 0
fi

# Deliberately exit 0. A release that is otherwise healthy must not be failed
# by a cache that could not be cleared -- the worst case is the stale window
# the Caddyfile already bounds, which is where we were before this script.
echo "WARN: cache purge failed, continuing. Cloudflare said:" >&2
printf '%s\n' "$response" | head -c 400 >&2
echo >&2
exit 0

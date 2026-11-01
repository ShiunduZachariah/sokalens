#!/usr/bin/env bash
# Make Cloudflare cache SokaLens HTML at the edge.
#
# WHY THIS EXISTS. Cloudflare does not cache HTML by default, whatever the
# origin says. Caddy has been sending `Cache-Control: public, max-age=0,
# s-maxage=300, stale-while-revalidate=60` on the anonymous pages for weeks and
# every one of them still came back `cf-cache-status: DYNAMIC`, because only a
# Cache Rule marking a request "eligible for cache" changes that. Measured from
# outside: ~400ms TTFB on a fixture page, every visitor paying a full round trip
# to the VPS. That is the floor LCP cannot go below, and it is the largest
# single win left on the property.
#
# WHAT IT CREATES. Two rules in the http_request_cache_settings phase, in order:
#
#   1. BYPASS for requests carrying `RSC` or `Next-Router-Prefetch`. Next serves
#      a different body for the same URL to client-side navigations and signals
#      it with Vary, which Cloudflare cannot vary on. Without this rule the edge
#      could answer a navigation with a stored document and break routing. It
#      must come first: rules are evaluated in order and the first match wins.
#   2. ELIGIBLE FOR CACHE, honouring the origin's own Cache-Control.
#
# Rule 2 is deliberately NOT a copy of the Caddyfile path list. `respect_origin`
# means the origin decides, and Caddy sends a cacheable header only for
# @cacheableHtml; every other page still answers Next's `private, no-cache,
# no-store`, which the edge honours by storing nothing. The Caddyfile therefore
# stays the single source of truth for what is cacheable, and duplicating that
# list here would only create two lists to drift apart.
#
# What rule 2 does add is an explicit exclusion of the personalised surfaces, so
# they can never be stored even if an origin header is one day set wrongly --
# belt as well as braces on the paths where a mistake would serve one reader
# another reader's page.
#
# Next's `Vary: rsc, ...` is left alone on purpose. Cloudflare ignores Vary on
# anything but Accept-Encoding, so it does not block caching -- measured: pages
# cache to HIT with that header present. Browsers DO honour it, and it is what
# keeps a browser from serving an RSC payload as a document, so narrowing it at
# the origin would trade a non-problem for a real one.
#
# IDEMPOTENT. Re-running updates the existing ruleset rather than adding a
# second one, so this is safe to run from a deploy or by hand.
#
# CREDENTIALS. Read from .env.prod on the host and never printed. The token
# needs a single permission: Zone -> Cache Rules -> Edit, on this zone.
set -euo pipefail

ENV_FILE="${ENV_FILE:-/home/deploy/sokalens/.env.prod}"
[ -r "$ENV_FILE" ] || { echo "ERROR: cannot read $ENV_FILE" >&2; exit 1; }

# Strip carriage returns and surrounding whitespace.
#
# .env.prod has CRLF endings, so a value read naively carries a trailing \r into
# the request URL and curl rejects it with "Malformed input to a URL function",
# which looks like a broken script rather than a stray byte.
read_env() {
  grep "^$1=" "$ENV_FILE" | head -1 | cut -d= -f2- | tr -d '\r' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//'
}

CF_TOKEN="$(read_env CLOUDFLARE_API_TOKEN)"
CF_ZONE="$(read_env CLOUDFLARE_ZONE_ID)"
[ -n "$CF_TOKEN" ] || { echo "ERROR: CLOUDFLARE_API_TOKEN is empty in $ENV_FILE" >&2; exit 1; }
[ -n "$CF_ZONE" ] || { echo "ERROR: CLOUDFLARE_ZONE_ID is empty in $ENV_FILE" >&2; exit 1; }

api() {
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -sS --max-time 30 -X "$method" \
      -H "Authorization: Bearer $CF_TOKEN" \
      -H "Content-Type: application/json" \
      --data "$body" \
      "https://api.cloudflare.com/client/v4$path"
  else
    curl -sS --max-time 30 -X "$method" \
      -H "Authorization: Bearer $CF_TOKEN" \
      "https://api.cloudflare.com/client/v4$path"
  fi
}

# Fail with the API's own message rather than a stack trace, so a bad token or a
# missing permission reads as the sentence Cloudflare wrote.
check() {
  python3 -c '
import json, sys
payload = sys.stdin.read()
try:
    data = json.loads(payload)
except json.JSONDecodeError:
    sys.exit("Cloudflare returned a non-JSON response: " + payload[:200])
if not data.get("success"):
    messages = "; ".join(e.get("message", "?") for e in data.get("errors", []))
    sys.exit("Cloudflare API error: " + (messages or payload[:200]))
print(json.dumps(data.get("result")))
'
}

# Verify against the zone rather than /user/tokens/verify.
#
# Account-scoped tokens -- the ones with a cfat_ prefix -- are not user tokens,
# and that endpoint returns 401 "Invalid API Token" for them even when they are
# valid and active. It reads exactly like a dead credential, which is how one
# was misdiagnosed here. Reading the zone proves the token works AND that it can
# see this particular zone, which is the thing that actually has to be true.
echo "verifying access to the zone..."
ZONE_NAME="$(api GET "/zones/$CF_ZONE" | check | python3 -c 'import json,sys; print((json.load(sys.stdin) or {}).get("name","?"))')"
echo "token OK for zone: $ZONE_NAME"

# Personalised surfaces, excluded explicitly.
#
# `contains` rather than a path list because one clause covers every locale
# prefix: /es/results, /de/account and /results are a single condition instead
# of twenty-one. Regex is not an option -- the `matches` operator requires a
# Business plan, and this zone is not on one.
PERSONALISED_PATHS=(
  "/predictions/today"
  "/predictions/tomorrow"
  "/predictions/weekend"
  "/predictions/markets/"
  "/matches/"
  "/results"
  "/acca-predictions"
  "/account"
  "/login"
  "/register"
  "/favorites"
  "/subscription"
  "/verify-email"
)

CACHE_EXPRESSION='(http.request.method in {"GET" "HEAD"}) and not starts_with(http.request.uri.path, "/api/")'
for personalised in "${PERSONALISED_PATHS[@]}"; do
  CACHE_EXPRESSION="$CACHE_EXPRESSION and not (http.request.uri.path contains \"$personalised\")"
done

RULES_JSON="$(python3 - "$CACHE_EXPRESSION" <<'PYEOF'
import json, sys

cacheable = sys.argv[1]
navigation = '(http.request.headers["rsc"][0] ne "" or http.request.headers["next-router-prefetch"][0] ne "")'

rules = {
    "rules": [
        {
            "description": "Bypass: client-side navigations return a different body for the same URL",
            "expression": navigation,
            "action": "set_cache_settings",
            "action_parameters": {"cache": False},
        },
        {
            "description": "Eligible for cache; the origin Cache-Control decides what is stored",
            "expression": cacheable,
            "action": "set_cache_settings",
            "action_parameters": {
                "cache": True,
                "edge_ttl": {"mode": "respect_origin"},
                "browser_ttl": {"mode": "respect_origin"},
            },
        },
    ],
}

# Two shapes, because Cloudflare treats them differently. A zone's cache-phase
# ruleset already exists as an "entrypoint" whose name is literally "zone", and
# an update that also sends name/kind/phase is rejected with "the name field
# cannot be modified". Creating one on a zone that has never had cache rules
# needs all of them.
print(json.dumps({
    "update": rules,
    "create": {
        "name": "zone",
        "description": "Cache anonymous HTML at the edge; never cache RSC navigations.",
        "kind": "zone",
        "phase": "http_request_cache_settings",
        **rules,
    },
}))
PYEOF
)"

UPDATE_JSON="$(printf '%s' "$RULES_JSON" | python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["update"]))')"
CREATE_JSON="$(printf '%s' "$RULES_JSON" | python3 -c 'import json,sys; print(json.dumps(json.load(sys.stdin)["create"]))')"

echo "looking for an existing cache ruleset..."
EXISTING_ID="$(api GET "/zones/$CF_ZONE/rulesets" | check | python3 -c '
import json, sys
for ruleset in json.load(sys.stdin) or []:
    if ruleset.get("phase") == "http_request_cache_settings" and ruleset.get("kind") == "zone":
        print(ruleset["id"])
        break
')"

if [ -n "$EXISTING_ID" ]; then
  echo "updating ruleset $EXISTING_ID"
  api PUT "/zones/$CF_ZONE/rulesets/$EXISTING_ID" "$UPDATE_JSON" | check >/dev/null
else
  echo "creating a new cache ruleset"
  api POST "/zones/$CF_ZONE/rulesets" "$CREATE_JSON" | check >/dev/null
fi

echo "cache rule applied"
echo
echo "verify with:"
echo "  curl -sI https://sokalens.com/best-bets | grep -i cf-cache-status"
echo "the first request reports MISS and the second HIT; DYNAMIC means the rule did not take."

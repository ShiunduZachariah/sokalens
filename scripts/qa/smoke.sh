#!/usr/bin/env bash
# Smoke-test the QA environment against its origin ports.
#
#   bash scripts/qa/smoke.sh
#
# WHY IT ASSERTS ON CONTENT. A status code is not evidence the page works. A
# 200 homepage has hidden five dead routes here before, and on 2026-09-16 the
# QA stack reported nine healthy containers while /in-play returned 500 and the
# match feed answered 503 to every request. Next.js also serves its own error
# page with a 200 in some configurations, so "HTTP 200" and "the page rendered"
# are different claims.
#
# Each check therefore requires a marker that only appears when the page really
# rendered, and rejects markers that only appear when it did not.
#
# It talks to 127.0.0.1 rather than qa.sokalens.com on purpose: the public host
# is behind a basic-auth gate whose credentials are not in the repo, and going
# through it would test Caddy rather than the application.
set -uo pipefail

WEB="${QA_WEB_ORIGIN:-http://127.0.0.1:13000}"
API="${QA_API_ORIGIN:-http://127.0.0.1:18080}"
FAILURES=0

fail() { printf '  FAIL  %s\n' "$*"; FAILURES=$((FAILURES + 1)); }
pass() { printf '  ok    %s\n' "$*"; }

# A page is healthy when it answers 200, its <title> is the page's own, and the
# body is substantial.
#
# Deliberately NOT a body-wide search for error strings. "This page could not
# be found" appears twice inside a perfectly healthy 61KB homepage -- it ships
# in the Next.js client bundle on every route -- so scanning the whole body for
# it fails every page. <title> is server-rendered per route and does
# discriminate; MIN_BYTES catches a route that renders a stub instead of
# content.
MIN_BYTES="${QA_MIN_PAGE_BYTES:-5000}"

check_page() {
  local path="$1" title_marker="$2" body code title bytes
  body="$(curl -sS --max-time 30 -w '\n%{http_code}' "$WEB$path" 2>/dev/null)" || {
    fail "$path -- request failed"; return; }
  code="${body##*$'\n'}"
  body="${body%$'\n'*}"
  bytes="$(printf '%s' "$body" | wc -c)"
  title="$(grep -oE '<title>[^<]*</title>' <<<"$body" | head -1 | sed 's/<[^>]*>//g')"

  if [ "$code" != "200" ]; then fail "$path -- HTTP $code"; return; fi
  if ! grep -qi -- "$title_marker" <<<"$title"; then
    fail "$path -- 200 but title is '${title:-<none>}', expected to contain '$title_marker'"; return
  fi
  if [ "$bytes" -lt "$MIN_BYTES" ]; then
    fail "$path -- 200 with correct title but only ${bytes}B (< ${MIN_BYTES}B)"; return
  fi
  pass "$path (${bytes}B, '$title')"
}

# An endpoint is healthy when it answers 200 and the named JSON key is present.
# A 503 body is still valid JSON, so the status check alone is not enough.
#
# The one retry is for the API's own request timeout, and only that. The smoke
# test runs seconds after the API restarts, so the prediction cache is empty
# and the first read of a busy matchday can outlast the 2,750ms request budget.
# The cache keeps loading detached for up to 10s (internal/predictions/cache.go)
# and the next read is served from it. Any other status, or a timeout that
# persists, still fails.
API_TIMEOUT_RETRIES="${QA_API_TIMEOUT_RETRIES:-3}"

check_api() {
  local path="$1" key="$2" body code attempt=0
  while :; do
    body="$(curl -sS --max-time 30 -w '\n%{http_code}' "$API$path" 2>/dev/null)" || {
      fail "$path -- request failed"; return; }
    code="${body##*$'\n'}"
    body="${body%$'\n'*}"
    if [ "$code" = "503" ] && grep -q '"request_timeout"' <<<"$body" \
      && [ "$attempt" -lt "$API_TIMEOUT_RETRIES" ]; then
      attempt=$((attempt + 1))
      printf '  wait  %s -- request timeout on a cold cache, retry %d/%d in 5s\n' \
        "$path" "$attempt" "$API_TIMEOUT_RETRIES"
      sleep 5
      continue
    fi
    break
  done

  if [ "$code" != "200" ]; then
    fail "$path -- HTTP $code $(head -c 120 <<<"$body")"; return
  fi
  if ! grep -q "\"$key\"" <<<"$body"; then
    fail "$path -- 200 but no \"$key\" in response"; return
  fi
  pass "$path"
}

echo "QA smoke test"
echo "web: $WEB"
echo "api: $API"

echo "--- pages ---"
check_page "/"                   "SokaLens"
check_page "/predictions/today"  "Today"
check_page "/acca-predictions"   "Acca predictions"
check_page "/in-play"            "In-play"

echo "--- api ---"
check_api "/v1/matches/today?limit=50"     "matches"
check_api "/v1/predictions/today?limit=20" "predictions"
check_api "/v1/accas?limit=10"             "items"

echo
if [ "$FAILURES" -ne 0 ]; then
  echo "SMOKE FAILED: $FAILURES check(s)"
  exit 1
fi
echo "SMOKE PASSED"

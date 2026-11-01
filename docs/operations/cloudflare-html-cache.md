# Edge-caching the HTML document

## The problem, measured

Cloudflare sits in front of sokalens.com and caches the static assets happily —
`/_next/static/*` returns `cf-cache-status: HIT`, `Age: 202367`. The HTML
document does not:

```
Cache-Control: private, no-cache, no-store, max-age=0, must-revalidate
cf-cache-status: DYNAMIC
CF-RAY: ...-LOS
```

Next marks every dynamically rendered response uncacheable, so every visitor —
including one already at the Lagos edge — pays a full round trip to the VPS.
Measured from a warm connection: **TTFB 0.60–1.00s**. That is the floor LCP
cannot go below, and it is worth more than any amount of front-end tuning.

## What the repository already does

`infra/deploy/Caddyfile` overrides the header for pages whose server render is
anonymous:

```
Cache-Control: public, max-age=0, s-maxage=1800, stale-while-revalidate=600
```

This document said `s-maxage=300, stale-while-revalidate=3600` until
2026-09-22, and the Caddyfile said `300/60`. Trust the Caddyfile: it is the
thing that runs. The numbers here are now the ones it sends.

`s-maxage` governs shared caches only. `max-age=0` keeps browsers revalidating,
so a corrected prediction is never pinned in someone's local cache.

**This alone changes nothing.** Cloudflare decides what is eligible for cache by
file extension, and HTML is not on that list at any origin header's request. A
Cache Rule is required. Until one exists, the header is inert — which is why it
is safe to ship first.

## The rule to create

Dashboard → **Caching** → **Cache Rules** → *Create rule*.

**Name:** `Cache anonymous HTML`

**When incoming requests match:**

```
(http.request.uri.path eq "/") or
(starts_with(http.request.uri.path, "/teams/")) or
(starts_with(http.request.uri.path, "/leagues")) or
(starts_with(http.request.uri.path, "/predictions/leagues/")) or
(http.request.uri.path matches "^/predictions/[^/]+/[^/]+-[0-9]+$") or
(http.request.uri.path in {"/about" "/best-bets" "/cookies" "/football" "/privacy" "/responsible-gambling" "/terms" "/acca-generator" "/multi-match-builder"})
```

**Then:**

- Cache eligibility: **Eligible for cache**
- Edge TTL: **Use cache-control header if present** (do not override — the
  origin already sends `s-maxage=300`)
- Browser TTL: **Respect origin**

That last choice matters. Setting a fixed Edge TTL here would silently diverge
from the Caddyfile, and the next person to change the caching window would
change one of the two and not the other.

## Purging on release

`infra/deploy/cloudflare-purge-html.sh` runs from the deploy job once the
release is healthy, before the public verification step.

It exists because a cached document names the exact JS chunk hashes of the
build that produced it. A deploy replaces those files, so HTML cached before
the release points at chunks that no longer resolve -- the page renders from
SSR and then nothing hydrates, which is worse than a slow page because it looks
like it works.

That risk is the only reason `stale-while-revalidate` was pinned to 60s, and
that bound was paid on every request by every visitor: a short window means a
low hit rate, and a miss is a full round trip to the VPS. Purging removes the
reason for the bound, which is why the window is now 600s and `s-maxage` 1800s.

**On this plan the purge is always a full purge.** Purge-by-prefix was refused
by the API when measured against the live zone, and the script falls back. That
is cheaper than it sounds: `/_next/static` is content-addressed, so a new build
references new filenames and the old ones are unreferenced the moment it lands.

The script exits 0 whatever happens -- a healthy release must not be failed by
a cache that could not be cleared -- so a broken purge is quiet. The 1800s
ceiling is the bound on what that quietness can cost.

## What must never be cached, and why

These pages read a cookie and genuinely render per viewer:

`/predictions/today`, `/predictions/tomorrow`, `/predictions/weekend`,
`/predictions/markets/*`, `/matches/today`, `/results`, `/acca-predictions`

The path list above excludes them, and
`tests/architecture/test_caddy_seo_contract.py` asserts both halves — that the
anonymous pages match and that every one of these does not.

Two further exclusions are handled at Caddy rather than in the rule:

- **Requests carrying `RSC` or `Next-Router-Prefetch`.** Next serves a different
  body for the same URL to client-side navigations, and signals it with `Vary`.
  Cloudflare only varies on `Accept-Encoding`, so caching one and serving it as
  the other breaks navigation.
- **Responses that set a cookie.** Cloudflare bypasses its cache on
  `Set-Cookie`. That is a safety net, not the primary control.

## Why this is safe despite signed-in users

No route reads a session cookie server-side. Authentication is entirely
client-side: `AccountProvider` fetches `/api/account/v1/access` after
hydration. Signed-in and anonymous visitors therefore receive **byte-identical
HTML** and diverge only once JavaScript runs.

Before widening the path list, re-establish that:

```bash
grep -rn "cookies()" apps/web/app
```

It should return only the timezone reads and the ACCA seed. If a route ever
starts reading a session cookie server-side, it must be excluded here **before**
that change ships, or one visitor's page will be served to another.

## Verifying it works

```bash
curl -sS -o /dev/null -D - https://sokalens.com/ | grep -i "cf-cache-status\|cache-control\|age"
curl -sS -o /dev/null -w "ttfb=%{time_starttransfer}s\n" https://sokalens.com/
```

Expect `cf-cache-status: HIT` on the second request and TTFB dropping from
~600ms to edge latency. Then confirm a personalised page is untouched:

```bash
curl -sS -o /dev/null -D - https://sokalens.com/predictions/today | grep -i "cf-cache-status"
```

That must stay `DYNAMIC`.

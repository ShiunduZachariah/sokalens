# Ad provider routing

> **Hidden since 2026-11-01.** The public site shows no ads, and everything
> below is switched off by `ADS_ENABLED=false` (the default). With it off, the
> API answers 404 on `/v1/ads/*` and `/v1/admin/ads`, `/v1/access/context`
> reports `ads.allowed: false` with reason `ads_disabled`, and the admin hides
> its Ads page. Restriction rules and feature flags are policy controls and
> keep working. The web app hides its slots behind a separate build-time switch,
> `NEXT_PUBLIC_ADS_ENABLED` (an `apps/web/Dockerfile` build argument, default
> `false`), so restoring ads means setting both and rebuilding the web image.

SokaLens assigns each eligible browser session a preferred advertising provider,
then serves each placement through a sticky, ordered fallback chain. The initial
assignment uses `traffic_weight`; failover uses `global_priority`. A provider
with weight zero can therefore act as emergency inventory without receiving new
sessions directly.

## Safety defaults

Migration `000082_ad_provider_routing` creates the provider catalogue and
outcome ledger. Its AdSense, Adsterra, and Monetag rows are disabled. Applying
the migration cannot start a third-party script.

Only browser-safe data belongs in `public_config`. Never store secrets, raw HTML,
arbitrary script URLs, or executable JavaScript there. Provider adapters and
their allowed origins are reviewed in source code. Adsterra and Monetag
currently fail closed with `INVALID_ZONE`; enable them only after their exact
zone formats have dedicated reviewed adapters.

## Configure AdSense

Create one real AdSense display unit per placement in the approved AdSense
account. Then configure the public publisher ID and placement slot IDs:

```sql
UPDATE ad_providers
SET enabled = TRUE,
    health_status = 'HEALTHY',
    global_priority = 10,
    traffic_weight = 100,
    failure_threshold = 20,
    cooldown_seconds = 300,
    request_timeout_ms = 1500,
    public_config = jsonb_build_object(
      'client_id', 'ca-pub-5331089211665627',
      'placements', jsonb_build_object(
        'homepage-after-hero', jsonb_build_object('slot_id', '<REAL_SLOT_ID>'),
        'homepage-mid-content', jsonb_build_object('slot_id', '<REAL_SLOT_ID>'),
        'global-footer', jsonb_build_object('slot_id', '<REAL_SLOT_ID>')
      )
    ),
    updated_at = now()
WHERE code = 'adsense';
```

Replace every placeholder with an AdSense slot ID before enabling the row. A
placement without a configured slot reports `INVALID_ZONE` and falls through;
it never renders an empty labelled box. Keep `apps/web/public/ads.txt` reachable
at `https://sokalens.com/ads.txt` and keep the publisher verification metadata
in the root layout.

## Placement inventory

Five ids, named for the **position** rather than the page. That is deliberate: an
id per page meant 39 AdSense units to create by hand before those pages earned
anything, and AdSense allows one ad unit to be rendered many times on a page — so
a feed that injects an ad every ten rows needs **one** unit, not one per position.

**You create five display units.** Their slot ids go in the `placements` object in
the `UPDATE ad_providers` statement above.

The trade is real: AdSense reports these as five units, so revenue cannot be split
between the Builder's feed and Today's at the network level. Page-level
attribution has to come from the site's own analytics.

**A placement with no configured slot reports `INVALID_ZONE` and renders
nothing** — correctly, since an unfilled slot must leave nothing behind, but that
also means an unconfigured placement is indistinguishable from one that simply did
not fill. It earns nothing, silently, for as long as it is missing.

`apps/web/app/components/adPlacements.test.mjs` pins this table to the code: add
or remove a placement and it fails until this list matches. It also fails if a
placement id is ever built from a template again, since a templated id cannot be
enumerated and therefore cannot be configured.

| Placement | Where |
| --- | --- |
| `feed-inline` | **Dynamic.** Injected after every 10 rows through the end of every scrolling feed — predictions, Best Bets, the Builder's match list, the ACCA catalogue — with content below each block. One unit appears on narrow screens, two above 650px of available row width, and three above 980px. Fixed full-width placements use the same block. Provider requests begin only as blocks approach the viewport. Rendered many times from this single ad unit. |
| `global-footer` | Every page, above the footer |
| `mobile-adhesion` | The fixed mobile band above the tab bar, on every page except the Builder |
| `page-rail` | The desktop side rail on every page that has one |
| `page-top` | The banner under every page masthead (home, match, prediction lists, leagues, league hub, team, results, Best Bets, in-play, Builder, ACCA pages) |

## Routing behavior

Redis holds a 24-hour session record containing the original preferred provider,
the currently active provider, and per-provider retry times. The attempt order is:

1. active provider;
2. original preferred provider, when its retry window has expired;
3. every remaining healthy provider by global priority.

Duplicates, disabled providers, non-healthy providers, open circuits, and
session-cooled providers are omitted. A successful fallback becomes the active
provider for later placements. When every provider is exhausted, the web
component returns `null` and the layout collapses.

Provider attempts use the configured 500-5000 ms timeout. Browser reports are
bound to a signed, five-minute, single-use attempt token and recorded in
`ad_provider_outcomes` with one of:

- `NO_FILL`
- `SCRIPT_ERROR`
- `TIMEOUT`
- `NETWORK_ERROR`
- `BLOCKED`
- `INVALID_ZONE`
- `PROVIDER_UNAVAILABLE`

Only transport/provider availability failures contribute to the global circuit.
One session can contribute at most once per provider per cooldown window. At the
failure threshold, the Redis circuit opens for `cooldown_seconds`; after that,
one replica receives a half-open probe. Success closes it and failure reopens it.

## Rollout and verification

1. Apply the migration through the normal CI deployment path.
2. Confirm all provider rows remain disabled.
3. Add reviewed placement IDs while the row is still disabled.
4. Verify `https://sokalens.com/ads.txt` returns the expected publisher line.
5. Enable one provider with a small initial weight, then load a configured
   placement in a non-Pro session without an ad blocker.
6. Confirm the provider appears on the admin Ads page and that
   `ad_provider_outcomes` receives a success or typed failure.
7. Inspect browser console and network failures, AdSense Policy Center, account
   approval, and consent requirements before increasing traffic.

Useful read-only checks:

```sql
SELECT code, enabled, health_status, traffic_weight, global_priority,
       request_timeout_ms, cooldown_seconds
FROM ad_providers
ORDER BY global_priority, code;

SELECT provider_code, outcome, failure_reason, count(*)
FROM ad_provider_outcomes
WHERE occurred_at >= now() - interval '1 hour'
GROUP BY provider_code, outcome, failure_reason
ORDER BY provider_code, outcome, failure_reason;
```

Changing database health to `DEGRADED`, `UNHEALTHY`, or `DISABLED` removes the
provider from routing immediately. Set `enabled = FALSE` for an explicit
operator shutdown. Runtime circuit state remains in Redis and does not rewrite
the PostgreSQL configuration row.

### QA expectations

QA applies migration `000082` but must leave every seeded provider disabled and
must not use production ad-network credentials. After the QA deployment:

1. Confirm `/v1/admin/ads` lists AdSense, Adsterra, and Monetag as disabled.
2. Request a public placement through the QA web proxy and confirm it returns a
   hidden/no-provider decision rather than a blank ad container.
3. Confirm a Pro account receives `pro_no_ads` before any provider routing.
4. Confirm the QA page source does not contain the AdSense loader while all
   providers are disabled.
5. Confirm the QA migration ledger contains version `000082_ad_provider_routing` and the normal QA
   smoke test completes.

Do not enable a network merely to prove fallback in QA. The routing service and
browser adapters are covered by deterministic unit/contract tests; any live
provider exercise requires separately approved non-production inventory.

The original ad-provider branch used migration number 000077. QA integration
renumbered this unreleased migration to 000082 because 000077 through 000081
already belong to in-play changes. Preserve those migrations unchanged; the
ledger records full names, including `000077_inplay_settlement_results` and
`000082_ad_provider_routing`.

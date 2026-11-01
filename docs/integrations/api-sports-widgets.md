# API-SPORTS Widget Integration

This page defines the reusable pattern for embedding official API-SPORTS/API-Football widgets in public or admin Next.js surfaces.

## Status

- Widget support is optional and must be behind a feature flag.
- The feature flag defaults to off in every environment.
- Widgets are display-only embeds. They do not replace worker ingestion, normalized platform data, or audited prediction publication.
- API-Football data used by the product must still be ingested through backend workers before it becomes platform source-of-truth data.

## Official Script

Load the official API-SPORTS widget script from:

```text
https://widgets.api-sports.io/3.1.0/widgets.js
```

The script should be loaded only when the widget feature flag is enabled and a page actually renders a widget. Avoid placing it in a global shell until the rollout proves the UX, performance, and commercial value.

## Custom Element

API-SPORTS widgets are rendered with the `api-sports-widget` custom element.

Use a reusable wrapper component or preset registry instead of copying raw custom-element markup into pages. The wrapper should own script loading, feature-flag checks, key selection, layout constraints, and fallback behavior.

Example shape:

```html
<api-sports-widget
  data-type="standings"
  data-league="39"
  data-season="2026"
  data-key="browser-visible-widget-key"
></api-sports-widget>
```

The exact `data-*` attributes depend on the official widget type being embedded. Keep those attribute sets in named presets so page code chooses intent, not low-level widget configuration.

## Keys And Secrets

You can run widgets in one of two supported modes:

- Direct mode (fastest to enable): use a browser-visible API-SPORTS widget key that is domain-restricted in the API-SPORTS dashboard.
- Secure proxy/CDN mode (recommended): leave the widget key empty in page config and set a proxy/CDN URL that injects `x-apisports-key` server/edge-side.

In this repo, secure mode is controlled with:

- `NEXT_PUBLIC_API_SPORTS_WIDGET_FOOTBALL_URL`
- Optional `NEXT_PUBLIC_API_SPORTS_WIDGET_LOGO_URL`

Never expose backend ingestion credentials to frontend code:

- Do not use `API_FOOTBALL_KEY` in public web, admin web, or client-rendered code.
- Do not send `x-apisports-key` from the browser.
- Do not proxy arbitrary widget requests through the backend to hide client usage.
- Do not reuse worker-ingestion credentials for widgets, even in staging.

The widget key is not a source-of-truth data credential. Treat it as a restricted embed token whose allowed domains, quota, and capabilities can be rotated independently from backend ingestion.

## Local Runtime Flags

For Docker web runtime:

- `NEXT_PUBLIC_ENABLE_API_SPORTS_WIDGETS=true`
- Direct mode: set `NEXT_PUBLIC_API_SPORTS_WIDGET_KEY` and keep `NEXT_PUBLIC_API_SPORTS_WIDGET_FOOTBALL_URL` empty.
- Secure mode: set `NEXT_PUBLIC_API_SPORTS_WIDGET_FOOTBALL_URL` and keep `NEXT_PUBLIC_API_SPORTS_WIDGET_KEY` empty.

## Seeing Live Data On Web

Use this checklist when `http://localhost:3000/football` does not show live widget data:

1. Confirm Docker services are running: `web`, `api`, and `worker`.
2. Confirm worker/provider live mode flags:
   - `ENABLE_REAL_API_FOOTBALL_CALLS=true`
   - `API_SERVICE_ENABLE_REAL_PROVIDER_CALLS=true`
   - `API_FOOTBALL_KEY` set for backend ingestion.
3. Confirm widget feature flags:
   - `NEXT_PUBLIC_ENABLE_API_SPORTS_WIDGETS=true`
   - Either `NEXT_PUBLIC_API_SPORTS_WIDGET_KEY` (direct mode) or `NEXT_PUBLIC_API_SPORTS_WIDGET_FOOTBALL_URL` (secure mode) is set.
4. Recreate web container after env changes:
   - `docker compose --env-file .env -f infra/docker/docker-compose.yml up -d --build web`
5. Open:
   - `http://localhost:3000/football`
   - `http://localhost:3000/predictions/today`
6. If widget is blank, enable widget error output by adding `data-show-errors=\"true\"` in widget config during debugging and check browser console.

Important: Widget data availability still depends on API-Sports live schedule windows and your plan quota.

## Provider Plan Note (Ingestion)

- For API-Football free plans, `fixtures?next=...` is not available.
- Worker ingestion in this repo now uses `fixtures?date=YYYY-MM-DD` for daily fixture sync on free-plan-compatible paths.

## Feature Flag

Use a dedicated flag such as `apiSportsWidgetsEnabled`.

Default behavior:

- Local development: off unless explicitly enabled.
- Preview environments: off unless a specific review requires widget verification.
- Production: off until legal, performance, product, and analytics checks are complete.

When the flag is off, pages should render the platform-native experience or a quiet empty state. Do not show broken placeholders or ask users to enable anything.

## Reusable Presets

Create reusable presets before adding widgets to product pages. Suggested presets:

- `fixtureSummary`: compact match details for a single fixture.
- `leagueStandings`: current league table for a configured league and season.
- `teamFixtures`: upcoming or recent fixtures for one team.
- `liveScoreStrip`: compact live fixtures for an approved competition set.

Each preset should define:

- Widget type and required `data-*` attributes.
- Required entity identifiers such as league, season, team, or fixture.
- Allowed surfaces, for example public match page, league page, or admin review page.
- Responsive sizing expectations.
- Fallback behavior when the script, key, or widget response fails.
- Analytics event names for render success, render failure, and user interaction where measurable.

Prefer one shared widget loader and one preset registry over page-specific embed code.

## Rollout Guidance

Roll out widgets incrementally:

1. Add the feature flag with the default set to off.
2. Add a shared script loader and wrapper component.
3. Add one low-risk preset, preferably on an internal or admin-only surface first.
4. Verify the browser uses only the domain-restricted widget key.
5. Enable the flag in a preview environment for product review.
6. Check layout, performance, analytics, consent, and country-policy behavior.
7. Enable in production for a narrow page set.
8. Monitor quota, console errors, widget availability, and page performance before expanding.

Widgets must not be used to bypass server-side restriction rules. Any legal, country, ad, or subscription behavior remains controlled by platform policy code.

## Verification Checklist

Before marking a widget integration complete:

- Confirm `https://widgets.api-sports.io/3.1.0/widgets.js` loads only when the feature flag is enabled.
- Confirm the rendered element is `api-sports-widget`.
- Confirm browser network traffic never includes `API_FOOTBALL_KEY`.
- Confirm browser network traffic never sends `x-apisports-key`.
- Confirm the visible widget key is domain-restricted and separate from worker-ingestion credentials.
- Confirm the page still works when the script fails to load.
- Confirm the page still works when the feature flag is off.
- Confirm mobile and desktop layouts do not shift or overflow while the widget loads.
- Confirm analytics can distinguish widget render success from failure.
- Confirm no product decision treats widget data as PostgreSQL source-of-truth data.

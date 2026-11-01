# football-data.org Provider

Last updated: 2026-07-14

football-data.org is the primary free test provider for provider connectivity and prediction-engine data ingestion. API-Football remains available as the richer secondary provider for odds, broader statistics, and advanced markets.

Official references:

- Quickstart and sample requests: <https://www.football-data.org/documentation/quickstart>
- v4 API reference: <https://docs.football-data.org/general/v4/index.html>
- Coverage: <https://www.football-data.org/coverage>
- Pricing and free-plan limitations: <https://www.football-data.org/pricing>

## Configuration

Set these variables in local `.env` or the process environment:

```env
FOOTBALL_PROVIDER=hybrid
ENABLE_REAL_API_FOOTBALL_CALLS=true
ENABLE_MOCK_API_FOOTBALL_CALLS=false
API_FOOTBALL_KEY=
FOOTBALL_DATA_ORG_BASE_URL=https://api.football-data.org/v4
ENABLE_REAL_FOOTBALL_DATA_ORG_CALLS=true
FOOTBALL_DATA_ORG_API_KEY=
```

Authentication uses the required HTTP header:

```http
X-Auth-Token: <FOOTBALL_DATA_ORG_API_KEY>
```

Never commit the real token. `.env.example` contains only the variable name.

## Provider Strategy

Continuous hybrid ingestion resolves data in this order:

1. Existing local historical data and imported database fixtures.
2. API-Football's bounded rolling window for current fixtures and odds.
3. football-data.org current-season competition matches for completed history and
   fixture fallback.
4. API-Football richer statistics or events where the configured plan permits.

The backend should preserve provider provenance through `provider_entity_maps`,
`provider_request_logs`, and the normalized tables that ingest each provider.
Missing provider fields must stay nullable or unavailable; prediction generation
should skip or void markets that cannot be supported by the available data.

The worker discovers accessible `TIER_ONE` competitions daily and queues one
deduplicated current-season match job per competition. Provider match identities
are merged when canonical league/team identities and kickoff agree. Successful
normalization queues prediction generation without waiting for odds.
Common cross-provider aliases for the World Cup, Brazil Serie A, and known club
designators are normalized before the slug fallback. Reconciliation repoints a
provider mapping to an existing natural fixture while preserving terminal scores.

## Supported Free-Plan Data

The free plan currently covers 12 competitions according to football-data.org coverage/pricing pages: Champions League, Primeira Liga, Premier League, Eredivisie, Bundesliga, Ligue 1, Serie A, La Liga, Championship, Brazil Serie A, World Cup, and European Championships.

Implemented worker-ingestion endpoints:

- `competitions`: competition catalog, normalized into canonical leagues and `provider_entity_maps`.
- `matches`: daily match list for subscribed competitions, normalized into `matches` and engine `fixtures`.
- `competitions/{code}/matches`: competition-scoped fixtures and historical matches where available.
- `competitions/{code}/teams`: team catalog, normalized into canonical teams and provider maps.

Free-plan fields are treated as nullable. Scheduled match scores remain `NULL`; missing halftime score, venue, minute, status detail, or historical fields do not break normalization.
World Cup-style `SCHEDULED`/`TIMED` placeholder fixtures whose participants are
still `null` are counted and logged as `unresolved_participants`, then skipped
until a later refresh. The worker never invents TBD teams or lets those records
discard the other valid matches in the response. A played fixture without real
teams remains an error.

## Rate Limits

football-data.org free authenticated clients are limited to 10 calls/minute. The client inspects:

- `X-RequestsAvailable`
- `X-RequestCounter-Reset`
- `Retry-After`

When headers show no remaining requests, the client waits until the reset window before the next request. It does not retry aggressively inside a request. `429` responses are logged and returned to the worker queue retry path.

## Free-Plan Gaps

Unavailable or plan-dependent data is left nullable/unavailable:

- Odds: not part of the free plan; use API-Football or a football-data.org odds add-on later.
- Corners, fouls, throw-ins, possession, saves, shots, and cards: statistic/deep-data add-on dependent.
- Lineups, substitutions, bookings, scorers, squads, and detailed match events: plan-dependent.
- Live scores are delayed on the free plan.

Prediction markets that rely only on fixture scores can use football-data.org data. Markets requiring odds or advanced statistics must remain unavailable or use API-Football later.

## Local API Checks

After ingestion and prediction jobs run, use the backend API without the UI:

```text
GET /healthz
GET /readyz
GET /docs
GET /openapi.yaml
GET /v1/providers/status
GET /v1/matches/today
GET /v1/matches/history?date=2025-05-18&status=finished
GET /v1/matches/{match_id}
GET /v1/predictions/today
GET /v1/predictions/{prediction_id}
GET /v1/predictions/hit-rate
```

Prediction generation is not a synchronous HTTP operation. Use worker jobs or
the Python engine CLI for generation and settlement, then read results through
the API endpoints above.

## Local Validation

Controlled live validation:

```powershell
$env:FOOTBALL_PROVIDER = "football-data-org"
$env:ENABLE_REAL_FOOTBALL_DATA_ORG_CALLS = "true"
$env:FOOTBALL_DATA_ORG_API_KEY = "<token>"
go run ./services/worker/cmd/providercheck
```

The long-running Docker worker schedules these jobs automatically. For a bounded
manual enqueue/process check:

```powershell
$env:FOOTBALL_PROVIDER = "football-data-org"
$env:ENABLE_REAL_FOOTBALL_DATA_ORG_CALLS = "true"
$env:FOOTBALL_DATA_ORG_API_KEY = "<token>"
go run ./services/worker/cmd/enqueueprovider
go run ./services/worker/cmd/runprovider --queues provider-reference,provider-fixtures --max-jobs 5
```

Keep live validation bounded because the free plan has a small minute quota.
For continuous Docker operation and queue/API checks, use
`docs/operations/backend-continuous-runtime-runbook.md`.

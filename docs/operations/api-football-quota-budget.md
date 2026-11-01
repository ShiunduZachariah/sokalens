# API-Football quota budget: 7,500 requests per provider day

Provider-confirmed: **7,500 requests/day and 300/minute** (the Pro plan).

The account is on the **7,500 requests/day** package. This document is the
budget: what the platform is allowed to spend, what it actually spends, and
which control stops it.

Two numbers are easy to confuse and the difference decides everything:

- **7,500 is a ceiling, not a target.** The worker refuses provider calls past
  it, on its own count of requests sent.
- **Spending nothing is also a failure.** Unused quota expires at 00:00 UTC.
  The pacer exists to spend the surplus on prediction history rather than let
  it evaporate.

## The ceiling, and why a header cannot raise it

`quota.DailyBudget` holds two kinds of number. `limit`, `remaining` and
`consumed` are beliefs about the provider's balance, and provider response
headers overwrite them on every call — correctly, because the provider owns
that balance.

`planLimit` and `issued` are not beliefs. `planLimit` is the package we pay
for and no response may raise it. `issued` is how many requests this process
has sent since 00:00 UTC, counted by us. `CanSpend` refuses when
`issued + cost > planLimit`, and that check sits **above** all three escape
hatches in that file — the unconfirmed-budget probe allowance, the recovery
probe, and the reserve that decays toward midnight. Each of those exists so a
wrong belief cannot stall ingestion, and each is a way a request gets sent
after pacing refused it. Checked last instead, any one of them would be a way
to overspend the package.

A header may *lower* the ceiling. If the provider says the package is smaller
than configured, the provider is the one billing us and it is right.

The count is seeded on boot from the spend already in `provider_request_logs`,
not from zero. A ceiling that reset on restart could be spent once per
restart, and the 2026-09-21 incident began with a restart.

## Where the 7,500 goes

| Line | Requests/day | Control |
| --- | --- | --- |
| Live poll `fixtures?live=all`, 60s inside the watch window | ≤1,440 | `QUOTA_LIVE_POLL_SECONDS` |
| Today's fixtures, 5 min (the FT source of truth) | 288 | `todayFixturesRefreshInterval` |
| Tomorrow's fixtures, hourly | 24 | `tomorrowFixturesRefreshInterval` |
| The other 9 dates in the window, 4-hourly | 54 | `PROVIDER_SCHEDULE_INTERVAL` |
| Odds sweeps (2 dates × 6) plus pages | ~70 | `PROVIDER_SCHEDULE_INTERVAL` |
| Results reconcile, hourly, batched by id | **~371 measured** | hourly bucket |
| Quota sync, hourly | 24 | hourly bucket |
| Per-fixture provider predictions | ≤200 | planner `limit` |
| Reference sync (per-league teams) | ≤100 | repository `limit` |
| Countries + leagues, daily | 2 | daily bucket |
| **Discretionary backfill** | **≤1,200** | `QUOTA_BACKFILL_MAX_REQUESTS_PER_DAY` |
| Daily reserve | 900 | `PROVIDER_DAILY_RESERVE` |
| Emergency reserve (12%) | 900 | `EmergencyReserveFraction` |

Worst case that sums to roughly 5,260 of 7,500, which is the point: the
schedule has to fit inside the package with room, or the ceiling is reached
every day and the first symptom is frozen scores.

The batched `fixtures?ids=` statistics poll is **off**, and the planner clamps
it to a five-minute cadence whenever the effective plan is 7,500 or less. At
the 60-second cadence it would cost ~7,200 requests for one 200-match
afternoon, which is the whole package.

**Pace against `PlanLimit`, not `Limit`.** The planner's plan-size clamps —
live poll at least 60s, statistics at least 5 minutes — are chosen by reading
the effective daily limit. Read from `Limit`, which is whatever the provider's
last header claimed, a header reading 75,000 switches every clamp off while
the budget governor goes on refusing at 7,500, because that ceiling is
config-owned. Cadences relax, the day's spend doubles, and the ceiling cuts in
mid-afternoon with live scores as the thing that stops. `PlanLimit` is the
number we are actually refused at, and pacing against it is the only way the
two agree.

**Live polling is not the expensive part.** `fixtures?live=all` is one request
per poll regardless of how many matches are in play, and it only runs inside a
calendar-derived watch window. Everything that scales with the size of the
card is fetched per date in bulk — one request covering every match on it.

## The three corrections that made it fit

**The forecast omitted the recurring schedule.** Every term in
`quota.Planner` scaled with the fixture calendar; the date sweeps, odds
sweeps and hourly jobs scale with nothing and cost several hundred requests a
day. Omitted from the projection, that cost was reported as spendable surplus
and handed to the pacer. `FixedDailyOverhead` is now part of the forecast,
pro-rated by the hours left in the day.

**The per-match cost was an estimate of a design that never shipped.** The old
coefficients — 10, 6, 4 and 2 by kickoff window, 22.5 a match — describe
refreshing each fixture individually. The schedule fetches by date in bulk
instead. At 22.5 a match the forecast claimed a 500-fixture Saturday needed
11,250 requests, so the planner declared critical pressure and paused
discretionary work on exactly the days with the most to cover.

**Per-cycle caps cannot bound a day.** On 2026-09-21 a restart left 4,076
backfill jobs queued, every per-cycle cap was respected, and the day spent
about 74,000 requests. `QUOTA_BACKFILL_MAX_REQUESTS_PER_DAY` counts jobs
already *enqueued* today from the `jobs` table — a queued job is a committed
request — and it fails closed when that query cannot be answered, because a
database under load after a restart is the exact state it exists to survive.

## Watching it

`refreshQuotaBurnGauges` publishes today's spend per endpoint group against
the package:

- `worker_quota_today_requests`, `worker_quota_plan_limit`,
  `worker_quota_today_fraction`
- `worker_quota_today_endpoint_{live,fixtures,odds,reference,backfill,other}`

Grouping is taken from the job's **reason**, not the endpoint. Every
fixture-shaped job calls the provider's `fixtures` endpoint, so the endpoint
cannot tell the live poll from the reconcile from a backfill; classifying on it
reported `live 0` and `backfill 0` while 361 live polls and 70 backfill
requests had been made. Per-reason figures are also published as
`worker_requests_today_by_reason_*`, which is finer than these six groups and
is how the reconcile cost below was found.

**Measured 2026-10-16**, 1,514 requests by 11:20 UTC: reconcile 371, live 361,
predictions 200 (capped), prematch 194, inplay_statistics 137, reference 100
(capped), backfill 70, statistics 58, quota_sync 12, odds 6. Roughly 3,200
projected for the day, 43% of the package. The hourly results reconcile is the
largest single line at 24% — it, not the live poll, is the lever if spend needs
cutting, and the ~60/day in the table above was badly wrong.

It warns at **60%** and logs an error at **80%**, not at 100%. An alert at the
ceiling reports an event nobody can act on any more. Sixty percent of 7,500 is
reached in the evening on a normal day and before noon on a runaway one, and
that difference is the whole signal. Per endpoint group, because "we are at
80%" is not actionable and "we are at 80% and backfill is two thirds of it" is.

## Changing the plan

`PROVIDER_DAILY_LIMIT` in the repository, not on the host.
`infra/deploy/ci-deploy.sh` **upserts** it on every green-main release, so a
value corrected on the box is reverted by the next deploy. That line was
pointed at 75,000; it now normalises to 7,500 along with the reserve, the
backfill ceiling, the fixed overhead and the poll cadence.

If the package changes, change it in `ci-deploy.sh`, `.env.example`,
`infra/deploy/.env.prod.example`, `infra/docker/docker-compose.yml` and the
defaults in `services/worker/internal/config/config.go` and
`services/worker/internal/quota/planner.go`. Two tests hold you to it:
`infra/deploy/staged-deploy.test.mjs` asserts the deploy script's normalised
limit, reserve and minute ceiling, and `TestLoadUsesSafeWorkerDefaults`
asserts the recurring commitments still fit inside whatever the package is.
A plan change that does not fit fails the suite rather than production.

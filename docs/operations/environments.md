# Environments

**There are exactly two: production and QA. Do not create a third.**

Read this before standing up any stack, adding a Compose project, registering a
runner, or picking a port. It is the authoritative map; if something on the host
disagrees with this file, the discrepancy is the bug.

## The two

| | Production | QA |
|---|---|---|
| Compose project | `sports-prediction-platform` | `sokalens-dev` (see note) |
| Docker daemon | rootful (`root`) | rootless, own non-root user |
| Host directory | `/home/deploy/sokalens` | the QA user's `sokalens` checkout |
| Public hosts | `sokalens.com`, `www`, `hq.sokalens.com` | `qa.sokalens.com`, `qa-admin.sokalens.com` |
| Overlay | `infra/deploy/docker-compose.prod.yml` | `infra/deploy/docker-compose.qa.yml` |
| Env file | `.env.prod` | `.env.qa` |
| Database / role | `sports_prediction` / `sports` | `sports_prediction_qa` / `sports` (see below) |
| Provider calls | live | **off** (mock) |
| Deployed by | CI `deploy` job on green `main` | `.github/workflows/qa-deploy.yml` on pushes to `qa` |

**QA promotion is a real deployment.** Since 2026-09-22, a push to `qa`
triggers `.github/workflows/qa-deploy.yml`. It invokes
`scripts/qa/deploy.sh` as the `dev` user, synchronizes `/home/dev/sokalens`
to `origin/qa`, builds the rootless QA stack, applies QA migrations, and runs
`scripts/qa/smoke.sh`. The `ci.yml` `deploy` job remains exclusive to green
`main`. Before promoting a feature to `main`, verify the QA deploy run, the
live QA checkout SHA, relevant user/API behavior, and QA CI. A green `qa`
branch check alone does not prove the deployed QA service works.

**Testing a change on production's data.** QA's mock cannot show how a model
behaves on production's fixtures. Run this after a change to the prediction
engine and before promoting it:

```bash
gh workflow run qa-deploy.yml --ref qa -f production_copy_test=true
```

`scripts/qa/production-copy-test.sh` streams the football tables it names into a
scratch database inside QA's PostgreSQL, runs QA's worker against it, prints the
results and drops it. Production sessions are read-only and no user table is
copied. It is one database for one run, not a third environment.

**Note on the QA database and role.** Until 2026-09-18 this table was
aspirational: QA was actually running on `sports_prediction` / `sports` --
production's database name and production's role name, on production's host.
Nothing in the repo said so, because the QA env file simply omitted
`POSTGRES_DB` and `POSTGRES_USER` and inherited the base compose defaults,
which are production's. The database has since been renamed in place
(`ALTER DATABASE`, no volume recreation, no data loss) and the names are now
pinned literally in `docker-compose.qa.yml`, the same way the ports are.

The **role is still `sports`**. A session user cannot rename itself and the
image creates no second login role, so the rename needs a temporary superuser:

```sql
CREATE ROLE tmp_rename SUPERUSER LOGIN;   -- then reconnect as tmp_rename
ALTER ROLE sports RENAME TO sokalens_qa;  -- scram-sha-256 keeps the password
DROP ROLE tmp_rename;
```

followed by the three URLs in `.env.qa`, `POSTGRES_USER`, and the healthcheck
in `docker-compose.qa.yml`. Until that lands, QA and production share a role
name and **only the password separates them** -- mechanism 3 of the three
below is doing half its job.

**Note on the QA project name.** The public hostnames were renamed from `dev.*`
to `qa.*` on 2026-09-16, but the Compose project is still `sokalens-dev`.
Renaming it recreates every container against a freshly-named volume, which
discards the QA database — so it is left alone deliberately. "QA", "dev" and
`sokalens-dev` all refer to this one environment; there is no second one.

Both run on the same Contabo host (8 vCPU / 24 GB / 290 GB). They are kept apart
by three independent mechanisms, in order of how badly each fails:

1. **Separate Docker daemons.** QA is rootless under its own user and cannot see
   production's containers, volumes, networks or images at all.
2. **Disjoint host ports** (below). Loopback-only on both sides.
3. **Separate database, role and password**, so a mistyped URL cannot
   authenticate against production data.

## Ports

Loopback only; nothing below is published to the internet. Caddy runs on the
host, not in Compose, and is the only thing bound publicly (80/443).

| Service | Production | QA |
|---|---|---|
| web | 3000 | 13000 |
| admin | 3001 | 13001 |
| postgres | 5433 | 15433 |
| redis | 6379 | 16379 |
| api | 8080 | 18080 |
| mock-api-football | 8090 | 18090 |
| ops-controller | 8091 | 18091 |
| typesense | 8108 | 18108 |

Production additionally runs the observability profile (prometheus 9090,
node-exporter 9100, loki 3100, grafana, cadvisor, uptime, backup). **QA does not
run observability** — it binds more host ports and buys nothing for testing.

`13000` and `13001` are not arbitrary: the Caddyfile proxies the QA hosts to
exactly those, so changing them silently takes the QA site down.

**CI also runs on this host now**, and its throwaway PostgreSQL service
container takes **25433**. It used to take 15433, chosen before QA existed here
-- which is QA's PostgreSQL. A self-hosted runner either fails to bind that or,
far worse, lets `task db:migrate` and `task db:seed` find QA's database instead
of the throwaway one. Anything else CI needs to publish must come from a third
range, not from either environment's.

The `worker` job runs the Python prediction-engine suite against its own
throwaway PostgreSQL on **25434**, from that same CI range. It needs a separate
container rather than sharing the `api` job's: the two jobs are independent and
can be in flight on this host at the same time. `15434` would have been free
today but sits inside QA's block, which is how 15433 was chosen in the first
place.

The Go worker's PostgreSQL-gated tests use **that same 25434 container**, not a
third port. The rule is about jobs, not suites: two jobs that can run
concurrently need their own databases, while the Go and Python worker suites are
sequential steps of the one `worker` job and each builds its own schema or
temporary tables. The job runs `task db:migrate` against 25434 before the
tests, because the ingestion cases query the real migrated schema. So a fourth
CI port would cost a container start and ~100 MB of runner RAM on every worker
build and prove nothing extra -- and on a host that already holds production and
QA, not adding one is the point.

Sharing it was verified in the job's own order -- migrate, Go suite, Python
suite, on one database -- because both suites now meet a migrated `public`
schema where they previously met an empty one. Both pass, and re-running
`task db:migrate` against an already-migrated database is a sub-second no-op.

**A database test must not be able to reach `public` by accident.** That is the
condition sharing rests on, and migrating the database is what makes it matter:
an unqualified name that once failed loudly now finds a real, empty table and
answers nothing. Every PostgreSQL-gated test here therefore pins its own
namespace and is responsible for keeping `public` off its path:

- A test that creates a named schema does `SET search_path` to that schema
  alone, and **bounds its pool to one connection**, because `SET` is
  per-session: without that, a pooled statement can arrive on a connection
  whose path is still `"$user", public`. This is not theoretical -- forcing a
  new session per statement makes `matchstate`'s first `CREATE TABLE matches`
  fail with `relation "matches" already exists`, against the real table.
- A test that uses `CREATE TEMP TABLE` does `SET search_path TO pg_temp`.
  `pg_temp` already resolves ahead of `public`, but only for the names the test
  creates; restricting the path is what makes a name it does *not* create fail
  rather than silently resolve. Verified by mutation in both languages: with a
  migrated-`public` table added to the statement under test, the guarded test
  fails with `relation "teams" does not exist` and the unguarded one passes.

## The trap that has already bitten once

`infra/docker/docker-compose.yml` sets `restart: unless-stopped` on twelve
services but **not on `web`, `admin` or `typesense`**. Production survives
reboots only because `docker-compose.prod.yml` adds it to those three.

QA ran the base file with no overlay for months. On 2026-09-16 at 04:33 every
container stopped; everything with a restart policy came back and those three
did not. They exited `255` with `oom=false`, no error, and a clean `✓ Ready` as
their last log line — so nothing looked broken anywhere. The QA site was simply
dark until someone went looking.

**Any environment running the base compose needs an overlay that sets a restart
policy on web, admin and typesense.** That is the main reason
`docker-compose.qa.yml` exists; the resource limits are secondary.

## Deploying to QA

```bash
sudo -u dev env HOME=/home/dev bash /home/dev/sokalens/scripts/qa/deploy.sh
```

Run it from a directory the `dev` user can read -- `/home/dev/sokalens`, or
anywhere under `/tmp`. Compose resolves build contexts through the *calling
process's* working directory, so launching it from an operator's own checkout
under another home fails the build with `permission denied` on a path the stack
never needed. The script now `cd`s to the deploy directory itself, but an older
copy on the host will not.

The QA env file is `.env.qa`. It was `.env.dev` on the host until 2026-09-18,
while this file, `.env.qa.example` and the overlay's header all said `.env.qa`.

That script exists for one reason: **`docker compose up` does not run
migrations.** Production is covered because `ci-deploy.sh` runs `dbtool`
separately; QA had no equivalent, so on 2026-09-16 it came up on code expecting
migration `000065` against a database that never had it. `/v1/matches/today`
answered 503 with `column m.live_state_at does not exist` and `/in-play`
returned 500 — while all nine containers reported healthy. Never bring QA up
with a bare `docker compose up`.

`scripts/qa/smoke.sh` runs at the end and can be run alone. It asserts on
server-rendered `<title>` and response size, not status codes, because a 200 is
not evidence a page works — and it deliberately does not scan the body for
error strings: "This page could not be found" ships inside the Next.js bundle
on every healthy route.

## Rules

- **Never add an environment.** If you think you need one, you need this file
  updated first, and almost certainly you do not need one.
- **The QA host tracks the `qa` branch. Nothing else.** It was found on
  2026-09-16 sitting on `feat/sokalens-mobile-v2` with uncommitted changes,
  which is why QA served a build from 2026-09-08 and returned 404 for routes
  that had shipped since. Never park a feature branch there. Mobile in
  particular is out of scope: `apps/mobile` will be redone later, and no mobile
  branch belongs on the VPS or in QA.
- **Never hard-code a port from the base compose.** Its defaults are
  *production's* ports. An overlay must write QA ports literally, with
  `!override`, so a missing variable cannot bind QA onto a production port.
- **Never point QA at live provider credentials.** QA and production share one
  API-Football daily quota, and the quota planner does not know a second
  consumer exists. The failure looks like unexplained production quota
  exhaustion, not like a QA problem.
- **Verify QA as a user, in a browser.** A 200 from `curl` has hidden dead
  routes here before. Exercise the actual journey.
- **Sizing is for a 24 GB host.** The `6 GB` budget in older docs describes the
  retired HostPinnacle box and is wrong.

## Removed, and why

Three environments beyond these two existed on the host and were removed in
September 2026. Recorded so they are not recreated by accident:

- **`sokalens-dev`** — renamed to `sokalens-qa`. It *was* the QA environment
  under an older name, reusing production's database name and role. Not a
  duplicate, a rename.
- **`ci`** (rootless daemon) — was removed in September 2026 as abandoned, and
  is **back as of 2026-09-19**. CI itself now runs on this host: the `CI_RUNNER`
  repository variable points at the `contabo-ci` self-hosted runner, and the
  workflow needs a Docker daemon for its PostgreSQL service container and three
  image builds. It is the runner's daemon, not an environment — no stack runs
  under it. Giving the `ci` user the rootful daemon instead would hand CI
  root-equivalent access to production's containers, which is exactly what
  `ci-isolation.service` exists to prevent.
- **`lab`** (rootless daemon) — 0 containers, 0 images, 0 volumes. Empty, and
  still costing ~123 MB of RAM to run a daemon that held nothing.

**A trap found while removing them:** disabling linger for the `ci` user to stop
its idle Docker daemon also stopped `actions-runner.service`, a *user* systemd
service in the same account — taking the `contabo-ci` deploy runner offline and
silently blocking every production deploy. An idle Docker daemon does not mean
an idle user. Check `ls ~<user>/.config/systemd/user/` before disabling linger.
See `self-hosted-runner.md`.

Each idle rootless daemon costs 120–155 MB of RAM whether or not it runs
anything. That is the concrete argument against "just one more environment".

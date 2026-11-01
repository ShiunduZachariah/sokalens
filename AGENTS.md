# AGENTS.md

Instructions for AI coding agents working in this repository. Read
[README.md](README.md) first for what the system is and how it fits together.

## Environments and releases

- **There are exactly two environments: production and QA.**
  `docs/operations/environments.md` is the map: projects, ports, databases,
  and the restart-policy trap that once took QA down silently for months. Read
  it before standing up any stack, choosing a port, or adding a Compose
  project. Do not add a third environment.
- **Every change is a pull request.** Nothing is pushed directly to `main`
  (production) or `qa` (QA); a ruleset enforces it. Open feature PRs into
  `qa`; promote with a `qa` → `main` PR. A code owner approves every merge.
  Agents open pull requests; they do not merge them.
- **Deployments go through CI only.** The workflows reach the server through
  `.github/actions/vps-ssh`, which reads the host, port, user, key and pinned
  host key from repository secrets. Never write a server address, SSH port,
  username or key into a tracked file, a workflow, a commit message or a PR.
- **CI and deployment use self-hosted runners** (`vars.CI_RUNNER`,
  `vars.DEPLOY_RUNNER`). Pull requests from forks are routed to GitHub-hosted
  runners so outside code never runs on the production host. Keep CI test
  ports separate from production and QA; see `docs/operations/environments.md`.
- **Registration and login are Turnstile-gated.** Automated end-to-end runs
  cannot complete them: Turnstile correctly refuses headless browsers. Do not
  treat a failing automated signup as a regression without checking the
  security-check step first.
- **`dbtool-linux` does not read `.env.prod` itself.** It needs `DATABASE_URL`
  exported with the host rewrite (`@postgres:5432` → `@127.0.0.1:5433`), as
  `infra/deploy/ci-deploy.sh` does. Without it, it fails SASL auth and looks
  like a password problem.
- **Caddy runs on the host**, not in Compose, and needs `/var/log/caddy` owned
  by `caddy:caddy`. Running `caddy validate` under `sudo` creates a root-owned
  log file that stops the service starting; fix with
  `chown -R caddy:caddy /var/log/caddy`.

## The public snapshot

`ShiunduZachariah/sokalens` is a public copy of `main`, one commit per
refresh. This repository stays private, and its history must never be pushed
there: it holds old server details, personal addresses and third-party data.

- Publish with `scripts/public/refresh.sh` (dry run by default, `--push` to
  publish). Never push a branch of this repository to the public one.
- Bring a public pull request in with `scripts/public/import-pr.sh <number>`,
  then read every line before pushing the branch: CI here runs on the
  production host. Then open a normal PR into `qa`.
- The public repo has Actions disabled. Never add secrets to it or enable
  them; `ci.yml`'s deploy job would run there.

## In-play work: read the contract first

In-play prediction spans the model, the worker, the API and both clients.
`docs/integrations/in-play-data-contract.md` is **the contract** between the
backend and every client: what the backend provides, the wire format, the
cadences and the settlement states. If your work needs a field it does not
describe, raise it there rather than inventing one.

Four rules hold across every in-play change, because each has already cost
something:

1. **One id per thing.** `fixtures.id` is a primary key referencing
   `matches(id)`; team ids are canonical `teams.id`. Provider ids are never
   canonical — resolve through `provider_entity_maps`, and skip an unmapped
   entity rather than guessing. Guessing once split team history across two ids
   and made the engine see 6 matches instead of 15.
2. **Everything in-play lives behind `/v1/inplay/`.** The whole prefix routes to
   one handler so a new path either passes the entitlement gate or 404s. An
   endpoint added outside it bypasses revenue protection silently.
3. **The provider is the source of truth, and corrections are visible.** A
   withdrawn goal is marked retracted, never deleted. Settlement is immediate
   and reversible with no time bound — a reversal can arrive after full time.
4. **Full time closes every market.** A market that cannot be graded closes as
   `unsettled_data_unavailable`, excluded from accuracy. Nothing stays pending.

## Architecture constraints

- Backend is a Go modular monolith.
- PostgreSQL is the source of truth. Redis is cache/queue/lock support only.
- API-Football calls happen through worker ingestion only.
- Public web and admin are Next.js. Mobile is Flutter.
- Python is for the prediction engine's prepared-data inference (ADR 0002) and
  offline training/backtesting, nothing else, unless explicitly approved.
- Restricted country/legal/ad behaviour goes through server-side policy
  controls and audited restriction rules.
- Published predictions are versioned and auditable.
- Migrations under `infra/db/migrations` are checksummed by
  `task architecture:migrations:verify`. Never edit an applied migration;
  add a new one.

## Commits, pull requests and personal data

- No `Co-Authored-By` (or `Co-authored-by`) trailers, and no email addresses,
  in commit messages, PR descriptions or tracked files. `.claude/settings.json`
  turns attribution off for Claude Code; `git config core.hooksPath .githooks`
  enables a hook that strips the trailers from any tool.
- Never commit a real person's email, name-and-account pairing, or account
  credentials. Test data uses `example.com` / `.test` addresses.
- The PR description is the handoff: what changed, why, how it was verified,
  and what is left. There is no separate handoff log.

## CI gate failures: diagnose before retrying, never bypass

A red security gate is not automatically a code problem, and it is never a
reason to weaken the gate. Establish which of these you have before acting:

- **A finding.** The scanner ran and reported something. Fix the dependency.
- **A non-answer.** The scanner never ran. An unavailable audit is *not* a
  clean audit.

For `npm audit` failing on
`POST https://registry.npmjs.org/-/npm/v1/security/advisories/bulk`, this
separates the possible causes in about a minute:

```bash
curl -s -m 30 -o /dev/null -w "%{http_code} %{time_total}\n" https://registry.npmjs.org/
curl -s -m 30 -o /dev/null -w "%{http_code} %{time_total}\n" \
  https://registry.npmjs.org/-/npm/v1/security/advisories/bulk            # GET -> 405 fast = route alive
curl -s -m 30 -X POST -o /dev/null -w "%{http_code} %{time_total}\n" \
  https://registry.npmjs.org/-/npm/v1/security/advisories/nonexistent     # POST -> 404 fast = POST alive
curl -s -m 60 -X POST -H 'Content-Type: application/json' \
  -d '{"lodash":["4.17.20"]}' -o /dev/null -w "%{http_code} %{time_total}\n" \
  https://registry.npmjs.org/-/npm/v1/security/advisories/bulk            # minimal payload
```

| Observation | Cause |
| --- | --- |
| Root fast, `GET` 405 fast, `POST` 404 fast, bulk hangs/503 | Service-side: their audit workers, not you |
| Minimal payload fails too | Not payload size and not package-specific |
| Fails from your machine *and* CI | Not the runner or its network |
| Works locally, fails only in CI | Runner egress, proxy or DNS |

Do not trust a status page to settle it, and do not claim a global outage;
report what you observed and from where.

The correct response to a confirmed non-answer is to **retry**, not to bypass,
not to add `|| true`, and not to lower `--audit-level`.
`scripts/ci/npm-audit-with-retry.sh` retries transport failures with backoff
and still fails closed when the service never answers; it never retries a real
finding.

**Keep independent scanners in independent jobs**, so one scanner's outage can
never hide another's finding.

## Deployment gates: widen the budget, never the contract

Promotion gates such as `infra/deploy/acca-readiness.sh` mix two kinds of
condition:

- **Timing** — how long to wait for a healthy system to converge. Adjust
  freely, and say why in the commit.
- **Validity** — duplicates, invalid rows, minimum coverage, non-empty API
  responses. This is the promotion contract. Never loosen it to get a deploy
  through.

Before changing either, confirm the system can reach the state the gate wants:
the feature flag is on, the generating job has run, and counts are converging
rather than oscillating. Oscillation is a real defect.

## Working alongside other agents

- Split work into narrow, self-contained tasks with disjoint write scopes.
  You are not alone in the codebase: never revert or overwrite edits you did
  not make.
- Ownership by path: `docs/ai/designs/**` and `apps/**` belong to the design and
  client workstreams; `services/**` and `infra/db/migrations/**` to the backend.
  Avoid editing another workstream's files in the same run.
- Approved agent skills are listed in `skills-lock.json` and install with
  `scripts/install-agent-skills.sh all` (or `.ps1`) into `.agents/skills/`,
  which is not tracked. Load a skill because it applies, not to pad.

## Clean up what you create

Anything an agent stands up to get a job done is rubbish the moment the job is
done, unless someone will deliberately run it again: helper scripts, one-off
containers or Compose projects, scratch databases, probe binaries, generated
fixtures, temporary worktrees, debug endpoints. **The run that creates it owns
deleting it.** If a human will run it again, give it a home in the repo and a
line in the docs saying when to run it.

Duplicated workspaces (`-v2` folders, extra worktrees, scratch clones) are
deleted in the run that merges their work, after confirming the work is on
`main` and the duplicate has no uncommitted or unpushed changes. Remove a
worktree with `git worktree remove` then `git worktree prune`, never `rm -rf`.

Stop and report instead of deleting if the thing holds state nobody has
copied — an unpushed branch, the only dump of a database.

## Local runtime: everything through Docker

Do not start long-lived development processes directly on the host (no bare
`npm run dev`, `next dev`, `go run` servers, watchers or tunnels). Run services
through Compose:

```bash
docker compose -f infra/docker/docker-compose.yml --profile core up -d
```

Short-lived foreground commands are fine on the host: `go build`, `go test`,
`go vet`, `gofmt`, `npm run lint`, `npm test`, migrations, one-shot scripts.

`docker builder prune -a -f` and `docker image prune -f` are safe.
**`docker volume prune` is not**: volumes hold PostgreSQL and Typesense data.
Never prune volumes without explicit confirmation.

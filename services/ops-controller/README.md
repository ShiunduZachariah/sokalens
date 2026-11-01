# SokaLens ops-controller

This service is the observe-only foundation of the SokaLens operations control plane. It exposes process probes, metrics, and a signed alert-ingestion boundary. Accepted alerts can flow through the OPS-004 typed incident classifier and PostgreSQL repository seam. The service has no provider/AI integration or mutation authority.

## Safety boundary

- `OPS_AUTONOMY_MODE` defaults to `observe` and every other value fails configuration validation.
- The service contains no arbitrary command or shell execution API.
- Future operational requests must use the typed `internal/operations` contract with targets, scopes, idempotency keys, and bounded timeouts.
- The observe-only executor validates a request and rejects its execution.
- Alert ingestion is disabled unless `OPS_ALERT_SIGNING_SECRET` contains at least 32 characters. Secrets and alert bodies are never logged.

## Run locally

```sh
go run ./cmd/ops-controller
```

Endpoints:

- `GET /healthz`: process liveness
- `GET /readyz`: serving readiness and current autonomy mode
- `GET /metrics`: Prometheus text exposition
- `POST /v1/alerts`: authenticated, replay-protected alert ingestion

Configuration uses `OPS_`-prefixed environment variables. The default listener is `:8090`. HTTP and shutdown timeouts are validated during startup.

Durable incident ingestion is opt-in and requires all of:

- `OPS_INCIDENT_STORE_ENABLED=true`
- `OPS_ALERT_SIGNING_SECRET` with at least 32 characters
- `OPS_DATABASE_URL` containing a PostgreSQL connection URL

Startup opens the database through pgx, applies bounded pool settings, performs a bounded readiness ping, and verifies the `ops_incidents` and `ops_incident_observations` relations before installing the incident sink. Any missing configuration, invalid connection, failed ping, or unapplied schema stops startup or leaves the default sink disabled; an alert is never acknowledged and discarded. The URL is never logged. Production configuration rejects the repository's development credentials and requires an explicit TLS mode of `sslmode=require`, `sslmode=verify-ca`, or `sslmode=verify-full`; omitted, disabled, and opportunistic TLS modes are rejected.

Pool and latency bounds are configurable with `OPS_DB_MAX_OPEN_CONNS` (default 10), `OPS_DB_MAX_IDLE_CONNS` (5), `OPS_DB_CONN_MAX_LIFETIME` (30m), `OPS_DB_CONN_MAX_IDLE_TIME` (5m), `OPS_DB_PING_TIMEOUT` (3s, maximum 10s), and `OPS_INCIDENT_OPERATION_TIMEOUT` (2s, maximum 10s). `OPS_INCIDENT_CORRELATION_WINDOW` defaults to 15m and cannot exceed 24h. The process closes the pool during graceful shutdown.

## Signed alert contract

Send strict `application/json` with these headers:

- `X-Ops-Timestamp`: an RFC3339 timestamp inside the configured clock-skew window
- `X-Ops-Delivery-ID`: a unique bounded token for replay protection
- `X-Ops-Signature`: `v1=` followed by the hex HMAC-SHA256 of `<timestamp>.<raw body>`

The JSON fields are `version`, `alert_id`, `source`, `name`, `service`, `environment`, `severity`, `starts_at`, and `summary`. Unknown fields and severities outside `info`, `warning`, and `critical` are rejected. Defaults are a 64 KiB body limit, five-minute clock window, and 10,000-entry bounded replay cache. `OPS_ALERT_MAX_BODY_BYTES`, `OPS_ALERT_MAX_CLOCK_SKEW`, and `OPS_ALERT_REPLAY_LIMIT` may narrow or tune those validated bounds.

The response idempotency key is deterministic from normalized alert identity and start time. Replay protection here is process-local transport protection. The default sink fails closed with `503` rather than acknowledging and discarding an alert.

## OPS-004 incident persistence seam

`internal/incidents` classifies accepted alerts using normalized condition, service, and environment dimensions. Migration `000038` adds an immutable observation ledger with unique source-event and idempotency identities. Transport and source-event retries therefore return the existing incident without inflating its occurrence count. Different, genuinely new source events with the same fingerprint correlate only inside a configured rolling window; different services, environments, and conditions remain separate. A severity may only escalate. Evidence is a fixed JSON shape with occurrence count and the latest bounded delivery metadata.

`PostgresRepository` uses a serializable transaction and a fingerprint advisory transaction lock, then updates or inserts `ops_incidents`. SQL states `40001`, `40P01`, and the source-event uniqueness race `23505` are retried at most three times; all other storage, commit, or context errors fail closed. PostgreSQL remains authoritative and the sink performs no job, action, deployment, provider, AI, shell, or production mutation.

`main` now wires the ingestion repository only after explicit enablement and successful PostgreSQL readiness. Deployment still requires migration `000038` to have been applied and a least-privilege database role with only the incident read/insert/update permissions required by this observe-only flow. OPS-005 provides a separate transactional state-machine package and migration `000039`; it is not exposed through the HTTP server or automatically invoked by ingestion. This service still has no automated remediation or production mutation authority.

## OPS-006 durable jobs

`internal/jobs` treats PostgreSQL `ops_jobs` rows as the only durable queue and lease authority. Migration `000040` adds legacy-safe failure classification, dead-letter timestamps, lease/terminal shape constraints, and a deterministic claim index. Enqueue is serializable and idempotent; claim uses `FOR UPDATE SKIP LOCKED`; renew, complete, and fail require a live matching worker/token lease. Retryable failures use capped exponential backoff, while permanent or exhausted work enters the dead-letter state. Expired leases are reclaimed in bounded batches.

Semantic priorities map explicitly to the existing higher-number-first database order: P0=900, P1=600, and P2=300. Redis is optional wake-up acceleration only. The dependency-light adapter publishes a payload-free queue hint after PostgreSQL commits; Redis never contains the job payload, grants a lease, or authorizes execution, and a Redis failure cannot lose committed work.

## OPS-007 bounded scheduling

`internal/scheduler` provides bounded parallel coordination without a global sequential queue. It enforces a global cap, P0/P1 reserved capacity, per-service exclusion within the controller process, weighted selection, aging-based starvation protection, bounded admission/backpressure, cooperative cancellation, and clean shutdown. The durable source/handler boundary remains explicit so a future runtime adapter must claim and settle through `internal/jobs`; the scheduler itself cannot manufacture a durable job or lease.

The jobs and scheduler packages are not started by `main` yet because no executable operation handler is authorized in observe mode. This prevents the queue foundation from silently creating a mutation path.

## OPS-011 observe-only reachability

`internal/policy` accepts bounded typed recommendations, normalizes them into hash-versioned audit records, appends them through an append-only audit sink, and always returns `ErrObserveOnly`. The observe policy does not retain the supplied `operations.Executor`, so mutation is unreachable by construction. Tests enumerate every typed operation and use a tripwire executor to prove zero calls. Missing, cancelled, or failing audit storage denies the recommendation and exposes no backend error.

The policy audit sink remains an internal seam and is not connected to HTTP or job execution. Adding durable recommendation persistence later must preserve append-only behavior and cannot grant operation authority.

## OPS-013 release canary policy

`internal/canary` is a pure, versioned decision contract for exact 5%, 25%, and 100% release stages. Each stage requires a complete bounded observation window, fresh finite samples, and both absolute and prior-baseline-relative evidence. Noncritical failures hold progression; invalid evidence and critical failures recommend rollback. Unauthorized admin access, expired-token acceptance, duplicate payment processing, unverified entitlement grants, and unsupported active entitlements are mandatory zero-tolerance rollback signals.

The package has no traffic, deployment, incident, or rollback executor. Runtime evidence collection, last-known-good correlation, and typed rollback authorization remain future integration work.

## OPS-014 entitlement reconciliation foundation

`internal/reconciliation` consumes already-fetched provider snapshots and deterministically compares them with stored subscription/grant state. It performs no provider or network calls. PostgreSQL serializable transactions record immutable run, item, and correction evidence; stable correction identities deduplicate effects across scheduled runs. Corrections require the exact provider, environment, subscription, provider-subscription, and subscription-sourced grant scope. Provider failures freeze only new grants in the affected scope; existing verified grants remain readable.

Migration `000041` adds the append-only reconciliation ledger, correction-effect ledger, scoped new-grant freezes, and grant verification metadata. The engine is not scheduled by `main`; continuous provider-backed reconciliation remains `OPS-037` and requires authorized provider integration plus operational alerting.

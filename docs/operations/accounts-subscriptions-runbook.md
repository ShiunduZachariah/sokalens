# SokaLens accounts, recommendations, and subscriptions runbook

This implementation is a progressive-access MVP. Public fixtures, top predictions, 1X2 probabilities, and basic goals signals remain public. Guest deep analysis is limited by a signed first-party visitor session. Verified Basic accounts receive standard analysis, favorites, recommendations, and a digest entitlement. Active Pro subscriptions add advanced analysis, real-time notifications, expanded history, and server-authorized ad suppression.

## Local startup

1. Copy `.env.example` to `.env` and replace `APP_SECRET` with a long random value.
2. Start PostgreSQL and Redis:

   `docker compose --env-file .env -f infra/docker/docker-compose.yml --profile core up -d postgres redis`

3. Apply migrations and seed configuration:

   `go run ./services/api/cmd/dbtool -env-file .env -mode up -migrations infra/db/migrations`

   `go run ./services/api/cmd/dbtool -env-file .env -mode seed -seed infra/db/seeds/001_foundation_seed.sql`

4. Start or rebuild API and worker:

   `docker compose --env-file .env -f infra/docker/docker-compose.yml --profile core up -d --build api worker`

5. Run the web applications from `apps/web` and `apps/admin`, or start the Compose `frontend` profile.

Normal public requests read PostgreSQL or prepared recommendation rows only. Football provider calls, model execution, billing reconciliation, recommendation candidate generation, and notification delivery run in worker paths.

## Required environment variables

- `APP_SECRET`: session/guest signing and admin MFA-secret encryption root. Required outside local.
- `PUBLIC_WEB_URL`: absolute public URL used in one-time email links.
- `GUEST_ANALYSIS_VIEW_LIMIT`, `GUEST_ANALYSIS_WINDOW`: rolling anonymous detailed-view allowance.
- `USER_SESSION_TTL`, `ADMIN_SESSION_TTL`, `AUTH_TOKEN_TTL`: account security lifetimes.
- `BILLING_MODE`: `disabled`, `sandbox`, or `live`. Keep disabled until a provider adapter is configured.
- `BILLING_PROVIDER`, `BILLING_WEBHOOK_SECRET`: provider inbox identity and HMAC verification secret.
- `EMAIL_PROVIDER`: `log`, `mailtrap`, `smtp`, or `resend`. Use `log` only for local development.
- `EMAIL_FROM_ADDRESS`, `EMAIL_FROM_NAME`, `EMAIL_REPLY_TO`: sender identity shared by every transport.
- `MAILTRAP_SMTP_HOST`, `MAILTRAP_SMTP_PORT`, `MAILTRAP_SMTP_USERNAME`, `MAILTRAP_SMTP_PASSWORD`, `MAILTRAP_SMTP_ENCRYPTION`: Mailtrap sandbox SMTP settings.
- `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_ENCRYPTION`: generic SMTP settings. The legacy `SMTP_ADDRESS` and `SMTP_FROM` aliases remain supported during migration.
- `RESEND_API_KEY`, `RESEND_API_BASE_URL`: Resend server-side transport settings. Never expose the key through a `NEXT_PUBLIC_` variable.
- `SEED_ADMIN_EMAIL`, `SEED_ADMIN_PASSWORD`, `SEED_BASIC_EMAIL`, `SEED_BASIC_PASSWORD`: optional, local-only inputs for the idempotent seed command. Keep them out of committed files and secret managers scoped to public clients.
- `RECOMMENDATION_SCHEDULE_INTERVAL`, `NOTIFICATION_BATCH_SIZE`, `NOTIFICATION_CONCURRENCY`: background throughput controls.

Subscription reminder offsets are data-configured through `app_config.billing.renewal_reminder_days` (default `7,3,1`) so changing the schedule does not require rebuilding the worker. Notification quiet hours use the user's stored IANA timezone; UTC remains authoritative for matches, billing periods, and delivery records.

Prices are not hardcoded in application code. Add or synchronize active `plan_prices` rows for the selected provider and environment before enabling checkout. A `sandbox` checkout adapter is available for local flow testing; a production payment-provider adapter is still required.

The seed data configures the current public offer as USD 1.99 monthly and USD 20 yearly, with the calculated yearly saving exposed as approximately 16%. Prices, labels, provider identifiers, and plan entitlements remain editable through the restricted admin plan-management screen and are read from the database rather than application constants.

## Local email and account verification

Run `task account:seed` after migrations to converge the configured Basic and Super Admin accounts. Existing account passwords are intentionally not overwritten. The generated administrator TOTP enrollment URI is written once to the ignored `.local/seed-admin-totp-uri.txt` file with owner-only permissions; it is never printed in normal command output.

Run `task email:test` to send a transport smoke message to `EMAIL_TEST_RECIPIENT`. The command reports only the selected transport, acceptance state, and safe message identifier. Verification, reset, and welcome messages are inserted into the notification outbox and sent by workers. Their payload is AES-GCM encrypted at rest with `APP_SECRET`, so raw email addresses and one-time tokens are not present in outbox JSON.

## Secure admin provisioning

Admin accounts have no public registration path and password-only admin login is rejected. Provision one from a trusted shell:

`$env:DATABASE_URL='postgres://...'; $env:APP_SECRET='...'; $env:ADMIN_BOOTSTRAP_PASSWORD='a-unique-14+-character-password'; go run ./services/api/cmd/adminctl -email admin@example.com -role super_admin`

The command prints a TOTP URI once. Enroll it immediately, then sign in at `/login` on the admin application with email, password, and current six-digit TOTP code. Supported roles are `super_admin`, `support_admin`, `content_admin`, `billing_admin`, and `analyst`.

## Billing webhook contract

The provider-facing API accepts the canonical adapter envelope at `POST /v1/billing/webhooks` with `X-Event-ID`, `X-Event-Type`, `X-Event-Time`, and `X-Signature: sha256=<hex HMAC-SHA256 of the raw body>`. It persists only verified events. Workers claim them with `FOR UPDATE SKIP LOCKED`, serialize each subscription aggregate, ignore duplicate/out-of-order revisions, refresh entitlement snapshots transactionally, and enqueue notifications.

The canonical JSON body contains `user_id`, `plan_key`, `provider_subscription_id`, `status`, provider `current_period_start` and `current_period_end` UTC timestamps, `cancel_at_period_end`, `revision`, and `effective_at`. Dispute events may include a `dispute` object. An open chargeback should carry subscription status `suspended`; this revokes Pro entitlements while preserving the account, webhook evidence, dispute record, and audit event for manual review.

## Security and privacy notes

- Passwords use Argon2id; session, verification, and reset secrets are stored only as digests.
- The minimum password length is eight characters. Password inputs expose an accessible show/hide control, and the backend intentionally applies no low maximum-length truncation.
- User mutations use a double-submit CSRF token and SameSite cookies. Production cookies are Secure and authentication cookies are HttpOnly.
- Registration records versioned Terms, Privacy, Cookie, responsible-use, and prediction-disclaimer acceptance. Marketing consent is separate and optional.
- Guest identities are signed, first-party, and deliberately not an absolute cross-device fingerprint. Clearing all storage can create a new identity; risk signals and rate limits reduce abuse but cannot identify every VPN/private-browser visitor with certainty.
- Notification and recommendation workers use bounded concurrency, idempotent outbox/candidate records, leases, retries, and deduplication.
- Draft legal pages and document version `2026-08-05` require qualified professional review before production.

## Verification

Run:

- `go test ./services/api/...`
- `go test ./services/worker/...`
- `go vet ./services/api/... ./services/worker/...`
- `npm run lint && npm test && npm run build` in both `apps/web` and `apps/admin`
- `npm --prefix packages/contracts run validate`
- `docker compose --env-file .env -f infra/docker/docker-compose.yml config --quiet`
- `node apps/web/scripts/load-smoke.mjs http://127.0.0.1:3100` against an optimized `next start` process
- `docker run --rm -e WEB_BASE_URL=http://host.docker.internal:3100 -e WEB_LOAD_VUS=4 -e WEB_LOAD_DURATION=20s -v "${PWD}/tests/load:/scripts:ro" grafana/k6:latest run /scripts/public-web-smoke.js`

Use `tests/load/account-api-slo.js` for the account/read SLO load gate and `tests/load/public-api-slo.js` for existing public endpoints. The target is p95 below three seconds and HTTP/schema failure rate below 0.5%. Restricted administrators can inspect the current-process bounded metrics at `/diagnostics`; the underlying `/v1/admin/observability/metrics` and `/v1/admin/observability/diagnostics` endpoints require the analytics permission and expose no credentials, raw URLs, or request bodies.

The account foundation is introduced by migrations `000024` through `000029`. Apply all of them before starting the account API or worker. Migration `000029` adds the managed price configuration and seed-account support used by the current pricing and local verification flows.

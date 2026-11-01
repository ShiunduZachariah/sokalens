# ADR 0005: Default-proceed operations with an independent trust root

- Status: accepted
- Date: 2026-08-14
- Owners: platform, security, billing, and operations maintainers
- Amends: [ADR 0003](0003-operations-autonomy-modes.md)

## Context

Routine manual approval for every authentication, payment, deployment, or controller change does not scale and is not the primary safety mechanism. Safety must be executable and repeatable while preserving ADR 0003's fail-closed runtime authority boundary. Payment state also requires continuous verification: signed, idempotent webhooks are necessary but do not prove that internal entitlements still agree with the provider.

## Decision

Within an already authorized autonomy mode, validated releases proceed by default. Risky components are made safe through executable invariants, continuous reconciliation, bounded canaries, independent verification, immutable versioning, automatic rollback, and a small independent trust root. Absence of an owner response is not a deployment hold.

Human authority is required only for actions outside the repository's existing authority: new credentials or paid resources, provider KYC or merchant/bank changes, subscription pricing, legal/compliance decisions, irreversible bulk actions, and expansion of an autonomy mode's configured production authority. Default-proceed never lets a release expand its own authority.

### Permanent security invariants

A stable, hand-maintained auth/security battery is a non-negotiable merge invariant for changes to authentication, sessions, entitlements, subscriptions, middleware, RBAC, or admin authorization. It proves unauthenticated, Basic, cross-user, disabled-account, expired/revoked/rotated-token, admin-MFA, rate-limit, CSRF, CORS, cookie, and signature boundaries. Failure blocks merge without discretionary review.

### Entitlement reconciliation

A deterministic reconciliation job runs every 15 minutes initially, with configuration controlling cadence. It compares provider-verified subscriptions with internal active Pro entitlements and classifies pending payment, grace, renewal, refund, chargeback, cancellation, retry, and expiry states.

An unexplained material mismatch raises `ENTITLEMENT_RECONCILIATION_FAILED`, freezes only new entitlement grants in the affected scope, verifies affected subscription IDs with the provider, safely corrects or revokes unsupported grants, and alerts the owner. It must not freeze unrelated verified subscriptions. Each run records provider, internal, matched, missing, corrected, and revoked counts plus timestamps and result. Provider billing state remains externally authoritative; PostgreSQL remains the operational source of truth.

### Canary and business-invariant gate

Auth, session, subscription, entitlement, payment, and critical-state changes use configurable progressive traffic stages, initially 5%, 25%, and 100%. Each observation window compares the candidate with both the prior-version baseline and absolute limits for errors, latency, auth behavior, webhook processing, reconciliation, database failures, resource use, and business invariants.

Critical invariants include zero unauthorized admin access, accepted expired tokens, duplicate payment processing, entitlement grants without a verified event, and unsupported active entitlements. Passing candidates auto-promote; critical failures auto-rollback and create an incident. No operator needs to watch the canary.

### Independent trust root

The trust root is deliberately small and outside ordinary controller self-modification:

- watchdog recovery and last-known-good rollback;
- promotion, version selection, and canary decision logic;
- candidate health validation;
- an immutable test-harness launcher and hand-written controller trust suite.

Neither the active nor candidate controller may alter or bypass its own promotion criteria. Trust-root changes use a separate pipeline, fixed tests, independent CI and Codex QA, external validation, a scheduled default-yes promotion, and retention of the prior known-good version.

Normal controller changes and trust-harness changes cannot share an autonomous pull request. Updating the fixed harness requires the independent trust-root maintenance path.

Controller candidates run separately against deterministic synthetic incidents before bounded shadow/canary control. The trust suite must pass 100%, including alert validation/deduplication, incident concurrency and priority, Redis/PostgreSQL recovery, deployment regression and idempotent rollback, GitHub dispatch, AI failure/budget exhaustion, malformed/signature-invalid webhooks, and secret redaction. A configurable 30-60 minute soak auto-promotes a healthy candidate.

Every deployable records `current_sha`, `previous_sha`, `last_known_good_sha`, and `candidate_sha`; production uses immutable digest or Git-SHA images and retains rollback versions.

### Maintenance loop

Codex performs a configurable bimonthly architecture and maintenance review of the controller, watchdog, dependencies, security advisories, integrations, observability, queues, incidents, rollback history, resource efficiency, AI budgets, technical debt, and trust-root weaknesses. Recommendations are classified:

- `REQUIRED`: security, compatibility, correctness, or reliability work;
- `RECOMMENDED`: performance, maintainability, or observability work;
- `OPTIONAL`: capability or architecture expansion.

Within policy and budget, REQUIRED and RECOMMENDED work may flow through DeepSeek implementation, deterministic tests, Codex QA, candidate isolation, soak, and automatic promotion. OPTIONAL work creates backlog by default.

## Failure behavior

- Missing evidence, failed security/trust tests, ambiguous reconciliation, stale baseline, unavailable rollback artifact, or failed audit write prevents promotion or mutation.
- A canary critical-invariant failure rolls back to the recorded known-good artifact and creates an incident.
- Trust-root maintenance failure retains the current trust root and prior known-good version.
- Exhausted autonomous recovery creates an action-required incident; it does not weaken policy or increase authority.

## Consequences

Auth, payment, and controller code can deploy autonomously after fixed gates instead of waiting for routine approval. The design adds reconciliation, candidate infrastructure, a separate trust-root maintenance path, and durable version evidence. The control plane remains small and deterministic: Go, PostgreSQL, Redis acceleration, bounded workers, state machines, and external AI.

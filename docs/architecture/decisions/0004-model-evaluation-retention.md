# ADR 0004: Separate public, audit, model-evaluation, and provider retention

- Status: accepted
- Date: 2026-08-14
- Owners: prediction, data, privacy, and platform maintainers
- Related: [ADR 0001](0001-canonical-prediction-storage.md)

## Context

Public raw result detail is intentionally short-lived, while daily accuracy aggregates support long-lived product reporting. Neither is sufficient to reproduce calibration, lifecycle stability, champion/challenger comparisons, or drift decisions. Conversely, retaining full provider payloads or user data indefinitely for model evaluation is unnecessary and creates licensing and privacy risk.

## Decision

Use four distinct retention classes with separate deletion rules:

| Class | Purpose | Minimum fields | Retention rule |
|---|---|---|---|
| public result detail | recent user-facing result history and dispute support | published prediction reference, outcome/settlement, public context | short, configurable product window; current policy remains authoritative |
| durable accuracy aggregates | public reporting and trend summaries | date/scope counts, correct/incorrect/void, published denominator | durable; contains no recoverable raw provider payload or user activity |
| `model_evaluation_facts` | calibration, backtest, lifecycle, drift, and promotion evidence | immutable prediction/model/fixture/market/stage/cutoff/version/probability/confidence/outcome/odds-reference/eligibility facts | durable for the documented model-governance horizon; deletion requires governance audit and must not be coupled to public-detail purge |
| raw provider data | replay inputs only where licensed and necessary | provider payload plus source-observed, available, and ingested timestamps | provider-, field-, and jurisdiction-specific window; licensing controls override modeling convenience |

`model_evaluation_facts` is compact and immutable. It must not contain user identifiers, search/activity history, notification data, secrets, raw provider payloads, or free-form logs. It references the exact `engine_predictions` and model-registry rows and stores only the facts required to reproduce inclusion and metrics.

## Write and deletion semantics

1. A settled prediction produces at most one evaluation fact using a stable unique key. Duplicate settlement is idempotent.
2. The fact records final outcome, settlement classification, evaluation eligibility, and an explicit exclusion reason; excluded examples are not silently dropped.
3. The fact preserves the prediction-time cutoff and lifecycle stage. Later fixture/provider corrections do not rewrite the original feature boundary; corrections append audited superseding evaluation state only where the schema explicitly supports it.
4. Public-detail purge must not cascade into evaluation facts. Foreign keys use retention-compatible restriction or stable identifiers.
5. Aggregate rebuilds read the authoritative ledger but do not replace it.
6. Raw provider deletion must leave enough non-proprietary provenance to explain why a fact was eligible, while respecting the provider license.
7. Retention jobs are server-side, bounded, observable, restore-safe, and audit row counts and policy version.

## Access and protection

- Public APIs never expose the evaluation ledger directly.
- Training, calibration, drift, and admin reporting use least-privilege read roles.
- Backups inherit the class's retention and deletion obligations; restore procedures prevent expired data from silently returning to service.
- Provider licensing, privacy requirements, and legal holds are configuration/policy inputs, not hardcoded exceptions.

## Validation gates

- Settlement duplication and concurrent settlement create exactly one fact.
- Public-detail purge leaves eligible evaluation facts and aggregates intact.
- User/account deletion cannot be reconstructed from model facts because user identifiers are absent.
- Temporal-leakage tests prove every fact references data available at its recorded cutoff.
- Calibration/promotion reports reproduce from a frozen fact manifest and record exclusions.
- Restore-and-repurge tests enforce expired raw/public data after backup recovery.

## Consequences

Model governance becomes reproducible without turning public or provider storage into an indefinite archive. Storage policies are more explicit and require coordinated purge, backup, and licensing tests.


# ADR 0001: Canonical prediction storage and migration ownership

- Status: accepted
- Date: 2026-08-14
- Owners: prediction engine and API maintainers
- Related: [`prediction-engine-architecture-v2.md`](../specifications/prediction-engine-architecture-v2.md), [`prediction-engine/runtime-decision.md`](../../prediction-engine/runtime-decision.md)

## Context

The v2 specification uses the generic name `predictions`. The running repository already has several prediction-related structures, including an older generic `predictions` table from the foundation/content workflow, but `engine_predictions` is the versioned, idempotent live engine-output contract. Python persists it, the Go API reads current unsuperseded rows, worker recommendation jobs read it, and `prediction_results` settles rows against it. Reusing the legacy table as a second engine source or creating another engine table would split history, current-row semantics, settlement, and public reads.

The repository also has one authoritative migration stream under `infra/db/migrations`. A service-local migration history would permit incompatible versions of a shared PostgreSQL schema.

## Decision

1. Every v2 reference to the core `predictions` table maps to the existing `engine_predictions` table.
2. `engine_predictions` remains the only canonical store for live-engine published and shadow candidate prediction rows. The existing legacy `predictions` table remains a separate compatibility/content structure until an explicitly reviewed retirement migration; v2 does not write model output to it. V2 extends `engine_predictions` additively with nullable columns when the value did not exist for v1 rows.
3. Python prediction-engine repository code is the owner of engine prediction writes. It must preserve content-hash idempotency, lifecycle versioning, supersession, point-in-time timestamps, and model identity.
4. Go API prediction, match, and analysis repositories are public read owners. They read prepared PostgreSQL data and must not invoke Python, a provider, or an AI system on a request path.
5. Go worker orchestration may schedule inference and read predictions for downstream prepared-data jobs. It does not reimplement model math or write a competing prediction schema.
6. Settlement writes `prediction_results` and, after its additive migration, one immutable `model_evaluation_facts` row in the same transaction or an idempotent transactionally-linked follow-up.
7. Model candidates, calibration artifacts, shadow settlement, and drift data use separate purpose-built tables linked to `engine_predictions`; they do not replace the canonical prediction row.
8. All database migrations, including future ops-controller tables, remain in `infra/db/migrations` and run through the existing migration runner. Services may own repository code and schema contract tests, not independent migration histories.
9. `python-engine-v1` model identity is resolved through the immutable, explicit `engine_prediction_market_family` mapping. Unsupported market names return no family and fail governed new writes; they are never assigned to a convenient default Champion.

## Reader and writer map

| Data | Authoritative writer | Readers | Constraint |
|---|---|---|---|
| `engine_predictions` | Python engine repository | Go API, worker recommendation/notification jobs, settlement/evaluation | one current row per fixture/market; versioned and auditable |
| `prediction_results` | settlement pipeline | public accuracy and internal evaluation | one idempotent settlement per prediction |
| `model_evaluation_facts` | settlement/evaluation transaction | calibration, backtest, drift, promotion | immutable compact facts; no public-retention cascade |
| model registry and artifacts | model governance workflow | engine, evaluation, admin reporting | exactly one production model per market family |
| shadow settlements | evaluation workflow | champion/challenger analysis | never public or best-bet eligible |

## Compatibility and rollout

- Add columns and tables in numbered central migrations; existing columns and rows stay valid.
- New v2 columns are nullable for legacy rows. `NULL` means the value was not produced, not that it should be fabricated.
- Build indexes concurrently where PostgreSQL transaction rules and the migration runner support it; otherwise stage index creation and measure lock impact on a restored production-shaped copy.
- Public readers continue selecting current unsuperseded rows until an explicitly reviewed projection replaces that contract.
- A rollback disables v2 writers/features while retaining additive columns and audit rows. It must not delete settled history.

## Consequences

This avoids dual truth and keeps public reads fast. Schema evolution needs coordination across Python writes, Go reads, settlement, and migrations. Generic upstream examples must be translated through this map before implementation.

# ADR 0003: Progressive operations autonomy modes

- Status: accepted
- Date: 2026-08-14
- Owners: platform and operations maintainers

## Context

An operations controller needs a precise authority boundary. A single autonomous switch would conflate observation, workflow creation, deterministic recovery, and AI-generated code. It would also let a controller failure increase blast radius.

## Decision

Autonomy is an ordered, configuration-controlled policy state. The modes are:

| Mode | Permitted behavior | Explicitly forbidden |
|---|---|---|
| `observe` | ingest signed alerts; deduplicate/correlate; collect bounded redacted evidence; record incidents and recommendations; notify | any production or repository mutation |
| `assist` | all observe behavior; create an issue or proposed workflow requiring approval | merge, deploy, rollback, restart, scale, cache mutation |
| `auto_rollback` | all assist behavior; deterministically roll back one recent correlated eligible deployment to recorded last-known-good | AI-selected target; schema rollback; unrelated or ambiguous rollback |
| `auto_heal` | all rollback behavior; execute approved typed restart, lease recovery, bounded scale, safe-cache, and backup-check operations | arbitrary shell; unbounded scaling; destructive cache/database operations |
| `auto_patch` | all heal behavior; dispatch bounded AI patch workflow through PR, deterministic CI, independent QA, staging, canary, and promotion | direct self-rewrite; direct push to protected branch; bypass of CI/QA/staging/canary |

The initial and fail-closed mode is `observe`.

## Authority and promotion rules

1. Mode configuration is external to the controller artifact, validated at startup, and recorded with actor, prior value, new value, reason, and timestamp.
2. A controller cannot change its own mode or deploy a configuration that expands its authority.
3. Promotion advances at most one level and requires the previous level's acceptance and fault-injection evidence. Demotion may be immediate.
4. Break-glass disable and manual rollback remain available outside the controller failure domain.
5. Every mutable operation is a typed allowlisted command with validated service/scope, idempotency key, deadline, concurrency lock, preconditions, postcondition check, and immutable audit result.
6. AI output is untrusted evidence or a schema-validated proposal. It is never an executable shell command and never receives production credentials or unredacted secrets.
7. Software deployment and model promotion are separate state machines. A healthy application release cannot promote a model, and an ops mode change cannot bypass model-governance gates.
8. PostgreSQL stores incidents, jobs, actions, deployments, authority changes, and audit history. Redis may accelerate dispatch/leases but loss of Redis cannot lose or authorize durable work.
9. Within an externally authorized mode, routine releases follow the default-proceed verification and independent-trust-root rules in [ADR 0005](0005-default-proceed-operations-and-independent-trust-root.md). A routine release does not require owner approval merely because it changes auth, payment, or controller code.
10. Human approval remains necessary when expanding configured production authority or exercising external business authority; a release and its controller cannot approve those changes themselves.

## Failure behavior

- Missing/invalid mode configuration, stale deployment correlation, ambiguous evidence, audit-write failure, unavailable last-known-good metadata, or expired leases fail closed.
- Controller restart reconstructs durable state from PostgreSQL and does not replay a completed idempotency key.
- Resource caps reserve capacity for critical incidents without starving normal work; no global sequential queue is introduced.
- Repeated failed mitigation opens a circuit and escalates to a human instead of recursively increasing authority.

## Acceptance evidence by promotion

- `observe`: alert validation, dedup storm, redaction, restart recovery, and proof of zero mutation.
- `assist`: issue/workflow sandbox tests and permission-boundary tests.
- `auto_rollback`: deterministic deployment correlation, concurrent rollback serialization, canary failure, and last-known-good restoration drills.
- `auto_heal`: service/queue/database/Redis fault matrix, bounded concurrency, and postcondition verification.
- `auto_patch`: budget exhaustion, invalid AI output, CI/QA rejection, staging failure, canary failure, controller self-update failure, and external watchdog recovery.

## Consequences

Useful automation arrives incrementally with auditable authority. Higher modes require operational evidence and infrastructure outside the controller. Some incidents remain action-required by design.

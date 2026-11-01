# Architecture decision records

Accepted decisions are binding repository-specific interpretations of the reviewed architecture specifications.

| ADR | Decision |
|---|---|
| [0001](0001-canonical-prediction-storage.md) | V2 extends canonical `engine_predictions`; central migrations remain authoritative |
| [0002](0002-python-inference-runtime-exception.md) | Retain the bounded Go-worker-to-Python prepared-data inference exception temporarily |
| [0003](0003-operations-autonomy-modes.md) | Promote audited autonomy through `observe`, `assist`, `auto_rollback`, `auto_heal`, and `auto_patch` |
| [0004](0004-model-evaluation-retention.md) | Keep public, aggregate, compact evaluation, and raw-provider retention classes separate |
| [0005](0005-default-proceed-operations-and-independent-trust-root.md) | Default-proceed validated releases using reconciliation, executable invariants, canaries, rollback, and an independent trust root |
| [0006](0006-public-locale-routing-and-preference.md) | Use one allowlisted locale registry for public URLs, SEO, RTL, guest choice, and account preference |
| [0007](0007-public-inplay-predictions.md) | Publish in-play predictions without login while preserving data safeguards and private-route authorization |

New implementation work must cite the applicable decision when a generic source specification conflicts with the running repository. Superseded ADRs remain in this directory with their status changed; they are not deleted.

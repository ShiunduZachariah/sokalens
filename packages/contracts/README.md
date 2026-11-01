# API Contracts Boundary

Phase 1 starts `packages/contracts` with `openapi.yaml` for health, readiness, Scalar API docs, mobile config, neutral insight/watchlist schema placeholders, subscription entitlement placeholders, and the admin auth shell.

The Go API serves this contract at `/openapi.yaml` and renders Scalar API Reference at `/docs`.

Next contracts should cover public match/prediction reads, provider status, ad placements, account deletion, notification preferences, insight watchlist APIs, insight alert APIs, and entitlement checks in their proper phases.

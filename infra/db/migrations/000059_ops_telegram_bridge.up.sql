-- Durable state for the outbound-only Telegram ops bridge
-- (docs/ai/telegram-ops-bridge-plan.md, phase T1). Additive only: new tables
-- and nullable columns. Nothing here weakens an existing constraint, and no
-- statement in this file registers an HTTP surface.
--
-- The bridge is Postgres-authoritative. The poller may not advance a Telegram
-- offset it has not durably written, so every piece of state the loop depends
-- on lives in these tables rather than in process memory.

-- Exactly one row, ever. The bridge is a singleton consumer of one bot token:
-- a second offset row would mean a second consumer, which Telegram answers
-- with a 409 rather than a split stream. The CHECK makes that structural.
CREATE TABLE ops_telegram_state (
  id SMALLINT PRIMARY KEY DEFAULT 1,
  last_offset BIGINT NOT NULL DEFAULT 0,
  last_poll_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (id = 1),
  CHECK (last_offset >= 0)
);

-- Seed the singleton. ON CONFLICT DO NOTHING keeps the migration re-runnable
-- and keeps a rollback-then-reapply cycle from clobbering a live offset.
INSERT INTO ops_telegram_state (id, last_offset)
VALUES (1, 0)
ON CONFLICT (id) DO NOTHING;

-- Forensic ledger of every update the bridge has seen, authorized or not.
-- update_id is the primary key and that IS the replay defence: Telegram
-- redelivers an update until the offset advances past it, and a redelivered
-- update must become a no-op rather than a second execution. Authorization
-- outcome is recorded here even when nothing is sent back, because silence
-- towards an unauthorized sender is deliberate and must still leave evidence.
CREATE TABLE ops_telegram_updates (
  update_id BIGINT PRIMARY KEY,
  chat_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  username TEXT,
  command TEXT,
  raw_text TEXT,
  authorized BOOLEAN NOT NULL,
  received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  processed_at TIMESTAMPTZ,
  error TEXT,
  CHECK (update_id > 0),
  CHECK (processed_at IS NULL OR processed_at >= received_at),
  CHECK (error IS NULL OR btrim(error) <> '')
);

CREATE INDEX idx_ops_telegram_updates_received
  ON ops_telegram_updates (received_at DESC);

-- Supports the §8 abuse threshold ("more than 5 rejections in 10 minutes"),
-- which is a bounded scan over a rare predicate. The partial index keeps that
-- count off the full ledger as it grows.
CREATE INDEX idx_ops_telegram_updates_unauthorized_recent
  ON ops_telegram_updates (received_at DESC)
  WHERE NOT authorized;

-- Two-phase confirmation for mutating runbook commands. A token is single use
-- (consumed_at), short lived (expires_at), and bound to the requesting user
-- and chat so a token observed in a group cannot be redeemed by someone else.
--
-- job_id is UUID, not BIGINT as the source spec assumes: ops_jobs.id is a UUID
-- (migration 000035). NO ACTION on the reference is intentional; a confirmation
-- record is evidence and must not vanish with the job it dispatched.
CREATE TABLE ops_pending_confirmations (
  token TEXT PRIMARY KEY,
  runbook TEXT NOT NULL,
  args JSONB NOT NULL DEFAULT '{}'::jsonb,
  requested_by BIGINT NOT NULL,
  chat_id BIGINT NOT NULL,
  job_id UUID REFERENCES ops_jobs(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  consumed_at TIMESTAMPTZ,
  CHECK (btrim(token) <> ''),
  CHECK (btrim(runbook) <> ''),
  CHECK (jsonb_typeof(args) = 'object'),
  CHECK (expires_at > created_at),
  CHECK (consumed_at IS NULL OR consumed_at >= created_at)
);

-- Redemption and expiry sweeps both read only outstanding tokens, which are a
-- tiny minority of the table once the bridge has been running.
CREATE INDEX idx_ops_pending_confirmations_expiry
  ON ops_pending_confirmations (expires_at)
  WHERE consumed_at IS NULL;

-- Batched release trains (phase T4). PRs accumulate against a train until the
-- cap or the window is reached; HELD is a distinct terminal-ish state so a
-- hold rolls forward into the next window instead of being lost.
CREATE TABLE ops_release_trains (
  id BIGSERIAL PRIMARY KEY,
  status TEXT NOT NULL,
  pr_numbers INTEGER[] NOT NULL DEFAULT '{}',
  head_sha TEXT,
  base_sha TEXT,
  window_at TIMESTAMPTZ,
  deployed_at TIMESTAMPTZ,
  rolled_back_at TIMESTAMPTZ,
  held_reason TEXT,
  held_by BIGINT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (status IN ('PENDING', 'DEPLOYING', 'DEPLOYED', 'ROLLED_BACK', 'HELD')),
  CHECK (held_reason IS NULL OR btrim(held_reason) <> ''),
  CHECK (head_sha IS NULL OR btrim(head_sha) <> ''),
  CHECK (base_sha IS NULL OR btrim(base_sha) <> ''),
  -- A train can only be rolled back after it was deployed. Ordering is not
  -- asserted between window_at and deployed_at: a train that fills to its PR
  -- cap deploys early, ahead of its window.
  CHECK (rolled_back_at IS NULL OR (deployed_at IS NOT NULL AND rolled_back_at >= deployed_at))
);

CREATE INDEX idx_ops_release_trains_status_window
  ON ops_release_trains (status, window_at);

-- Deployment classification, additive. Existing rows keep NULL deploy_class:
-- unclassified, not misclassified. The CHECK is NOT VALID for the same reason
-- migration 000040 used NOT VALID on ops_jobs — ops_deployments is live, and a
-- validating ADD CONSTRAINT takes a lock while it scans every existing row.
-- New writes are still checked; the backfill and VALIDATE CONSTRAINT are a
-- separate, non-blocking follow-up.
ALTER TABLE ops_deployments
  ADD COLUMN IF NOT EXISTS deploy_class TEXT,
  ADD COLUMN IF NOT EXISTS train_id BIGINT,
  ADD COLUMN IF NOT EXISTS train_pr_numbers INTEGER[];

ALTER TABLE ops_deployments
  ADD CONSTRAINT ops_deployments_deploy_class_check
    CHECK (deploy_class IS NULL OR deploy_class IN ('IMMEDIATE', 'BATCHED')) NOT VALID;

-- Short human reference for an incident. ops_incidents.id is a UUID, which is
-- unusable from a phone: /incident 143 has nothing to bind to, and the /issue
-- body template's "#143" would be fiction. This column is the "Decision 1,
-- Option A" of the plan.
--
-- Two properties of this statement, deliberately accepted:
--
--   1. Unlike the ops_deployments columns above, an identity column is
--      implicitly NOT NULL, so this ADD COLUMN rewrites the table under an
--      ACCESS EXCLUSIVE lock and backfills every existing row from the new
--      sequence. That is acceptable here only because ops_incidents is small:
--      the controller has been writing incidents for a matter of weeks, the
--      table is a few tens of kilobytes, and the rewrite is sub-second. If
--      this table ever reaches millions of rows, do NOT repeat this pattern —
--      add a nullable BIGINT plus a sequence default and backfill in batches.
--
--   2. ops_incidents carries no reject_ops_immutable_mutation() trigger. That
--      function guards only ops_audit_events, ops_incident_observations and
--      ops_incident_transitions, all of which are append-only ledgers.
--      ops_incidents is mutable by design (status transitions, updated_at), so
--      this column does not collide with any immutability guarantee, and no
--      trigger had to be weakened to add it.
--
-- BY DEFAULT rather than ALWAYS so a restore or a backfill can supply explicit
-- references; the UNIQUE constraint is therefore load-bearing, not decorative.
ALTER TABLE ops_incidents
  ADD COLUMN IF NOT EXISTS reference BIGINT GENERATED BY DEFAULT AS IDENTITY;

ALTER TABLE ops_incidents
  ADD CONSTRAINT ops_incidents_reference_key UNIQUE (reference);

COMMENT ON TABLE ops_telegram_state IS
  'Singleton Telegram long-poll cursor. One row enforced by CHECK (id = 1); the offset is authoritative and must be durable before it advances.';
COMMENT ON TABLE ops_telegram_updates IS
  'Forensic ledger of received Telegram updates. The update_id primary key is the replay defence: a redelivered update is a duplicate insert, not a second execution.';
COMMENT ON TABLE ops_pending_confirmations IS
  'Single-use, short-lived, user-bound confirmation tokens for mutating runbook commands.';
COMMENT ON TABLE ops_release_trains IS
  'Batched release trains. Deployment of a train must emit its own immutable ops_audit_events row.';
COMMENT ON COLUMN ops_deployments.deploy_class IS
  'IMMEDIATE or BATCHED. NULL means unclassified (pre-bridge deployment), never misclassified.';
COMMENT ON COLUMN ops_deployments.train_id IS
  'Originating ops_release_trains.id for a BATCHED deployment. Intentionally not a foreign key: deployment evidence outlives train bookkeeping.';
COMMENT ON COLUMN ops_deployments.train_pr_numbers IS
  'Snapshot of the train PR list at deploy time, so the deployment record stays readable after the train row changes.';
COMMENT ON COLUMN ops_incidents.reference IS
  'Short human incident reference (#143) for phone-first commands and issue templates. ops_incidents.id remains the authoritative UUID identity.';

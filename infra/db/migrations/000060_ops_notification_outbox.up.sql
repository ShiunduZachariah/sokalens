-- Outbound notification outbox for the ops controller
-- (docs/ai/telegram-ops-bridge-plan.md, phase T2). Additive only: one new
-- table and its indexes. No existing table, constraint or trigger is touched,
-- and no statement here registers an HTTP surface.
--
-- NUMBERING: the plan named this migration 000058. That number was taken by
-- 000058_email_delivery_observability while T2 was being written, so this file
-- is 000059. Two files sharing a version is a hard error in golang-migrate,
-- not a cosmetic clash.
--
-- WHY THIS TABLE EXISTS AT ALL. The source spec (§12) says to "register
-- Telegram as a transport with the existing notification worker". There is no
-- such worker in this service: internal/alerts is an *inbound* HMAC validator
-- with a Sink interface, and there is no ops_notifications table (recon A5).
-- So the outbox is the worker's durable queue, built here rather than assumed.
--
-- The load-bearing property is that a notification is never lost. A row leaves
-- 'pending' only by being delivered or by exhausting its attempts into
-- 'failed', which is itself a durable, queryable state. Nothing deletes rows.

CREATE TABLE ops_notification_outbox (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

  -- Tier drives routing, not severity. The mapping from ops_incidents.severity
  -- (info|warning|critical) happens in Go, once, so the two vocabularies never
  -- drift into each other.
  tier TEXT NOT NULL,
  subject TEXT NOT NULL,
  body TEXT NOT NULL,

  -- fingerprint is the incident fingerprint, reused verbatim. This service
  -- already deduplicates and correlates on it in internal/incidents; carrying
  -- the same value here means the outbox consumes that identity instead of
  -- building a second, disagreeing correlation path.
  fingerprint TEXT NOT NULL,

  -- dedup_key is exactly-once-for-all-time, unlike the fingerprint index below
  -- which only collapses rows that are still open. The daily digest and the
  -- per-window suppression notice use it: re-running either must not produce a
  -- second message even years later.
  dedup_key TEXT UNIQUE,

  -- Incident reference, both halves. The UUID is the join key; the BIGINT is
  -- the short human reference added in 000059 (plan Decision 1), which is what
  -- a message can actually print as "#143". Both nullable: a notification need
  -- not originate from an incident.
  incident_id UUID REFERENCES ops_incidents(id),
  incident_reference BIGINT,

  -- How many correlated observations this one message stands for.
  occurrence_count BIGINT NOT NULL DEFAULT 1,

  status TEXT NOT NULL DEFAULT 'pending',

  -- Per-transport delivery state. transport names the transport that actually
  -- delivered, so a row that fell back to email is distinguishable after the
  -- fact from one Telegram carried.
  transport TEXT,
  delivered_at TIMESTAMPTZ,
  -- delivery_error survives a successful delivery. When Telegram fails and
  -- email succeeds the row is 'delivered' via 'email' AND still carries the
  -- Telegram error: the fallback must be visible, not silently papered over.
  delivery_error TEXT,
  attempts INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),

  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

  CHECK (tier IN ('info', 'important', 'action_required')),
  CHECK (status IN ('pending', 'digest', 'delivered', 'failed', 'suppressed')),
  CHECK (transport IS NULL OR transport IN ('telegram', 'email', 'digest')),
  CHECK (btrim(subject) <> ''),
  CHECK (btrim(fingerprint) <> ''),
  CHECK (dedup_key IS NULL OR btrim(dedup_key) <> ''),
  CHECK (occurrence_count >= 1),
  CHECK (attempts >= 0),
  CHECK (delivery_error IS NULL OR btrim(delivery_error) <> ''),
  CHECK (incident_reference IS NULL OR incident_reference > 0),

  -- Spec §12: INFO is never sent, it appears in the daily digest only. That is
  -- a routing rule in Go, but a routing rule can be edited by accident, so it
  -- is also structural here: an INFO row can never be in the state the claim
  -- query selects. An INFO notification is enqueued as 'digest' and reaches
  -- 'delivered' only through the digest.
  CHECK (tier <> 'info' OR status <> 'pending'),

  -- 'delivered' and delivered_at are the same fact stated twice; keep them
  -- from disagreeing.
  CHECK ((status = 'delivered') = (delivered_at IS NOT NULL)),
  CHECK (status <> 'delivered' OR transport IS NOT NULL),
  CHECK (delivered_at IS NULL OR delivered_at >= created_at),
  CHECK (updated_at >= created_at)
);

-- The claim query: pending work whose next attempt is due, oldest first. The
-- partial predicate keeps the index the size of the backlog rather than the
-- size of the history, which under an alert storm is the difference that
-- matters.
CREATE INDEX idx_ops_notification_outbox_claim
  ON ops_notification_outbox (next_attempt_at, created_at)
  WHERE status = 'pending';

-- Dedup by fingerprint while a notification is still open. Four alerts sharing
-- a root cause collapse onto one row here as well as in internal/incidents;
-- this index is the structural half of that guarantee, so a bug in the Go
-- caller degrades into a bumped occurrence_count rather than four messages.
-- 'digest' is inside the predicate deliberately: an undelivered INFO row is
-- still open and must absorb a correlated observation.
CREATE UNIQUE INDEX ux_ops_notification_outbox_open_fingerprint
  ON ops_notification_outbox (fingerprint)
  WHERE status IN ('pending', 'digest');

-- The digest reads the INFO backlog for one window.
CREATE INDEX idx_ops_notification_outbox_digest
  ON ops_notification_outbox (created_at)
  WHERE status = 'digest';

-- "What did we tell the operator about this incident?" is a question worth one
-- index; the column is null for most rows, so the partial predicate applies.
CREATE INDEX idx_ops_notification_outbox_incident
  ON ops_notification_outbox (incident_id)
  WHERE incident_id IS NOT NULL;

COMMENT ON TABLE ops_notification_outbox IS
  'Durable outbox for outbound ops notifications. A row leaves pending only by delivery or by exhausting attempts into failed; nothing deletes rows, which is what makes "a Telegram failure never loses a notification" true rather than aspirational.';
COMMENT ON COLUMN ops_notification_outbox.tier IS
  'info | important | action_required. INFO is never sent and is structurally unclaimable (see the tier/status CHECK); it appears in the daily digest only.';
COMMENT ON COLUMN ops_notification_outbox.fingerprint IS
  'Incident fingerprint, reused verbatim from internal/incidents so correlation has one owner rather than two.';
COMMENT ON COLUMN ops_notification_outbox.dedup_key IS
  'Exactly-once key for notifications that must never repeat at all (daily digest, per-window suppression notice). Unlike the open-fingerprint index this is unique for all time.';
COMMENT ON COLUMN ops_notification_outbox.transport IS
  'The transport that actually delivered: telegram, email (the fallback), or digest.';
COMMENT ON COLUMN ops_notification_outbox.delivery_error IS
  'Last delivery failure. Retained on a successful email fallback so the Telegram failure stays visible after the fact.';

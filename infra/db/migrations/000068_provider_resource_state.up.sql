-- Adaptive TTL (architecture v2 4.2).
--
-- One row per (provider, resource) recording when it was last fetched, whether
-- the response changed, and when it becomes eligible again. The fetch path
-- consults this before spending a request, so a 0-0 match with nothing
-- happening backs off while a 3-2 thriller keeps its floor interval.
--
-- This is a cache of scheduling decisions, not a source of truth: losing the
-- whole table costs one round of over-fetching and nothing else. Every column
-- is recomputable from the next response.

CREATE TABLE IF NOT EXISTS provider_resource_state (
  provider           TEXT        NOT NULL,
  -- Opaque to this table. Callers namespace it, e.g. 'fixture_stats:1234567'.
  resource_key       TEXT        NOT NULL,
  last_fetched_at    TIMESTAMPTZ NOT NULL,
  last_response_hash TEXT,
  unchanged_streak   SMALLINT    NOT NULL DEFAULT 0,
  current_interval_s INTEGER     NOT NULL,
  next_eligible_at   TIMESTAMPTZ NOT NULL,
  -- Terminal. A finished settled fixture, or history older than the retention
  -- horizon, can never change again, so it is never fetched again.
  immutable          BOOLEAN     NOT NULL DEFAULT FALSE,
  PRIMARY KEY (provider, resource_key),
  CONSTRAINT provider_resource_state_interval_positive CHECK (current_interval_s > 0),
  CONSTRAINT provider_resource_state_streak_non_negative CHECK (unchanged_streak >= 0)
);

-- The scheduler asks "what is due now", and the answer must never scan the
-- immutable rows -- those accumulate forever and are the majority of the table
-- within a season. Partial index, so they cost nothing to skip.
CREATE INDEX IF NOT EXISTS idx_provider_resource_state_due
  ON provider_resource_state (next_eligible_at)
  WHERE immutable = FALSE;

-- Freshness for live match state (in-play architecture §6).
--
-- THE DEFECT. A match reads "second half" three hours after kickoff. Nothing
-- in this system ticks a match minute forward, so the cause is the opposite of
-- a runaway clock: the fixture stops appearing in the provider's live feed, no
-- later observation contradicts what was last written, and matches.status
-- keeps saying 'second_half' indefinitely. The web app renders status_elapsed
-- faithfully and has no way to know the number is hours old.
--
-- It has no way to know because nothing records it. matches has kickoff_at and
-- created_at and no column anywhere that says when the live state was last
-- confirmed. Staleness is not merely unhandled today -- it is undetectable.
-- This column is the prerequisite for every other part of the fix.
--
-- ADDITIVE AND NULLABLE, per the existing migration policy. Every current row
-- gets NULL, which the Go side reads as "never confirmed" rather than as
-- "confirmed at the epoch". That distinction matters: treating NULL as an
-- ancient timestamp would declare the entire back catalogue stale on the first
-- deploy. Rows with no stamp are bounded by elapsed time since kickoff
-- instead, which is what catches the fixtures that are already stuck.
--
-- No existing column, constraint or trigger is touched, and nothing here
-- backfills. A backfill would have to invent a confirmation time, and an
-- invented freshness stamp is worse than an absent one: it would read as
-- authoritative.

ALTER TABLE matches
  ADD COLUMN IF NOT EXISTS live_state_at TIMESTAMPTZ;

COMMENT ON COLUMN matches.live_state_at IS
  'When a provider observation last confirmed this match''s live state (status, elapsed, score). NULL means never confirmed -- rows predating in-play architecture 6. Staleness is derived from this, never stored, so it cannot itself go stale.';

-- The reaper sweeps non-terminal matches past a plausible duration every two
-- minutes. Without an index that is a full scan of every match ever played,
-- several times an hour, forever.
--
-- The predicate is deliberately IMMUTABLE-safe: no now(), no CURRENT_DATE. A
-- partial index whose predicate depends on the current time is not something
-- Postgres will accept, and an index that silently stops matching as time
-- passes would be worse than none. Filtering on the small set of non-terminal
-- statuses is what makes it selective; the planner applies the kickoff_at
-- bound at query time.
CREATE INDEX IF NOT EXISTS idx_matches_non_terminal_kickoff
  ON matches (kickoff_at)
  WHERE status IN (
    'scheduled', 'upcoming', 'delayed',
    'live', 'halftime', 'second_half', 'extra_time', 'penalties', 'break',
    'interrupted', 'suspended'
  );

-- Ordering the in-play surface by staleness, and finding the rows the reaper
-- should look at first, both read live_state_at for matches currently claiming
-- to be in play.
CREATE INDEX IF NOT EXISTS idx_matches_in_play_live_state_at
  ON matches (live_state_at)
  WHERE status IN ('live', 'halftime', 'second_half', 'extra_time', 'penalties', 'break');

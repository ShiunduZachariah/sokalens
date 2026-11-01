-- Reverse of 000061_match_live_state_freshness. Drops only what the up file
-- created: one nullable column on matches and its two partial indexes.
--
-- Dropping the column discards recorded freshness, which is not recoverable by
-- re-running the up file -- the stamps are written by live observation, not
-- derived. The system degrades to its previous behaviour rather than breaking:
-- with no stamp, staleness falls back to the elapsed-time-since-kickoff bound.

DROP INDEX IF EXISTS idx_matches_in_play_live_state_at;
DROP INDEX IF EXISTS idx_matches_non_terminal_kickoff;

ALTER TABLE matches
  DROP COLUMN IF EXISTS live_state_at;

-- The ACCA readiness gate degraded to ~43s and rolled back every deploy. The
-- cause was not the query: the dedup scan reported 125,982 heap fetches against
-- 658,335 rows, meaning an "index only" scan was visiting the heap for ~19% of
-- rows because the visibility map was stale. A manual VACUUM (ANALYZE) cut the
-- whole statement from 43.0s to 9.3s and heap fetches to 2,351.
--
-- Left alone, that decays straight back. Both tables run on the defaults, and
-- autovacuum_vacuum_scale_factor = 0.2 means vacuum waits for a fifth of the
-- table to turn over first:
--
--   acca_slips      658k rows -> ~131,700 dead tuples before a vacuum
--   acca_slip_legs  6.0M rows -> ~1.2M dead tuples before a vacuum
--
-- Those thresholds assume a table that changes occasionally. These do the
-- opposite: the slip pool is regenerated on a 15-minute cadence, so rows are
-- superseded continuously and the map spends most of its life out of date --
-- which is precisely the state that makes the gate slow enough to time out.
--
-- 0.02 trades a little more background I/O for a visibility map that stays
-- current, which is the whole point: the gate reads these tables and nothing
-- else reads them this hard. The analyze factor drops in step so the planner
-- keeps seeing the real row counts rather than a fifth-stale estimate.
--
-- This reduces how often the gate goes slow. It does not stop the table
-- growing -- 92% of acca_slips is 'invalid' or 'expired' and nothing prunes it,
-- so the scan gets longer every day regardless of vacuum health. Retention on
-- non-active slips is the fix for that and is tracked separately; this change
-- buys the headroom to do it deliberately rather than under a broken deploy.

ALTER TABLE public.acca_slips SET (
  autovacuum_vacuum_scale_factor = 0.02,
  autovacuum_analyze_scale_factor = 0.01
);

ALTER TABLE public.acca_slip_legs SET (
  autovacuum_vacuum_scale_factor = 0.02,
  autovacuum_analyze_scale_factor = 0.01
);

-- Bounded settlement: a prediction may not stay pending forever.
--
-- Settlement only ever ran for fixtures whose status is ft/aet/pen/finished
-- and whose full-time goals are both present. A fixture that never reaches
-- that state -- the reaper parks it at `unknown`, the provider never supplies
-- a result, or it is postponed and never replayed -- produces no
-- `prediction_results` row at all, and the API renders "no row" as `pending`.
-- On 2026-09-21 production held 1,548 such fixtures and 18,197 current
-- predictions, the oldest kicked off on 2026-08-21.
--
-- The terminal state for those is `void`, which `settlement_status` already
-- permits. What was missing is *why*: a void written because draw-no-bet drew
-- is a graded outcome, and a void written because no result ever arrived is
-- an admission. They must not read the same, to an auditor or to a user.
--
-- `void_reason` is nullable and unconstrained by a CHECK on purpose. Existing
-- rows keep NULL, which means "void by market rule" -- the only kind that
-- existed before this migration.
ALTER TABLE prediction_results
  ADD COLUMN IF NOT EXISTS void_reason TEXT;

COMMENT ON COLUMN prediction_results.void_reason IS
  'Why a void settlement was written. NULL means the market rule voided it '
  '(a drawn draw-no-bet). Non-NULL means the expiry sweep terminated it: '
  'result_never_arrived (no confirmed result), fixture_not_played '
  '(postponed/cancelled/abandoned), settlement_unavailable (a result exists '
  'and settlement still produced nothing -- a defect, usually a market with '
  'no mapping).';

-- The expiry sweep looks for current predictions with no result row whose
-- fixture is long past kickoff. Without this it is a sequential scan of
-- engine_predictions on every run.
CREATE INDEX IF NOT EXISTS idx_prediction_results_void_reason
  ON prediction_results (void_reason, settled_at DESC)
  WHERE void_reason IS NOT NULL;

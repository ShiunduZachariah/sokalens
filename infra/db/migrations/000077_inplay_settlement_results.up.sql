-- In-play settlement results, and reversible settlement (in-play architecture
-- v2 §7.4, §14).
--
-- THE SAME DEFECT AS 000071, ONE TABLE OVER. Migration 000071 widened
-- `engine_predictions.lifecycle_stage` to the eight §7.4 stages, and a later run
-- widened the Python enum to match. `prediction_results` was missed both times:
-- its `lifecycle_stage_at_settlement` CHECK still names only the pre-match
-- three, so settling an in-play prediction fails at the database even when the
-- settlement logic is correct. A value the code can express and the column
-- rejects is a column that can never be written -- the same sentence 000071 was
-- written to end.
--
-- Verified against a fresh cluster with every migration applied before this one:
--
--   prediction_results_lifecycle_stage_at_settlement_check
--     CHECK (lifecycle_stage_at_settlement = ANY (ARRAY['EARLY','PRE_MATCH','LINEUP_CONFIRMED']))
--   prediction_results_probability_check
--     CHECK (probability >= 0.01 AND probability <= 0.99)
--   prediction_results_settlement_status_check
--     CHECK (settlement_status = ANY (ARRAY['settled','void','unsettled_data_unavailable']))
--
-- All three block in-play settlement. This migration addresses each, and adds
-- the two things reversible settlement needs.

-- 1. The lifecycle vocabulary, aligned with engine_predictions.
--
-- Named explicitly rather than dropped, because the constraint is the record of
-- which stages may reach settlement, and an unconstrained column would let a
-- typo through silently.
ALTER TABLE prediction_results
  DROP CONSTRAINT IF EXISTS prediction_results_lifecycle_stage_at_settlement_check;

ALTER TABLE prediction_results
  ADD CONSTRAINT prediction_results_lifecycle_stage_at_settlement_check
  CHECK (lifecycle_stage_at_settlement IN (
    'EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED',
    'IN_PLAY_1H', 'HALF_TIME', 'IN_PLAY_2H', 'EXTRA_TIME', 'FINAL'
  ));

-- 2. Probability bounds, widened for in-play only.
--
-- The 0.01-0.99 band is a real pre-match invariant and is kept for pre-match
-- rows: a pre-match model claiming 99.5% is far more likely to be broken than
-- right. In play it is simply wrong. A 1X2 on a three-goal lead with five
-- minutes left genuinely exceeds 0.99, and it is not a determined market -- a
-- comeback remains possible, `publishable_markets` still publishes it, and the
-- old CHECK would reject the settlement of a prediction we had already shown a
-- paying customer.
--
-- Exclusive of 0 and 1 rather than inclusive: `publishable_markets` drops
-- markets whose selections have collapsed onto certainty, so a published
-- in-play probability is never exactly 0 or 1, and a row that claims otherwise
-- is a bug worth failing on.
ALTER TABLE prediction_results
  DROP CONSTRAINT IF EXISTS prediction_results_probability_check;

ALTER TABLE prediction_results
  ADD CONSTRAINT prediction_results_probability_check
  CHECK (
    CASE
      WHEN lifecycle_stage_at_settlement IN ('EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED')
        THEN probability >= 0.01 AND probability <= 0.99
      ELSE probability > 0 AND probability < 1
    END
  );

-- 2b. The same band on engine_predictions, which is upstream of everything else.
--
-- Found while proving the above end to end: `engine_predictions.probability`
-- carries its own identical CHECK from 000014, so an in-play market outside
-- 0.01-0.99 cannot be *written* at all, never mind settled.
-- `EnginePrediction.validate()` applies the same flat band in Python and rejects
-- the row before it reaches the database.
--
-- This is the most consequential of the four, because it suppresses exactly the
-- calls the model is most confident about: a 1X2 on a three-goal lead at 85
-- minutes is both the clearest read of the match and the one a flat 0.99 ceiling
-- throws away. Widened the same way, for in-play stages only.
ALTER TABLE engine_predictions
  DROP CONSTRAINT IF EXISTS engine_predictions_probability_check;

ALTER TABLE engine_predictions
  ADD CONSTRAINT engine_predictions_probability_check
  CHECK (
    CASE
      WHEN lifecycle_stage IN ('EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED')
        THEN probability >= 0.01 AND probability <= 0.99
      ELSE probability > 0 AND probability < 1
    END
  );

-- 3. The reversal state.
--
-- §14 and the 2026-09-29 operator decision: the provider is the source of
-- truth, and a withdrawn goal must be shown as withdrawn however long it takes.
-- Settlement is therefore immediate and reversible rather than delayed.
ALTER TABLE prediction_results
  DROP CONSTRAINT IF EXISTS prediction_results_settlement_status_check;

ALTER TABLE prediction_results
  ADD CONSTRAINT prediction_results_settlement_status_check
  CHECK (settlement_status IN (
    'settled', 'void', 'unsettled_data_unavailable', 'settlement_reversed'
  ));

-- 4. Which events decided a settlement.
--
-- Stored as `identity_key` values, not surrogate ids, for two reasons. The
-- provider sends no event id at all, so `identity_key` -- derived from fixture,
-- minute, extra, type, detail, team and player -- is the only stable identity a
-- row has; and `fixture_events` is partitioned on `observed_at`, so its primary
-- key is (id, observed_at) and a foreign key to it would have to carry the
-- partition column. Keys also survive re-observation of the same event, which
-- surrogate ids do not.
--
-- Without this link a retraction cannot find what to reverse, which is the
-- whole mechanism.
ALTER TABLE prediction_results
  ADD COLUMN IF NOT EXISTS settled_by_event_keys TEXT[];

COMMENT ON COLUMN prediction_results.settled_by_event_keys IS
  'fixture_events.identity_key values whose occurrence decided this settlement. '
  'Used to find the settlements a retracted event invalidates. NULL means the '
  'settlement was decided by the full-time score rather than by named events.';

CREATE INDEX IF NOT EXISTS idx_prediction_results_settled_by_event_keys
  ON prediction_results USING GIN (settled_by_event_keys)
  WHERE settled_by_event_keys IS NOT NULL;

-- 5. Settlement history, because the current row is not the record.
--
-- `prediction_results` is keyed on prediction_id -- one row per prediction --
-- so a reversal that updated it in place would erase the settlement it
-- reversed. The audit trail is the integrity artefact here: "we said won, the
-- provider withdrew the goal, we reversed it" is the story a user and an
-- auditor both need, and it cannot be told from a single mutable row.
--
-- Append-only. Never updated, never deleted.
CREATE TABLE IF NOT EXISTS prediction_result_revisions (
  id BIGSERIAL PRIMARY KEY,
  prediction_id UUID NOT NULL REFERENCES engine_predictions(id) ON DELETE CASCADE,
  fixture_id BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE CASCADE,

  -- The state this revision recorded, using the same vocabulary as
  -- prediction_results.settlement_status.
  settlement_status TEXT NOT NULL CHECK (settlement_status IN (
    'settled', 'void', 'unsettled_data_unavailable', 'settlement_reversed'
  )),
  actual TEXT,
  is_correct BOOLEAN,
  void_reason TEXT,
  settled_by_event_keys TEXT[],

  -- Why this revision was written. 'initial' is the first settlement;
  -- 'event_retracted' is the provider withdrawing evidence; 'rescored' is a
  -- re-settlement after the retraction changed the answer without invalidating
  -- it entirely.
  revision_reason TEXT NOT NULL CHECK (revision_reason IN (
    'initial', 'event_retracted', 'rescored', 'manual_correction'
  )),
  -- The event whose retraction caused this revision, when one did.
  triggered_by_event_key TEXT,

  recorded_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE prediction_result_revisions IS
  'Append-only history of every settlement state a prediction has held. '
  'prediction_results holds only the current state, keyed one row per '
  'prediction, so this table is where a reversal keeps the settlement it '
  'reversed. Never updated, never deleted: the history is the integrity record.';

-- The two read patterns: the history of one prediction, and everything reversed
-- recently (for the accuracy recomputation and for an operator review).
CREATE INDEX IF NOT EXISTS idx_prediction_result_revisions_prediction
  ON prediction_result_revisions (prediction_id, recorded_at DESC);

CREATE INDEX IF NOT EXISTS idx_prediction_result_revisions_recent
  ON prediction_result_revisions (recorded_at DESC)
  WHERE revision_reason <> 'initial';

-- 6. Accuracy must be able to forget a reversed result.
--
-- prediction_result_rollup_ledger marks predictions already folded into
-- published accuracy. A reversal that left the ledger alone would keep a
-- withdrawn result in the accuracy figure permanently -- the least obvious
-- consequence of reversible settlement and the easiest to forget. This column
-- lets the rollup identify what it must recompute without rebuilding history.
ALTER TABLE prediction_result_rollup_ledger
  ADD COLUMN IF NOT EXISTS needs_recount BOOLEAN NOT NULL DEFAULT false;

COMMENT ON COLUMN prediction_result_rollup_ledger.needs_recount IS
  'Set when a settlement already counted in accuracy was reversed or rescored. '
  'The accuracy rollup clears it after recomputing the affected aggregates.';

CREATE INDEX IF NOT EXISTS idx_prediction_result_rollup_ledger_needs_recount
  ON prediction_result_rollup_ledger (aggregated_at DESC)
  WHERE needs_recount;

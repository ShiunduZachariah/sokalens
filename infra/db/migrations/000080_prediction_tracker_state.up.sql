-- Server-side tracker state for a pre-match pick (client contract §8).
--
-- WHY IT IS STORED AND NOT DERIVED AT READ TIME. The state has to be the same in
-- the web app and in Flutter, and the only way to guarantee that is to compute it
-- once and send it as a value. The market rules that decide it live in the Python
-- engine, next to settlement, because "is this pick winning" and "did this pick
-- win" are the same question at different moments -- a second implementation in
-- Go would mean twenty-one market arms maintained twice, and the first
-- divergence would surface as a pick that tracked green all match and settled
-- lost. So the engine writes it and the API reads it.
--
-- ONE ROW PER PREDICTION, replaced in place. This is a current-state projection,
-- not a history: the history of what a pick was doing minute by minute is the
-- snapshot series, and duplicating it here would be a second, worse copy.
CREATE TABLE IF NOT EXISTS prediction_tracker_state (
    prediction_id UUID PRIMARY KEY REFERENCES engine_predictions(id) ON DELETE CASCADE,
    fixture_id    BIGINT NOT NULL REFERENCES fixtures(id) ON DELETE CASCADE,

    -- The five states the contract names, plus `unknown`.
    --
    -- `unknown` is deliberately distinct from `void`. Void is a settled outcome
    -- -- the market rule voided it, or no result will ever arrive. Unknown says
    -- "we cannot judge this from what we hold": a corners pick before any corner
    -- data, a market with no settlement arm, or a settlement that has been
    -- reversed and not yet re-decided. A client shows no tracker for unknown
    -- rather than a misleading green or red, which it could not do if the two
    -- were the same value.
    state TEXT NOT NULL CHECK (state IN (
        'on_track', 'off_track', 'won', 'lost', 'void', 'unknown'
    )),

    -- The match state the judgement was made against, so a surprising tracker
    -- can be explained without re-running anything.
    home_goals SMALLINT,
    away_goals SMALLINT,
    match_minute SMALLINT,

    computed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE prediction_tracker_state IS
  'Current tracker state of a pre-match pick, computed by the engine so web and '
  'Flutter cannot derive it differently. One row per prediction, replaced in '
  'place: this is a projection, not a history -- the minute-by-minute record is '
  'inplay_snapshots.';

COMMENT ON COLUMN prediction_tracker_state.state IS
  'on_track means "would win if the match ended now" -- a report on the pick '
  'already made, not a second prediction. won/lost are terminal: a decided pick '
  'is never on_track, so FT over 1.5 with two goals at minute 10 reads won. '
  'unknown means unjudgeable, which is not the same as void.';

-- The read pattern: every tracked pick for one fixture.
CREATE INDEX IF NOT EXISTS idx_prediction_tracker_state_fixture
  ON prediction_tracker_state (fixture_id);

-- Finding stale projections when a sweep needs to catch up.
CREATE INDEX IF NOT EXISTS idx_prediction_tracker_state_computed
  ON prediction_tracker_state (computed_at);

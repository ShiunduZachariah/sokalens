// Package inplay reads live fixture state and the in-play predictions
// attached to it.
//
// It exists because the in-play feature had every part except this one. The
// worker generates in-play predictions, the schema holds them, the HTTP
// handler and the entitlement filter are written and tested -- and
// ServerOptions.InPlay was nil, so /v1/inplay/fixtures answered 503 to every
// caller. Nothing reached a reader.
//
// The package returns entitlement.FixtureState and never a response type.
// That separation is the handler's design rather than an accident of
// layering: this code loads whatever it likes and the gate between that and
// the client is not its decision. A source that could shape the response
// would be a source that could skip the gate.
package inplay

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/predictions/entitlement"
)

// staleAfter is when the live clock stops being presented as current.
//
// Mirrors StaleAfter in the worker's matchstate package. It is duplicated
// rather than imported because that package is internal to a different Go
// module, and a shared module for one constant would be heavier coupling than
// the duplication. The number must move in step with it. The reason it exists
// at all is the "second half, three hours" defect: a fixture drops out of the
// provider's live feed and the last minute written keeps being rendered as
// the current one.
//
// Ninety seconds is three missed polls at the healthy thirty-second cadence.
const staleAfter = 90 * time.Second

// snapshotLookback bounds the partition scan.
//
// fixture_live_snapshots is range-partitioned on captured_at. Without a lower
// bound the newest-snapshot lookup reads every partition, including ones
// holding months of history. Six hours covers a match plus extra time,
// penalties and a long feed lag, and lets Postgres prune to one or two
// partitions.
const snapshotLookback = 6 * time.Hour

// liveStatuses are the match statuses that mean a ball is in play.
//
// Kept in step with the inPlay set in the worker's matchstate package. Note
// that "unknown" is deliberately absent: it means the reaper gave up on a
// match, and serving it as in-play would put a stalled clock back on the page
// this whole mechanism exists to keep it off.
var liveStatuses = []string{"live", "halftime", "second_half", "extra_time", "penalties", "break"}

// inPlayStages are the lifecycle stages that make a prediction an in-play one.
//
// Kept in step with the CHECK constraint widened by migration 000071 and with
// PredictionLifecycleStage in the Python engine. A stage missing here is a
// prediction the engine writes and this never serves -- which is the exact
// shape of the bug that held in-play rows at zero, where the migration and
// the model were both right and the vocabulary in between was short.
var inPlayStages = []string{"IN_PLAY_1H", "HALF_TIME", "IN_PLAY_2H", "EXTRA_TIME"}

// ErrNotFound is returned when a fixture is not currently in play.
//
// Distinct from a transport failure on purpose: a match that has finished is
// not an outage, and reporting it as one would drive a working system into an
// alerting state every time a game ended.
var ErrNotFound = errors.New("in-play fixture not found")

// Source reads in-play fixtures from Postgres.
type Source struct {
	db *sql.DB
	// now is injectable so staleness is testable without waiting.
	now func() time.Time
}

// NewSource builds the reader.
func NewSource(db *sql.DB) *Source {
	return &Source{db: db, now: time.Now}
}

// selectInPlay loads live fixtures with their newest snapshot and current
// in-play markets. Publication membership is the writer's complete latest
// engine set. Rows absent from it remain in the database for settlement but
// must not reappear on the board. Legacy fixtures without a publication state
// expose only one generation, never a mixed-vintage ladder.
//
// One query rather than one per fixture. The N+1 shape is what makes a live
// list expensive exactly when the most matches are on, which is the worst
// possible time for it to get slower.
const selectInPlay = `
WITH live AS (
  SELECT m.id, m.status, m.live_state_at,
         COALESCE(home.logo_url, '') AS home_team_logo,
         COALESCE(away.logo_url, '') AS away_team_logo
  FROM matches m
  LEFT JOIN teams home ON home.id = m.home_team_id
  LEFT JOIN teams away ON away.id = m.away_team_id
  WHERE m.status = ANY($1)
    AND m.kickoff_at > now() - $2::interval
    __FILTER__
  ORDER BY m.kickoff_at ASC
  LIMIT $3
)
SELECT
  live.id,
  live.status,
  live.live_state_at,
  live.home_team_logo,
  live.away_team_logo,
  snap.elapsed_minute,
  snap.home_goals,
  snap.away_goals,
  pred.market,
  pred.prediction,
  pred.probability,
  pred.confidence_tier,
  pred.confidence,
  pred.data_cutoff_at,
  pred.match_minute,
  res.settlement_status,
  res.is_correct
FROM live
LEFT JOIN LATERAL (
  SELECT s.elapsed_minute, s.home_goals, s.away_goals
  FROM fixture_live_snapshots s
  WHERE s.fixture_id = live.id
    AND s.captured_at > now() - $2::interval
  ORDER BY s.captured_at DESC
  LIMIT 1
) snap ON true
LEFT JOIN inplay_publication_state pub ON pub.fixture_id = live.id
LEFT JOIN LATERAL (
  SELECT p.id AS prediction_id, p.market, p.prediction, p.probability,
         p.confidence_tier, p.confidence, p.data_cutoff_at, p.match_minute
  FROM engine_predictions p
  WHERE p.fixture_id = live.id
    AND p.superseded_at IS NULL
    AND p.lifecycle_stage = ANY($4)
    AND (
      (pub.fixture_id IS NOT NULL AND p.market = ANY(pub.current_markets))
      OR (pub.fixture_id IS NULL AND p.generated_at = (
        SELECT MAX(legacy.generated_at)
        FROM engine_predictions legacy
        WHERE legacy.fixture_id = live.id
          AND legacy.superseded_at IS NULL
          AND legacy.lifecycle_stage = ANY($4)
      ))
    )
) pred ON true
-- The grade, keyed on the published prediction's OWN id.
--
-- prediction_results is one row per prediction_id, so this reads the grade for
-- exactly the pick on display. If the graded generation was later superseded,
-- this is NULL and the market reads ungraded -- deliberately, because showing
-- an older generation's "won" beside a different selection would be a wrong
-- answer rather than a missing one. The known gap is recorded in
-- docs/integrations/in-play-data-contract.md.
--
-- No new scan: prediction_results is keyed on prediction_id, so this is a PK
-- lookup per row already being returned.
LEFT JOIN prediction_results res ON res.prediction_id = pred.prediction_id
ORDER BY live.id ASC, pred.market ASC`

// retainedWindowBefore / retainedWindowAfter bound the partition scan for a
// single fixture's retained history.
//
// The list query bounds snapshots at `now() - snapshotLookback`, which is right
// for a board of matches happening now and wrong for a match that finished
// yesterday: its snapshots were captured around ITS kickoff, not around now.
// Anchoring to the fixture's own kickoff keeps the scan just as bounded -- a
// fixed window over a known instant -- while reaching the partitions that
// actually hold the data.
const (
	retainedWindowBefore = time.Hour
	retainedWindowAfter  = 12 * time.Hour
)

// selectRetainedFixture loads one fixture's in-play state whatever its status.
// It applies the same publication membership as the live list, including the
// conservative latest-generation fallback for fixtures predating the state.
//
// Deliberately separate from selectInPlay rather than a flag on it. The list is
// "what is happening now" and must stay live-only; this is "show me what was
// recorded for this match", which is a different question and the only one a
// finished match can answer. Sharing a query would have meant one predicate
// doing both jobs badly.
const selectRetainedFixture = `
WITH fixture AS (
  SELECT m.id, m.status, m.live_state_at, m.kickoff_at,
         COALESCE(home.logo_url, '') AS home_team_logo,
         COALESCE(away.logo_url, '') AS away_team_logo
  FROM matches m
  LEFT JOIN teams home ON home.id = m.home_team_id
  LEFT JOIN teams away ON away.id = m.away_team_id
  WHERE m.id = $1
)
SELECT
  fixture.id,
  fixture.status,
  fixture.live_state_at,
  fixture.home_team_logo,
  fixture.away_team_logo,
  snap.elapsed_minute,
  snap.home_goals,
  snap.away_goals,
  pred.market,
  pred.prediction,
  pred.probability,
  pred.confidence_tier,
  pred.confidence,
  pred.data_cutoff_at,
  pred.match_minute,
  res.settlement_status,
  res.is_correct
FROM fixture
LEFT JOIN LATERAL (
  SELECT s.elapsed_minute, s.home_goals, s.away_goals
  FROM fixture_live_snapshots s
  WHERE s.fixture_id = fixture.id
    AND s.captured_at >= fixture.kickoff_at - $2::interval
    AND s.captured_at <= fixture.kickoff_at + $3::interval
  ORDER BY s.captured_at DESC
  LIMIT 1
) snap ON true
LEFT JOIN inplay_publication_state pub ON pub.fixture_id = fixture.id
LEFT JOIN LATERAL (
  SELECT p.id AS prediction_id, p.market, p.prediction, p.probability,
         p.confidence_tier, p.confidence, p.data_cutoff_at, p.match_minute
  FROM engine_predictions p
  WHERE p.fixture_id = fixture.id
    AND p.superseded_at IS NULL
    AND p.lifecycle_stage = ANY($4)
    AND (
      (pub.fixture_id IS NOT NULL AND p.market = ANY(pub.current_markets))
      OR (pub.fixture_id IS NULL AND p.generated_at = (
        SELECT MAX(legacy.generated_at)
        FROM engine_predictions legacy
        WHERE legacy.fixture_id = fixture.id
          AND legacy.superseded_at IS NULL
          AND legacy.lifecycle_stage = ANY($4)
      ))
    )
) pred ON true
-- The grade, keyed on the published prediction's OWN id.
--
-- prediction_results is one row per prediction_id, so this reads the grade for
-- exactly the pick on display. If the graded generation was later superseded,
-- this is NULL and the market reads ungraded -- deliberately, because showing
-- an older generation's "won" beside a different selection would be a wrong
-- answer rather than a missing one. The known gap is recorded in
-- docs/integrations/in-play-data-contract.md.
--
-- No new scan: prediction_results is keyed on prediction_id, so this is a PK
-- lookup per row already being returned.
LEFT JOIN prediction_results res ON res.prediction_id = pred.prediction_id
ORDER BY pred.market ASC`

// ListInPlayFixtures returns the fixtures currently in play.
func (s *Source) ListInPlayFixtures(ctx context.Context, limit int) ([]entitlement.FixtureState, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.load(ctx, limit, 0)
}

// GetInPlayFixture returns one fixture, or ErrNotFound.
func (s *Source) GetInPlayFixture(ctx context.Context, fixtureID int64) (entitlement.FixtureState, error) {
	if s.db == nil {
		return entitlement.FixtureState{}, errors.New("in-play source has no database")
	}
	rows, err := s.db.QueryContext(ctx, selectRetainedFixture,
		fixtureID, retainedWindowBefore.String(), retainedWindowAfter.String(), pgTextArray(inPlayStages))
	if err != nil {
		return entitlement.FixtureState{}, fmt.Errorf("query retained fixture: %w", err)
	}
	defer rows.Close()

	states, err := s.scanStates(rows)
	if err != nil {
		return entitlement.FixtureState{}, err
	}
	if len(states) == 0 {
		return entitlement.FixtureState{}, ErrNotFound
	}
	return states[0], nil
}

func (s *Source) load(ctx context.Context, limit int, fixtureID int64) ([]entitlement.FixtureState, error) {
	if s.db == nil {
		return nil, errors.New("in-play source has no database")
	}

	filter := ""
	args := []any{pgTextArray(liveStatuses), snapshotLookback.String(), limit, pgTextArray(inPlayStages)}
	if fixtureID > 0 {
		filter = "AND m.id = $5"
		args = append(args, fixtureID)
	}
	query := strings.Replace(selectInPlay, "__FILTER__", filter, 1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query in-play fixtures: %w", err)
	}
	defer rows.Close()

	return s.scanStates(rows)
}

// settlementState maps the grader's two columns onto the in-play contract's
// four words.
//
// The database stores "did we grade it" and "were we right" separately, which
// is the right shape for the grader -- a void is not a wrong answer and must
// not be counted as one. The contract's vocabulary is what a client renders,
// so the translation belongs here rather than in three clients.
//
// An ungraded market returns "", which the marshaller omits. A status the
// database has and this function does not recognise also returns "": a new
// settlement_status must be added deliberately, and inventing a chip for a
// word no client knows would be worse than showing none.
func settlementState(status sql.NullString, correct sql.NullBool) string {
	if !status.Valid {
		return ""
	}
	switch status.String {
	case "void":
		return "void"
	case "unsettled_data_unavailable":
		return "unsettled_data_unavailable"
	case "settled":
		// is_correct is what makes a settled row won or lost. A settled row
		// with no verdict is a grader bug, and "" is the honest answer to it
		// -- better an absent chip than a coin flip presented as a result.
		if !correct.Valid {
			return ""
		}
		if correct.Bool {
			return "won"
		}
		return "lost"
	default:
		return ""
	}
}

// scanStates turns rows into fixture states.
//
// Shared by the live list and the single retained fixture: the two ask
// different questions of the database but return the same seventeen columns in
// the same order, and a second copy of this loop is a second place for the
// market accumulation to drift.
func (s *Source) scanStates(rows *sql.Rows) ([]entitlement.FixtureState, error) {
	now := s.now().UTC()
	// Ordered by fixture id, so every row for one fixture arrives together
	// and markets accumulate onto the state being built. A map would lose the
	// market ordering the query already established.
	var (
		states  []entitlement.FixtureState
		current *entitlement.FixtureState
	)
	for rows.Next() {
		var (
			id           int64
			status       string
			liveStateAt  sql.NullTime
			homeTeamLogo string
			awayTeamLogo string
			minute       sql.NullInt32
			homeGoals    sql.NullInt32
			awayGoals    sql.NullInt32
			market       sql.NullString
			selection    sql.NullString
			probability  sql.NullFloat64
			confidence   sql.NullString
			confScore    sql.NullFloat64
			dataCutoffAt sql.NullTime
			matchMinute  sql.NullInt32
			settlement   sql.NullString
			isCorrect    sql.NullBool
		)
		if err := rows.Scan(&id, &status, &liveStateAt, &homeTeamLogo, &awayTeamLogo,
			&minute, &homeGoals, &awayGoals,
			&market, &selection, &probability, &confidence, &confScore, &dataCutoffAt, &matchMinute,
			&settlement, &isCorrect); err != nil {
			return nil, fmt.Errorf("scan in-play row: %w", err)
		}

		if current == nil || current.FixtureID != id {
			states = append(states, entitlement.FixtureState{
				FixtureID: id,
				Status:    status,
				Minute:    int(minute.Int32),
				HomeGoals: int(homeGoals.Int32),
				AwayGoals: int(awayGoals.Int32),
				// Decoration carried through from the teams joins the CTE
				// already makes. A side is still resolved by the fixture's own
				// team ids; nothing keys on the crest, and an empty one only
				// means the client draws a shield instead of an <img>.
				HomeTeamLogo: homeTeamLogo,
				AwayTeamLogo: awayTeamLogo,
				// A clock nobody has confirmed recently is not a current
				// clock. Derived here rather than served as a raw timestamp:
				// the usual objection -- that a flag ages in the client's
				// hands while a timestamp stays true -- does not apply to a
				// response the handler already marks private, no-store.
				Stale: !liveStateAt.Valid || now.Sub(liveStateAt.Time.UTC()) >= staleAfter,
			})
			current = &states[len(states)-1]
		}

		// A fixture with no in-play prediction still belongs in the list. The
		// score and the clock are worth serving on their own, and an absent
		// prediction is not a locked one -- that distinction is what tells an
		// entitled reader "not computed" apart from "not for you".
		if !market.Valid {
			continue
		}
		state := entitlement.MarketState{
			Key: market.String,
			// Deliberately the key, not a rendered label. The label table
			// lives in apps/web/app/lib/markets.ts and is localized per
			// request; a second copy here could not localize and would drift.
			Label:       market.String,
			MatchMinute: int(matchMinute.Int32),
		}
		if dataCutoffAt.Valid {
			state.DataCutoffAt = dataCutoffAt.Time.UTC()
		}
		state.SettlementState = settlementState(settlement, isCorrect)
		if selection.Valid {
			state.Prediction = &entitlement.Prediction{
				Selection:       selection.String,
				Probability:     probability.Float64,
				Confidence:      confidence.String,
				ConfidenceScore: confScore.Float64,
			}
		}
		current.Markets = append(current.Markets, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate in-play rows: %w", err)
	}
	return states, nil
}

// pgTextArray renders a Go slice as a Postgres text[] literal.
//
// Used instead of pq.Array so this package does not take a driver dependency
// for two fixed, code-owned lists. Both lists are compile-time constants with
// no user input anywhere near them, and the quoting below would not be
// sufficient if that ever stopped being true.
func pgTextArray(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, `"`+strings.ReplaceAll(value, `"`, `\"`)+`"`)
	}
	return "{" + strings.Join(quoted, ",") + "}"
}

// maxSeriesPoints bounds one fixture's graph.
//
// A snapshot is written on every goal, red card and period change, and
// otherwise on a timer, so a long match with extra time lands around 120
// points; 400 is well clear of that and still bounds a fixture whose feed
// misbehaved. The cap takes the OLDEST points, not the newest: a graph that
// silently loses its first half is worse than one that is visibly shorter.
const maxSeriesPoints = 400

// FixtureSeries returns a fixture's probability graph, oldest point first.
//
// Reads inplay_snapshots directly rather than going through the change feed:
// the feed answers "what has happened since cursor N", which is the right
// question for a live board and the wrong one for a chart that needs the whole
// match from the first minute.
func (s *Source) FixtureSeries(ctx context.Context, fixtureID int64) ([]entitlement.SeriesPoint, error) {
	if s.db == nil {
		return nil, errors.New("in-play source has no database")
	}
	// The event is joined on minute, not on s.triggered_by_event_key: that column
	// is empty on every row in production, because the writer infers "a goal
	// happened" from a change of scoreline rather than from an event record, so
	// it never has a key to store. Matching on minute is the link that exists.
	//
	// Restricted to the triggers a marker is drawn for, so a timer tick that
	// happens to share a minute with a yellow card does not acquire one. The
	// lateral join takes a single event per snapshot -- a minute can hold
	// several, and the marker names one incident.
	//
	// `fixture_events.team_id` is a PROVIDER id, not a canonical teams.id, and
	// there is no foreign key saying so. Its values collide with real team ids,
	// so comparing it to f.home_team_id directly does not fail -- it quietly
	// resolves to the wrong club. On fixture 4513068 (Barranquilla vs
	// Millonarios) the raw ids 1466 and 1125 are "Instituto Cordoba" and
	// "Santos Laguna U21" in the teams table; across all 58,783 events, exactly
	// zero match their own fixture's teams. It has to be translated through
	// provider_entity_maps, keyed by the provider the event itself came from.
	rows, err := s.db.QueryContext(ctx, `
SELECT s.sequence, s.elapsed_minute, s.trigger, s.triggered_by_event_key,
       s.home_goals, s.away_goals, s.captured_at, s.markets,
       e.event_type, e.event_detail, e.player_name,
       CASE WHEN e.canonical_team_id = f.home_team_id THEN 'home'
            WHEN e.canonical_team_id = f.away_team_id THEN 'away' END AS side
FROM inplay_snapshots s
JOIN fixtures f ON f.id = s.fixture_id
LEFT JOIN LATERAL (
  SELECT ev.event_type, ev.event_detail, ev.player_name,
         NULLIF(pem.canonical_entity_id, '')::bigint AS canonical_team_id
  FROM fixture_events ev
  LEFT JOIN provider_entity_maps pem
    ON pem.entity_type = 'team'
   AND pem.canonical_entity_type = 'team'
   AND pem.provider = ev.provider
   AND pem.provider_entity_id = ev.team_id::text
   AND pem.retired_at IS NULL
  WHERE ev.fixture_id = s.fixture_id
    AND ev.elapsed_minute = s.elapsed_minute
    AND NOT ev.is_retracted
    AND ( (s.trigger = 'post_goal' AND ev.event_type = 'Goal')
       OR (s.trigger = 'post_red_card' AND ev.event_type = 'Card'
           AND ev.event_detail ILIKE '%red%') )
  ORDER BY ev.sequence ASC
  LIMIT 1
) e ON TRUE
WHERE s.fixture_id = $1
ORDER BY s.sequence ASC
LIMIT $2`, fixtureID, maxSeriesPoints)
	if err != nil {
		return nil, fmt.Errorf("query fixture series: %w", err)
	}
	defer rows.Close()

	var points []entitlement.SeriesPoint
	for rows.Next() {
		var (
			point    entitlement.SeriesPoint
			minute   sql.NullInt32
			trigger  sql.NullString
			eventKey sql.NullString
			home     sql.NullInt32
			away     sql.NullInt32
			markets  []byte

			eventType   sql.NullString
			eventDetail sql.NullString
			eventPlayer sql.NullString
			eventSide   sql.NullString
		)
		if err := rows.Scan(&point.Sequence, &minute, &trigger, &eventKey,
			&home, &away, &point.CapturedAt, &markets,
			&eventType, &eventDetail, &eventPlayer, &eventSide); err != nil {
			return nil, fmt.Errorf("scan fixture series: %w", err)
		}
		// Only when the join actually matched. A marker with a type but no side
		// would claim an incident happened without saying to whom.
		if eventType.Valid && eventSide.Valid {
			point.Event = &entitlement.SeriesEvent{
				Type:   eventType.String,
				Detail: eventDetail.String,
				Side:   eventSide.String,
				Player: eventPlayer.String,
			}
		}
		point.Minute = int(minute.Int32)
		point.Trigger = trigger.String
		point.EventKey = eventKey.String
		point.HomeGoals = int(home.Int32)
		point.AwayGoals = int(away.Int32)
		if len(markets) > 0 {
			// A point whose markets will not parse is still a point: it carries
			// the score and the minute, and dropping it would put a gap in the
			// line rather than a flat segment.
			_ = json.Unmarshal(markets, &point.Markets)
		}
		points = append(points, point)
	}
	return points, rows.Err()
}

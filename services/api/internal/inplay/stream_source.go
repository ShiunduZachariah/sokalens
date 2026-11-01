package inplay

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/predictions/entitlement"
)

// StreamSource reads the in-play model series and the change feed.
//
// Separate from Source because they read different things -- Source answers
// "what does the model say now", this answers "what has happened since sequence
// N" -- and because the transport routes must keep working when the
// point-in-time reader is unavailable, and vice versa.
//
// Like Source it returns entitlement domain values and never a response type. A
// source that could shape the response would be a source that could skip the
// gate, and the series is the paid asset over its whole history.
type StreamSource struct {
	db      *sql.DB
	fixture *Source
}

// NewStreamSource builds the reader. It takes the point-in-time Source because
// the combined first load includes the current board, and rebuilding that query
// here would be a second definition of "what is live" that could disagree with
// the first.
func NewStreamSource(db *sql.DB, fixture *Source) *StreamSource {
	return &StreamSource{db: db, fixture: fixture}
}

// selectChangesSince reads snapshots and events after a cursor.
//
// ONE QUERY, BOTH RECORD TYPES, ORDERED BY THE SHARED SEQUENCE. They draw from
// `inplay_stream_sequence` together, so a single ORDER BY puts them in the order
// they happened. Two queries merged in Go would need the merge to be correct in a
// second place, and would read the two tables at different instants.
//
// The limit is applied to the union, not per table: a client asking for 200
// changes wants 200 changes, not 200 of each.
const selectChangesSince = `
WITH changes AS (
  SELECT
    s.sequence,
    'snapshot'::text AS kind,
    s.fixture_id,
    s.captured_at,
    s.data_cutoff_at,
    s.trigger,
    s.triggered_by_event_key,
    s.model_health,
    s.period,
    s.elapsed_minute,
    s.elapsed_extra,
    s.home_goals,
    s.away_goals,
    s.home_red_cards,
    s.away_red_cards,
    s.markets,
    NULL::text AS event_type,
    NULL::text AS event_detail,
    NULL::text AS identity_key,
    NULL::bigint AS team_id,
    NULL::text AS player_name,
    false AS is_retracted
  FROM inplay_snapshots s
  WHERE s.sequence > $1
  UNION ALL
  SELECT
    e.sequence,
    'event'::text AS kind,
    e.fixture_id,
    e.observed_at AS captured_at,
    NULL::timestamptz AS data_cutoff_at,
    NULL::text AS trigger,
    NULL::text AS triggered_by_event_key,
    NULL::text AS model_health,
    NULL::text AS period,
    e.elapsed_minute,
    e.elapsed_extra,
    NULL::smallint AS home_goals,
    NULL::smallint AS away_goals,
    NULL::smallint AS home_red_cards,
    NULL::smallint AS away_red_cards,
    NULL::jsonb AS markets,
    e.event_type,
    e.event_detail,
    e.identity_key,
    e.team_id,
    e.player_name,
    e.is_retracted
  FROM fixture_events e
  WHERE e.sequence > $1
)
SELECT * FROM changes
ORDER BY sequence ASC
LIMIT $2
`

// selectRecentSeries loads the tail of the series for the fixtures on the board.
//
// Bounded per fixture rather than overall: a cold client needs enough points to
// draw a line for EVERY live match, and a flat overall limit would give a
// Saturday's first few fixtures a full series and the rest nothing.
const selectRecentSeries = `
SELECT
  sequence, fixture_id, captured_at, data_cutoff_at, trigger, triggered_by_event_key,
  model_health, period, elapsed_minute, home_goals, away_goals,
  home_red_cards, away_red_cards, markets
FROM (
  SELECT
    s.*,
    row_number() OVER (PARTITION BY s.fixture_id ORDER BY s.captured_at DESC, s.id DESC) AS recency
  FROM inplay_snapshots s
  WHERE s.fixture_id = ANY($1::bigint[])
) ranked
WHERE recency <= $2
ORDER BY fixture_id ASC, captured_at ASC
`

// currentCursor is the stream position as at this read.
//
// last_value on the sequence rather than max() over the tables: a snapshot can be
// committed between the series read and the cursor read, and a cursor derived
// from rows already seen would make a client skip it for ever. Reading the
// sequence itself is never behind.
const currentCursor = `SELECT COALESCE(last_value, 0) FROM inplay_stream_sequence`

// defaultSeriesPerFixture is how many points a cold client gets per match.
//
// Sixty points is an hour of play at the 60-second timer, which is enough to draw
// a recognisable line without shipping a whole match to a client that may only
// glance at it. A client wanting more asks the changes feed from an older cursor.
const defaultSeriesPerFixture = 60

// ChangesSince returns everything after the cursor, in sequence order.
func (s *StreamSource) ChangesSince(ctx context.Context, cursor int64, limit int) (entitlement.ChangeSetState, error) {
	if limit <= 0 {
		limit = 200
	}
	// One extra row, to distinguish "exactly the limit" from "there is more".
	// Asking the client to poll again when there is nothing left would have it
	// spin; not telling it when there is would leave it behind.
	rows, err := s.db.QueryContext(ctx, selectChangesSince, cursor, limit+1)
	if err != nil {
		return entitlement.ChangeSetState{}, fmt.Errorf("read in-play changes: %w", err)
	}
	defer rows.Close()

	state := entitlement.ChangeSetState{Cursor: cursor}
	count := 0
	for rows.Next() {
		count++
		if count > limit {
			state.More = true
			break
		}
		snapshot, event, sequence, err := scanChange(rows)
		if err != nil {
			return entitlement.ChangeSetState{}, err
		}
		if snapshot != nil {
			state.Snapshots = append(state.Snapshots, *snapshot)
		}
		if event != nil {
			state.Events = append(state.Events, *event)
		}
		// The cursor advances to the last row actually delivered, never past it:
		// a cursor ahead of what the client holds silently drops changes.
		state.Cursor = sequence
	}
	if err := rows.Err(); err != nil {
		return entitlement.ChangeSetState{}, fmt.Errorf("scan in-play changes: %w", err)
	}
	return state, nil
}

// Overview is the combined first load.
func (s *StreamSource) Overview(ctx context.Context, fixtureLimit, seriesLimit int) (entitlement.OverviewState, error) {
	// The cursor is read FIRST, before the series.
	//
	// A snapshot committed between the two reads is then delivered again by the
	// client's first changes call -- harmless, because applying a point twice
	// lands in the same place. Reading the cursor last would let that snapshot
	// fall in the gap and never be delivered at all, which is a hole in the
	// graph that nothing later repairs.
	var cursor int64
	if err := s.db.QueryRowContext(ctx, currentCursor).Scan(&cursor); err != nil {
		return entitlement.OverviewState{}, fmt.Errorf("read in-play cursor: %w", err)
	}

	fixtures, err := s.fixture.ListInPlayFixtures(ctx, fixtureLimit)
	if err != nil {
		return entitlement.OverviewState{}, err
	}
	if len(fixtures) == 0 {
		return entitlement.OverviewState{Cursor: cursor}, nil
	}

	ids := make([]int64, 0, len(fixtures))
	for _, fixture := range fixtures {
		ids = append(ids, fixture.FixtureID)
	}
	perFixture := seriesLimit
	if perFixture <= 0 || perFixture > defaultSeriesPerFixture {
		perFixture = defaultSeriesPerFixture
	}
	snapshots, err := s.seriesFor(ctx, ids, perFixture)
	if err != nil {
		return entitlement.OverviewState{}, err
	}
	return entitlement.OverviewState{Fixtures: fixtures, Snapshots: snapshots, Cursor: cursor}, nil
}

func (s *StreamSource) seriesFor(ctx context.Context, ids []int64, perFixture int) ([]entitlement.SnapshotState, error) {
	rows, err := s.db.QueryContext(ctx, selectRecentSeries, pgInt64Array(ids), perFixture)
	if err != nil {
		return nil, fmt.Errorf("read in-play series: %w", err)
	}
	defer rows.Close()

	series := make([]entitlement.SnapshotState, 0, len(ids)*perFixture)
	for rows.Next() {
		var (
			snapshot    entitlement.SnapshotState
			cutoff      sql.NullTime
			trigger     sql.NullString
			triggeredBy sql.NullString
			health      sql.NullString
			period      sql.NullString
			minute      sql.NullInt64
			homeGoals   sql.NullInt64
			awayGoals   sql.NullInt64
			homeReds    sql.NullInt64
			awayReds    sql.NullInt64
			markets     []byte
		)
		if err := rows.Scan(
			&snapshot.Sequence, &snapshot.FixtureID, &snapshot.CapturedAt, &cutoff,
			&trigger, &triggeredBy, &health, &period, &minute,
			&homeGoals, &awayGoals, &homeReds, &awayReds, &markets,
		); err != nil {
			return nil, fmt.Errorf("scan in-play series: %w", err)
		}
		snapshot.DataCutoffAt = cutoff.Time
		snapshot.Trigger = trigger.String
		snapshot.TriggeredByKey = triggeredBy.String
		snapshot.ModelHealth = health.String
		snapshot.Period = period.String
		snapshot.Minute = int(minute.Int64)
		snapshot.HomeGoals = int(homeGoals.Int64)
		snapshot.AwayGoals = int(awayGoals.Int64)
		snapshot.HomeRedCards = int(homeReds.Int64)
		snapshot.AwayRedCards = int(awayReds.Int64)
		snapshot.Markets = decodeMarkets(markets)
		series = append(series, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan in-play series: %w", err)
	}
	return series, nil
}

// decodeMarkets turns the stored JSONB into basis points.
//
// A malformed payload yields no markets rather than an error: one unreadable
// snapshot must not blank the whole series a client is drawing, and the point
// still carries its clock, score and trigger.
func decodeMarkets(raw []byte) map[string]map[string]int {
	if len(raw) == 0 {
		return nil
	}
	markets := map[string]map[string]int{}
	if err := json.Unmarshal(raw, &markets); err != nil {
		return nil
	}
	if len(markets) == 0 {
		return nil
	}
	return markets
}

// pgInt64Array renders a Go slice as a Postgres bigint[] literal.
//
// Follows pgTextArray in this package, and for the same reason: no driver
// dependency for a list this package built itself. These ids come from the
// fixture query a few lines above, never from a caller, so there is no quoting
// question -- and strconv cannot emit anything but digits and a sign.
func pgInt64Array(values []int64) string {
	rendered := make([]string, 0, len(values))
	for _, value := range values {
		rendered = append(rendered, strconv.FormatInt(value, 10))
	}
	return "{" + strings.Join(rendered, ",") + "}"
}

type changeScanner interface {
	Scan(dest ...any) error
}

// scanChange reads one union row as whichever record type it is.
func scanChange(rows changeScanner) (*entitlement.SnapshotState, *entitlement.Event, int64, error) {
	var (
		sequence    int64
		kind        string
		fixtureID   int64
		capturedAt  time.Time
		cutoff      sql.NullTime
		trigger     sql.NullString
		triggeredBy sql.NullString
		health      sql.NullString
		period      sql.NullString
		minute      sql.NullInt64
		extra       sql.NullInt64
		homeGoals   sql.NullInt64
		awayGoals   sql.NullInt64
		homeReds    sql.NullInt64
		awayReds    sql.NullInt64
		markets     []byte
		eventType   sql.NullString
		eventDetail sql.NullString
		identityKey sql.NullString
		teamID      sql.NullInt64
		playerName  sql.NullString
		retracted   bool
	)
	if err := rows.Scan(
		&sequence, &kind, &fixtureID, &capturedAt, &cutoff, &trigger, &triggeredBy,
		&health, &period, &minute, &extra, &homeGoals, &awayGoals, &homeReds, &awayReds,
		&markets, &eventType, &eventDetail, &identityKey, &teamID, &playerName, &retracted,
	); err != nil {
		return nil, nil, 0, fmt.Errorf("scan in-play change: %w", err)
	}

	if kind == "snapshot" {
		return &entitlement.SnapshotState{
			FixtureID:      fixtureID,
			Sequence:       sequence,
			CapturedAt:     capturedAt,
			DataCutoffAt:   cutoff.Time,
			Trigger:        trigger.String,
			TriggeredByKey: triggeredBy.String,
			ModelHealth:    health.String,
			Minute:         int(minute.Int64),
			Period:         period.String,
			HomeGoals:      int(homeGoals.Int64),
			AwayGoals:      int(awayGoals.Int64),
			HomeRedCards:   int(homeReds.Int64),
			AwayRedCards:   int(awayReds.Int64),
			Markets:        decodeMarkets(markets),
		}, nil, sequence, nil
	}

	event := entitlement.Event{
		Sequence:    sequence,
		FixtureID:   fixtureID,
		IdentityKey: identityKey.String,
		Kind:        eventType.String,
		Detail:      eventDetail.String,
		Minute:      int(minute.Int64),
		PlayerName:  playerName.String,
		Retracted:   retracted,
	}
	if extra.Valid {
		value := int(extra.Int64)
		event.Extra = &value
	}
	if teamID.Valid {
		value := teamID.Int64
		event.TeamID = &value
	}
	return nil, &event, sequence, nil
}

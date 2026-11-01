// Package fixtureevents persists the event timeline embedded in API-Football's
// bulk live-fixtures response.
//
// Two storage decisions here are load-bearing, and both are read back by
// services/api/internal/matches/timeline.go:
//
//   - team_id holds the PROVIDER's team id, not ours. This table is a record of
//     what a provider said, like fixture_live_snapshots; the read path resolves
//     the side through provider_entity_maps and renders an unresolved team
//     without taking a side rather than guessing. Storing a canonical id here
//     would make that join match nothing, and every event would silently lose
//     its side -- a timeline that renders, with no error anywhere.
//   - Identity is enforced here, not by a unique constraint. PostgreSQL
//     requires the partition key in every unique constraint and the partition
//     key is observed_at, which differs between polls of the same event -- so
//     the constraint would permit exactly the duplicate it exists to prevent.
package fixtureevents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
)

// identityWindow bounds the lookup to partitions that can plausibly contain
// an earlier observation of a fixture that is live now.
const identityWindow = 24 * time.Hour

// Repository appends live fixture events while suppressing repeat observations.
type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) Repository { return Repository{db: db} }

// Append stores event against the canonical fixture id, and reports whether it
// wrote a row. A repeat observation of an event already recorded is not an
// error: the live poll sees every event again every time it runs.
func (r Repository) Append(ctx context.Context, fixtureID int64, provider string, observedAt time.Time, event apifootball.FixtureEvent) (bool, error) {
	if r.db == nil {
		return false, errors.New("fixture event DB is nil")
	}
	if fixtureID <= 0 {
		return false, errors.New("fixture event requires a positive canonical fixture id")
	}
	if strings.TrimSpace(provider) == "" {
		return false, errors.New("fixture event requires a provider")
	}
	if observedAt.IsZero() {
		return false, errors.New("fixture event requires an observation time")
	}
	if event.ElapsedMinute < 0 {
		return false, errors.New("fixture event requires a non-negative elapsed minute")
	}
	if strings.TrimSpace(event.IdentityKey) == "" {
		return false, errors.New("fixture event requires an identity key")
	}

	// An event we already hold is merged, not skipped.
	//
	// The provider publishes an event the moment it happens and completes it
	// afterwards: a goal appears with no scorer and gains the name, and often
	// the assist, seconds later. Flashscore shows the same thing -- a ball icon
	// first, the player underneath it shortly after. Treating the second
	// observation as a duplicate to discard would leave that goal permanently
	// unattributed, which is the mirror of showing it twice and just as wrong.
	//
	// So the newer observation wins field by field, and only where it actually
	// says something. COALESCE keeps the stored value whenever the new one is
	// null, because the provider also drops fields it sent earlier and a goal
	// must not lose its scorer to a thinner later poll.
	//
	// The clock is the exception: elapsed minute and extra are taken from the
	// newer observation outright, since the provider revises them as a half
	// settles and its latest word is the correct one.
	//
	// observed_at is deliberately not touched. The table is range-partitioned
	// on it, so moving it would migrate the row between partitions for no gain.
	//
	// The retraction timestamp is passed in rather than taken from now(): this
	// package's tests run against SQLite, which has no such function, and a
	// caller-supplied clock is deterministic besides.
	var existingID int64
	err := r.db.QueryRowContext(ctx, `
SELECT id FROM fixture_events
WHERE fixture_id = $1 AND identity_key = $2 AND observed_at >= $3
ORDER BY observed_at DESC
LIMIT 1`, fixtureID, event.IdentityKey, observedAt.UTC().Add(-identityWindow)).Scan(&existingID)
	if err == nil {
		return r.mergeExisting(ctx, existingID, event, observedAt)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("look up fixture event identity: %w", err)
	}

	// event_kind is deliberately not stored. The read path derives the same
	// meaning from type+detail (matches.EventKind, held to this package's
	// classification by TestEventKindMatchesTheWriter), so a column would be
	// written and never read -- and adding one means either editing migration
	// 000070 after it has been applied, which leaves any environment that
	// already ran it without the column, or a migration for a field nobody
	// reads.
	_, err = r.db.ExecContext(ctx, `
INSERT INTO fixture_events (
  fixture_id, provider, observed_at, elapsed_minute, elapsed_extra,
  event_type, event_detail, team_id,
  player_id, player_name, assist_id, assist_name, comments,
  identity_key, is_retracted, retracted_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		fixtureID,
		strings.TrimSpace(provider),
		observedAt.UTC(),
		event.ElapsedMinute,
		nullableInt(event.ElapsedExtra),
		event.Type,
		event.Detail,
		nullableInt64(event.TeamID),
		nullableInt64(event.PlayerID),
		nullableText(event.PlayerName),
		nullableInt64(event.AssistID),
		nullableText(event.AssistName),
		nullableText(event.Comments),
		event.IdentityKey,
		event.IsRetraction,
		retractedAt(event.IsRetraction, observedAt),
	)
	if err != nil {
		return false, fmt.Errorf("append fixture event: %w", err)
	}
	return true, nil
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableText(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func retractedAt(retraction bool, observedAt time.Time) any {
	if !retraction {
		return nil
	}
	return observedAt.UTC()
}

// ReconcileScoringEvents retires goals which disappeared from a complete live
// provider list. The provider can revise an own goal into an ordinary goal (or
// vice versa), changing both kind and team; those are different identity keys,
// so Append alone would leave both on the public timeline. Only reconcile when
// the latest nonempty list has the exact goals for each side in the provider's
// authoritative score. An absent or incomplete event list must never withdraw
// a goal.
// The row remains as a retracted event for the audit trail and can be restored
// if a later complete list contains it again.
func (r Repository) ReconcileScoringEvents(ctx context.Context, fixtureID int64, provider string, observedAt time.Time, events []apifootball.FixtureEvent, homeTeamID, awayTeamID int64, homeGoals, awayGoals *int) (int, error) {
	if r.db == nil || fixtureID <= 0 || strings.TrimSpace(provider) == "" || observedAt.IsZero() ||
		homeTeamID <= 0 || awayTeamID <= 0 || homeTeamID == awayTeamID || len(events) == 0 ||
		homeGoals == nil || awayGoals == nil || *homeGoals < 0 || *awayGoals < 0 {
		return 0, nil
	}
	current := make(map[string]struct{}, len(events))
	homeCount, awayCount := 0, 0
	for _, event := range events {
		switch event.Kind {
		case apifootball.EventKindGoal, apifootball.EventKindOwnGoal, apifootball.EventKindPenaltyGoal:
			if event.IdentityKey == "" || event.TeamID == nil {
				return 0, nil
			}
			switch *event.TeamID {
			case homeTeamID:
				homeCount++
			case awayTeamID:
				awayCount++
			default:
				return 0, nil
			}
			current[event.IdentityKey] = struct{}{}
		}
	}
	if homeCount != *homeGoals || awayCount != *awayGoals || len(current) != homeCount+awayCount {
		return 0, nil
	}

	rows, err := r.db.QueryContext(ctx, `
SELECT id, observed_at, identity_key, is_retracted
FROM fixture_events
WHERE fixture_id = $1 AND provider = $2 AND observed_at >= $3
  AND lower(event_type) = 'goal'
  AND lower(event_detail) NOT LIKE '%missed penalty%'`,
		fixtureID, provider, observedAt.UTC().Add(-identityWindow))
	if err != nil {
		return 0, fmt.Errorf("read scoring events for reconciliation: %w", err)
	}
	type storedGoal struct {
		id        int64
		observed  time.Time
		identity  string
		retracted bool
	}
	var stored []storedGoal
	for rows.Next() {
		var goal storedGoal
		if err := rows.Scan(&goal.id, &goal.observed, &goal.identity, &goal.retracted); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan scoring event: %w", err)
		}
		stored = append(stored, goal)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate scoring events: %w", err)
	}
	rows.Close()

	changed := 0
	for _, goal := range stored {
		_, present := current[goal.identity]
		if goal.retracted == !present {
			continue
		}
		var retiredAt any
		if !present {
			retiredAt = observedAt.UTC()
		}
		result, err := r.db.ExecContext(ctx, `
UPDATE fixture_events SET is_retracted = $3, retracted_at = $4
WHERE id = $1 AND observed_at = $2 AND is_retracted = $5`,
			goal.id, goal.observed, !present, retiredAt, goal.retracted)
		if err != nil {
			return changed, fmt.Errorf("reconcile scoring event: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return changed, fmt.Errorf("count reconciled scoring events: %w", err)
		}
		changed += int(count)
	}
	return changed, nil
}

// mergeExisting folds a later observation into the row already stored.
//
// The comparison happens here rather than in the UPDATE's WHERE clause
// because this package's tests run against SQLite while production is
// Postgres, and the dialects disagree about IS DISTINCT FROM and about
// casting a parameter. Dense portable SQL would be harder to read than the
// rule it encodes, and the rule is the part worth being able to check.
//
// Reports whether anything changed, so a poll that re-sends an event
// unchanged -- which is most polls, since the provider returns the whole list
// every time -- is not counted as a write.
func (r Repository) mergeExisting(ctx context.Context, id int64, event apifootball.FixtureEvent, observedAt time.Time) (bool, error) {
	var (
		curMinute  sql.NullInt64
		curExtra   sql.NullInt64
		curPlayer  sql.NullInt64
		curName    sql.NullString
		curAssist  sql.NullInt64
		curAssistN sql.NullString
		curComment sql.NullString
		curRetract bool
	)
	if err := r.db.QueryRowContext(ctx, `
SELECT elapsed_minute, elapsed_extra, player_id, player_name, assist_id, assist_name, comments, is_retracted
FROM fixture_events WHERE id = $1`, id).Scan(
		&curMinute, &curExtra, &curPlayer, &curName, &curAssist, &curAssistN, &curComment, &curRetract,
	); err != nil {
		return false, fmt.Errorf("read stored fixture event: %w", err)
	}

	// The clock is taken from the newer observation outright: the provider
	// revises minutes as a half settles and its latest word is correct.
	minute := int64(event.ElapsedMinute)
	var extra sql.NullInt64
	if event.ElapsedExtra != nil {
		extra = sql.NullInt64{Int64: int64(*event.ElapsedExtra), Valid: true}
	}

	// Everything else only ever gains detail. The provider drops fields it
	// sent earlier, and a goal must not lose its scorer to a thinner poll.
	playerID := preferInt(event.PlayerID, curPlayer)
	playerName := preferText(event.PlayerName, curName)
	assistID := preferInt(event.AssistID, curAssist)
	assistName := preferText(event.AssistName, curAssistN)
	comments := preferText(event.Comments, curComment)

	// A retraction only ever goes on. VAR disallowing a goal is new
	// information; a later poll omitting the flag is not evidence the goal was
	// reinstated, and clearing it would put a cancelled goal back on the
	// timeline.
	retracted := curRetract || event.IsRetraction

	unchanged := curMinute.Valid && curMinute.Int64 == minute &&
		sameInt(curExtra, extra) &&
		sameInt(curPlayer, playerID) &&
		sameText(curName, playerName) &&
		sameInt(curAssist, assistID) &&
		sameText(curAssistN, assistName) &&
		sameText(curComment, comments) &&
		curRetract == retracted
	if unchanged {
		return false, nil
	}

	var retractedAt any
	if retracted {
		retractedAt = observedAt.UTC()
	}
	if _, err := r.db.ExecContext(ctx, `
UPDATE fixture_events SET
  elapsed_minute = $2, elapsed_extra = $3,
  player_id = $4, player_name = $5,
  assist_id = $6, assist_name = $7,
  comments = $8, is_retracted = $9,
  retracted_at = COALESCE(retracted_at, $10)
WHERE id = $1`,
		id, minute, extra, playerID, playerName, assistID, assistName, comments, retracted, retractedAt,
	); err != nil {
		return false, fmt.Errorf("merge fixture event: %w", err)
	}
	return true, nil
}

// preferInt keeps the stored value when the new observation says nothing.
func preferInt(incoming *int64, stored sql.NullInt64) sql.NullInt64 {
	if incoming != nil {
		return sql.NullInt64{Int64: *incoming, Valid: true}
	}
	return stored
}

// preferText keeps the stored value when the new observation is blank.
func preferText(incoming string, stored sql.NullString) sql.NullString {
	if strings.TrimSpace(incoming) != "" {
		return sql.NullString{String: incoming, Valid: true}
	}
	return stored
}

func sameInt(a, b sql.NullInt64) bool {
	return a.Valid == b.Valid && (!a.Valid || a.Int64 == b.Int64)
}

func sameText(a, b sql.NullString) bool {
	return a.Valid == b.Valid && (!a.Valid || a.String == b.String)
}

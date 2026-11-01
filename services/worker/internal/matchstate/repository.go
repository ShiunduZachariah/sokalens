package matchstate

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Repository is the reaper's Postgres-backed store.
type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) Repository { return Repository{db: db} }

// StuckMatches returns non-terminal matches whose live claim is overdue.
//
// The SQL is a coarse filter, not the decision: it selects candidates by the
// two cheapest signals and lets Assess make the actual call in Go, so the
// thresholds live in exactly one place. Duplicating them here would create two
// definitions of "stale" that drift.
//
// The two cutoffs are computed in Go rather than written as SQL interval
// literals: the same statement runs against Postgres in production and SQLite
// in tests, and `interval '4 minutes'` parses in only one of them. They are
// deliberately looser than the Go thresholds -- a candidate the Go side then
// rejects costs nothing, a candidate the SQL never returns is invisible.
func (r Repository) StuckMatches(ctx context.Context, now time.Time, limit int) ([]StuckMatch, error) {
	if r.db == nil {
		return nil, errors.New("match state DB is nil")
	}
	if limit <= 0 {
		limit = 200
	}
	moment := now.UTC()
	staleBefore := moment.Add(-(ReconcileAfter - time.Minute))
	kickoffBefore := moment.Add(-(PlausibleDuration - 5*time.Minute))
	rows, err := r.db.QueryContext(ctx, `
SELECT id, status, kickoff_at, live_state_at
FROM matches
WHERE (
    -- A match claiming to be in play long after it could still be in play.
    (
      status IN ('live', 'halftime', 'second_half', 'extra_time', 'penalties', 'break', 'interrupted', 'suspended')
      AND (
        live_state_at IS NULL
        OR live_state_at < $1
        OR kickoff_at < $2
      )
    )
    -- Or a match still claiming it has not kicked off, long after it did.
    --
    -- This half was missing, and it is the larger half. The sweep only ever
    -- looked at matches that had reached a live state, so a match live
    -- polling never saw at all stayed 'upcoming' for ever: on 2026-09-22
    -- there were 3,207 of them, the oldest from 2025-12-18, none with a
    -- score, and 680 were in the indexable sitemap being advertised as
    -- fixtures still to come. They also read as "starting soon" on the
    -- in-play board while the match was long over.
    --
    -- Gated on kickoff alone, deliberately. Every upcoming match has a NULL
    -- live_state_at -- that is what upcoming means -- so reusing the staleness
    -- clause above would select every fixture on the calendar, including ones
    -- kicking off next month, and the sweep would mark the whole schedule
    -- unknown. Only the clock can say an upcoming match is overdue.
    OR (status = 'upcoming' AND kickoff_at < $2)
  )
ORDER BY COALESCE(live_state_at, kickoff_at) ASC
LIMIT $3`, staleBefore, kickoffBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stuck := []StuckMatch{}
	for rows.Next() {
		var (
			match     StuckMatch
			status    string
			liveState sql.NullTime
		)
		if err := rows.Scan(&match.MatchID, &status, &match.KickoffAt, &liveState); err != nil {
			return nil, err
		}
		match.State = Normalize(status)
		match.KickoffAt = match.KickoffAt.UTC()
		if liveState.Valid {
			match.LiveStateAt = liveState.Time.UTC()
		}
		stuck = append(stuck, match)
	}
	return stuck, rows.Err()
}

// MarkUnknown moves a match out of its live state.
//
// Three properties matter, and all three are in the WHERE clause rather than
// in application code, because the reaper is not the only writer:
//
//   - `status = $2` is a compare-and-set. A provider observation landing
//     between the read and this write wins, and the reaper reports no change
//     rather than overwriting a fresher truth with an absence of one.
//   - Only the status changes. Scores, elapsed minute and half-time scores are
//     left exactly as last observed. The row stops claiming to be in play; it
//     does not claim anything new.
//   - `live_state_at` is untouched, so the row keeps an honest record of when
//     it was last actually confirmed. Stamping it here would make an
//     administrative write look like a provider observation.
//
// The `fixtures` projection is updated in the same transaction. `fixtures.id`
// is `matches.id` -- the engine fixture shares the match's primary key -- and
// the catalogue reads `fixtures.status`, filtering "upcoming" as anything not
// in a finished-like state. Leaving the projection saying `second_half` would
// fix the live surface and leave the same stranded match sitting in the
// catalogue forever, which is half a fix presented as a whole one.
func (r Repository) MarkUnknown(ctx context.Context, matchID int64, from State, now time.Time) (bool, error) {
	if r.db == nil {
		return false, errors.New("match state DB is nil")
	}
	allowed, rejection := Allowed(from, StateUnknown)
	if !allowed {
		return false, errors.New("illegal transition: " + rejection.Reason)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `
UPDATE matches
SET status = $1
WHERE id = $2 AND status = $3`, string(StateUnknown), matchID, string(from))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		// Lost the compare-and-set: a real observation landed first and the
		// projection will follow it, not us.
		return false, tx.Commit()
	}

	// A match with no projection row is not an error: the UPDATE simply
	// affects nothing, and the match is still bounded.
	//
	// A projection *failure* is different and does roll back the whole
	// transaction, deliberately. Letting the match update stand while the
	// projection kept saying `second_half` would recreate exactly the split
	// state this method exists to prevent -- the live surface fixed, the
	// catalogue still stranded -- and it would do so silently. Failing whole
	// means the next sweep, two minutes later, tries again.
	//
	// updated_at comes from the sweep's clock rather than now(): the same
	// statement runs against SQLite in tests, which has no now(), and passing
	// the timestamp keeps the write deterministic either way.
	if _, err := tx.ExecContext(ctx, `
UPDATE fixtures
SET status = $1, updated_at = $3
WHERE id = $2`, string(StateUnknown), matchID, now.UTC()); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// InPlayStatuses is the SQL-facing list of states that claim a match is
// happening now. Exported so the demand query and the API read the same set as
// the reaper rather than each spelling it out.
func InPlayStatuses() []string {
	return []string{
		string(StateLive), string(StateHalftime), string(StateSecondHalf),
		string(StateExtraTime), string(StatePenalties), string(StateBreak),
	}
}

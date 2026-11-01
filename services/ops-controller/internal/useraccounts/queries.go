// Package useraccounts provides read-only, aggregate-only projections over the
// application's users and user_sessions tables for the operator read commands.
//
// It is a new package rather than an addition to any existing one because
// every other storage package in this service owns an ops_* table: this is the
// first read into the product's own account tables, and it is deliberately
// walled off so that the one rule that governs it is impossible to miss.
//
// # The rule: counts only, never identities
//
// Nothing in this package may select, return, or otherwise carry a value that
// identifies a person. Not an email, not email_normalized, not a name, not
// public_id, not ip_prefix_hash, not device_name, not user_agent, not
// token_hash, not country_code, not timezone. The identifying columns are not
// selected and then discarded — they are never named in a query at all, so
// there is no value in memory for a later change to accidentally render.
//
// This is not caution for its own sake. The only consumer is the Telegram ops
// bridge; Telegram cloud chats are not end-to-end encrypted, the ops-bridge
// spec (§18) forbids user PII in any message, and internal/evidence exists in
// this same service precisely because output from here has been shown to leak.
// An aggregate ("18 accounts are pending verification") carries no personal
// data and answers the operational question. A list ("the last 5 signups")
// answers the same question while exporting real people's identities into a
// third party's cloud, permanently, to satisfy curiosity.
//
// So: if a future change here wants to add a listing of recent signups, of
// currently-online users, or of anything else with one row per person, the
// answer is no. Add another count.
//
// Everything in this file is a bounded SELECT. There are no writes and no
// schema changes.
package useraccounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrRepositoryUnavailable mirrors the sentinel used by the other storage
// packages in this service.
var ErrRepositoryUnavailable = errors.New("user account repository unavailable")

// Queryer is a read-only, non-transactional seam over *sql.DB.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (Rows, error)
}

// Rows is the bounded multi-row cursor contract; *sql.Rows satisfies it.
type Rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

// SQLQueryer adapts *sql.DB to Queryer.
type SQLQueryer struct{ DB *sql.DB }

func (q SQLQueryer) QueryContext(ctx context.Context, query string, args ...any) (Rows, error) {
	if q.DB == nil {
		return nil, ErrRepositoryUnavailable
	}
	return q.DB.QueryContext(ctx, query, args...)
}

const (
	// NewAccountWindow is the "new users" window the owner asked for.
	NewAccountWindow = 24 * time.Hour

	// PresenceWindow is how recently a session must have been seen for its
	// user to count as actually present. A session can stay valid for weeks,
	// which says nothing about whether anybody is at the keyboard now.
	PresenceWindow = 15 * time.Minute

	// MaxStatusRows bounds the account_status breakdown. The column is
	// effectively an enum, so this is a guard rather than a limit anyone
	// should reach.
	MaxStatusRows = 16
)

// ---------------------------------------------------------------------------
// What "online" means here.
//
// "Online" is not a fact this schema records. There is no presence channel, no
// heartbeat table, and no logout event for a browser that was simply closed.
// The closest true statements the data supports are two different things, and
// this package reports both rather than picking one and calling it "online":
//
//	LiveSessionUsers  — users holding a session that is still valid right now:
//	                    not revoked, not past expires_at, and not past
//	                    absolute_expires_at. This is "logged in".
//	PresentUsers      — of those, the ones whose session has been seen within
//	                    PresenceWindow (user_sessions.last_seen_at). This is
//	                    the closest thing to "here now".
//
// Both are counted as DISTINCT user_id, never as sessions. One person with a
// phone and a laptop is one user, and reporting session rows as people inflates
// the figure by whatever the average device count happens to be — silently, and
// always upwards. LiveSessions is reported alongside, labelled as sessions, so
// the ratio is visible instead of hidden.
//
// guest_sessions is deliberately not read. That table is anonymous traffic;
// counting it here would answer "how many users do we have" with a number that
// includes people who have no account.
// ---------------------------------------------------------------------------

// liveSessionPredicate is the definition of a session that is still valid. It
// is a named constant so the test can assert it, and so a change to the
// meaning of "logged in" has exactly one place to happen.
const liveSessionPredicate = `s.revoked_at IS NULL
    AND s.expires_at > $1
    AND (s.absolute_expires_at IS NULL OR s.absolute_expires_at > $1)`

// liveSessionsSQL returns one row: distinct live users, live session rows, and
// distinct live users seen within the presence window.
//
// $1 is the as-of instant and $2 the presence cutoff. Both come from the
// caller's clock rather than the database's now(), so every number in one
// report describes the same instant and the window boundaries are testable.
// Skew against the database clock is sub-second on the same host and cannot
// move a count by a meaningful amount.
//
// Only user_id, the three lifetime columns and last_seen_at are named. No
// token_hash, no ip_prefix_hash, no device_name, no user_agent.
const liveSessionsSQL = `SELECT
    COUNT(DISTINCT s.user_id),
    COUNT(*),
    COUNT(DISTINCT s.user_id) FILTER (WHERE s.last_seen_at IS NOT NULL AND s.last_seen_at >= $2)
  FROM user_sessions s
  WHERE ` + liveSessionPredicate

// accountTotalsSQL returns one row: live accounts, soft-deleted accounts, and
// accounts created within the new-account window.
//
// $1 is the new-account cutoff, compared with >= so the boundary is inclusive.
// Soft-deleted rows are excluded from both the headline total and the new
// count, and reported on their own line instead of being dropped: an account
// that signed up and deleted itself is not growth, and hiding the number
// entirely makes a purge look like churn.
//
// Only deleted_at and created_at are named. No email, no email_normalized, no
// public_id, no country_code.
const accountTotalsSQL = `SELECT
    COUNT(*) FILTER (WHERE u.deleted_at IS NULL),
    COUNT(*) FILTER (WHERE u.deleted_at IS NOT NULL),
    COUNT(*) FILTER (WHERE u.deleted_at IS NULL AND u.created_at >= $1)
  FROM users u`

// accountStatusSQL breaks live accounts down by account_status, largest first.
//
// account_status is a lifecycle enum (active, pending_verification,
// deletion_pending), not personal data. It is here because a pile of
// unverified signups and a pile of real users are the same number on the
// headline and completely different situations underneath.
const accountStatusSQL = `SELECT
    COALESCE(NULLIF(u.account_status, ''), 'unknown'),
    COUNT(*)
  FROM users u
  WHERE u.deleted_at IS NULL
  GROUP BY 1
  ORDER BY COUNT(*) DESC, 1 ASC
  LIMIT $1`

// Sessions is the logged-in picture. Available is false when the session read
// failed or was never wired, so the caller renders "unavailable" for this
// section rather than a confident zero.
type Sessions struct {
	Available bool
	// LiveSessionUsers is DISTINCT user_id over valid sessions: people who
	// are logged in.
	LiveSessionUsers int64
	// LiveSessions is the session-row count behind LiveSessionUsers. It is
	// reported as sessions and must never be labelled as users.
	LiveSessions int64
	// PresentUsers is DISTINCT user_id whose session was seen within
	// PresenceWindow: the closest thing to "here now".
	PresentUsers int64
	// PresenceWindow is the window PresentUsers was measured over, so the
	// caller labels the number with its own definition instead of assuming.
	PresenceWindow time.Duration
}

// Accounts is the population picture.
type Accounts struct {
	Available bool
	// Total excludes soft-deleted accounts.
	Total int64
	// SoftDeleted is the count excluded from Total, reported rather than
	// silently dropped.
	SoftDeleted int64
	// New is the number of live accounts created within NewWindow.
	New int64
	// NewWindow is the window New was measured over.
	NewWindow time.Duration
}

// StatusCount is one account_status bucket.
type StatusCount struct {
	Status string
	Count  int64
}

// Breakdown is the account_status split over live accounts.
type Breakdown struct {
	Available bool
	Counts    []StatusCount
	// Truncated reports that MaxStatusRows was filled and further statuses
	// exist.
	Truncated bool
}

// Stats is the whole /users projection. Every field is a count; nothing here
// identifies a person.
type Stats struct {
	AsOf     time.Time
	Sessions Sessions
	Accounts Accounts
	Statuses Breakdown
}

// Queries is the read-only user projection API. It never writes.
type Queries struct {
	db  Queryer
	now func() time.Time
}

// NewQueries returns the read-only user query API. now may be nil, meaning
// time.Now; tests inject a fixed clock.
func NewQueries(db Queryer, now func() time.Time) (*Queries, error) {
	if db == nil {
		return nil, ErrRepositoryUnavailable
	}
	if now == nil {
		now = time.Now
	}
	return &Queries{db: db, now: now}, nil
}

// Stats reads all three sections.
//
// The sections degrade independently. A returned error reports that at least
// one section failed, and the returned Stats still carries every section that
// succeeded with its Available flag set — so a caller can render "unavailable"
// for the broken section and real numbers for the rest, rather than dropping
// the whole reply because one query timed out. A caller that wants
// all-or-nothing simply checks the error.
//
// An empty database is not an error: it is zeros.
func (q *Queries) Stats(ctx context.Context) (Stats, error) {
	if q == nil || q.db == nil {
		return Stats{}, ErrRepositoryUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Stats{}, err
	}

	asOf := q.now().UTC()
	stats := Stats{AsOf: asOf}
	var errs []error

	sessions, err := q.sessions(ctx, asOf)
	if err != nil {
		errs = append(errs, fmt.Errorf("live sessions: %w", err))
	} else {
		stats.Sessions = sessions
	}

	accounts, err := q.accounts(ctx, asOf)
	if err != nil {
		errs = append(errs, fmt.Errorf("account totals: %w", err))
	} else {
		stats.Accounts = accounts
	}

	statuses, err := q.statuses(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("account status breakdown: %w", err))
	} else {
		stats.Statuses = statuses
	}

	return stats, errors.Join(errs...)
}

func (q *Queries) sessions(ctx context.Context, asOf time.Time) (Sessions, error) {
	rows, err := q.db.QueryContext(ctx, liveSessionsSQL, asOf, asOf.Add(-PresenceWindow))
	if err != nil {
		return Sessions{}, err
	}
	defer rows.Close()

	out := Sessions{PresenceWindow: PresenceWindow}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Sessions{}, err
		}
		// An aggregate without GROUP BY always yields exactly one row. No row
		// means the read did not complete, which is not the same as zero
		// users online, and must not be reported as it.
		return Sessions{}, errors.New("useraccounts: live session aggregate returned no row")
	}
	if err := rows.Scan(&out.LiveSessionUsers, &out.LiveSessions, &out.PresentUsers); err != nil {
		return Sessions{}, err
	}
	if err := rows.Err(); err != nil {
		return Sessions{}, err
	}
	out.Available = true
	return out, nil
}

func (q *Queries) accounts(ctx context.Context, asOf time.Time) (Accounts, error) {
	rows, err := q.db.QueryContext(ctx, accountTotalsSQL, asOf.Add(-NewAccountWindow))
	if err != nil {
		return Accounts{}, err
	}
	defer rows.Close()

	out := Accounts{NewWindow: NewAccountWindow}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Accounts{}, err
		}
		return Accounts{}, errors.New("useraccounts: account totals aggregate returned no row")
	}
	if err := rows.Scan(&out.Total, &out.SoftDeleted, &out.New); err != nil {
		return Accounts{}, err
	}
	if err := rows.Err(); err != nil {
		return Accounts{}, err
	}
	out.Available = true
	return out, nil
}

func (q *Queries) statuses(ctx context.Context) (Breakdown, error) {
	rows, err := q.db.QueryContext(ctx, accountStatusSQL, MaxStatusRows)
	if err != nil {
		return Breakdown{}, err
	}
	defer rows.Close()

	out := Breakdown{Counts: make([]StatusCount, 0, MaxStatusRows)}
	for rows.Next() {
		var count StatusCount
		if err := rows.Scan(&count.Status, &count.Count); err != nil {
			return Breakdown{}, err
		}
		out.Counts = append(out.Counts, count)
	}
	if err := rows.Err(); err != nil {
		return Breakdown{}, err
	}
	out.Truncated = len(out.Counts) == MaxStatusRows
	out.Available = true
	return out, nil
}

package useraccounts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The read seam is exercised with a fake Queryer, matching the fake-DB
// convention used by internal/deployments and the other storage packages.

type queryRows struct {
	values [][]any
	index  int
	err    error
	closed int
}

func (r *queryRows) Next() bool {
	if r.err != nil || r.index >= len(r.values) {
		return false
	}
	r.index++
	return true
}

func (r *queryRows) Scan(dest ...any) error {
	values := r.values[r.index-1]
	if len(dest) != len(values) {
		return fmt.Errorf("scan arity: got %d dest for %d values", len(dest), len(values))
	}
	for i, value := range values {
		switch target := dest[i].(type) {
		case *string:
			*target = value.(string)
		case *int64:
			*target = value.(int64)
		case *time.Time:
			*target = value.(time.Time)
		default:
			return fmt.Errorf("unsupported scan dest %T", dest[i])
		}
	}
	return nil
}

func (r *queryRows) Err() error   { return r.err }
func (r *queryRows) Close() error { r.closed++; return nil }

// queryDB answers per statement, because Stats issues three different reads.
type queryDB struct {
	rows    map[string]*queryRows
	errs    map[string]error
	queries []string
	args    map[string][]any
}

func newQueryDB() *queryDB {
	return &queryDB{
		rows: map[string]*queryRows{},
		errs: map[string]error{},
		args: map[string][]any{},
	}
}

func (d *queryDB) QueryContext(_ context.Context, query string, args ...any) (Rows, error) {
	d.queries = append(d.queries, query)
	d.args[query] = args
	if err := d.errs[query]; err != nil {
		return nil, err
	}
	if rows, ok := d.rows[query]; ok {
		return rows, nil
	}
	return &queryRows{}, nil
}

var clock = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

func now() time.Time { return clock }

// fullDB is a database with plausible content in every section.
func fullDB() *queryDB {
	db := newQueryDB()
	db.rows[liveSessionsSQL] = &queryRows{values: [][]any{{int64(7), int64(11), int64(3)}}}
	db.rows[accountTotalsSQL] = &queryRows{values: [][]any{{int64(1204), int64(9), int64(23)}}}
	db.rows[accountStatusSQL] = &queryRows{values: [][]any{
		{"active", int64(1100)},
		{"pending_verification", int64(96)},
		{"deletion_pending", int64(8)},
	}}
	return db
}

func newTestQueries(t *testing.T, db Queryer) *Queries {
	t.Helper()
	queries, err := NewQueries(db, now)
	if err != nil {
		t.Fatal(err)
	}
	return queries
}

// TestStatsCountsDistinctUsersNotSessions is the load-bearing assertion of the
// "online" number. Two sessions belonging to one person are one person.
func TestStatsCountsDistinctUsersNotSessions(t *testing.T) {
	db := fullDB()
	// One user, two live sessions (phone and laptop), seen recently.
	db.rows[liveSessionsSQL] = &queryRows{values: [][]any{{int64(1), int64(2), int64(1)}}}
	queries := newTestQueries(t, db)

	stats, err := queries.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Sessions.LiveSessionUsers != 1 {
		t.Fatalf("two sessions for one person must be one user online, got %d",
			stats.Sessions.LiveSessionUsers)
	}
	if stats.Sessions.LiveSessions != 2 {
		t.Fatalf("session rows must still be reported as sessions, got %d", stats.Sessions.LiveSessions)
	}

	// The count itself must be DISTINCT in the SQL, not deduplicated
	// afterwards in Go where it could quietly stop happening.
	if !strings.Contains(liveSessionsSQL, "COUNT(DISTINCT s.user_id)") {
		t.Fatalf("live user count is not distinct: %s", liveSessionsSQL)
	}
	if db.rows[liveSessionsSQL].closed != 1 {
		t.Fatalf("rows closed %d times, want 1", db.rows[liveSessionsSQL].closed)
	}
}

// TestLiveSessionPredicateExcludesRevokedExpiredAndAbsolutelyExpired pins the
// definition of a live session.
//
// The filtering happens in Postgres, so the fake Queryer cannot exercise it by
// feeding rows — re-implementing the predicate in Go to "test" it would only
// test the copy. What is asserted instead is the predicate itself, which is
// the thing that would actually break, and that it is applied as a WHERE
// clause rather than left to the caller.
func TestLiveSessionPredicateExcludesRevokedExpiredAndAbsolutelyExpired(t *testing.T) {
	for _, required := range []string{
		"s.revoked_at IS NULL",
		"s.expires_at > $1",
		"(s.absolute_expires_at IS NULL OR s.absolute_expires_at > $1)",
	} {
		if !strings.Contains(liveSessionPredicate, required) {
			t.Fatalf("live session predicate is missing %q: %s", required, liveSessionPredicate)
		}
	}
	// Each clause is ANDed, so any one of them disqualifies a session.
	if strings.Contains(liveSessionPredicate, " OR s.revoked_at") ||
		!strings.Contains(liveSessionPredicate, "AND s.expires_at") {
		t.Fatalf("live session clauses must all be required: %s", liveSessionPredicate)
	}
	if !strings.Contains(liveSessionsSQL, "WHERE "+liveSessionPredicate) {
		t.Fatalf("the predicate is not applied to the read: %s", liveSessionsSQL)
	}
	// guest_sessions is anonymous traffic and must not be counted as users.
	for _, sql := range allStatements() {
		if strings.Contains(sql, "guest_sessions") {
			t.Fatalf("guest sessions are not users: %s", sql)
		}
	}
}

// TestSessionWindowsAreParameterisedFromTheInjectedClock covers the presence
// window boundary: "logged in" and "here now" are different questions and the
// second is measured against last_seen_at, inclusive at the cutoff.
func TestSessionWindowsAreParameterisedFromTheInjectedClock(t *testing.T) {
	db := fullDB()
	queries := newTestQueries(t, db)
	stats, err := queries.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	args := db.args[liveSessionsSQL]
	if len(args) != 2 {
		t.Fatalf("live session read takes as-of and presence cutoff, got %v", args)
	}
	if got := args[0].(time.Time); !got.Equal(clock) {
		t.Fatalf("as-of=%s want %s", got, clock)
	}
	if got, want := args[1].(time.Time), clock.Add(-PresenceWindow); !got.Equal(want) {
		t.Fatalf("presence cutoff=%s want %s", got, want)
	}
	if !strings.Contains(liveSessionsSQL, "s.last_seen_at >= $2") {
		t.Fatalf("presence must be inclusive at the cutoff: %s", liveSessionsSQL)
	}
	if stats.Sessions.PresenceWindow != PresenceWindow {
		t.Fatalf("the window must be reported with the number: %+v", stats.Sessions)
	}
	if stats.Sessions.PresentUsers != 3 || stats.Sessions.LiveSessionUsers != 7 {
		t.Fatalf("present and logged-in are distinct figures: %+v", stats.Sessions)
	}
}

// TestNewAccountWindowBoundaryIsTwentyFourHoursInclusive covers the new-user
// boundary the owner asked for.
func TestNewAccountWindowBoundaryIsTwentyFourHoursInclusive(t *testing.T) {
	db := fullDB()
	queries := newTestQueries(t, db)
	stats, err := queries.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	args := db.args[accountTotalsSQL]
	if len(args) != 1 {
		t.Fatalf("account totals take the new-account cutoff, got %v", args)
	}
	if got, want := args[0].(time.Time), clock.Add(-24*time.Hour); !got.Equal(want) {
		t.Fatalf("new-account cutoff=%s want %s", got, want)
	}
	if NewAccountWindow != 24*time.Hour {
		t.Fatalf("new-account window=%s want 24h", NewAccountWindow)
	}
	// An account created exactly on the boundary is inside the window.
	if !strings.Contains(accountTotalsSQL, "u.created_at >= $1") {
		t.Fatalf("the 24h boundary must be inclusive: %s", accountTotalsSQL)
	}
	if stats.Accounts.New != 23 || stats.Accounts.NewWindow != 24*time.Hour {
		t.Fatalf("new accounts not projected with their window: %+v", stats.Accounts)
	}
}

// TestSoftDeletedAccountsAreExcludedFromTheTotalAndReportedSeparately: the
// headline must not count deleted people, and the exclusion must not be silent.
func TestSoftDeletedAccountsAreExcludedFromTheTotalAndReportedSeparately(t *testing.T) {
	db := fullDB()
	queries := newTestQueries(t, db)
	stats, err := queries.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.Accounts.Total != 1204 {
		t.Fatalf("total=%d want the live-account count", stats.Accounts.Total)
	}
	if stats.Accounts.SoftDeleted != 9 {
		t.Fatalf("soft-deleted accounts must be reported, got %d", stats.Accounts.SoftDeleted)
	}
	if !strings.Contains(accountTotalsSQL, "COUNT(*) FILTER (WHERE u.deleted_at IS NULL)") ||
		!strings.Contains(accountTotalsSQL, "COUNT(*) FILTER (WHERE u.deleted_at IS NOT NULL)") {
		t.Fatalf("the total and the exclusion must be separate figures: %s", accountTotalsSQL)
	}
	// The status breakdown describes live accounts only, or it would not add
	// up against the headline.
	if !strings.Contains(accountStatusSQL, "WHERE u.deleted_at IS NULL") {
		t.Fatalf("status breakdown must exclude deleted accounts: %s", accountStatusSQL)
	}
	if len(stats.Statuses.Counts) != 3 || stats.Statuses.Counts[1].Status != "pending_verification" {
		t.Fatalf("status breakdown not projected: %+v", stats.Statuses)
	}
}

// TestStatsOnAnEmptyDatabaseIsZerosNotAnError. A brand new deployment has no
// users; that is a fact to report, not a failure.
func TestStatsOnAnEmptyDatabaseIsZerosNotAnError(t *testing.T) {
	db := newQueryDB()
	// Postgres returns exactly one row of zeros for an aggregate with no
	// GROUP BY, and no rows at all for the grouped breakdown.
	db.rows[liveSessionsSQL] = &queryRows{values: [][]any{{int64(0), int64(0), int64(0)}}}
	db.rows[accountTotalsSQL] = &queryRows{values: [][]any{{int64(0), int64(0), int64(0)}}}
	db.rows[accountStatusSQL] = &queryRows{}

	queries := newTestQueries(t, db)
	stats, err := queries.Stats(context.Background())
	if err != nil {
		t.Fatalf("an empty database is not an error: %v", err)
	}
	if !stats.Sessions.Available || !stats.Accounts.Available || !stats.Statuses.Available {
		t.Fatalf("every section read successfully and must be available: %+v", stats)
	}
	if stats.Sessions.LiveSessionUsers != 0 || stats.Accounts.Total != 0 {
		t.Fatalf("empty database must be zeros: %+v", stats)
	}
	if stats.Statuses.Counts == nil || len(stats.Statuses.Counts) != 0 || stats.Statuses.Truncated {
		t.Fatalf("no statuses must be an empty breakdown: %+v", stats.Statuses)
	}
	if !stats.AsOf.Equal(clock) {
		t.Fatalf("as-of=%s want the injected clock", stats.AsOf)
	}
}

// TestSectionsDegradeIndependently: one failing read must not erase the
// sections that worked, and must not be reported as a zero either.
func TestSectionsDegradeIndependently(t *testing.T) {
	boom := errors.New("connection reset")
	db := fullDB()
	db.errs[liveSessionsSQL] = boom

	queries := newTestQueries(t, db)
	stats, err := queries.Stats(context.Background())
	if !errors.Is(err, boom) {
		t.Fatalf("Stats err=%v want the underlying failure", err)
	}
	if stats.Sessions.Available {
		t.Fatalf("a failed section must not claim to be available: %+v", stats.Sessions)
	}
	if stats.Sessions.LiveSessionUsers != 0 {
		t.Fatalf("a failed section must carry no numbers: %+v", stats.Sessions)
	}
	if !stats.Accounts.Available || stats.Accounts.Total != 1204 {
		t.Fatalf("the sections that succeeded must survive: %+v", stats.Accounts)
	}
	if !stats.Statuses.Available {
		t.Fatalf("the status section must survive: %+v", stats.Statuses)
	}
}

// TestAggregateWithNoRowIsAnErrorNotZeroUsersOnline. "Nobody is online" and
// "the read did not complete" are different answers.
func TestAggregateWithNoRowIsAnErrorNotZeroUsersOnline(t *testing.T) {
	db := fullDB()
	db.rows[liveSessionsSQL] = &queryRows{}
	queries := newTestQueries(t, db)
	stats, err := queries.Stats(context.Background())
	if err == nil {
		t.Fatal("a missing aggregate row must be an error")
	}
	if stats.Sessions.Available {
		t.Fatalf("sessions must be unavailable, not zero: %+v", stats.Sessions)
	}
}

func TestStatsRequiresADatabaseAndRespectsCancellation(t *testing.T) {
	if _, err := NewQueries(nil, nil); !errors.Is(err, ErrRepositoryUnavailable) {
		t.Fatalf("nil database err=%v", err)
	}
	var nilQueries *Queries
	if _, err := nilQueries.Stats(context.Background()); !errors.Is(err, ErrRepositoryUnavailable) {
		t.Fatalf("nil receiver err=%v want ErrRepositoryUnavailable", err)
	}

	db := fullDB()
	queries := newTestQueries(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := queries.Stats(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stats err=%v want context.Canceled", err)
	}
	if len(db.queries) != 0 {
		t.Fatalf("a cancelled context must issue no query: %v", db.queries)
	}
}

func TestStatusBreakdownIsBoundedAndReportsTruncation(t *testing.T) {
	rows := &queryRows{}
	for i := 0; i < MaxStatusRows; i++ {
		rows.values = append(rows.values, []any{fmt.Sprintf("status_%d", i), int64(i)})
	}
	db := fullDB()
	db.rows[accountStatusSQL] = rows

	queries := newTestQueries(t, db)
	stats, err := queries.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Statuses.Truncated {
		t.Fatalf("filling the limit must report truncation: %+v", stats.Statuses)
	}
	if got := db.args[accountStatusSQL]; len(got) != 1 || got[0].(int) != MaxStatusRows {
		t.Fatalf("the breakdown limit must be a bound parameter, got %v", got)
	}
}

// allStatements is every SQL constant in the package. The read-only and
// no-PII guards run over all of them so a new query cannot skip them.
func allStatements() map[string]string {
	return map[string]string{
		"liveSessionsSQL":  liveSessionsSQL,
		"accountTotalsSQL": accountTotalsSQL,
		"accountStatusSQL": accountStatusSQL,
	}
}

func TestEveryStatementIsSelectOnlyAndParameterised(t *testing.T) {
	for name, sql := range allStatements() {
		upper := strings.ToUpper(sql)
		for _, forbidden := range []string{
			"INSERT ", "UPDATE ", "DELETE ", "ALTER ", "DROP ", "TRUNCATE ", "FOR UPDATE", "FOR SHARE",
		} {
			if strings.Contains(upper, forbidden) {
				t.Fatalf("%s must be read-only, found %q", name, forbidden)
			}
		}
		if !strings.HasPrefix(strings.TrimSpace(upper), "SELECT") {
			t.Fatalf("%s does not start with SELECT: %s", name, sql)
		}
		if strings.Contains(sql, "%s") || strings.Contains(sql, "%d") || strings.Contains(sql, "%v") {
			t.Fatalf("%s looks string-built rather than parameterised", name)
		}
	}
	// The only statement that returns more than one row is bounded.
	if !strings.Contains(accountStatusSQL, "LIMIT $1") {
		t.Fatalf("the only multi-row read is not bounded: %s", accountStatusSQL)
	}
}

// TestNoStatementNamesAPersonallyIdentifyingColumn is the standing guard on
// the rule this package exists to enforce. PII columns are not selected and
// discarded; they are never named, so no value ever reaches memory that a
// later change could render into a Telegram chat.
func TestNoStatementNamesAPersonallyIdentifyingColumn(t *testing.T) {
	forbidden := []string{
		"email", "password", "public_id", "token_hash", "ip_prefix_hash",
		"device_name", "user_agent", "country_code", "timezone", "language",
		"deletion_reason", "deleted_by_admin_id", "marketing_consent",
	}
	for name, sql := range allStatements() {
		lower := strings.ToLower(sql)
		for _, column := range forbidden {
			if strings.Contains(lower, column) {
				t.Fatalf("%s names the identifying column %q; this package reports counts only", name, column)
			}
		}
		// Nothing may select whole rows either: SELECT * would pull every
		// column named above straight back in.
		if strings.Contains(sql, "SELECT *") || strings.Contains(sql, "u.*") || strings.Contains(sql, "s.*") {
			t.Fatalf("%s selects whole rows: %s", name, sql)
		}
	}
}

// TestOnlyCountsAndStatusLabelsLeaveThePackage checks the return shape rather
// than the queries: no exported field may carry free text from a user.
func TestOnlyCountsAndStatusLabelsLeaveThePackage(t *testing.T) {
	db := fullDB()
	queries := newTestQueries(t, db)
	stats, err := queries.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rendered := fmt.Sprintf("%+v", stats)
	if strings.Contains(rendered, "@") {
		t.Fatalf("no value returned by this package may look like an address: %s", rendered)
	}
	for _, count := range stats.Statuses.Counts {
		// account_status is a lifecycle enum. Anything long or address-shaped
		// arriving in that column is not a status.
		if len(count.Status) > 32 || strings.Contains(count.Status, "@") {
			t.Fatalf("status label %q is not a lifecycle status", count.Status)
		}
	}
}

package provider

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The read seam is exercised with a fake Queryer, matching the fake-DB
// convention used by the other read-only projection packages in this service.

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
		case *bool:
			*target = value.(bool)
		case *int64:
			*target = value.(int64)
		case *time.Time:
			*target = value.(time.Time)
		case *sql.NullTime:
			*target = value.(sql.NullTime)
		default:
			return fmt.Errorf("unsupported scan dest %T", dest[i])
		}
	}
	return nil
}

func (r *queryRows) Err() error   { return r.err }
func (r *queryRows) Close() error { r.closed++; return nil }

// queryDB answers each successive QueryContext from its script, so the two
// reads inside Today can return different row sets.
type queryDB struct {
	script  []*queryRows
	err     error
	queries []string
	args    [][]any
}

func (d *queryDB) QueryContext(_ context.Context, query string, args ...any) (Rows, error) {
	d.queries = append(d.queries, query)
	d.args = append(d.args, args)
	if d.err != nil {
		return nil, d.err
	}
	if len(d.script) == 0 {
		return &queryRows{}, nil
	}
	rows := d.script[0]
	d.script = d.script[1:]
	return rows, nil
}

var clock = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

func now() time.Time { return clock }

func newTestQueries(t *testing.T, db Queryer) *Queries {
	t.Helper()
	queries, err := NewQueries(db, now)
	if err != nil {
		t.Fatal(err)
	}
	return queries
}

// quotaRow builds one provider_rate_limits row in the column order usageSQL
// selects.
func quotaRow(provider string, dailyLimit, dailyRemaining, minuteLimit, minuteRemaining int64,
	throttle sql.NullTime, updatedAgo time.Duration) []any {
	return []any{
		provider, strings.ToUpper(provider), true,
		dailyLimit, dailyRemaining, minuteLimit, minuteRemaining,
		int64(1500), int64(15),
		throttle, clock.Add(-updatedAgo),
	}
}

func noThrottle() sql.NullTime { return sql.NullTime{} }

// zeroThrottle is the real shape of a never-throttled row in this database:
// throttle_until is not NULL, it is the zero timestamp.
func zeroThrottle() sql.NullTime {
	return sql.NullTime{Valid: true, Time: time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func findQuota(t *testing.T, usage Usage, provider string) Quota {
	t.Helper()
	for _, p := range usage.Providers {
		if p.Provider == provider {
			return p
		}
	}
	t.Fatalf("provider %q not in usage: %+v", provider, usage.Providers)
	return Quota{}
}

func TestUsageDerivesUsedFromLimitMinusRemaining(t *testing.T) {
	db := &queryDB{script: []*queryRows{{values: [][]any{
		quotaRow("api-football", 100, 12, 10, 9, zeroThrottle(), 2*time.Minute),
	}}}}

	usage, err := newTestQueries(t, db).Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p := findQuota(t, usage, "api-football")
	if !p.DailyKnown || p.DailyUsed != 88 || p.DailyLimit != 100 {
		t.Fatalf("daily usage must be limit-remaining: %+v", p)
	}
	if p.DailyPercent != 88 {
		t.Fatalf("daily percent=%v want 88", p.DailyPercent)
	}
	if !p.MinuteKnown || p.MinuteUsed != 1 {
		t.Fatalf("minute usage must be derived the same way: %+v", p)
	}
	if p.DailyReserve != 1500 || p.MinuteReserve != 15 {
		t.Fatalf("reserve not projected: %+v", p)
	}
	if p.Throttled || !p.ThrottledUntil.IsZero() {
		t.Fatalf("a zero throttle_until is not an active throttle: %+v", p)
	}
	if p.Stale {
		t.Fatalf("counters 2 minutes old are not stale: %+v", p)
	}
	if db.script != nil && len(db.script) != 0 {
		t.Fatalf("unread rows remain")
	}
}

// The whole point of the unknown branch: a provider with no reported ceiling
// must not render as "0% used", which reads as "plenty left".
func TestUsageUnknownDailyLimitLeavesThePercentageUndefined(t *testing.T) {
	for _, tc := range []struct {
		name      string
		limit     int64
		remaining int64
	}{
		{"zero limit", 0, 0},
		{"zero limit with a balance", 0, 4},
		{"negative limit", -1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := &queryDB{script: []*queryRows{{values: [][]any{
				quotaRow("football-data-org", tc.limit, tc.remaining, 10, 4, noThrottle(), time.Minute),
			}}}}
			usage, err := newTestQueries(t, db).Usage(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			p := findQuota(t, usage, "football-data-org")
			if p.DailyKnown {
				t.Fatalf("a limit of %d cannot yield a known percentage: %+v", tc.limit, p)
			}
			if p.DailyPercent != 0 || p.DailyUsed != 0 {
				t.Fatalf("undefined usage must stay at the zero value, flagged unknown: %+v", p)
			}
			if p.UnderPressure() {
				t.Fatalf("an unknown ceiling is not pressure: %+v", p)
			}
			// The minute window is independent and stays known.
			if !p.MinuteKnown || p.MinuteUsed != 6 {
				t.Fatalf("minute window must be unaffected: %+v", p)
			}
		})
	}
}

func TestUsageFlagsAProviderAtOrAboveTheWarningThreshold(t *testing.T) {
	db := &queryDB{script: []*queryRows{{values: [][]any{
		quotaRow("api-football", 100, 15, 10, 10, noThrottle(), time.Minute), // exactly 85%
		quotaRow("football-data-org", 100, 16, 10, 10, noThrottle(), time.Minute),
	}}}}
	usage, err := newTestQueries(t, db).Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	at := findQuota(t, usage, "api-football")
	if !at.UnderPressure() {
		t.Fatalf("%.1f%% must be flagged at threshold %.0f", at.DailyPercent, WarnPercent)
	}
	below := findQuota(t, usage, "football-data-org")
	if below.UnderPressure() {
		t.Fatalf("%.1f%% is below the threshold and must not be flagged", below.DailyPercent)
	}
	pressured := usage.Pressured()
	if len(pressured) != 1 || pressured[0].Provider != "api-football" {
		t.Fatalf("Pressured must list only the flagged provider: %+v", pressured)
	}
}

func TestUsageReportsAnActiveThrottleAndIgnoresAnExpiredOne(t *testing.T) {
	active := sql.NullTime{Valid: true, Time: clock.Add(4 * time.Minute)}
	expired := sql.NullTime{Valid: true, Time: clock.Add(-time.Minute)}
	db := &queryDB{script: []*queryRows{{values: [][]any{
		quotaRow("api-football", 100, 1, 10, 0, active, time.Minute),
		quotaRow("football-data-org", 100, 50, 10, 5, expired, time.Minute),
	}}}}
	usage, err := newTestQueries(t, db).Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	throttled := findQuota(t, usage, "api-football")
	if !throttled.Throttled || throttled.ThrottleRemaining != 4*time.Minute {
		t.Fatalf("active throttle not reported: %+v", throttled)
	}
	if past := findQuota(t, usage, "football-data-org"); past.Throttled {
		t.Fatalf("a throttle that has expired is not in force: %+v", past)
	}
	list := usage.Throttled()
	if len(list) != 1 || list[0].Provider != "api-football" {
		t.Fatalf("Throttled must list only the live throttle: %+v", list)
	}
}

func TestUsageSurfacesStaleCounters(t *testing.T) {
	db := &queryDB{script: []*queryRows{{values: [][]any{
		quotaRow("api-football", 100, 99, 10, 9, noThrottle(), 6*time.Hour),
		quotaRow("football-data-org", 100, 99, 10, 9, noThrottle(), StaleAfter-time.Minute),
	}}}}
	usage, err := newTestQueries(t, db).Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stale := findQuota(t, usage, "api-football")
	if !stale.Stale || stale.Age != 6*time.Hour {
		t.Fatalf("counters six hours old must be reported stale with their age: %+v", stale)
	}
	if fresh := findQuota(t, usage, "football-data-org"); fresh.Stale {
		t.Fatalf("counters inside the staleness window are current: %+v", fresh)
	}
}

// A clock that has drifted behind updated_at must not produce a negative age.
func TestUsageNeverReportsANegativeAge(t *testing.T) {
	db := &queryDB{script: []*queryRows{{values: [][]any{
		quotaRow("api-football", 100, 99, 10, 9, noThrottle(), -time.Hour),
	}}}}
	usage, err := newTestQueries(t, db).Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p := findQuota(t, usage, "api-football"); p.Age != 0 || p.Stale {
		t.Fatalf("age must clamp at zero: %+v", p)
	}
}

// A balance above the ceiling means the window reset between header reads.
func TestUsageClampsUsedWhenTheWindowJustReset(t *testing.T) {
	db := &queryDB{script: []*queryRows{{values: [][]any{
		quotaRow("api-football", 100, 140, 10, 10, noThrottle(), time.Minute),
	}}}}
	usage, err := newTestQueries(t, db).Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p := findQuota(t, usage, "api-football")
	if p.DailyUsed != 0 || p.DailyPercent != 0 || !p.DailyKnown {
		t.Fatalf("a balance above the ceiling means nothing spent, not negative usage: %+v", p)
	}
}

func TestUsageEmptyTableIsAnEmptyProjectionNotAnError(t *testing.T) {
	db := &queryDB{script: []*queryRows{{}}}
	usage, err := newTestQueries(t, db).Usage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if usage.Providers == nil || len(usage.Providers) != 0 || usage.Truncated {
		t.Fatalf("no rows must be an empty usage: %+v", usage)
	}
	if len(usage.Throttled()) != 0 || len(usage.Pressured()) != 0 {
		t.Fatal("empty usage has nothing throttled or pressured")
	}
	if usage.AsOf != clock {
		t.Fatalf("AsOf=%v want the injected clock", usage.AsOf)
	}
}

func TestTodayCountsRequestsErrorsCostAndCategories(t *testing.T) {
	db := &queryDB{script: []*queryRows{
		{values: [][]any{
			{"api-football", int64(412), int64(9), int64(430)},
			{"football-data-org", int64(11), int64(0), int64(11)},
		}},
		{values: [][]any{
			{"api-football", "rate_limit", int64(7)},
			{"api-football", "route", int64(2)},
		}},
	}}
	today, err := newTestQueries(t, db).Today(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	af, ok := today.Find("api-football")
	if !ok {
		t.Fatalf("api-football missing: %+v", today.Providers)
	}
	if af.Requests != 412 || af.Errors != 9 || af.QuotaCost != 430 {
		t.Fatalf("day totals not projected: %+v", af)
	}
	if len(af.Categories) != 2 || af.Categories[0].Category != "rate_limit" || af.Categories[0].Count != 7 {
		t.Fatalf("error breakdown not attached: %+v", af.Categories)
	}
	// quota_cost is what the provider bills and need not equal the request
	// count; the projection must not conflate them.
	if af.QuotaCost == af.Requests {
		t.Fatalf("cost was collapsed into the request count: %+v", af)
	}
	other, _ := today.Find("football-data-org")
	if len(other.Categories) != 0 {
		t.Fatalf("a provider with no errors gets no buckets: %+v", other)
	}
	if today.Since != time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("Since=%v want the start of the clock's day", today.Since)
	}
	if today.Truncated || today.CategoriesTruncated {
		t.Fatalf("two rows do not fill either bound: %+v", today)
	}
}

// An error category whose provider fell outside the bounded totals read is
// still an error and must not vanish.
func TestTodayKeepsCategoriesForAProviderMissingFromTheTotals(t *testing.T) {
	db := &queryDB{script: []*queryRows{
		{},
		{values: [][]any{{"thesportsdb", "provider_transport", int64(3)}}},
	}}
	today, err := newTestQueries(t, db).Today(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	orphan, ok := today.Find("thesportsdb")
	if !ok || len(orphan.Categories) != 1 || orphan.Requests != 0 {
		t.Fatalf("orphaned category dropped: %+v", today.Providers)
	}
}

func TestTodayEmptyTablesAreAnEmptyProjection(t *testing.T) {
	db := &queryDB{script: []*queryRows{{}, {}}}
	today, err := newTestQueries(t, db).Today(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if today.Providers == nil || len(today.Providers) != 0 {
		t.Fatalf("no rows must be an empty projection: %+v", today)
	}
	if _, ok := today.Find("api-football"); ok {
		t.Fatal("Find must report a miss on an empty projection")
	}
}

func TestReadsRespectCancellationAndSurfaceErrors(t *testing.T) {
	if _, err := NewQueries(nil, nil); !errors.Is(err, ErrRepositoryUnavailable) {
		t.Fatalf("nil database err=%v", err)
	}
	var nilQueries *Queries
	if _, err := nilQueries.Usage(context.Background()); !errors.Is(err, ErrRepositoryUnavailable) {
		t.Fatalf("a nil receiver must degrade, not panic: %v", err)
	}
	if _, err := nilQueries.Today(context.Background()); !errors.Is(err, ErrRepositoryUnavailable) {
		t.Fatalf("a nil receiver must degrade, not panic: %v", err)
	}

	db := &queryDB{}
	queries := newTestQueries(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := queries.Usage(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Usage err=%v want context.Canceled", err)
	}
	if _, err := queries.Today(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Today err=%v want context.Canceled", err)
	}
	if len(db.queries) != 0 {
		t.Fatalf("a cancelled context must issue no query: %v", db.queries)
	}

	boom := errors.New("connection reset")
	failing := newTestQueries(t, &queryDB{err: boom})
	if _, err := failing.Usage(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Usage err=%v want the underlying failure", err)
	}
	if _, err := failing.Today(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Today err=%v want the underlying failure", err)
	}
}

func TestRowsAreAlwaysClosed(t *testing.T) {
	usageRows := &queryRows{values: [][]any{quotaRow("api-football", 100, 99, 10, 9, noThrottle(), time.Minute)}}
	db := &queryDB{script: []*queryRows{usageRows}}
	if _, err := newTestQueries(t, db).Usage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if usageRows.closed != 1 {
		t.Fatalf("usage rows closed %d times, want 1", usageRows.closed)
	}

	totals := &queryRows{values: [][]any{{"api-football", int64(1), int64(0), int64(1)}}}
	categories := &queryRows{}
	if _, err := newTestQueries(t, &queryDB{script: []*queryRows{totals, categories}}).
		Today(context.Background()); err != nil {
		t.Fatal(err)
	}
	if totals.closed != 1 || categories.closed != 1 {
		t.Fatalf("today rows closed totals=%d categories=%d, want 1 each", totals.closed, categories.closed)
	}
}

func TestQueriesAreReadOnlyBoundedAndParameterised(t *testing.T) {
	all := map[string]string{
		"usage":        usageSQL,
		"today totals": todayTotalsSQL,
		"today errors": todayErrorsSQL,
	}
	for name, query := range all {
		upper := strings.ToUpper(query)
		for _, forbidden := range []string{"INSERT ", "UPDATE ", "DELETE ", "ALTER ", "DROP ", "TRUNCATE ", "FOR UPDATE"} {
			if strings.Contains(upper, forbidden) {
				t.Fatalf("%s must be read-only, found %q", name, forbidden)
			}
		}
		if !strings.Contains(query, "LIMIT $1") {
			t.Fatalf("%s is not bounded by a bind parameter: %s", name, query)
		}
		if strings.Contains(query, "%s") || strings.Contains(query, "%d") {
			t.Fatalf("%s looks string-built rather than parameterised", name)
		}
	}
	// The day bound must come from the database's own clock, not from a value
	// interpolated by the caller.
	for _, query := range []string{todayTotalsSQL, todayErrorsSQL} {
		if !strings.Contains(query, "date_trunc('day', now())") {
			t.Fatalf("day-bounded read is missing its boundary: %s", query)
		}
	}
}

func TestBoundsArePassedAsParameters(t *testing.T) {
	db := &queryDB{}
	queries := newTestQueries(t, db)
	if _, err := queries.Usage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := db.args[0][0].(int); got != ProviderLimit {
		t.Fatalf("usage limit=%d want %d", got, ProviderLimit)
	}
	db.args = nil
	if _, err := queries.Today(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := db.args[0][0].(int); got != ProviderLimit {
		t.Fatalf("totals limit=%d want %d", got, ProviderLimit)
	}
	if got := db.args[1][0].(int); got != CategoryLimit {
		t.Fatalf("category limit=%d want %d", got, CategoryLimit)
	}
}

// Verified against the local database on 2026-09-05: provider_rate_limits
// holds api-football and football-data-org, football-data-org really does have
// daily_limit = 0 (the undefined-percentage case), and api-football's
// throttle_until is the zero timestamp rather than NULL.

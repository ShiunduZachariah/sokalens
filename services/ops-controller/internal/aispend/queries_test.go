package aispend

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The read seam is exercised with a fake Queryer, matching how the write path
// is tested in postgres_test.go: no driver, no live database.

type spendRow struct {
	values []any
	err    error
}

func (r spendRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return fmt.Errorf("scan arity: got %d dest for %d values", len(dest), len(r.values))
	}
	for i, value := range r.values {
		target, ok := dest[i].(*int64)
		if !ok {
			return fmt.Errorf("unsupported scan dest %T", dest[i])
		}
		*target = value.(int64)
	}
	return nil
}

type spendDB struct {
	row     Row
	queries []string
	args    [][]any
}

func (d *spendDB) QueryRowContext(_ context.Context, query string, args ...any) Row {
	d.queries = append(d.queries, query)
	d.args = append(d.args, args)
	return d.row
}

const monthlyCap = int64(1_000_000)

// 4 September 2026, 12:00 UTC: the 4th day of a 30-day month.
func spendClock() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) }

func newSpendQueries(t *testing.T, db Queryer, capMicros int64) *Queries {
	t.Helper()
	queries, err := NewQueries(db, SpendConfig{
		MonthlyCapMicros: capMicros,
		Location:         time.UTC,
		Now:              spendClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return queries
}

func TestSpendProjectsTodayMonthToDateAndProjection(t *testing.T) {
	db := &spendDB{row: spendRow{values: []any{int64(40_000), int64(120_000), int64(15_000)}}}
	queries := newSpendQueries(t, db, monthlyCap)

	snapshot, err := queries.Spend(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TodayMicros != 40_000 || snapshot.MonthToDateMicros != 120_000 ||
		snapshot.ActiveReservedMicros != 15_000 {
		t.Fatalf("amounts not projected: %+v", snapshot)
	}
	// 120_000 over 4 elapsed days of a 30-day month is 900_000 projected.
	if snapshot.ProjectedMonthEndMicros != 900_000 {
		t.Fatalf("projection=%d want 900000", snapshot.ProjectedMonthEndMicros)
	}
	if snapshot.MonthlyCapMicros != monthlyCap || snapshot.PercentOfCap != 12 {
		t.Fatalf("cap/percent: %+v", snapshot)
	}
	if snapshot.ProjectedPercentOfCap != 90 {
		t.Fatalf("projected percent=%v want 90", snapshot.ProjectedPercentOfCap)
	}
	// The tier is spend-based on month-to-date, not on the projection: 12%
	// of the cap is NORMAL even though the run rate points at 90%.
	if snapshot.Tier != TierNormal {
		t.Fatalf("tier=%s want NORMAL", snapshot.Tier)
	}
	if !snapshot.ReservedBasis {
		t.Fatalf("the reserved-cost basis must be declared on the snapshot")
	}
}

func TestSpendBoundsTheQueryToTheCurrentMonthAndDay(t *testing.T) {
	db := &spendDB{row: spendRow{values: []any{int64(0), int64(0), int64(0)}}}
	queries := newSpendQueries(t, db, monthlyCap)
	snapshot, err := queries.Spend(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	monthStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	dayStart := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	monthEnd := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if !snapshot.MonthStart.Equal(monthStart) || !snapshot.DayStart.Equal(dayStart) ||
		!snapshot.MonthEnd.Equal(monthEnd) {
		t.Fatalf("period boundaries: %+v", snapshot)
	}
	if !db.args[0][0].(time.Time).Equal(monthStart) ||
		!db.args[0][1].(time.Time).Equal(dayStart) ||
		!db.args[0][2].(time.Time).Equal(monthEnd) {
		t.Fatalf("query bounds not parameterised from the period: %v", db.args[0])
	}
}

func TestSpendZeroLedgerIsNormalNotAnError(t *testing.T) {
	db := &spendDB{row: spendRow{values: []any{int64(0), int64(0), int64(0)}}}
	queries := newSpendQueries(t, db, monthlyCap)
	snapshot, err := queries.Spend(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.TodayMicros != 0 || snapshot.MonthToDateMicros != 0 ||
		snapshot.ProjectedMonthEndMicros != 0 || snapshot.PercentOfCap != 0 ||
		snapshot.Tier != TierNormal {
		t.Fatalf("empty ledger: %+v", snapshot)
	}
}

func TestSpendNoRowsIsTreatedAsZeroSpend(t *testing.T) {
	db := &spendDB{row: spendRow{err: sql.ErrNoRows}}
	queries := newSpendQueries(t, db, monthlyCap)
	snapshot, err := queries.Spend(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.MonthToDateMicros != 0 || snapshot.Tier != TierNormal {
		t.Fatalf("no rows: %+v", snapshot)
	}
}

func TestTierBoundaries(t *testing.T) {
	for _, testCase := range []struct {
		percent float64
		want    SpendTier
	}{
		{0, TierNormal},
		{59.99, TierNormal},
		{60, TierWatch},
		{74.99, TierWatch},
		{75, TierWarning},
		{84.99, TierWarning},
		{85, TierCritical},
		{94.99, TierCritical},
		{95, TierCritical},
		{95.01, TierExhaustion},
		{150, TierExhaustion},
	} {
		t.Run(fmt.Sprintf("%.2f%%", testCase.percent), func(t *testing.T) {
			if got := TierFor(testCase.percent); got != testCase.want {
				t.Fatalf("TierFor(%v)=%s want=%s", testCase.percent, got, testCase.want)
			}
		})
	}
}

func TestSpendTierIsDerivedFromMonthToDateAgainstTheCap(t *testing.T) {
	for _, testCase := range []struct {
		name              string
		monthToDateMicros int64
		want              SpendTier
	}{
		{"below watch", 599_999, TierNormal},
		{"at watch", 600_000, TierWatch},
		{"at warning", 750_000, TierWarning},
		{"at critical", 850_000, TierCritical},
		{"at the exhaustion boundary", 950_000, TierCritical},
		{"past exhaustion", 950_001, TierExhaustion},
		{"over the cap", 1_400_000, TierExhaustion},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := &spendDB{row: spendRow{values: []any{int64(0), testCase.monthToDateMicros, int64(0)}}}
			queries := newSpendQueries(t, db, monthlyCap)
			snapshot, err := queries.Spend(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Tier != testCase.want {
				t.Fatalf("mtd=%d percent=%v tier=%s want=%s",
					testCase.monthToDateMicros, snapshot.PercentOfCap, snapshot.Tier, testCase.want)
			}
		})
	}
}

func TestProjectionIsStableAndCannotExplodeEarlyInTheMonth(t *testing.T) {
	monthStart := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	monthEnd := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	// One minute into the month, today still counts as one whole elapsed day,
	// so the projection is 30x, not a division by an infinitesimal fraction.
	firstMinute := monthStart.Add(time.Minute)
	if got := projectMonthEnd(1_000, firstMinute, monthStart, monthEnd); got != 30_000 {
		t.Fatalf("first-minute projection=%d want 30000", got)
	}
	// On the last day the projection is simply what has been spent.
	lastDay := time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)
	if got := projectMonthEnd(1_000, lastDay, monthStart, monthEnd); got != 1_000 {
		t.Fatalf("last-day projection=%d want 1000", got)
	}
	if got := projectMonthEnd(0, firstMinute, monthStart, monthEnd); got != 0 {
		t.Fatalf("zero spend must project zero, got %d", got)
	}
}

func TestSpendRequiresAPositiveCapAndALiveDatabase(t *testing.T) {
	if _, err := NewQueries(nil, SpendConfig{MonthlyCapMicros: monthlyCap}); !errors.Is(err, ErrRepositoryUnavailable) {
		t.Fatalf("nil database err=%v", err)
	}
	for _, capMicros := range []int64{0, -1} {
		if _, err := NewQueries(&spendDB{}, SpendConfig{MonthlyCapMicros: capMicros}); !errors.Is(err, ErrInvalidBudgetConfig) {
			t.Fatalf("cap=%d err=%v want ErrInvalidBudgetConfig", capMicros, err)
		}
	}
}

func TestSpendRespectsContextCancellationAndSurfacesErrors(t *testing.T) {
	db := &spendDB{row: spendRow{values: []any{int64(0), int64(0), int64(0)}}}
	queries := newSpendQueries(t, db, monthlyCap)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := queries.Spend(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Spend err=%v want context.Canceled", err)
	}
	if len(db.queries) != 0 {
		t.Fatalf("a cancelled context must issue no query: %v", db.queries)
	}

	boom := errors.New("connection reset")
	failing := newSpendQueries(t, &spendDB{row: spendRow{err: boom}}, monthlyCap)
	if _, err := failing.Spend(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Spend err=%v want the underlying failure", err)
	}
}

func TestSpendQueryIsSelectOnlyBoundedAndParameterised(t *testing.T) {
	upper := strings.ToUpper(spendSQL)
	for _, forbidden := range []string{"INSERT ", "UPDATE ", "DELETE ", "ALTER ", "DROP ", "FOR UPDATE"} {
		if strings.Contains(upper, forbidden) {
			t.Fatalf("spend read must be read-only, found %q", forbidden)
		}
	}
	// The month window is the bound: without it this would scan every
	// reservation ever taken.
	if !strings.Contains(spendSQL, "created_at >= $1 AND created_at < $3") {
		t.Fatalf("spend read is not bounded to the current month: %s", spendSQL)
	}
	if strings.Contains(spendSQL, "%s") || strings.Contains(spendSQL, "%d") {
		t.Fatalf("spend read looks string-built rather than parameterised")
	}
}

// Not verified against a live database. Also note the accounting basis in
// queries.go: settlement adjusts ops_ai_budget_usage without writing the
// actual cost back to the reservation row, so month-to-date here is the
// worst-case reserved amount, an upper bound on settled spend.

// Package aispend is the read-only projection of AI spend behind /budget.
//
// It lives apart from internal/durablebudget deliberately. That package holds
// the budget *enforcement* machinery and imports packages/ai-contract, a
// sibling Go module resolved through go.work. The deployable ops-controller
// image builds from services/ops-controller alone, so anything the binary
// reaches must stay inside this module — importing durablebudget from
// cmd/ops-controller broke the image build with "no required module provides
// package .../packages/ai-contract".
//
// Separating them is also the better shape: a chat command that reports spend
// needs a projection, not the ability to reserve or enforce against a budget.
package aispend

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Read-only AI spend projections backing the /budget command.
//
// Everything in this file is a bounded SELECT: no reservations are taken,
// settled, released, or reclaimed. Callers receive typed structs and render
// them; nothing here knows about any transport.
//
// Accounting basis, and its one important caveat. The ledger's only
// time-stamped per-charge rows are ops_ai_budget_reservations
// (reserved_cost_micros, created_at). ops_ai_budget_usage.cost_micros is a
// running per-incident total with no time dimension, so it cannot be bucketed
// into a day or a month. These reads therefore report *reserved* spend, which
// is the conservative worst-case charge taken at reservation time. Settlement
// lowers ops_ai_budget_usage but does not write the actual cost back to the
// reservation row, so month-to-date here is an upper bound on actual spend.
// Fail-safe for a budget guard, but it is an upper bound, not the truth.

// Queryer is a read-only, non-transactional seam over *sql.DB, matching the
// database/sql-compatible DB/Tx seam the write path already uses.
// Row is one scannable result row.
type Row interface{ Scan(...any) error }

type Queryer interface {
	QueryRowContext(context.Context, string, ...any) Row
}

// SQLQueryer adapts *sql.DB to Queryer.
type SQLQueryer struct{ DB *sql.DB }

func (q SQLQueryer) QueryRowContext(ctx context.Context, query string, args ...any) Row {
	if q.DB == nil {
		return errRow{err: ErrRepositoryUnavailable}
	}
	return q.DB.QueryRowContext(ctx, query, args...)
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// ErrInvalidBudgetConfig is returned when the configured monthly cap is not a
// positive amount. Tiers are percentages of the cap, so there is no meaningful
// answer without one.
var (
	ErrInvalidBudgetConfig = errors.New("invalid AI budget configuration")
	// ErrRepositoryUnavailable mirrors the enforcement package's error of the
	// same name. It is redeclared rather than imported: this package must not
	// depend on durablebudget, which reaches a sibling module the deployable
	// image does not build.
	ErrRepositoryUnavailable = errors.New("AI spend repository unavailable")
)

// SpendTier is the escalation band for month-to-date spend as a percentage of
// the configured monthly cap. This is spend-based, not balance-based: the
// percentage rises as money is consumed.
type SpendTier string

const (
	TierNormal     SpendTier = "NORMAL"
	TierWatch      SpendTier = "WATCH"
	TierWarning    SpendTier = "WARNING"
	TierCritical   SpendTier = "CRITICAL"
	TierExhaustion SpendTier = "EXHAUSTION"
)

// Tier boundaries as percentages of the monthly cap:
//
//	NORMAL     < 60
//	WATCH      [60, 75)
//	WARNING    [75, 85)
//	CRITICAL   [85, 95]
//	EXHAUSTION > 95
const (
	tierWatchPercent      = 60.0
	tierWarningPercent    = 75.0
	tierCriticalPercent   = 85.0
	tierExhaustionPercent = 95.0
)

// TierFor maps a percentage of the monthly cap to its escalation band.
func TierFor(percentOfCap float64) SpendTier {
	switch {
	case percentOfCap < tierWatchPercent:
		return TierNormal
	case percentOfCap < tierWarningPercent:
		return TierWatch
	case percentOfCap < tierCriticalPercent:
		return TierWarning
	case percentOfCap <= tierExhaustionPercent:
		return TierCritical
	default:
		return TierExhaustion
	}
}

// SpendSnapshot is the /budget projection. All amounts are micros, the unit
// the ledger stores.
type SpendSnapshot struct {
	AsOf       time.Time
	DayStart   time.Time
	MonthStart time.Time
	// MonthEnd is exclusive: the first instant of the following month.
	MonthEnd time.Time

	TodayMicros       int64
	MonthToDateMicros int64
	// ActiveReservedMicros is the part of month-to-date still held by
	// unsettled, unreleased reservations.
	ActiveReservedMicros int64
	// ProjectedMonthEndMicros is a straight-line run rate:
	// MonthToDate * daysInMonth / elapsedDaysIncludingToday.
	ProjectedMonthEndMicros int64
	MonthlyCapMicros        int64

	// PercentOfCap is MonthToDate as a percentage of the cap, and is what Tier
	// is derived from. ProjectedPercentOfCap is the same for the projection
	// and is informational only.
	PercentOfCap          float64
	ProjectedPercentOfCap float64
	Tier                  SpendTier

	// ReservedBasis records that these amounts are worst-case reserved cost,
	// an upper bound on settled spend. See the package note above.
	ReservedBasis bool
}

// SpendConfig configures the read. MonthlyCapMicros must be positive.
// Location determines the day and month boundaries (the deployment runs on
// EAT); nil means UTC. Now may be nil, meaning time.Now.
type SpendConfig struct {
	MonthlyCapMicros int64
	Location         *time.Location
	Now              func() time.Time
}

// spendSQL is bounded to the current calendar month by both the WHERE clause
// and the FILTER predicates, so it never scans the whole reservation history.
const spendSQL = `SELECT
  COALESCE(SUM(reserved_cost_micros) FILTER (WHERE created_at >= $2), 0),
  COALESCE(SUM(reserved_cost_micros), 0),
  COALESCE(SUM(reserved_cost_micros) FILTER (WHERE status = 'active'), 0)
FROM ops_ai_budget_reservations
WHERE created_at >= $1 AND created_at < $3`

// Queries is the read-only AI spend projection API. It never writes.
type Queries struct {
	db       Queryer
	cap      int64
	location *time.Location
	now      func() time.Time
}

// NewQueries returns the read-only spend query API.
func NewQueries(db Queryer, config SpendConfig) (*Queries, error) {
	if db == nil {
		return nil, ErrRepositoryUnavailable
	}
	if config.MonthlyCapMicros <= 0 {
		return nil, ErrInvalidBudgetConfig
	}
	location := config.Location
	if location == nil {
		location = time.UTC
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Queries{db: db, cap: config.MonthlyCapMicros, location: location, now: now}, nil
}

// Spend returns today's spend, month-to-date, a straight-line month-end
// projection, the configured cap, and the escalation tier. A month with no
// reservations returns zeros and TierNormal, not an error.
func (q *Queries) Spend(ctx context.Context) (SpendSnapshot, error) {
	if q == nil || q.db == nil {
		return SpendSnapshot{}, ErrRepositoryUnavailable
	}
	if err := ctx.Err(); err != nil {
		return SpendSnapshot{}, err
	}

	asOf := q.now().In(q.location)
	dayStart := time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, q.location)
	monthStart := time.Date(asOf.Year(), asOf.Month(), 1, 0, 0, 0, 0, q.location)
	monthEnd := monthStart.AddDate(0, 1, 0)

	snapshot := SpendSnapshot{
		AsOf:             asOf,
		DayStart:         dayStart,
		MonthStart:       monthStart,
		MonthEnd:         monthEnd,
		MonthlyCapMicros: q.cap,
		ReservedBasis:    true,
	}
	err := q.db.QueryRowContext(ctx, spendSQL, monthStart, dayStart, monthEnd).Scan(
		&snapshot.TodayMicros, &snapshot.MonthToDateMicros, &snapshot.ActiveReservedMicros)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SpendSnapshot{}, err
	}

	snapshot.ProjectedMonthEndMicros = projectMonthEnd(snapshot.MonthToDateMicros, asOf, monthStart, monthEnd)
	snapshot.PercentOfCap = percentOf(snapshot.MonthToDateMicros, q.cap)
	snapshot.ProjectedPercentOfCap = percentOf(snapshot.ProjectedMonthEndMicros, q.cap)
	snapshot.Tier = TierFor(snapshot.PercentOfCap)
	return snapshot, nil
}

// projectMonthEnd extrapolates month-to-date at a straight-line daily rate.
// Today counts as a whole elapsed day, so the projection is stable within a
// day and cannot blow up on the first hour of the month.
func projectMonthEnd(monthToDate int64, asOf, monthStart, monthEnd time.Time) int64 {
	if monthToDate <= 0 {
		return 0
	}
	daysInMonth := int64(monthEnd.Sub(monthStart).Hours() / 24)
	if daysInMonth <= 0 {
		return monthToDate
	}
	elapsed := int64(asOf.Day())
	if elapsed < 1 {
		elapsed = 1
	}
	if elapsed >= daysInMonth {
		return monthToDate
	}
	return monthToDate * daysInMonth / elapsed
}

func percentOf(amount, capMicros int64) float64 {
	if capMicros <= 0 {
		return 0
	}
	return float64(amount) / float64(capMicros) * 100
}

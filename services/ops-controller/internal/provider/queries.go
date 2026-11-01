// Package provider is the read-only projection of upstream data-provider quota
// behind the /quota command.
//
// It exists because the platform's hardest external ceiling — the API-Football
// daily call allowance — had no Go owner in this service. The ingestion path
// writes provider_rate_limits and provider_request_logs; nothing read them back
// for an operator. This package does only that.
//
// Everything in this file is a bounded SELECT. There are no writes, no
// reservations, no schema changes, and nothing here knows that Telegram exists.
package provider

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrRepositoryUnavailable mirrors the sentinel used by the other read-only
// projection packages in this service.
var ErrRepositoryUnavailable = errors.New("provider quota repository unavailable")

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
	// ProviderLimit bounds every per-provider read. provider_rate_limits holds
	// one row per configured provider, so the set is small by construction;
	// the limit is there so it stays small if a future ingestion bug starts
	// inserting a row per key or per region. It is passed as a bind parameter,
	// never interpolated.
	ProviderLimit = 16

	// CategoryLimit bounds the day's error breakdown, which is one row per
	// (provider, error_category) pair. It is deliberately separate from
	// ProviderLimit so capping the breakdown cannot distort the totals.
	CategoryLimit = 24

	// WarnPercent is the share of a provider's daily allowance at or above
	// which the provider is flagged. It is a warning threshold, not a limit:
	// nothing here throttles anything.
	WarnPercent = 85.0

	// StaleAfter is how old the stored counters may be before they stop being
	// evidence of anything.
	//
	// provider_rate_limits.updated_at is stamped from the rate-limit headers of
	// every provider response, so it only advances while the platform is
	// actually calling that provider. A gap wider than this means the numbers
	// describe some earlier moment: calls may have been made through another
	// path, or the provider's own daily window may have rolled over and reset
	// the allowance. Reporting such counters as current would be misleading, so
	// the age travels with them and the caller says so.
	StaleAfter = time.Hour
)

// Quota is one provider's current counters, as reported by the provider itself
// and stored by the ingestion path.
type Quota struct {
	// Provider is the stable key, e.g. "api-football". DisplayName comes from
	// the providers table and falls back to Provider when there is no row.
	Provider    string
	DisplayName string
	// Enabled is the providers table's flag; true when there is no row, since
	// a provider with live counters is evidently in use.
	Enabled bool

	// DailyLimit is the provider-reported ceiling and DailyRemaining the
	// provider-reported balance. DailyUsed is DERIVED, not stored: these APIs
	// report what is left, never what was spent, so used = limit - remaining.
	//
	// DailyKnown is false when DailyLimit is zero or NULL. A zero ceiling is
	// not "no calls used", it is "the ceiling was never reported", and both
	// DailyUsed and DailyPercent are meaningless in that state — callers must
	// render it as unknown rather than as 0%.
	DailyLimit     int64
	DailyRemaining int64
	DailyUsed      int64
	DailyPercent   float64
	DailyKnown     bool

	// The minute window follows exactly the same derivation and the same
	// unknown rule.
	MinuteLimit     int64
	MinuteRemaining int64
	MinuteUsed      int64
	MinutePercent   float64
	MinuteKnown     bool

	// DailyReserve and MinuteReserve are the platform's own held-back
	// allowance, not the provider's.
	DailyReserve  int64
	MinuteReserve int64

	// ThrottledUntil is set only while a throttle is actually in force, i.e.
	// throttle_until is in the future. The stored column also carries zero
	// timestamps for "never throttled", which are not throttles.
	ThrottledUntil    time.Time
	Throttled         bool
	ThrottleRemaining time.Duration

	// UpdatedAt is when the counters were last written, and Age how long ago
	// that was. Stale reports Age >= StaleAfter.
	UpdatedAt time.Time
	Age       time.Duration
	Stale     bool
}

// UnderPressure reports a provider at or above the daily warning threshold.
// An unknown ceiling is never "under pressure" — it is unknown, which the
// renderer reports separately; inventing a percentage here would turn a gap in
// the data into a false all-clear or a false alarm.
func (q Quota) UnderPressure() bool { return q.DailyKnown && q.DailyPercent >= WarnPercent }

// Usage is the current-counters projection, ordered by provider key.
type Usage struct {
	AsOf      time.Time
	Providers []Quota
	// Truncated reports that the limit was reached and more providers exist.
	Truncated bool
}

// Throttled returns the providers with a throttle in force.
func (u Usage) Throttled() []Quota {
	var out []Quota
	for _, p := range u.Providers {
		if p.Throttled {
			out = append(out, p)
		}
	}
	return out
}

// Pressured returns the providers at or above WarnPercent of their daily
// allowance.
func (u Usage) Pressured() []Quota {
	var out []Quota
	for _, p := range u.Providers {
		if p.UnderPressure() {
			out = append(out, p)
		}
	}
	return out
}

// ErrorCount is one error_category bucket for one provider.
type ErrorCount struct {
	Category string
	Count    int64
}

// ProviderDay is one provider's request history since the start of the current
// day.
type ProviderDay struct {
	Provider string
	Requests int64
	// Errors counts responses with an HTTP status of 400 or above. A request
	// that never got a status at all (transport failure) has a NULL
	// status_code and is not counted here; it shows up under Categories.
	Errors int64
	// QuotaCost is the sum of quota_cost, which is what the provider actually
	// bills for the day and need not equal Requests.
	QuotaCost  int64
	Categories []ErrorCount
}

// Today is the same-day request projection.
type Today struct {
	AsOf time.Time
	// Since is the day boundary the read used. The SQL bound is
	// date_trunc('day', now()) evaluated by Postgres, so this is the label for
	// that boundary, not an independently computed one.
	Since     time.Time
	Providers []ProviderDay
	// Truncated and CategoriesTruncated report that either bounded set filled
	// its limit, so what is shown is a prefix.
	Truncated           bool
	CategoriesTruncated bool
}

// Find returns one provider's day, if it was read.
func (t Today) Find(provider string) (ProviderDay, bool) {
	for _, p := range t.Providers {
		if p.Provider == provider {
			return p, true
		}
	}
	return ProviderDay{}, false
}

// usageSQL reads the authoritative current counters. NULL numeric columns are
// collapsed to zero here because zero and NULL mean the same thing downstream —
// "not reported" — and the Go side has one unknown branch instead of two.
const usageSQL = `SELECT r.provider,
       COALESCE(NULLIF(p.display_name, ''), r.provider),
       COALESCE(p.enabled, true),
       COALESCE(r.daily_limit, 0), COALESCE(r.daily_remaining, 0),
       COALESCE(r.minute_limit, 0), COALESCE(r.minute_remaining, 0),
       COALESCE(r.daily_reserve, 0), COALESCE(r.minute_reserve, 0),
       r.throttle_until, r.updated_at
FROM provider_rate_limits r
LEFT JOIN providers p ON p.provider = r.provider
ORDER BY r.provider
LIMIT $1`

// todayTotalsSQL aggregates the day's requests per provider. The grouped result
// is one row per provider, and the LIMIT bounds it in case that stops being
// true.
const todayTotalsSQL = `SELECT l.provider,
       count(*),
       count(*) FILTER (WHERE l.status_code >= 400),
       COALESCE(sum(l.quota_cost), 0)
FROM provider_request_logs l
WHERE l.requested_at >= date_trunc('day', now())
GROUP BY l.provider
ORDER BY count(*) DESC, l.provider
LIMIT $1`

// todayErrorsSQL is the error breakdown. error_category is ” on success in
// this schema (and NULL on older rows), so both are excluded rather than
// rendered as a bucket named "".
const todayErrorsSQL = `SELECT l.provider, l.error_category, count(*)
FROM provider_request_logs l
WHERE l.requested_at >= date_trunc('day', now())
  AND COALESCE(l.error_category, '') <> ''
GROUP BY l.provider, l.error_category
ORDER BY count(*) DESC, l.provider, l.error_category
LIMIT $1`

// Queries is the read-only provider quota API. It never writes.
type Queries struct {
	db  Queryer
	now func() time.Time
}

// NewQueries returns the read-only provider query API. now may be nil, meaning
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

// Usage returns the current per-provider counters: derived daily and minute
// usage, the platform's reserve, whether a throttle is in force, and how stale
// the counters are. No rows yields an empty usage, not an error.
func (q *Queries) Usage(ctx context.Context) (Usage, error) {
	if q == nil || q.db == nil {
		return Usage{}, ErrRepositoryUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Usage{}, err
	}
	asOf := q.now().UTC()
	usage := Usage{AsOf: asOf, Providers: make([]Quota, 0, ProviderLimit)}

	rows, err := q.db.QueryContext(ctx, usageSQL, ProviderLimit)
	if err != nil {
		return Usage{}, err
	}
	defer rows.Close()

	for rows.Next() {
		var p Quota
		var throttleUntil sql.NullTime
		if err := rows.Scan(&p.Provider, &p.DisplayName, &p.Enabled,
			&p.DailyLimit, &p.DailyRemaining,
			&p.MinuteLimit, &p.MinuteRemaining,
			&p.DailyReserve, &p.MinuteReserve,
			&throttleUntil, &p.UpdatedAt); err != nil {
			return Usage{}, err
		}
		p.DailyUsed, p.DailyPercent, p.DailyKnown = derive(p.DailyLimit, p.DailyRemaining)
		p.MinuteUsed, p.MinutePercent, p.MinuteKnown = derive(p.MinuteLimit, p.MinuteRemaining)
		applyThrottle(&p, throttleUntil, asOf)
		if age := asOf.Sub(p.UpdatedAt); age > 0 {
			p.Age = age
		}
		p.Stale = p.Age >= StaleAfter
		usage.Providers = append(usage.Providers, p)
	}
	if err := rows.Err(); err != nil {
		return Usage{}, err
	}
	usage.Truncated = len(usage.Providers) == ProviderLimit
	return usage, nil
}

// derive turns a provider-reported ceiling and balance into usage.
//
// used = limit - remaining, because these APIs report only what is left. A
// non-positive limit means the ceiling was never reported: used and the
// percentage are both undefined, and known is false so nothing downstream can
// mistake the zero values for "nothing used yet".
func derive(limit, remaining int64) (used int64, percent float64, known bool) {
	if limit <= 0 {
		return 0, 0, false
	}
	used = limit - remaining
	if used < 0 {
		// A balance above the ceiling means the window just reset between the
		// two header reads. Nothing has been spent against the new window.
		used = 0
	}
	if used > limit {
		used = limit
	}
	return used, float64(used) / float64(limit) * 100, true
}

// applyThrottle reports a throttle only while it is in force. The stored column
// carries zero timestamps for providers that have never been throttled, so a
// non-NULL value is not by itself a throttle.
func applyThrottle(p *Quota, until sql.NullTime, asOf time.Time) {
	if !until.Valid {
		return
	}
	deadline := until.Time.UTC()
	if !deadline.After(asOf) {
		return
	}
	p.ThrottledUntil = deadline
	p.Throttled = true
	p.ThrottleRemaining = deadline.Sub(asOf)
}

// since reports the day boundary the Today read is bounded by, in UTC. The
// provider's own daily allowance rolls over on a UTC day, which is the same
// boundary date_trunc('day', now()) produces on a UTC database.
func (q *Queries) since() time.Time {
	n := q.now().UTC()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

// Today returns the day's request count, error count, quota cost and error
// breakdown per provider. Two bounded reads: exact per-provider totals, and a
// separately bounded breakdown by error_category, so capping the breakdown
// cannot distort the totals. Empty tables yield an empty projection, not an
// error.
func (q *Queries) Today(ctx context.Context) (Today, error) {
	if q == nil || q.db == nil {
		return Today{}, ErrRepositoryUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Today{}, err
	}
	today := Today{
		AsOf:      q.now().UTC(),
		Since:     q.since(),
		Providers: make([]ProviderDay, 0, ProviderLimit),
	}

	totals, err := q.db.QueryContext(ctx, todayTotalsSQL, ProviderLimit)
	if err != nil {
		return Today{}, err
	}
	if err := scanTotals(totals, &today); err != nil {
		return Today{}, err
	}
	today.Truncated = len(today.Providers) == ProviderLimit

	categories, err := q.db.QueryContext(ctx, todayErrorsSQL, CategoryLimit)
	if err != nil {
		return Today{}, err
	}
	seen, err := scanCategories(categories, &today)
	if err != nil {
		return Today{}, err
	}
	today.CategoriesTruncated = seen == CategoryLimit
	return today, nil
}

func scanTotals(rows Rows, today *Today) error {
	defer rows.Close()
	for rows.Next() {
		var day ProviderDay
		if err := rows.Scan(&day.Provider, &day.Requests, &day.Errors, &day.QuotaCost); err != nil {
			return err
		}
		today.Providers = append(today.Providers, day)
	}
	return rows.Err()
}

// scanCategories attaches each bucket to its provider. A bucket for a provider
// the bounded totals read did not return is kept as its own entry rather than
// dropped: an error category with nowhere to attach is still an error.
func scanCategories(rows Rows, today *Today) (int, error) {
	defer rows.Close()
	count := 0
	for rows.Next() {
		var provider string
		var bucket ErrorCount
		if err := rows.Scan(&provider, &bucket.Category, &bucket.Count); err != nil {
			return count, err
		}
		count++
		attached := false
		for i := range today.Providers {
			if today.Providers[i].Provider == provider {
				today.Providers[i].Categories = append(today.Providers[i].Categories, bucket)
				attached = true
				break
			}
		}
		if !attached {
			today.Providers = append(today.Providers, ProviderDay{
				Provider:   provider,
				Categories: []ErrorCount{bucket},
			})
		}
	}
	return count, rows.Err()
}

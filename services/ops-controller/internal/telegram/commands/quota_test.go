package commands

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/provider"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
)

// The command is exercised through a fake provider.Queryer rather than a stub
// projection, so the rendering is tested against the same scan order the real
// SQL produces.

type quotaFakeRows struct {
	values [][]any
	index  int
}

func (r *quotaFakeRows) Next() bool {
	if r.index >= len(r.values) {
		return false
	}
	r.index++
	return true
}

func (r *quotaFakeRows) Scan(dest ...any) error {
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

func (r *quotaFakeRows) Err() error   { return nil }
func (r *quotaFakeRows) Close() error { return nil }

// quotaFakeDB routes by query shape, so each read gets its own row set.
type quotaFakeDB struct {
	limits     [][]any
	totals     [][]any
	categories [][]any
	err        error
}

func (d *quotaFakeDB) QueryContext(_ context.Context, query string, _ ...any) (provider.Rows, error) {
	if d.err != nil {
		return nil, d.err
	}
	switch {
	case strings.Contains(query, "provider_rate_limits"):
		return &quotaFakeRows{values: d.limits}, nil
	case strings.Contains(query, "error_category"):
		return &quotaFakeRows{values: d.categories}, nil
	default:
		return &quotaFakeRows{values: d.totals}, nil
	}
}

var quotaClock = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

func quotaTestDeps(t *testing.T, db provider.Queryer) QuotaDeps {
	t.Helper()
	queries, err := provider.NewQueries(db, func() time.Time { return quotaClock })
	if err != nil {
		t.Fatal(err)
	}
	return QuotaDeps{
		Quota:    queries,
		Location: time.UTC,
		Now:      func() time.Time { return quotaClock },
	}
}

// quotaLimitRow matches the column order of the provider package's usage read.
func quotaLimitRow(key, display string, dailyLimit, dailyRemaining, minuteLimit, minuteRemaining int64,
	throttle sql.NullTime, updatedAgo time.Duration) []any {
	return []any{
		key, display, true,
		dailyLimit, dailyRemaining, minuteLimit, minuteRemaining,
		int64(1500), int64(15),
		throttle, quotaClock.Add(-updatedAgo),
	}
}

func quotaNoThrottle() sql.NullTime { return sql.NullTime{} }

func quotaBody(t *testing.T, deps QuotaDeps) string {
	t.Helper()
	resp, err := deps.quota(context.Background(), telegram.Request{Command: "quota"})
	if err != nil {
		t.Fatalf("handler returned an error instead of degrading: %v", err)
	}
	if resp.Silent {
		t.Fatal("handler went silent; an ops read must always answer")
	}
	return resp.Body
}

// The degradation rule: a section whose source is missing renders
// "unavailable" and is never omitted, because a missing line reads as "fine".
func TestQuotaDegradesWhenTheProjectionIsNotWired(t *testing.T) {
	body := quotaBody(t, QuotaDeps{Location: time.UTC, Now: func() time.Time { return quotaClock }})
	if strings.Count(body, unavailable) < 2 {
		t.Fatalf("both sections must degrade, body was:\n%s", body)
	}
	if strings.Contains(body, "🟢") {
		t.Fatalf("an unreadable quota must not report headroom:\n%s", body)
	}
}

func TestQuotaDegradesWhenTheReadFails(t *testing.T) {
	deps := quotaTestDeps(t, &quotaFakeDB{err: errors.New("connection reset")})
	body := quotaBody(t, deps)
	if strings.Count(body, unavailable) < 2 {
		t.Fatalf("a failed read must render %q, body was:\n%s", unavailable, body)
	}
}

func TestQuotaRendersDerivedDailyUsage(t *testing.T) {
	deps := quotaTestDeps(t, &quotaFakeDB{
		limits: [][]any{
			quotaLimitRow("api-football", "API-Football", 100, 40, 10, 9, quotaNoThrottle(), 2*time.Minute),
		},
		totals:     [][]any{{"api-football", int64(412), int64(9), int64(430)}},
		categories: [][]any{{"api-football", "rate_limit", int64(7)}, {"api-football", "route", int64(2)}},
	})
	body := quotaBody(t, deps)

	if !strings.Contains(body, "60/100") || !strings.Contains(body, "60.0%") {
		t.Fatalf("derived daily usage missing:\n%s", body)
	}
	if !strings.Contains(body, "1/10") {
		t.Fatalf("minute window missing:\n%s", body)
	}
	if !strings.Contains(body, "🟢") {
		t.Fatalf("a provider with headroom is green:\n%s", body)
	}
	for _, want := range []string{"412", "430", "rate_limit 7", "route 2"} {
		if !strings.Contains(body, want) {
			t.Fatalf("today section missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, unavailable) {
		t.Fatalf("nothing was unavailable:\n%s", body)
	}
}

// The single most dangerous rendering bug here: a provider whose ceiling was
// never reported must not appear to have used none of it.
func TestQuotaRendersAnUnknownDailyLimitAsUnknownNotZeroPercent(t *testing.T) {
	deps := quotaTestDeps(t, &quotaFakeDB{
		limits: [][]any{
			quotaLimitRow("football-data-org", "football-data.org", 0, 0, 10, 4, quotaNoThrottle(), time.Minute),
		},
	})
	body := quotaBody(t, deps)

	if !strings.Contains(body, quotaUnknown) {
		t.Fatalf("an unreported ceiling must render as %q:\n%s", quotaUnknown, body)
	}
	if strings.Contains(body, "0.0%") {
		t.Fatalf("an undefined percentage must never render as 0.0%%:\n%s", body)
	}
	// The minute window is reported independently and stays real.
	if !strings.Contains(body, "6/10") {
		t.Fatalf("the minute window must still render:\n%s", body)
	}
}

func TestQuotaFlagsAProviderOverTheWarningThreshold(t *testing.T) {
	deps := quotaTestDeps(t, &quotaFakeDB{
		limits: [][]any{
			quotaLimitRow("api-football", "API-Football", 100, 1, 10, 9, quotaNoThrottle(), time.Minute),
		},
	})
	body := quotaBody(t, deps)

	if !strings.Contains(body, "🔴") {
		t.Fatalf("a near-exhausted quota must be red at a glance:\n%s", body)
	}
	if !strings.Contains(body, "⚠️") || !strings.Contains(body, "99.0%") {
		t.Fatalf("the pressure line is missing:\n%s", body)
	}
	if !strings.Contains(body, "1 call(s) left") {
		t.Fatalf("remaining headroom must be spelled out:\n%s", body)
	}
}

func TestQuotaBelowTheThresholdIsNotFlagged(t *testing.T) {
	deps := quotaTestDeps(t, &quotaFakeDB{
		limits: [][]any{
			quotaLimitRow("api-football", "API-Football", 100, 16, 10, 9, quotaNoThrottle(), time.Minute),
		},
	})
	body := quotaBody(t, deps)
	if strings.Contains(body, "⚠️") || strings.Contains(body, "🔴") {
		t.Fatalf("84%% is below the threshold and must not alarm:\n%s", body)
	}
}

func TestQuotaReportsAnActiveThrottleProminently(t *testing.T) {
	active := sql.NullTime{Valid: true, Time: quotaClock.Add(4 * time.Minute)}
	deps := quotaTestDeps(t, &quotaFakeDB{
		limits: [][]any{
			quotaLimitRow("api-football", "API-Football", 100, 90, 10, 0, active, time.Minute),
		},
	})
	body := quotaBody(t, deps)

	if !strings.Contains(body, "🔴") || !strings.Contains(body, "throttled") {
		t.Fatalf("an active throttle must lead the message:\n%s", body)
	}
	if !strings.Contains(body, "⛔") || !strings.Contains(body, "12:04") {
		t.Fatalf("the throttle deadline is missing:\n%s", body)
	}
}

// A throttle that has already expired is not in force and must not be reported
// as one.
func TestQuotaIgnoresAnExpiredThrottle(t *testing.T) {
	expired := sql.NullTime{Valid: true, Time: quotaClock.Add(-time.Minute)}
	deps := quotaTestDeps(t, &quotaFakeDB{
		limits: [][]any{
			quotaLimitRow("api-football", "API-Football", 100, 90, 10, 9, expired, time.Minute),
		},
	})
	if body := quotaBody(t, deps); strings.Contains(body, "⛔") {
		t.Fatalf("an expired throttle is not active:\n%s", body)
	}
}

func TestQuotaSurfacesStaleCounters(t *testing.T) {
	deps := quotaTestDeps(t, &quotaFakeDB{
		limits: [][]any{
			quotaLimitRow("api-football", "API-Football", 100, 90, 10, 9, quotaNoThrottle(), 6*time.Hour),
		},
	})
	body := quotaBody(t, deps)

	if !strings.Contains(body, "🕒") || !strings.Contains(body, "not live") {
		t.Fatalf("stale counters must say so:\n%s", body)
	}
	if !strings.Contains(body, "6h") {
		t.Fatalf("the age of the counters is the evidence and must be shown:\n%s", body)
	}
	if strings.Contains(body, "🟢") {
		t.Fatalf("counters that are not live cannot report headroom:\n%s", body)
	}
}

// Stale counters that are also near the ceiling must report both, not one.
func TestQuotaReportsPressureAndStalenessTogether(t *testing.T) {
	deps := quotaTestDeps(t, &quotaFakeDB{
		limits: [][]any{
			quotaLimitRow("api-football", "API-Football", 100, 2, 10, 9, quotaNoThrottle(), 5*time.Hour),
		},
	})
	body := quotaBody(t, deps)
	if !strings.Contains(body, "⚠️") || !strings.Contains(body, "🕒") {
		t.Fatalf("both conditions must be reported:\n%s", body)
	}
}

func TestQuotaEmptyTablesRenderASensibleEmptyState(t *testing.T) {
	body := quotaBody(t, quotaTestDeps(t, &quotaFakeDB{}))
	if strings.Contains(body, unavailable) {
		t.Fatalf("an empty table is not an unavailable source:\n%s", body)
	}
	for _, want := range []string{"No provider counters recorded", "No provider requests recorded"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing empty state %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "🟢") {
		t.Fatalf("no counters at all is not a green headline:\n%s", body)
	}
}

func TestQuotaRegistersOnceAndRefusesANilRegistry(t *testing.T) {
	if err := RegisterQuota(nil, QuotaDeps{}); err == nil {
		t.Fatal("a nil registry must be refused")
	}
	registry := telegram.NewRegistry()
	if err := RegisterQuota(registry, QuotaDeps{}); err != nil {
		t.Fatal(err)
	}
	reg, ok := registry.Lookup("quota")
	if !ok {
		t.Fatal("/quota was not registered")
	}
	if reg.Mutating {
		t.Fatal("/quota is a read command and must not be marked mutating")
	}
	if err := RegisterQuota(registry, QuotaDeps{}); err == nil {
		t.Fatal("registering /quota twice must fail")
	}
}

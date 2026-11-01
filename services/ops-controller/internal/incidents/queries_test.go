package incidents

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/alerts"
)

// The read seam is exercised with a fake Queryer rather than a live database.
// Migration 000057 adds ops_incidents.reference concurrently with this work, so
// no local database has the column yet; see the note at the end of this file
// for what that leaves unverified.

func assignScan(dest []any, values []any) error {
	if len(dest) != len(values) {
		return fmt.Errorf("scan arity: got %d dest for %d values", len(dest), len(values))
	}
	for i, value := range values {
		switch target := dest[i].(type) {
		case *string:
			*target = value.(string)
		case *int64:
			*target = value.(int64)
		case *int:
			*target = value.(int)
		case *bool:
			*target = value.(bool)
		case *time.Time:
			*target = value.(time.Time)
		default:
			return fmt.Errorf("unsupported scan dest %T", dest[i])
		}
	}
	return nil
}

type queryRow struct {
	values []any
	err    error
}

func (r queryRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return assignScan(dest, r.values)
}

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
func (r *queryRows) Scan(dest ...any) error { return assignScan(dest, r.values[r.index-1]) }
func (r *queryRows) Err() error             { return r.err }
func (r *queryRows) Close() error           { r.closed++; return nil }

type queryDB struct {
	rows      []*queryRows
	rowsErr   error
	singles   []Row
	queries   []string
	args      [][]any
	rowsIndex int
	rowIndex  int
}

func (d *queryDB) QueryContext(_ context.Context, query string, args ...any) (Rows, error) {
	d.queries = append(d.queries, query)
	d.args = append(d.args, args)
	if d.rowsErr != nil {
		return nil, d.rowsErr
	}
	rows := d.rows[d.rowsIndex]
	d.rowsIndex++
	return rows, nil
}

func (d *queryDB) QueryRowContext(_ context.Context, query string, args ...any) Row {
	d.queries = append(d.queries, query)
	d.args = append(d.args, args)
	row := d.singles[d.rowIndex]
	d.rowIndex++
	return row
}

func at(hour int) time.Time {
	return time.Date(2026, 9, 4, hour, 0, 0, 0, time.UTC)
}

func summaryRow(id string, reference int64, severity string, last time.Time, occurrences int64) []any {
	return []any{id, reference, "title-" + id, "summary-" + id, "api", "prod",
		severity, "open", at(1), last, occurrences}
}

func TestListOpenOrdersBySeverityThenRecencyAndCountsObservations(t *testing.T) {
	db := &queryDB{rows: []*queryRows{{values: [][]any{
		summaryRow("critical-older", 3, "critical", at(9), 12),
		summaryRow("warning-newest", 2, "warning", at(20), 4),
		summaryRow("info-newest", 1, "info", at(23), 1),
	}}}}
	queries, err := NewQueries(db)
	if err != nil {
		t.Fatal(err)
	}

	summaries, err := queries.ListOpen(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 3 {
		t.Fatalf("expected 3 summaries, got %d", len(summaries))
	}
	if summaries[0].Severity != alerts.SeverityCritical || summaries[0].OccurrenceCount != 12 {
		t.Fatalf("first summary should be the critical one with its observation count: %+v", summaries[0])
	}
	if summaries[0].Reference != 3 || summaries[0].Status != StatusOpen || summaries[0].Service != "api" {
		t.Fatalf("summary fields not projected: %+v", summaries[0])
	}

	// The database, not Go, must impose the ordering: alphabetical severity
	// would sort critical < info < warning, which is wrong.
	query := db.queries[0]
	if !strings.Contains(query, "CASE i.severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END") {
		t.Fatalf("severity ordering is not explicit: %s", query)
	}
	if !strings.Contains(query, "last_observed_at DESC") {
		t.Fatalf("recency ordering missing: %s", query)
	}
	if !strings.Contains(query, "COUNT(*) FROM ops_incident_observations") {
		t.Fatalf("occurrence count must come from observations: %s", query)
	}
	if db.rows[0].closed != 1 {
		t.Fatalf("rows closed %d times, want 1", db.rows[0].closed)
	}
}

func TestListOpenEmptyReturnsEmptySliceNotError(t *testing.T) {
	db := &queryDB{rows: []*queryRows{{}}}
	queries, _ := NewQueries(db)
	summaries, err := queries.ListOpen(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if summaries == nil || len(summaries) != 0 {
		t.Fatalf("empty open set must be an empty slice, got %#v", summaries)
	}
}

func TestListOpenClampsLimitAndNeverScansUnbounded(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		limit int
		want  int
	}{
		{"zero uses default", 0, DefaultListLimit},
		{"negative uses default", -5, DefaultListLimit},
		{"oversized is clamped", MaxListLimit * 10, MaxListLimit},
		{"in range is honoured", 7, 7},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := &queryDB{rows: []*queryRows{{}}}
			queries, _ := NewQueries(db)
			if _, err := queries.ListOpen(context.Background(), testCase.limit); err != nil {
				t.Fatal(err)
			}
			if got := db.args[0][0].(int); got != testCase.want {
				t.Fatalf("limit=%d want=%d", got, testCase.want)
			}
			if !strings.Contains(db.queries[0], "LIMIT $1") {
				t.Fatalf("limit is not parameterised: %s", db.queries[0])
			}
		})
	}
}

func detailValues(reference int64) []any {
	return []any{
		"11111111-2222-3333-4444-555555555555", reference, "checkout latency",
		"p99 above budget", "api", "prod", ClassificationVersion,
		"critical", "investigating", "grafana", "event-9", strings.Repeat("a", 64),
		at(1), at(6), false, at(1), false, at(1), at(6),
		`{"service":"api","occurrence_count":3}`, int64(3),
	}
}

func TestGetByReferenceProjectsDetailWithEvidenceAndCount(t *testing.T) {
	db := &queryDB{singles: []Row{queryRow{values: detailValues(143)}}}
	queries, _ := NewQueries(db)

	detail, err := queries.GetByReference(context.Background(), 143)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Reference != 143 || detail.Severity != alerts.SeverityCritical ||
		detail.Status != StatusInvestigating || detail.OccurrenceCount != 3 {
		t.Fatalf("detail not projected: %+v", detail)
	}
	if string(detail.Evidence) != `{"service":"api","occurrence_count":3}` {
		t.Fatalf("evidence JSONB not returned verbatim: %s", detail.Evidence)
	}
	// Unset lifecycle timestamps must not leak the COALESCE fallback.
	if !detail.AcknowledgedAt.IsZero() || !detail.ResolvedAt.IsZero() {
		t.Fatalf("unacknowledged/unresolved incident must report zero times: %+v", detail)
	}
	if db.args[0][0].(int64) != 143 || !strings.Contains(db.queries[0], "WHERE i.reference = $1") {
		t.Fatalf("lookup must be by parameterised reference: %s %v", db.queries[0], db.args[0])
	}
}

func TestGetByReferenceMissingIsDistinguishableNotFound(t *testing.T) {
	db := &queryDB{singles: []Row{queryRow{err: sql.ErrNoRows}}}
	queries, _ := NewQueries(db)
	if _, err := queries.GetByReference(context.Background(), 999); !errors.Is(err, ErrIncidentNotFound) {
		t.Fatalf("missing incident err=%v want ErrIncidentNotFound", err)
	}
}

func TestGetByReferenceRejectsNonPositiveReferenceWithoutQuerying(t *testing.T) {
	db := &queryDB{}
	queries, _ := NewQueries(db)
	if _, err := queries.GetByReference(context.Background(), 0); !errors.Is(err, ErrIncidentNotFound) {
		t.Fatalf("reference 0 err=%v want ErrIncidentNotFound", err)
	}
	if len(db.queries) != 0 {
		t.Fatalf("no query should be issued for an impossible reference: %v", db.queries)
	}
}

func TestGetByReferenceSurfacesRealErrors(t *testing.T) {
	boom := errors.New("connection reset")
	db := &queryDB{singles: []Row{queryRow{err: boom}}}
	queries, _ := NewQueries(db)
	_, err := queries.GetByReference(context.Background(), 143)
	if !errors.Is(err, boom) || errors.Is(err, ErrIncidentNotFound) {
		t.Fatalf("database failure must not masquerade as not-found: %v", err)
	}
}

func TestCountOpenBySeverity(t *testing.T) {
	db := &queryDB{singles: []Row{queryRow{values: []any{int64(2), int64(5), int64(1), int64(8)}}}}
	queries, _ := NewQueries(db)
	counts, err := queries.CountOpenBySeverity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts.Critical != 2 || counts.Warning != 5 || counts.Info != 1 || counts.Total != 8 {
		t.Fatalf("unexpected counts: %+v", counts)
	}
	if !strings.Contains(db.queries[0], "status IN ('open', 'investigating', 'mitigated')") {
		t.Fatalf("counts must be over the active status set: %s", db.queries[0])
	}
}

func TestCountOpenBySeverityEmptyIsZero(t *testing.T) {
	db := &queryDB{singles: []Row{queryRow{values: []any{int64(0), int64(0), int64(0), int64(0)}}}}
	queries, _ := NewQueries(db)
	counts, err := queries.CountOpenBySeverity(context.Background())
	if err != nil || counts != (SeverityCounts{}) {
		t.Fatalf("counts=%+v err=%v", counts, err)
	}
}

func correlationRows(deployStarted time.Time, service string, hasPrevious bool) []any {
	return []any{
		"dep-current", "api-2026-09-04", service, "prod", "abc1234", "succeeded",
		deployStarted, hasPrevious, "dep-previous", "api-2026-09-03", service,
		"prod", "def5678", "succeeded", deployStarted.Add(-24 * time.Hour),
	}
}

func TestCorrelateDeploymentStrengthLaddersOnTimeProximity(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		deployAge time.Duration
		want      CorrelationStrength
	}{
		{"within 30m is strong", 10 * time.Minute, CorrelationStrong},
		{"exactly 30m is strong", 30 * time.Minute, CorrelationStrong},
		{"within 2h is moderate", 90 * time.Minute, CorrelationModerate},
		{"beyond 2h but inside lookback is weak", 6 * time.Hour, CorrelationWeak},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			onset := at(12)
			db := &queryDB{singles: []Row{
				queryRow{values: []any{onset, "api", "prod"}},
				queryRow{values: correlationRows(onset.Add(-testCase.deployAge), "api", true)},
			}}
			queries, _ := NewQueries(db)
			correlation, err := queries.CorrelateDeployment(context.Background(), "incident-1")
			if err != nil {
				t.Fatal(err)
			}
			if correlation.Strength != testCase.want {
				t.Fatalf("strength=%s want=%s", correlation.Strength, testCase.want)
			}
			if correlation.SinceDeploy != testCase.deployAge {
				t.Fatalf("interval=%s want=%s", correlation.SinceDeploy, testCase.deployAge)
			}
			if !correlation.HasCurrent || !correlation.HasPrevious ||
				correlation.Current.SourceCommit != "abc1234" ||
				correlation.Previous.SourceCommit != "def5678" {
				t.Fatalf("current/previous deployments not projected: %+v", correlation)
			}
		})
	}
}

func TestCorrelateDeploymentCapsUnscopedMatchAtWeak(t *testing.T) {
	onset := at(12)
	db := &queryDB{singles: []Row{
		// Incident evidence carries no service, so the deployment cannot be
		// service-scoped however close in time it was.
		queryRow{values: []any{onset, "", ""}},
		queryRow{values: correlationRows(onset.Add(-time.Minute), "unrelated", false)},
	}}
	queries, _ := NewQueries(db)
	correlation, err := queries.CorrelateDeployment(context.Background(), "incident-1")
	if err != nil {
		t.Fatal(err)
	}
	if correlation.ServiceMatched || correlation.Strength != CorrelationWeak {
		t.Fatalf("unscoped match must cap at weak: %+v", correlation)
	}
	if correlation.HasPrevious {
		t.Fatalf("previous deployment must stay unset when the join found none: %+v", correlation)
	}
}

func TestCorrelateDeploymentBoundsLookbackAndReportsNoDeployment(t *testing.T) {
	onset := at(12)
	db := &queryDB{singles: []Row{
		queryRow{values: []any{onset, "api", "prod"}},
		queryRow{err: sql.ErrNoRows},
	}}
	queries, _ := NewQueries(db)
	correlation, err := queries.CorrelateDeployment(context.Background(), "incident-1")
	if err != nil {
		t.Fatal(err)
	}
	if correlation.HasCurrent || correlation.Strength != CorrelationNone {
		t.Fatalf("no deployment in window must be an answer, not an error: %+v", correlation)
	}
	if got := db.args[1][1].(time.Time); !got.Equal(onset.Add(-DeploymentLookback)) {
		t.Fatalf("lookback lower bound=%s want=%s", got, onset.Add(-DeploymentLookback))
	}
	if got := db.args[1][0].(time.Time); !got.Equal(onset) {
		t.Fatalf("upper bound must be the incident onset, got %s", got)
	}
}

func TestCorrelateDeploymentMissingIncidentIsNotFound(t *testing.T) {
	db := &queryDB{singles: []Row{queryRow{err: sql.ErrNoRows}}}
	queries, _ := NewQueries(db)
	if _, err := queries.CorrelateDeployment(context.Background(), "missing"); !errors.Is(err, ErrIncidentNotFound) {
		t.Fatalf("err=%v want ErrIncidentNotFound", err)
	}
}

func TestQueriesRespectContextCancellationAndNilDatabase(t *testing.T) {
	if _, err := NewQueries(nil); !errors.Is(err, ErrRepositoryUnavailable) {
		t.Fatalf("nil database err=%v", err)
	}
	db := &queryDB{}
	queries, _ := NewQueries(db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := queries.ListOpen(ctx, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListOpen err=%v want context.Canceled", err)
	}
	if _, err := queries.GetByReference(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetByReference err=%v want context.Canceled", err)
	}
	if _, err := queries.CountOpenBySeverity(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("CountOpenBySeverity err=%v want context.Canceled", err)
	}
	if _, err := queries.CorrelateDeployment(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CorrelateDeployment err=%v want context.Canceled", err)
	}
	if len(db.queries) != 0 {
		t.Fatalf("a cancelled context must issue no query: %v", db.queries)
	}
}

func TestReadQueriesAreSelectOnlyAndParameterised(t *testing.T) {
	for name, query := range map[string]string{
		"listOpen":    listOpenIncidentsSQL,
		"getByRef":    getIncidentByReferenceSQL,
		"countBySev":  countOpenBySeveritySQL,
		"onset":       incidentOnsetSQL,
		"correlation": correlateDeploymentSQL,
	} {
		upper := strings.ToUpper(query)
		for _, forbidden := range []string{"INSERT ", "UPDATE ", "DELETE ", "ALTER ", "DROP ", "FOR UPDATE"} {
			if strings.Contains(upper, forbidden) {
				t.Fatalf("%s must be read-only, found %q", name, forbidden)
			}
		}
		if strings.Contains(query, "%s") || strings.Contains(query, "%d") {
			t.Fatalf("%s looks string-built rather than parameterised", name)
		}
	}
	for _, name := range []string{"listOpen", "correlation"} {
		var query string
		if name == "listOpen" {
			query = listOpenIncidentsSQL
		} else {
			query = correlateDeploymentSQL
		}
		if !strings.Contains(query, "LIMIT") {
			t.Fatalf("%s must be bounded by an explicit LIMIT", name)
		}
	}
}

// Not verified against a live database: migration 000059 (ops_incidents
// .reference) is being written concurrently and does not exist in any local
// database yet, so the reference lookup and the COALESCE(reference, 0)
// projection are covered only at the fake-Queryer level.

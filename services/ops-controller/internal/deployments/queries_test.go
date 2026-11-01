package deployments

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The read seam is exercised with a fake Queryer, matching the fake-DB
// convention used by the other storage packages in this service.

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

type queryDB struct {
	rows    *queryRows
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
	return d.rows, nil
}

var clock = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

func now() time.Time { return clock }

func deploymentRow(key, previousCommit, class, status string, startedAgo time.Duration, completed, superseded bool) []any {
	started := clock.Add(-startedAgo)
	return []any{
		"id-" + key, key, "software", "prod", "api", "sha-" + key, previousCommit,
		class, status, "ci", started, completed, started.Add(2 * time.Minute), superseded,
	}
}

func newTestQueries(t *testing.T, db Queryer) *Queries {
	t.Helper()
	queries, err := NewQueries(db, now)
	if err != nil {
		t.Fatal(err)
	}
	return queries
}

func TestRecentProjectsShaClassAgeStatusAndRollbackState(t *testing.T) {
	db := &queryDB{rows: &queryRows{values: [][]any{
		deploymentRow("api-3", "sha-api-2", "IMMEDIATE", "succeeded", 30*time.Minute, true, false),
		deploymentRow("api-2", "sha-api-1", "BATCHED", "rolled_back", 4*time.Hour, true, true),
		deploymentRow("api-1", "", "BATCHED", "succeeded", 26*time.Hour, true, true),
	}}}
	queries := newTestQueries(t, db)

	listing, err := queries.Recent(context.Background(), 10, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(listing.Deployments) != 3 {
		t.Fatalf("expected 3 deployments, got %d", len(listing.Deployments))
	}
	head := listing.Deployments[0]
	if head.SourceCommit != "sha-api-3" || head.PreviousCommit != "sha-api-2" ||
		head.DeployClass != "IMMEDIATE" || head.Age != 30*time.Minute {
		t.Fatalf("head deployment not projected: %+v", head)
	}
	if head.RollbackState != RollbackNone {
		t.Fatalf("current head rollback state=%s want none", head.RollbackState)
	}
	// A rolled-back deployment stays reported as rolled back even though a
	// later deployment also supersedes it.
	if listing.Deployments[1].RollbackState != RollbackRolledBack {
		t.Fatalf("rolled-back state=%s", listing.Deployments[1].RollbackState)
	}
	if listing.Deployments[2].RollbackState != RollbackSuperseded {
		t.Fatalf("superseded state=%s", listing.Deployments[2].RollbackState)
	}
	if listing.Deployments[2].PreviousCommit != "" {
		t.Fatalf("the first deployment in a chain has no previous commit: %+v", listing.Deployments[2])
	}
	if db.rows.closed != 1 {
		t.Fatalf("rows closed %d times, want 1", db.rows.closed)
	}
}

func TestRecentEmptyReturnsEmptyListingNotError(t *testing.T) {
	db := &queryDB{rows: &queryRows{}}
	queries := newTestQueries(t, db)
	listing, err := queries.Recent(context.Background(), 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if listing.Deployments == nil || len(listing.Deployments) != 0 || listing.Truncated {
		t.Fatalf("no deployments must be an empty listing: %+v", listing)
	}
}

func TestRecentIncompleteDeploymentReportsNoCompletionTime(t *testing.T) {
	db := &queryDB{rows: &queryRows{values: [][]any{
		deploymentRow("api-4", "sha-api-3", "IMMEDIATE", "running", time.Minute, false, false),
	}}}
	queries := newTestQueries(t, db)
	listing, err := queries.Recent(context.Background(), 10, "prod")
	if err != nil {
		t.Fatal(err)
	}
	running := listing.Deployments[0]
	if running.Completed || !running.CompletedAt.IsZero() {
		t.Fatalf("a running deployment must not report a completion time: %+v", running)
	}
}

func TestRecentClampsLimitAndPassesEnvironmentAsAParameter(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		limit int
		want  int
	}{
		{"zero uses default", 0, DefaultListLimit},
		{"negative uses default", -3, DefaultListLimit},
		{"oversized is clamped", MaxListLimit * 10, MaxListLimit},
		{"in range is honoured", 5, 5},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := &queryDB{rows: &queryRows{}}
			queries := newTestQueries(t, db)
			if _, err := queries.Recent(context.Background(), testCase.limit, "prod"); err != nil {
				t.Fatal(err)
			}
			if got := db.args[0][0].(int); got != testCase.want {
				t.Fatalf("limit=%d want=%d", got, testCase.want)
			}
			if got := db.args[0][1].(string); got != "prod" {
				t.Fatalf("environment must be parameterised, got %q", got)
			}
		})
	}
}

func TestRecentReportsTruncationWhenTheLimitIsFilled(t *testing.T) {
	rows := &queryRows{}
	for i := 0; i < 2; i++ {
		rows.values = append(rows.values,
			deploymentRow(fmt.Sprintf("api-%d", i), "", "BATCHED", "succeeded", time.Hour, true, false))
	}
	queries := newTestQueries(t, &queryDB{rows: rows})
	listing, err := queries.Recent(context.Background(), 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if !listing.Truncated {
		t.Fatalf("filling the limit must report truncation: %+v", listing)
	}
}

func TestRecentRespectsCancellationAndSurfacesErrors(t *testing.T) {
	if _, err := NewQueries(nil, nil); !errors.Is(err, ErrRepositoryUnavailable) {
		t.Fatalf("nil database err=%v", err)
	}
	db := &queryDB{rows: &queryRows{}}
	queries := newTestQueries(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := queries.Recent(ctx, 5, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("Recent err=%v want context.Canceled", err)
	}
	if len(db.queries) != 0 {
		t.Fatalf("a cancelled context must issue no query: %v", db.queries)
	}

	boom := errors.New("connection reset")
	failing := newTestQueries(t, &queryDB{err: boom})
	if _, err := failing.Recent(context.Background(), 5, ""); !errors.Is(err, boom) {
		t.Fatalf("Recent err=%v want the underlying failure", err)
	}
}

func TestRecentQueryIsSelectOnlyBoundedAndParameterised(t *testing.T) {
	upper := strings.ToUpper(recentDeploymentsSQL)
	for _, forbidden := range []string{"INSERT ", "UPDATE ", "DELETE ", "ALTER ", "DROP ", "FOR UPDATE"} {
		if strings.Contains(upper, forbidden) {
			t.Fatalf("deployment read must be read-only, found %q", forbidden)
		}
	}
	// The LIMIT must sit inside the CTE so the decoration joins only touch
	// the bounded set.
	if !strings.Contains(recentDeploymentsSQL, "LIMIT $1\n)") {
		t.Fatalf("the scan is not bounded before the joins: %s", recentDeploymentsSQL)
	}
	if strings.Contains(recentDeploymentsSQL, "%s") || strings.Contains(recentDeploymentsSQL, "%d") {
		t.Fatalf("deployment read looks string-built rather than parameterised")
	}
}

// Not verified against a live database: ops_deployments.deploy_class is added
// by migration 000059, which is being written concurrently and is not in any
// local database yet.

package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/dbtest"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/metrics"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// refreshQuotaBurnGauges rolls provider_quota_minute up over the UTC day. Its
// only failure mode is a runtime one: a day boundary resolved in local time, or
// a sum taken across the wrong grouping, both of which produce gauges that look
// plausible and are wrong.
//
// Run with WORKER_TEST_DATABASE_URL pointed at a disposable database that has
// infra/db/migrations applied:
//
//	WORKER_TEST_DATABASE_URL='postgres://sports:sports@127.0.0.1:5433/scratch?sslmode=disable' \
//	  go test ./services/worker/cmd/worker/
func TestRefreshQuotaBurnGaugesAgainstPostgres(t *testing.T) {
	// Through dbtest rather than a local t.Skip, so CI cannot pass this test
	// by not running it. See the package comment there: every gated test in
	// this service skipped on every CI run from the day it was written,
	// because nothing set the DSN and a skip read as a pass.
	url := dbtest.URLOrSkip(t, "WORKER_TEST_DATABASE_URL", "run the quota burn rollup against PostgreSQL")
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	// search_path and TimeZone are per-connection, and the query under test
	// runs on whichever connection the pool hands out.
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var exists bool
	if err := db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_tables
		   WHERE schemaname = 'public' AND tablename = 'provider_quota_minute')`).Scan(&exists); err != nil {
		t.Fatalf("check for public.provider_quota_minute: %v", err)
	}
	if !exists {
		t.Skip("public.provider_quota_minute is missing; apply infra/db/migrations to the test database")
	}

	const schema = "worker_quota_burn_test"
	exec := func(statements ...string) {
		t.Helper()
		for _, statement := range statements {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				t.Fatalf("exec %.70s: %v", strings.ReplaceAll(statement, "\n", " "), err)
			}
		}
	}
	exec("DROP SCHEMA IF EXISTS "+schema+" CASCADE", "CREATE SCHEMA "+schema,
		"SET search_path TO "+schema+", public")
	defer func() {
		if _, err := db.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE"); err != nil {
			t.Logf("cleanup: %v", err)
		}
	}()

	// Non-UTC, and with a local day that begins before UTC midnight at every
	// hour: a local-time boundary would pull the pre-midnight row below into
	// today's total.
	exec("SET TimeZone TO 'Pacific/Kiritimati'")
	exec("CREATE TABLE provider_quota_minute (LIKE public.provider_quota_minute INCLUDING ALL)")

	// fixtures spans two minute buckets and two priority classes, so a rollup
	// that grouped by anything finer than endpoint_group would report 10, not
	// 22. The 1000 and the 500 are the two exclusions: before the boundary, and
	// another provider.
	exec(`
INSERT INTO provider_quota_minute
  (provider, minute_bucket, priority_class, endpoint_group, request_count)
SELECT provider, boundary + offset_from_boundary, priority_class, endpoint_group, request_count
FROM (VALUES
  ('api-football', interval '1 hour',   'P0', 'fixtures',   10),
  ('api-football', interval '1 hour',   'P4', 'fixtures',    5),
  ('api-football', interval '2 hours',  'P0', 'fixtures',    7),
  ('api-football', interval '1 hour',   'P0', 'odds',        3),
  ('api-football', interval '0',        'P4', 'backfill',    2),
  ('api-football', interval '-1 second','P0', 'fixtures', 1000),
  ('sportmonks',   interval '1 hour',   'P0', 'fixtures',  500)
) AS src (provider, offset_from_boundary, priority_class, endpoint_group, request_count),
LATERAL (SELECT date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC' AS boundary) AS b`)

	registry := metrics.New()
	// Seed a stale value so the zero-fill for a group with no spend today is
	// actually exercised rather than passing by never having been set.
	registry.SetGauge("worker_quota_today_endpoint_live", 4242)

	const planLimit = 7500
	refreshQuotaBurnGauges(ctx, db, registry, planLimit)

	response := httptest.NewRecorder()
	registry.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	for _, want := range []string{
		"worker_quota_today_endpoint_fixtures 22\n",
		"worker_quota_today_endpoint_odds 3\n",
		"worker_quota_today_endpoint_backfill 2\n",
		"worker_quota_today_endpoint_live 0\n",
		"worker_quota_today_endpoint_reference 0\n",
		"worker_quota_today_requests 27\n",
		"worker_quota_plan_limit 7500\n",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in gauges:\n%s", want, body)
		}
	}
	// 1000 before the boundary plus 500 from another provider must not appear
	// anywhere in the rollup.
	for _, unwanted := range []string{"1027", "527", "1527"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("rollup included excluded rows (found %q):\n%s", unwanted, body)
		}
	}
}

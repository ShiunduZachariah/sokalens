package ingestion

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/dbtest"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Run with INGESTION_TEST_DATABASE_URL pointed at a disposable PostgreSQL
// database. Temporary tables shadow production names on this one connection.
//
// The shadowing is made structural below rather than relied upon: pg_temp
// resolves ahead of public anyway, but only for the names this test creates.
// Since CI points this at a migrated database, a table the statement names and
// the test does not create would resolve to the real, empty public table and
// the query would return nothing -- a test passing by answering nothing, which
// is the failure this whole gating exercise is about. With search_path set to
// pg_temp alone, such a name resolves nowhere and fails loudly instead.
func TestExpireElapsedRequestsAgainstPostgres(t *testing.T) {
	url := dbtest.URLOrSkip(t, "INGESTION_TEST_DATABASE_URL", "run the PostgreSQL backfill lifecycle test")
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "SET search_path TO pg_temp"); err != nil {
		t.Fatalf("restrict search_path to pg_temp: %v", err)
	}
	// Asserted, not assumed: the guard above is the whole reason an unqualified
	// name cannot reach public, and SET is per-session.
	var searchPath string
	if err := db.QueryRowContext(ctx, "SELECT current_setting('search_path')").Scan(&searchPath); err != nil {
		t.Fatalf("read search_path: %v", err)
	}
	if searchPath != "pg_temp" {
		t.Fatalf("search_path = %q, want pg_temp -- public is reachable from this session", searchPath)
	}
	for _, statement := range []string{
		`CREATE TEMP TABLE data_backfill_requests (
			id BIGINT PRIMARY KEY, status TEXT NOT NULL, expires_at TIMESTAMPTZ NOT NULL,
			completed_at TIMESTAMPTZ, updated_at TIMESTAMPTZ, last_error TEXT)`,
		`CREATE TEMP TABLE jobs (
			id BIGINT PRIMARY KEY, job_key TEXT, idempotency_key TEXT,
			status TEXT, last_error TEXT)`,
		`INSERT INTO data_backfill_requests (id, status, expires_at) VALUES
			(1, 'in_progress', now() - interval '1 day'),
			(2, 'pending', now() - interval '1 day'),
			(3, 'in_progress', now() + interval '1 day')`,
		`INSERT INTO jobs (id, job_key, idempotency_key, status, last_error)
			VALUES (11, 'backfill.team_history', 'backfill.team_history:1',
			'failed', 'provider returned status 500')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("set up temporary tables: %v", err)
		}
	}

	closed, err := NewSQLBackfillRepository(db).ExpireElapsedRequests(ctx, 10)
	if err != nil {
		t.Fatalf("ExpireElapsedRequests: %v", err)
	}
	if closed != 2 {
		t.Fatalf("closed = %d, want 2", closed)
	}
	rows, err := db.QueryContext(ctx, `SELECT id, status, completed_at IS NOT NULL, last_error
		FROM data_backfill_requests ORDER BY id`)
	if err != nil {
		t.Fatalf("query requests: %v", err)
	}
	defer rows.Close()
	for _, want := range []struct {
		id        int64
		status    string
		completed bool
		errorText string
	}{
		{1, "expired", true, "status 500"},
		{2, "expired", true, "REQUEST_EXPIRED"},
		{3, "in_progress", false, ""},
	} {
		var id int64
		var status string
		var completed bool
		var lastError sql.NullString
		if !rows.Next() {
			t.Fatalf("missing request %d: %v", want.id, rows.Err())
		}
		if err := rows.Scan(&id, &status, &completed, &lastError); err != nil {
			t.Fatalf("scan request: %v", err)
		}
		if id != want.id || status != want.status || completed != want.completed ||
			!strings.Contains(lastError.String, want.errorText) {
			t.Fatalf("request %d = (%q, completed=%t, error=%q), want (%q, %t, %q)",
				id, status, completed, lastError.String, want.status, want.completed, want.errorText)
		}
	}
}

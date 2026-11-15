package maintenance

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/dbtest"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Use the existing worker CI database; a unique schema and a one-connection
// pool keep every test statement away from migrated public application data.
func TestRawRetentionAgainstPostgres(t *testing.T) {
	url := dbtest.URLOrSkip(t, "SNAPSHOTS_TEST_DATABASE_URL", "test raw retention in an isolated PostgreSQL schema")
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	schema := fmt.Sprintf("retention_test_%d", time.Now().UnixNano())
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	exec("CREATE SCHEMA " + schema)
	defer func() {
		if _, err := db.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	}()
	exec("SET search_path TO " + schema)
	exec(`CREATE TABLE fixtures (id bigint PRIMARY KEY,status text NOT NULL,kickoff_at timestamptz NOT NULL,finished_at timestamptz);
 CREATE TABLE engine_predictions (id uuid PRIMARY KEY,fixture_id bigint REFERENCES fixtures(id),generated_at timestamptz NOT NULL,public_id bigint NOT NULL);
 CREATE INDEX ON engine_predictions(fixture_id,generated_at,id);
 CREATE TABLE prediction_results(prediction_id uuid PRIMARY KEY REFERENCES engine_predictions(id) ON DELETE CASCADE);
 CREATE TABLE prediction_tracker_state(prediction_id uuid PRIMARY KEY REFERENCES engine_predictions(id) ON DELETE CASCADE,state text CHECK(state IN ('unknown','new')));
 CREATE TABLE raw_prediction_retention_recovery(prediction_id uuid PRIMARY KEY,release_sha text,deleted_at timestamptz DEFAULT now(),prediction_data jsonb,result_data jsonb,tracker_data jsonb);
 CREATE TABLE prediction_result_rollup_ledger(prediction_id uuid);
 CREATE TABLE model_evaluation_facts(prediction_id uuid);
 CREATE TABLE saved_predictions(prediction_id uuid);
 CREATE TABLE entity_favorites(entity_type text,entity_id text);
 CREATE TABLE acca_slips(id bigint,status text);
 CREATE TABLE acca_slip_legs(slip_id bigint,prediction_source text,prediction_id text);
 CREATE TABLE raw_prediction_drain_cursor(name text PRIMARY KEY,finished_at timestamptz,fixture_id bigint,generated_at timestamptz,prediction_id uuid,updated_at timestamptz DEFAULT now());
 CREATE TABLE data_maintenance_audit(operation text,criteria text,rows_examined bigint,rows_changed bigint,details jsonb);`)
	now := time.Now().UTC().Truncate(time.Microsecond)
	id := func(n int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012d", n) }
	for n := 1; n <= 12; n++ {
		status, kickoff := "unknown", now.Add(-8*24*time.Hour)
		var finish any
		switch n {
		case 1:
			status = "finished"
			finish = now.Add(-48 * time.Hour)
		case 2:
			status = "finished"
			finish = now.Add(-12 * time.Hour)
		case 3:
			status = "upcoming"
			kickoff = now.Add(24 * time.Hour)
		case 5:
			kickoff = now.Add(-3 * 24 * time.Hour)
		case 6:
			status = "postponed"
			kickoff = now.Add(24 * time.Hour)
		case 7:
			status = "cancelled"
		}
		exec("INSERT INTO fixtures VALUES($1,$2,$3,$4)", n, status, kickoff, finish)
		exec("INSERT INTO engine_predictions VALUES($1,$2,$3,$2)", id(n), n, now.Add(-9*24*time.Hour))
	}
	exec("INSERT INTO saved_predictions VALUES($1)", id(8))
	exec("INSERT INTO entity_favorites VALUES('prediction','9')")
	exec("INSERT INTO acca_slips VALUES(1,'active')")
	exec("INSERT INTO acca_slip_legs VALUES(1,'engine',$1)", id(10))
	for _, n := range []int{11, 12} {
		exec("INSERT INTO prediction_results VALUES($1)", id(n))
	}
	exec("INSERT INTO prediction_result_rollup_ledger VALUES($1)", id(12))
	exec("INSERT INTO model_evaluation_facts VALUES($1)", id(12))
	exec("INSERT INTO prediction_tracker_state VALUES($1,'unknown')", id(12))
	repository := NewRepository(db)
	policy := Payload{EnableDestructiveRetention: true, DryRun: true, ExpireUnresolvedPredictions: true, BatchSize: 2, MaxBatches: 20}
	if _, err := repository.PruneRaw(ctx, policy, now); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM engine_predictions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 12 {
		t.Fatalf("dry run changed predictions: %d", count)
	}
	policy.DryRun = false
	policy.ExpireUnresolvedPredictions = false
	if _, err := repository.PruneRaw(ctx, policy, now); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM engine_predictions WHERE id=$1)", id(4)).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("unresolved drain ran without its gate")
	}
	policy.ExpireUnresolvedPredictions = true
	policy.RetentionReleaseSHA = strings.Repeat("a", 40)
	policy.MaxBatches = 2
	// Repeated tiny batches must advance past protected rows and retain every
	// recent, future, saved, active-slip or unaggregated-result reference.
	var deleted int64
	for i := 0; i < 8; i++ {
		m, err := repository.PruneRaw(ctx, policy, now)
		if err != nil {
			t.Fatal(err)
		}
		if m.RawPredictionsExamined > 4 {
			t.Fatalf("unbounded batch: %+v", m)
		}
		deleted += m.UnresolvedRawPredictions
	}
	if deleted != 3 {
		t.Fatalf("expired deletions=%d, want 3", deleted)
	}
	for n := 1; n <= 12; n++ {
		if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM engine_predictions WHERE id=$1)", id(n)).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		want := n != 1 && n != 4 && n != 7 && n != 12
		if exists != want {
			t.Errorf("prediction %d exists=%t want=%t", n, exists, want)
		}
	}
	if err := db.QueryRow("SELECT count(*) FROM fixtures").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 12 {
		t.Fatal("raw retention removed canonical fixture history")
	}

	// Restore exactly the new-expiry release while preserving newer corrections
	// and unrelated live rows. A failed child insert must roll back the batch.
	sqlBody, err := os.ReadFile("../../../../infra/deploy/restore-expired-predictions.sql")
	if err != nil {
		t.Fatal(err)
	}
	restoreSQL := strings.ReplaceAll(string(sqlBody), ":'release_sha'", "$1")
	exec("INSERT INTO engine_predictions VALUES($1,4,$2,4)", id(4), now)
	var recovered int64
	if err := db.QueryRowContext(ctx, restoreSQL, strings.Repeat("c", 40)).Scan(&recovered); err != nil {
		t.Fatal(err)
	}
	if recovered != 0 {
		t.Fatal("recovery crossed a release boundary")
	}
	exec(`UPDATE raw_prediction_retention_recovery SET tracker_data=jsonb_set(tracker_data,'{state}','"invalid"'::jsonb) WHERE prediction_id=$1`, id(12))
	if err := db.QueryRowContext(ctx, restoreSQL, policy.RetentionReleaseSHA).Scan(&recovered); err == nil {
		t.Fatal("invalid child data did not fail recovery")
	}
	if err := db.QueryRow("SELECT count(*) FROM raw_prediction_retention_recovery").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatal("failed recovery lost its archive")
	}
	if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM engine_predictions WHERE id=$1)", id(7)).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("failed recovery retained a partial parent insert")
	}
	exec(`UPDATE raw_prediction_retention_recovery SET tracker_data=jsonb_set(tracker_data,'{state}','"unknown"'::jsonb) WHERE prediction_id=$1`, id(12))
	if err := db.QueryRowContext(ctx, restoreSQL, policy.RetentionReleaseSHA).Scan(&recovered); err != nil {
		t.Fatal(err)
	}
	if recovered != 3 {
		t.Fatalf("restored archive rows=%d", recovered)
	}
	for _, n := range []int{4, 7, 12} {
		if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM engine_predictions WHERE id=$1)", id(n)).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("prediction %d was not restored", n)
		}
	}
	var generated time.Time
	if err := db.QueryRow("SELECT generated_at FROM engine_predictions WHERE id=$1", id(4)).Scan(&generated); err != nil {
		t.Fatal(err)
	}
	if !generated.Equal(now) {
		t.Fatal("restoration overwrote a newer live row")
	}
	if err := db.QueryRow("SELECT count(*) FROM prediction_results WHERE prediction_id=$1", id(12)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("settlement detail was not restored")
	}
	if err := db.QueryRow("SELECT count(*) FROM prediction_tracker_state WHERE prediction_id=$1", id(12)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("tracker state was not restored")
	}
	if err := db.QueryRowContext(ctx, restoreSQL, policy.RetentionReleaseSHA).Scan(&recovered); err != nil {
		t.Fatal(err)
	}
	if recovered != 0 {
		t.Fatal("recovery was not idempotent")
	}
}

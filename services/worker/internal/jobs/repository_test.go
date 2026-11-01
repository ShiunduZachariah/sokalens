package jobs

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeDB struct {
	query     string
	args      []any
	execQuery string
	execArgs  []any
	row       fakeRow
	err       error
	execRows  *int64
}

func (f *fakeDB) QueryRowContext(_ context.Context, query string, args ...any) scanner {
	f.query = query
	f.args = args
	return f.row
}

func (f *fakeDB) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	f.execQuery = query
	f.execArgs = args
	rows := int64(1)
	if f.execRows != nil {
		rows = *f.execRows
	}
	return fakeResult(rows), f.err
}

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, value := range r.values {
		switch d := dest[i].(type) {
		case *int64:
			*d = value.(int64)
		case *int:
			*d = value.(int)
		case *string:
			*d = value.(string)
		case *[]byte:
			*d = value.([]byte)
		}
	}
	return nil
}

type fakeResult int64

func (f fakeResult) LastInsertId() (int64, error) { return int64(f), nil }
func (f fakeResult) RowsAffected() (int64, error) { return int64(f), nil }

func TestRepositoryEnqueueDeduplicatesCompletedScheduleSlots(t *testing.T) {
	db := &fakeDB{row: fakeRow{values: []any{int64(42)}}}
	repo := NewRepository(db)

	id, err := repo.Enqueue(context.Background(), NewJob{
		JobKey:               "provider.api-football.countries",
		Queue:                "provider-reference",
		Priority:             20,
		Payload:              []byte(`{"endpoint":"countries"}`),
		IdempotencyKey:       "api-football:countries:{}",
		DeduplicateCompleted: true,
		RunAfter:             time.Date(2026, 4, 29, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	if id != 42 {
		t.Fatalf("id = %d, want 42", id)
	}
	if !strings.Contains(db.query, "WITH existing AS") || !strings.Contains(db.query, "$7::boolean") || !strings.Contains(db.query, "idempotency_key = $6") {
		t.Fatalf("query = %s, want lifetime idempotency lookup", db.query)
	}
	if !strings.Contains(db.query, "NOT EXISTS (SELECT 1 FROM existing)") {
		t.Fatalf("query = %s, want insertion guarded by existing schedule slot", db.query)
	}
	if !strings.Contains(db.query, "max_attempts") || db.args[len(db.args)-1] != 3 {
		t.Fatalf("query = %s args=%v, want max_attempts written with configured budget", db.query, db.args)
	}
}

func TestRepositoryWithDefaultsOverridesRetryBudgetAndLease(t *testing.T) {
	db := &fakeDB{row: fakeRow{values: []any{int64(42)}}}
	repo := NewRepositoryWithDefaults(db, 5, 30*time.Minute)

	if _, err := repo.Enqueue(context.Background(), NewJob{
		JobKey:               "provider.api-football.countries",
		Queue:                "provider-reference",
		Priority:             20,
		Payload:              []byte(`{"endpoint":"countries"}`),
		IdempotencyKey:       "api-football:countries:{}",
		DeduplicateCompleted: true,
		RunAfter:             time.Date(2026, 4, 29, 8, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("Enqueue returned error: %v", err)
	}
	if got := db.args[len(db.args)-1]; got != 5 {
		t.Fatalf("max_attempts arg = %v, want 5", got)
	}

	now := time.Date(2026, 4, 29, 8, 0, 0, 0, time.UTC)
	if _, ok, err := repo.ClaimNext(context.Background(), "provider-reference", "worker-1", now); err != nil || !ok {
		t.Fatalf("ClaimNext = (%v, %v), want ok with lease override", ok, err)
	}
	// Lease seconds must be passed as the final arg of the claim query.
	if got := db.args[len(db.args)-1]; got != 1800 {
		t.Fatalf("lease seconds arg = %v, want 1800", got)
	}
}

func TestRepositoryClaimNextUsesSkipLockedAndMarksRunning(t *testing.T) {
	now := time.Date(2026, 4, 29, 8, 0, 0, 0, time.UTC)
	db := &fakeDB{row: fakeRow{values: []any{
		int64(7),
		"provider.api-football.countries",
		"provider-reference",
		[]byte(`{"endpoint":"countries"}`),
		1,
		3,
		"api-football:countries:{}",
	}}}
	repo := NewRepository(db)

	job, ok, err := repo.ClaimNext(context.Background(), "provider-reference", "worker-1", now)
	if err != nil {
		t.Fatalf("ClaimNext returned error: %v", err)
	}
	if !ok {
		t.Fatal("ClaimNext ok = false, want true")
	}
	if job.ID != 7 || job.Attempts != 1 || job.MaxAttempts != 3 {
		t.Fatalf("job = %+v, want claimed job with attempts", job)
	}
	if !strings.Contains(db.query, "FOR UPDATE SKIP LOCKED") {
		t.Fatalf("query = %s, want SKIP LOCKED", db.query)
	}
	if !strings.Contains(db.query, "status = 'running'") || !strings.Contains(db.query, "attempts = attempts + 1") {
		t.Fatalf("query = %s, want running status and attempt increment", db.query)
	}
	if !strings.Contains(db.query, "$3::timestamptz - ($4 * interval '1 second')") {
		t.Fatalf("query = %s, want parameterized lease interval", db.query)
	}
	if !strings.Contains(db.execQuery, "attempts >= max_attempts") || !strings.Contains(db.execQuery, "status = 'failed'") {
		t.Fatalf("stale cleanup query = %s, want exhausted leases failed", db.execQuery)
	}
	if !strings.Contains(db.execQuery, "$1::timestamptz - ($2 * interval '1 second')") {
		t.Fatalf("stale cleanup query = %s, want parameterized lease interval", db.execQuery)
	}
}

func TestRepositoryClaimNextReturnsFalseWhenNoJobIsAvailable(t *testing.T) {
	db := &fakeDB{row: fakeRow{err: sql.ErrNoRows}}
	repo := NewRepository(db)

	_, ok, err := repo.ClaimNext(context.Background(), "provider-reference", "worker-1", time.Now())
	if err != nil {
		t.Fatalf("ClaimNext returned error: %v", err)
	}
	if ok {
		t.Fatal("ClaimNext ok = true, want false")
	}
}

func TestRepositoryRetryAndFailClearLocksAndFenceOwnership(t *testing.T) {
	db := &fakeDB{}
	repo := NewRepository(db)

	if err := repo.Retry(context.Background(), 7, "worker-1", "temporary", time.Date(2026, 4, 29, 8, 5, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Retry returned error: %v", err)
	}
	if !strings.Contains(db.execQuery, "status = 'queued'") || !strings.Contains(db.execQuery, "locked_by = NULL") || !strings.Contains(db.execQuery, "AND locked_by = $2") {
		t.Fatalf("retry query = %s, want queued status and cleared lock", db.execQuery)
	}

	if err := repo.Fail(context.Background(), 7, "worker-1", "permanent", time.Date(2026, 4, 29, 8, 6, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Fail returned error: %v", err)
	}
	if !strings.Contains(db.execQuery, "status = 'failed'") || !strings.Contains(db.execQuery, "locked_at = NULL") || !strings.Contains(db.execQuery, "AND locked_by = $2") {
		t.Fatalf("fail query = %s, want failed status and cleared lock", db.execQuery)
	}
}

func TestRepositoryDeferRestoresAttemptAndQueuesAtExactDeadline(t *testing.T) {
	db := &fakeDB{}
	repo := NewRepository(db)
	runAfter := time.Date(2026, 9, 15, 20, 1, 0, 0, time.UTC)
	if err := repo.Defer(context.Background(), 7, "worker-1", "provider capacity unavailable", runAfter); err != nil {
		t.Fatalf("Defer returned error: %v", err)
	}
	for _, required := range []string{"status = 'queued'", "attempts = GREATEST(attempts - 1, 0)", "locked_by = NULL", "run_after = $4", "AND locked_by = $2"} {
		if !strings.Contains(db.execQuery, required) {
			t.Fatalf("defer query = %s, missing %q", db.execQuery, required)
		}
	}
	if got := db.execArgs[3]; got != runAfter {
		t.Fatalf("run_after arg=%v, want %v", got, runAfter)
	}
}

func TestRepositoryCompleteClearsPreviousRetryError(t *testing.T) {
	db := &fakeDB{}
	repo := NewRepository(db)

	if err := repo.Complete(context.Background(), 7, "worker-1", time.Date(2026, 4, 29, 8, 7, 0, 0, time.UTC)); err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if !strings.Contains(db.execQuery, "last_error = NULL") || !strings.Contains(db.execQuery, "AND locked_by = $2") {
		t.Fatalf("complete query = %s, want stale retry error cleared", db.execQuery)
	}
}

func TestRepositoryRenewsOnlyOwnedRunningLease(t *testing.T) {
	db := &fakeDB{}
	repo := NewRepository(db)
	now := time.Date(2026, 4, 29, 8, 4, 0, 0, time.UTC)
	if err := repo.RenewLease(context.Background(), 7, "worker-1", now); err != nil {
		t.Fatalf("RenewLease returned error: %v", err)
	}
	for _, required := range []string{"SET locked_at = $3", "status = 'running'", "locked_by = $2"} {
		if !strings.Contains(db.execQuery, required) {
			t.Fatalf("renew query = %s, missing %q", db.execQuery, required)
		}
	}
}

func TestRepositoryRejectsStaleOwnerTransition(t *testing.T) {
	zero := int64(0)
	db := &fakeDB{execRows: &zero}
	repo := NewRepository(db)
	if err := repo.Complete(context.Background(), 7, "stale-worker", time.Now()); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Complete error=%v, want ErrLeaseLost", err)
	}
}

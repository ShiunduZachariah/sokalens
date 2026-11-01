package jobs

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)

const (
	jobID      = "11111111-1111-4111-8111-111111111111"
	incidentID = "22222222-2222-4222-8222-222222222222"
	leaseToken = "33333333-3333-4333-8333-333333333333"
)

type fakeRow struct {
	values []any
	err    error
}

func (row fakeRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(dest) != len(row.values) {
		return errors.New("unexpected scan width")
	}
	for index, value := range row.values {
		switch target := dest[index].(type) {
		case *string:
			*target = value.(string)
		case *int:
			*target = value.(int)
		case *[]byte:
			*target = append((*target)[:0], value.([]byte)...)
		case *Status:
			*target = value.(Status)
		case *time.Time:
			*target = value.(time.Time)
		case *sql.NullTime:
			*target = value.(sql.NullTime)
		default:
			panic("unsupported scan target")
		}
	}
	return nil
}

func values(status Status, attempts int) []any {
	leaseOwner, token := "", ""
	leaseExpiry := sql.NullTime{}
	if status == StatusLeased {
		leaseOwner, token = "worker-a", leaseToken
		leaseExpiry = sql.NullTime{Time: fixedNow.Add(time.Minute), Valid: true}
	}
	return []any{
		jobID, incidentID, "critical", "reconcile", strings.Repeat("a", 64),
		900, []byte(`{"key":"value"}`), status, fixedNow, attempts, 3,
		leaseOwner, token, leaseExpiry, "", "", "", fixedNow,
		sql.NullTime{Time: fixedNow, Valid: attempts > 0}, sql.NullTime{}, sql.NullTime{}, fixedNow,
	}
}

type fakeResult int64

func (r fakeResult) LastInsertId() (int64, error) { return 0, errors.New("unsupported") }
func (r fakeResult) RowsAffected() (int64, error) { return int64(r), nil }

type fakeTx struct {
	rows      []Row
	queries   []string
	args      [][]any
	result    sql.Result
	execErr   error
	commitErr error
	commits   int
	rollbacks int
}

func (tx *fakeTx) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	tx.queries = append(tx.queries, query)
	tx.args = append(tx.args, args)
	if tx.execErr != nil {
		return nil, tx.execErr
	}
	if tx.result != nil {
		return tx.result, nil
	}
	return fakeResult(1), nil
}
func (tx *fakeTx) QueryRowContext(_ context.Context, query string, args ...any) Row {
	tx.queries = append(tx.queries, query)
	tx.args = append(tx.args, args)
	if len(tx.rows) == 0 {
		return fakeRow{err: sql.ErrNoRows}
	}
	row := tx.rows[0]
	tx.rows = tx.rows[1:]
	return row
}
func (tx *fakeTx) Commit() error   { tx.commits++; return tx.commitErr }
func (tx *fakeTx) Rollback() error { tx.rollbacks++; return nil }

type fakeDB struct {
	txs     []Tx
	options []*sql.TxOptions
}

func (db *fakeDB) BeginTx(_ context.Context, options *sql.TxOptions) (Tx, error) {
	db.options = append(db.options, options)
	if len(db.txs) == 0 {
		return nil, errors.New("no transaction")
	}
	tx := db.txs[0]
	db.txs = db.txs[1:]
	return tx, nil
}

type failingAccelerator struct{ calls int }

func (a *failingAccelerator) NotifyAvailable(context.Context, AvailabilityHint) error {
	a.calls++
	return errors.New("redis unavailable")
}

func repository(t *testing.T, tx Tx, accelerator Accelerator) *Repository {
	t.Helper()
	config := DefaultConfig()
	config.Now = func() time.Time { return fixedNow }
	repository, err := NewRepository(&fakeDB{txs: []Tx{tx}}, accelerator, config)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func enqueueRequest() EnqueueRequest {
	return EnqueueRequest{
		IncidentID: incidentID, Queue: "critical", Type: "reconcile",
		IdempotencyKey: strings.Repeat("a", 64), Priority: 900,
		Payload: []byte(`{ "key": "value" }`), MaxAttempts: 3,
	}
}

func TestEnqueueIsSerializedIdempotentAndRedisCannotLoseWork(t *testing.T) {
	tx := &fakeTx{rows: []Row{fakeRow{err: sql.ErrNoRows}, fakeRow{values: values(StatusPending, 0)}}}
	accelerator := &failingAccelerator{}
	job, err := repository(t, tx, accelerator).Enqueue(context.Background(), enqueueRequest())
	if err != nil || job.ID != jobID || accelerator.calls != 1 || tx.commits != 1 {
		t.Fatalf("job=%+v err=%v hints=%d commits=%d", job, err, accelerator.calls, tx.commits)
	}
	if len(tx.queries) != 3 || tx.queries[0] != lockIdempotencySQL || tx.queries[1] != findByKeySQL || tx.queries[2] != insertJobSQL {
		t.Fatalf("unexpected enqueue transaction: %v", tx.queries)
	}
}

func TestEnqueueRetryReturnsStoredJobAndConflictFailsClosed(t *testing.T) {
	accelerator := &failingAccelerator{}
	tx := &fakeTx{rows: []Row{fakeRow{values: values(StatusPending, 0)}}}
	job, err := repository(t, tx, accelerator).Enqueue(context.Background(), enqueueRequest())
	if err != nil || job.ID != jobID || len(tx.queries) != 2 || tx.commits != 1 || accelerator.calls != 0 {
		t.Fatalf("job=%+v err=%v queries=%d commits=%d hints=%d", job, err, len(tx.queries), tx.commits, accelerator.calls)
	}

	conflicting := enqueueRequest()
	conflicting.Priority = 600
	tx = &fakeTx{rows: []Row{fakeRow{values: values(StatusPending, 0)}}}
	_, err = repository(t, tx, nil).Enqueue(context.Background(), conflicting)
	if !errors.Is(err, ErrIdempotencyConflict) || tx.commits != 0 || tx.rollbacks != 1 {
		t.Fatalf("err=%v commits=%d rollbacks=%d", err, tx.commits, tx.rollbacks)
	}
}

func TestEnqueueCommitFailureNeverNotifiesRedis(t *testing.T) {
	accelerator := &failingAccelerator{}
	tx := &fakeTx{
		rows:      []Row{fakeRow{err: sql.ErrNoRows}, fakeRow{values: values(StatusPending, 0)}},
		commitErr: errors.New("commit connection failure"),
	}
	job, err := repository(t, tx, accelerator).Enqueue(context.Background(), enqueueRequest())
	if !errors.Is(err, ErrUnavailable) || job.ID != "" || accelerator.calls != 0 || tx.commits != 1 || tx.rollbacks != 1 {
		t.Fatalf("job=%+v err=%v hints=%d commits=%d rollbacks=%d", job, err, accelerator.calls, tx.commits, tx.rollbacks)
	}
}

func TestAtomicClaimAndLeaseFencing(t *testing.T) {
	tx := &fakeTx{rows: []Row{fakeRow{values: values(StatusLeased, 1)}}}
	job, err := repository(t, tx, nil).Claim(context.Background(), ClaimRequest{Queue: "critical", Worker: "worker-a", Lease: time.Minute})
	if err != nil || job.Status != StatusLeased || job.Attempts != 1 || tx.commits != 1 {
		t.Fatalf("job=%+v err=%v commits=%d", job, err, tx.commits)
	}
	if !strings.Contains(tx.queries[0], "FOR UPDATE SKIP LOCKED") || !strings.Contains(tx.queries[0], "priority DESC, available_at ASC, created_at ASC, id ASC") ||
		!strings.Contains(tx.queries[0], "lease_token = $4::uuid") || !strings.Contains(tx.queries[0], "j.attempts + 1") {
		t.Fatalf("claim is not atomic/fenced/ordered:\n%s", tx.queries[0])
	}

	tx = &fakeTx{rows: []Row{fakeRow{err: sql.ErrNoRows}}}
	_, err = repository(t, tx, nil).Complete(context.Background(), LeaseRequest{JobID: jobID, Worker: "worker-a", LeaseToken: leaseToken})
	if !errors.Is(err, ErrLeaseLost) || tx.commits != 0 || tx.rollbacks != 1 {
		t.Fatalf("stale completion err=%v commits=%d rollbacks=%d", err, tx.commits, tx.rollbacks)
	}
}

func TestEmptyClaimReturnsNotFoundWithoutCommit(t *testing.T) {
	tx := &fakeTx{rows: []Row{fakeRow{err: sql.ErrNoRows}}}
	job, err := repository(t, tx, nil).Claim(context.Background(), ClaimRequest{Queue: "critical", Worker: "worker-a", Lease: time.Minute})
	if !errors.Is(err, ErrNotFound) || job.ID != "" || tx.commits != 0 || tx.rollbacks != 1 {
		t.Fatalf("job=%+v err=%v commits=%d rollbacks=%d", job, err, tx.commits, tx.rollbacks)
	}
}

func TestRenewCompleteAndFailureContractsRequireLiveMatchingLease(t *testing.T) {
	for name, query := range map[string]string{"renew": renewJobSQL, "complete": completeJobSQL, "fail": failJobSQL} {
		if !strings.Contains(query, "status = 'leased'") || !strings.Contains(query, "lease_owner = $2") ||
			!strings.Contains(query, "lease_token = $3::uuid") || !strings.Contains(query, "lease_expires_at > $4") {
			t.Fatalf("%s lacks live lease fencing:\n%s", name, query)
		}
	}
	for _, required := range []string{"power(2", "attempts >= max_attempts", "dead_letter", "last_error_class = $5"} {
		if !strings.Contains(failJobSQL, required) {
			t.Fatalf("failure SQL lacks %q", required)
		}
	}
}

func TestExpiredLeaseReclaimIsBoundedAndDeadLettersExhaustion(t *testing.T) {
	tx := &fakeTx{result: fakeResult(7)}
	count, err := repository(t, tx, nil).ReclaimExpired(context.Background(), 10)
	if err != nil || count != 7 || tx.commits != 1 {
		t.Fatalf("count=%d err=%v commits=%d", count, err, tx.commits)
	}
	for _, required := range []string{"FOR UPDATE SKIP LOCKED LIMIT $2", "attempts >= j.max_attempts", "'dead_letter'", "'retry'", "lease_owner = NULL"} {
		if !strings.Contains(reclaimExpiredSQL, required) {
			t.Fatalf("reclaim SQL lacks %q", required)
		}
	}
}

func TestValidationCanonicalizesPayloadAndRejectsTrailingOrOversizedData(t *testing.T) {
	payload, err := normalizePayload([]byte(`{"b":2,"a":1}`), 100)
	if err != nil || string(payload) != `{"a":1,"b":2}` {
		t.Fatalf("payload=%s err=%v", payload, err)
	}
	for _, invalid := range [][]byte{[]byte(`[]`), []byte(`{} trailing`), nil, []byte(strings.Repeat("x", 101))} {
		if _, err = normalizePayload(invalid, 100); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("invalid payload %q err=%v", invalid, err)
		}
	}
	db := &fakeDB{}
	config := DefaultConfig()
	config.Now = func() time.Time { return fixedNow }
	repo, _ := NewRepository(db, nil, config)
	request := enqueueRequest()
	request.IdempotencyKey = "weak"
	if _, err = repo.Enqueue(context.Background(), request); !errors.Is(err, ErrInvalidRequest) || len(db.options) != 0 {
		t.Fatalf("invalid request err=%v transactions=%d", err, len(db.options))
	}
}

func TestPriorityMappingDoesNotReversePZeroSemantics(t *testing.T) {
	p0, _ := PriorityValue(PriorityP0)
	p1, _ := PriorityValue(PriorityP1)
	p2, _ := PriorityValue(PriorityP2)
	if !(p0 > p1 && p1 > p2) {
		t.Fatalf("raw descending claim order reversed semantic priority: P0=%d P1=%d P2=%d", p0, p1, p2)
	}
	if _, err := PriorityValue(PriorityClass(99)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unknown priority class err=%v", err)
	}
}

func TestUUIDGenerationIsConcurrentAndUnique(t *testing.T) {
	const count = 256
	values := make(chan string, count)
	var wait sync.WaitGroup
	for range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, err := randomUUID()
			if err != nil {
				t.Errorf("random UUID: %v", err)
				return
			}
			values <- value
		}()
	}
	wait.Wait()
	close(values)
	seen := make(map[string]struct{}, count)
	for value := range values {
		if !validUUID(value) {
			t.Fatalf("invalid UUID %q", value)
		}
		if _, exists := seen[value]; exists {
			t.Fatalf("duplicate UUID %q", value)
		}
		seen[value] = struct{}{}
	}
}

func TestMigrationIsAdditiveLegacySafeAndReversible(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "infra", "db", "migrations")
	upBytes, err := os.ReadFile(filepath.Join(root, "000040_ops_durable_jobs.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	downBytes, err := os.ReadFile(filepath.Join(root, "000040_ops_durable_jobs.down.sql"))
	if err != nil {
		t.Fatal(err)
	}
	up, down := string(upBytes), string(downBytes)
	for _, required := range []string{"ADD COLUMN last_error_class", "ADD COLUMN dead_lettered_at", "NOT VALID", "priority DESC", "Semantic P0/P1/P2"} {
		if !strings.Contains(up, required) {
			t.Fatalf("up migration lacks %q", required)
		}
	}
	for _, required := range []string{"DROP COLUMN IF EXISTS dead_lettered_at", "DROP COLUMN IF EXISTS last_error_class", "CREATE INDEX idx_ops_jobs_claim"} {
		if !strings.Contains(down, required) {
			t.Fatalf("down migration lacks %q", required)
		}
	}
	for _, forbidden := range []string{"DELETE FROM ops_jobs", "TRUNCATE", "DROP TABLE ops_jobs"} {
		if strings.Contains(up, forbidden) {
			t.Fatalf("migration mutates legacy jobs with %q", forbidden)
		}
	}
}

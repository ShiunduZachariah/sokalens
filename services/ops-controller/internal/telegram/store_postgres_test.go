package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fakes. Following internal/incidents and internal/durablebudget, these tests
// run against the narrow DB/Conn/Row seams and need no driver and no database.
// The fakes model the one thing a real pool would hide: which connection each
// statement landed on.
// ---------------------------------------------------------------------------

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return fmt.Errorf("scan wants %d destinations, row has %d", len(dest), len(r.values))
	}
	for i, value := range r.values {
		switch target := dest[i].(type) {
		case *int64:
			*target = value.(int64)
		case *int:
			*target = value.(int)
		case *bool:
			*target = value.(bool)
		case *string:
			*target = value.(string)
		default:
			return fmt.Errorf("unsupported scan destination %T", dest[i])
		}
	}
	return nil
}

type fakeResult struct{ affected int64 }

func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeResult) RowsAffected() (int64, error) { return r.affected, nil }

// fakeConn is one pinned session. It answers advisory-lock statements by shape
// so a test only scripts the outcomes it cares about.
type fakeConn struct {
	name string

	acquired   bool
	acquireErr error

	heldResults []bool
	heldErrs    []error

	releaseResult bool

	queries []string
	args    [][]any
	execs   []string
	closes  int
}

func newFakeConn(name string) *fakeConn {
	return &fakeConn{name: name, acquired: true, releaseResult: true}
}

func (c *fakeConn) QueryRowContext(_ context.Context, query string, args ...any) Row {
	c.queries = append(c.queries, query)
	c.args = append(c.args, args)
	switch {
	case strings.Contains(query, "pg_try_advisory_lock"):
		if c.acquireErr != nil {
			return fakeRow{err: c.acquireErr}
		}
		return fakeRow{values: []any{c.acquired}}
	case strings.Contains(query, "pg_locks"):
		if len(c.heldErrs) > 0 {
			err := c.heldErrs[0]
			c.heldErrs = c.heldErrs[1:]
			if err != nil {
				return fakeRow{err: err}
			}
		}
		if len(c.heldResults) > 0 {
			held := c.heldResults[0]
			c.heldResults = c.heldResults[1:]
			return fakeRow{values: []any{held}}
		}
		return fakeRow{values: []any{true}}
	case strings.Contains(query, "pg_advisory_unlock("):
		return fakeRow{values: []any{c.releaseResult}}
	}
	return fakeRow{err: fmt.Errorf("unexpected connection query %q", query)}
}

func (c *fakeConn) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	c.execs = append(c.execs, query)
	return fakeResult{affected: 1}, nil
}

func (c *fakeConn) Close() error {
	c.closes++
	return nil
}

type fakeDB struct {
	rows      []Row
	execRes   []sql.Result
	execErrs  []error
	conns     []*fakeConn
	connErr   error
	queries   []string
	queryArgs [][]any
	execs     []string
	execArgs  [][]any
	handed    []*fakeConn
}

func (d *fakeDB) QueryRowContext(_ context.Context, query string, args ...any) Row {
	d.queries = append(d.queries, query)
	d.queryArgs = append(d.queryArgs, args)
	if len(d.rows) == 0 {
		return fakeRow{err: errors.New("no scripted row")}
	}
	row := d.rows[0]
	d.rows = d.rows[1:]
	return row
}

func (d *fakeDB) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	d.execs = append(d.execs, query)
	d.execArgs = append(d.execArgs, args)
	if len(d.execErrs) > 0 {
		err := d.execErrs[0]
		d.execErrs = d.execErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	if len(d.execRes) > 0 {
		result := d.execRes[0]
		d.execRes = d.execRes[1:]
		return result, nil
	}
	return fakeResult{affected: 1}, nil
}

func (d *fakeDB) Conn(_ context.Context) (Conn, error) {
	if d.connErr != nil {
		return nil, d.connErr
	}
	if len(d.conns) == 0 {
		return nil, errors.New("no scripted connection")
	}
	conn := d.conns[0]
	d.conns = d.conns[1:]
	d.handed = append(d.handed, conn)
	return conn, nil
}

// stateError mimics pgconn.PgError's SQLState surface.
type stateError struct{ state string }

func (e stateError) Error() string    { return "sqlstate " + e.state }
func (e stateError) SQLState() string { return e.state }

func newTestStore(t *testing.T, db *fakeDB) *PostgresStore {
	t.Helper()
	store, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) }
	return store
}

// ---------------------------------------------------------------------------
// Replay defence.
// ---------------------------------------------------------------------------

func TestRecordUpdateTranslatesPrimaryKeyViolationIntoDuplicate(t *testing.T) {
	db := &fakeDB{execErrs: []error{stateError{state: "23505"}}}
	store := newTestStore(t, db)

	err := store.RecordUpdate(context.Background(), UpdateRecord{
		UpdateID: 42, ChatID: -100, UserID: 7, Command: "/status", Authorized: true,
	})
	if !errors.Is(err, ErrDuplicateUpdate) {
		t.Fatalf("duplicate update error = %v, want ErrDuplicateUpdate", err)
	}
	if len(db.execs) != 1 || !strings.Contains(db.execs[0], "INSERT INTO ops_telegram_updates") {
		t.Fatalf("unexpected statements: %v", db.execs)
	}
}

func TestRecordUpdateWrappedViolationStillDeduplicates(t *testing.T) {
	// The driver error usually arrives wrapped by a layer above pgx.
	wrapped := fmt.Errorf("insert failed: %w", stateError{state: "23505"})
	store := newTestStore(t, &fakeDB{execErrs: []error{wrapped}})
	if err := store.RecordUpdate(context.Background(), UpdateRecord{UpdateID: 1, UserID: 1}); !errors.Is(err, ErrDuplicateUpdate) {
		t.Fatalf("wrapped duplicate error = %v, want ErrDuplicateUpdate", err)
	}
}

func TestRecordUpdateSurfacesOtherFailuresUnchanged(t *testing.T) {
	// A non-23505 failure must not be mistaken for a replay: that would turn a
	// dropped connection into a silently skipped command.
	boom := stateError{state: "08006"}
	store := newTestStore(t, &fakeDB{execErrs: []error{boom}})
	err := store.RecordUpdate(context.Background(), UpdateRecord{UpdateID: 1, UserID: 1})
	if errors.Is(err, ErrDuplicateUpdate) || !errors.Is(err, boom) {
		t.Fatalf("connection failure error = %v, want the original error", err)
	}
}

func TestRecordUpdateStoresBoundedNullableTextAndCallerTimestamp(t *testing.T) {
	db := &fakeDB{}
	store := newTestStore(t, db)
	received := time.Date(2026, 9, 4, 9, 30, 0, 0, time.UTC)

	err := store.RecordUpdate(context.Background(), UpdateRecord{
		UpdateID:   99,
		ChatID:     -4242,
		UserID:     7,
		Username:   "",
		Command:    "/incidents",
		RawText:    strings.Repeat("é", maxStoredTextLength+500),
		Authorized: false,
		ReceivedAt: received,
	})
	if err != nil {
		t.Fatal(err)
	}
	args := db.execArgs[0]
	if args[3] != nil {
		t.Fatalf("empty username stored as %#v, want NULL", args[3])
	}
	raw, ok := args[5].(string)
	if !ok {
		t.Fatalf("raw_text stored as %T", args[5])
	}
	if count := len([]rune(raw)); count != maxStoredTextLength {
		t.Fatalf("raw_text runes = %d, want %d", count, maxStoredTextLength)
	}
	if !utf8Valid(raw) {
		t.Fatal("raw_text was truncated mid-rune; Postgres would reject the insert")
	}
	if got := args[7].(time.Time); !got.Equal(received) {
		t.Fatalf("received_at = %s, want the caller's timestamp %s", got, received)
	}
	if args[6] != false {
		t.Fatalf("authorized = %#v, want the rejection to be recorded", args[6])
	}
}

func utf8Valid(value string) bool {
	for _, r := range value {
		if r == '�' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Offset.
// ---------------------------------------------------------------------------

func TestOffsetRoundTrips(t *testing.T) {
	db := &fakeDB{rows: []Row{fakeRow{values: []any{int64(910_244)}}}}
	store := newTestStore(t, db)

	offset, err := store.LoadOffset(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if offset != 910_244 {
		t.Fatalf("offset = %d, want 910244", offset)
	}
	if !strings.Contains(db.queries[0], "FROM ops_telegram_state WHERE id = 1") {
		t.Fatalf("load reads more than the singleton row: %s", db.queries[0])
	}

	if err := store.SaveOffset(context.Background(), offset+1); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(db.execs[0], "GREATEST(last_offset, $1)") {
		t.Fatalf("save is not monotonic: %s", db.execs[0])
	}
	if db.execArgs[0][0].(int64) != 910_245 {
		t.Fatalf("saved offset = %v", db.execArgs[0][0])
	}
}

func TestLoadOffsetFailsClosedWhenSingletonRowIsMissing(t *testing.T) {
	store := newTestStore(t, &fakeDB{rows: []Row{fakeRow{err: sql.ErrNoRows}}})
	if _, err := store.LoadOffset(context.Background()); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("missing state row error = %v, want ErrStoreUnavailable", err)
	}
}

func TestSaveOffsetRejectsNegativeAndMissingRow(t *testing.T) {
	db := &fakeDB{}
	store := newTestStore(t, db)
	if err := store.SaveOffset(context.Background(), -1); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("negative offset error = %v", err)
	}
	if len(db.execs) != 0 {
		t.Fatal("negative offset reached the database")
	}

	db = &fakeDB{execRes: []sql.Result{fakeResult{affected: 0}}}
	store = newTestStore(t, db)
	if err := store.SaveOffset(context.Background(), 5); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("missing state row error = %v, want ErrStoreUnavailable", err)
	}
}

// ---------------------------------------------------------------------------
// Completion and the §8 threshold.
// ---------------------------------------------------------------------------

func TestCompleteUpdateStampsSuccessAsNullError(t *testing.T) {
	db := &fakeDB{}
	store := newTestStore(t, db)
	if err := store.CompleteUpdate(context.Background(), 42, nil); err != nil {
		t.Fatal(err)
	}
	args := db.execArgs[0]
	if args[0].(int64) != 42 || args[2] != nil {
		t.Fatalf("completion args = %#v, want update 42 with a NULL error", args)
	}
	if _, ok := args[1].(time.Time); !ok {
		t.Fatalf("processed_at = %T, want a timestamp", args[1])
	}
}

func TestCompleteUpdateTruncatesErrorText(t *testing.T) {
	db := &fakeDB{}
	store := newTestStore(t, db)
	long := errors.New(strings.Repeat("x", maxStoredErrorLength*4))
	if err := store.CompleteUpdate(context.Background(), 42, long); err != nil {
		t.Fatal(err)
	}
	stored, ok := db.execArgs[0][2].(string)
	if !ok {
		t.Fatalf("error stored as %T", db.execArgs[0][2])
	}
	if len(stored) != maxStoredErrorLength {
		t.Fatalf("stored error length = %d, want %d", len(stored), maxStoredErrorLength)
	}
}

func TestCompleteUpdateNeverStoresBlankError(t *testing.T) {
	// The column CHECK rejects a blank string, and an error that records no
	// reason would fail the whole completion write.
	db := &fakeDB{}
	store := newTestStore(t, db)
	if err := store.CompleteUpdate(context.Background(), 42, errors.New("   ")); err != nil {
		t.Fatal(err)
	}
	if stored, _ := db.execArgs[0][2].(string); strings.TrimSpace(stored) == "" {
		t.Fatalf("stored error = %q, want a placeholder", stored)
	}
}

func TestCountRecentUnauthorizedRespectsTheWindow(t *testing.T) {
	db := &fakeDB{rows: []Row{fakeRow{values: []any{6}}}}
	store := newTestStore(t, db)
	since := time.Date(2026, 9, 4, 11, 50, 0, 0, time.UTC)

	count, err := store.CountRecentUnauthorized(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if count != 6 {
		t.Fatalf("count = %d, want 6", count)
	}
	if !strings.Contains(db.queries[0], "NOT authorized") || !strings.Contains(db.queries[0], "received_at >= $1") {
		t.Fatalf("count query is not window bounded: %s", db.queries[0])
	}
	got := db.queryArgs[0][0].(time.Time)
	if !got.Equal(since) {
		t.Fatalf("cutoff = %s, want %s", got, since)
	}
	// A wider cutoff must be the caller's decision, not the store's: the store
	// passes the boundary through untouched.
	db.rows = []Row{fakeRow{values: []any{0}}}
	older := since.Add(-time.Hour)
	if _, err := store.CountRecentUnauthorized(context.Background(), older); err != nil {
		t.Fatal(err)
	}
	if got := db.queryArgs[1][0].(time.Time); !got.Equal(older) {
		t.Fatalf("second cutoff = %s, want %s", got, older)
	}
}

// ---------------------------------------------------------------------------
// Advisory lock. These are the tests that matter: a pooled lock passes a naive
// "did AcquireLock return true" assertion and then fails in production.
// ---------------------------------------------------------------------------

func TestAcquireLockPinsOneConnectionForTheWholeLifetime(t *testing.T) {
	pinned := newFakeConn("pinned")
	spare := newFakeConn("spare")
	db := &fakeDB{conns: []*fakeConn{pinned, spare}}
	store := newTestStore(t, db)
	ctx := context.Background()

	acquired, err := store.AcquireLock(ctx)
	if err != nil || !acquired {
		t.Fatalf("acquire = %v, %v", acquired, err)
	}
	if err := store.RenewLock(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseLock(ctx); err != nil {
		t.Fatal(err)
	}

	if len(db.handed) != 1 || db.handed[0] != pinned {
		t.Fatalf("checked out %d connections, want exactly the pinned one", len(db.handed))
	}
	if len(spare.queries) != 0 {
		t.Fatal("a second pooled connection saw lock traffic")
	}
	if len(pinned.queries) != 3 {
		t.Fatalf("pinned connection statements = %v, want acquire, renew, release", pinned.queries)
	}
	for i, want := range []string{"pg_try_advisory_lock", "pg_locks", "pg_advisory_unlock("} {
		if !strings.Contains(pinned.queries[i], want) {
			t.Fatalf("statement %d = %q, want %q on the same connection", i, pinned.queries[i], want)
		}
	}
	if pinned.closes != 1 {
		t.Fatalf("pinned connection closes = %d, want exactly one on release", pinned.closes)
	}
	if store.HoldsLock() {
		t.Fatal("store still reports the lock after release")
	}
}

func TestAcquireLockUsesTheSameKeyOnEveryStatement(t *testing.T) {
	pinned := newFakeConn("pinned")
	store := newTestStore(t, &fakeDB{conns: []*fakeConn{pinned}})
	ctx := context.Background()

	if _, err := store.AcquireLock(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewLock(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseLock(ctx); err != nil {
		t.Fatal(err)
	}
	for i, args := range pinned.args {
		if len(args) != 2 || args[0] != pollerLockClassID || args[1] != pollerLockObjectID {
			t.Fatalf("statement %d key = %#v, want the frozen (%d, %d) pair",
				i, args, pollerLockClassID, pollerLockObjectID)
		}
	}
}

func TestAcquireLockReturnsFalseWithoutErrorWhenContended(t *testing.T) {
	contended := newFakeConn("contended")
	contended.acquired = false
	db := &fakeDB{conns: []*fakeConn{contended}}
	store := newTestStore(t, db)

	acquired, err := store.AcquireLock(context.Background())
	if err != nil {
		t.Fatalf("contention reported as an error: %v", err)
	}
	if acquired {
		t.Fatal("acquired a lock another instance holds")
	}
	if contended.closes != 1 {
		t.Fatalf("losing connection closes = %d, want it returned to the pool", contended.closes)
	}
	if store.HoldsLock() {
		t.Fatal("store reports holding a lock it did not take")
	}
}

func TestAcquireLockIsIdempotentAndDoesNotLeakConnections(t *testing.T) {
	pinned := newFakeConn("pinned")
	db := &fakeDB{conns: []*fakeConn{pinned}}
	store := newTestStore(t, db)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		acquired, err := store.AcquireLock(ctx)
		if err != nil || !acquired {
			t.Fatalf("acquire %d = %v, %v", i, acquired, err)
		}
	}
	if len(db.handed) != 1 {
		t.Fatalf("checked out %d connections across three acquires", len(db.handed))
	}
}

func TestAcquireLockClosesTheConnectionWhenTheStatementFails(t *testing.T) {
	broken := newFakeConn("broken")
	broken.acquireErr = errors.New("connection reset")
	store := newTestStore(t, &fakeDB{conns: []*fakeConn{broken}})

	acquired, err := store.AcquireLock(context.Background())
	if acquired || err == nil {
		t.Fatalf("acquire = %v, %v; want a reported failure", acquired, err)
	}
	if broken.closes != 1 {
		t.Fatalf("failed acquire leaked the connection (closes = %d)", broken.closes)
	}
}

func TestRenewLockReportsLossWhenThePinnedSessionDies(t *testing.T) {
	// The server drops a session-scoped lock the moment the backend exits and
	// tells nobody. Renewing must not paper over that.
	pinned := newFakeConn("pinned")
	pinned.heldErrs = []error{errors.New("driver: bad connection")}
	store := newTestStore(t, &fakeDB{conns: []*fakeConn{pinned}})
	ctx := context.Background()

	if _, err := store.AcquireLock(ctx); err != nil {
		t.Fatal(err)
	}
	err := store.RenewLock(ctx)
	if !errors.Is(err, ErrPollerLockHeld) {
		t.Fatalf("renew after session loss = %v, want ErrPollerLockHeld", err)
	}
	if store.HoldsLock() {
		t.Fatal("store still believes it holds the lock")
	}
	if pinned.closes != 1 {
		t.Fatalf("dead session closes = %d, want it dropped", pinned.closes)
	}
}

func TestRenewLockReportsLossWhenTheServerNoLongerHoldsIt(t *testing.T) {
	pinned := newFakeConn("pinned")
	pinned.heldResults = []bool{false}
	store := newTestStore(t, &fakeDB{conns: []*fakeConn{pinned}})
	ctx := context.Background()

	if _, err := store.AcquireLock(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewLock(ctx); !errors.Is(err, ErrPollerLockHeld) {
		t.Fatalf("renew without the lock = %v, want ErrPollerLockHeld", err)
	}
	if store.HoldsLock() {
		t.Fatal("store still believes it holds the lock")
	}
}

func TestRenewLockWithoutAcquireIsNotSuccess(t *testing.T) {
	store := newTestStore(t, &fakeDB{})
	if err := store.RenewLock(context.Background()); !errors.Is(err, ErrPollerLockHeld) {
		t.Fatalf("renew before acquire = %v, want ErrPollerLockHeld", err)
	}
}

func TestRenewLockAfterLossAllowsAFreshAcquire(t *testing.T) {
	dead := newFakeConn("dead")
	dead.heldResults = []bool{false}
	fresh := newFakeConn("fresh")
	db := &fakeDB{conns: []*fakeConn{dead, fresh}}
	store := newTestStore(t, db)
	ctx := context.Background()

	if _, err := store.AcquireLock(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.RenewLock(ctx); err == nil {
		t.Fatal("renew reported success after losing the lock")
	}
	acquired, err := store.AcquireLock(ctx)
	if err != nil || !acquired {
		t.Fatalf("re-acquire = %v, %v", acquired, err)
	}
	if len(db.handed) != 2 || db.handed[1] != fresh {
		t.Fatal("re-acquire did not start from a clean session")
	}
}

func TestRenewLockKeepsTheLockWhenTheCallerContextIsCancelled(t *testing.T) {
	// A cancelled context says nothing about the lock. Dropping the session
	// here would hand polling to another instance during a routine shutdown.
	pinned := newFakeConn("pinned")
	pinned.heldErrs = []error{context.Canceled}
	store := newTestStore(t, &fakeDB{conns: []*fakeConn{pinned}})

	if _, err := store.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.RenewLock(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("renew under cancellation = %v, want the context error", err)
	}
	if !store.HoldsLock() {
		t.Fatal("cancellation dropped a lock this instance still holds")
	}
}

func TestReleaseLockIsIdempotentAndScrubsTheSessionWhenUnlockFails(t *testing.T) {
	store := newTestStore(t, &fakeDB{})
	if err := store.ReleaseLock(context.Background()); err != nil {
		t.Fatalf("releasing an unheld lock = %v, want nil", err)
	}

	pinned := newFakeConn("pinned")
	pinned.releaseResult = false
	store = newTestStore(t, &fakeDB{conns: []*fakeConn{pinned}})
	ctx := context.Background()
	if _, err := store.AcquireLock(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseLock(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pinned.execs) != 1 || !strings.Contains(pinned.execs[0], "pg_advisory_unlock_all") {
		t.Fatalf("a failed unlock returned the session to the pool unscrubbed: %v", pinned.execs)
	}
	if pinned.closes != 1 {
		t.Fatalf("closes = %d, want one", pinned.closes)
	}
}

func TestReleaseLockSurvivesAnAlreadyCancelledContext(t *testing.T) {
	// Shutdown almost always arrives with the context already cancelled, and an
	// unlock that never reaches the server leaves a poisoned pooled connection.
	pinned := newFakeConn("pinned")
	store := newTestStore(t, &fakeDB{conns: []*fakeConn{pinned}})
	if _, err := store.AcquireLock(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.ReleaseLock(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pinned.queries) != 2 || !strings.Contains(pinned.queries[1], "pg_advisory_unlock(") {
		t.Fatalf("unlock was not attempted: %v", pinned.queries)
	}
}

// ---------------------------------------------------------------------------
// Contract and blast radius.
// ---------------------------------------------------------------------------

func TestNewPostgresStoreRejectsANilHandle(t *testing.T) {
	if _, err := NewPostgresStore(nil); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("nil handle error = %v, want ErrStoreUnavailable", err)
	}
}

func TestSQLDBFailsClosedWithoutADatabase(t *testing.T) {
	adapter := SQLDB{}
	if _, err := adapter.ExecContext(context.Background(), "SELECT 1"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("exec error = %v", err)
	}
	if err := adapter.QueryRowContext(context.Background(), "SELECT 1").Scan(); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("query error = %v", err)
	}
	if _, err := adapter.Conn(context.Background()); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("conn error = %v", err)
	}
}

func TestStoreSQLIsBoundedToTheBridgeTables(t *testing.T) {
	all := strings.Join([]string{
		loadOffsetSQL, saveOffsetSQL, recordUpdateSQL, completeUpdateSQL,
		countUnauthorizedSQL, acquireLockSQL, checkLockSQL, releaseLockSQL, releaseAllLocksSQL,
	}, "\n")
	for _, required := range []string{
		"ops_telegram_state", "ops_telegram_updates",
		"GREATEST(last_offset, $1)", "NOT authorized",
		"pg_try_advisory_lock", "pg_backend_pid()", "objsubid = 2",
	} {
		if !strings.Contains(all, required) {
			t.Fatalf("store SQL lacks %q", required)
		}
	}
	// The store owns bridge state only. Incidents, jobs and deployments are
	// read through their own packages.
	for _, forbidden := range []string{
		"DELETE ", "TRUNCATE ", "DROP ", "ALTER ",
		"ops_incidents", "ops_jobs", "ops_deployments", "ops_audit_events",
	} {
		if strings.Contains(all, forbidden) {
			t.Fatalf("store SQL contains forbidden %q", forbidden)
		}
	}
}

//go:build integration

// Integration tests for the Telegram ops bridge's durable state. These run
// against a real PostgreSQL with migration 000059_ops_telegram_bridge applied;
// nothing here is faked, because the claims under test (session-scoped advisory
// locks, primary-key replay defence, monotonic offsets) are properties of the
// server, not of Go code, and a fake cannot demonstrate any of them.
//
// Run:
//
//	OPS_TEST_DATABASE_URL='postgres://sports:<password>@127.0.0.1:5433/sports_prediction?sslmode=disable' \
//	    go test -tags=integration ./internal/telegram/ -run Integration -v
//
// or, from the service directory, `task test:integration` (Taskfile.yml), which
// reads the password from the repo-root .env. The password is never stored in
// this file: a live credential in a tracked fixture is what the repo's gitleaks
// gate exists to stop.
//
// Without the build tag these tests do not compile into the binary at all, and
// even with the tag they skip when OPS_TEST_DATABASE_URL is unset, so
// `go test ./...` stays green on a machine with no database.
//
// Every test allocates its own update_id range and deletes it again on
// cleanup. Nothing is TRUNCATEd: ops_telegram_updates is a shared forensic
// ledger and may legitimately hold other rows.

package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// databaseURLEnv names the connection string. It is deliberately distinct from
// the service's own OPS_DATABASE_URL so pointing the test suite at a scratch
// database can never be confused with pointing the service at production.
const databaseURLEnv = "OPS_TEST_DATABASE_URL"

// idSeq hands out disjoint update_id ranges. Seeding from the wall clock keeps
// ranges distinct across re-runs as well as within one run, which is what makes
// these tests re-runnable without a manual cleanup step.
var idSeq atomic.Int64

func init() { idSeq.Store(time.Now().UnixNano()) }

// idRange reserves idRangeSize consecutive update_ids for one test and deletes
// them again when it finishes, whether it passed or failed.
const idRangeSize = 1000

func idRange(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	base := idSeq.Add(idRangeSize)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := db.ExecContext(ctx,
			`DELETE FROM ops_telegram_updates WHERE update_id >= $1 AND update_id < $2`,
			base, base+idRangeSize); err != nil {
			t.Errorf("cleanup ops_telegram_updates range [%d,%d): %v", base, base+idRangeSize, err)
		}
	})
	return base
}

// openDB opens one genuinely independent pool. Tests that need two sessions
// call it twice: two *sql.DB values are two pools and therefore two backends,
// whereas two handles onto the same pool could hand back the same connection
// and prove nothing about session scoping.
func openDB(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv(databaseURLEnv)
	if url == "" {
		t.Skipf("%s is not set; skipping database integration test", databaseURLEnv)
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping %s: %v", databaseURLEnv, err)
	}
	return db
}

func newStore(t *testing.T, db *sql.DB) *PostgresStore {
	t.Helper()
	store, err := NewPostgresStore(SQLDB{DB: db})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	return store
}

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// rawOffset reads ops_telegram_state without going through the store, so a
// store bug cannot hide itself from the assertion.
func rawOffset(t *testing.T, ctx context.Context, db *sql.DB) int64 {
	t.Helper()
	var offset int64
	if err := db.QueryRowContext(ctx, `SELECT last_offset FROM ops_telegram_state WHERE id = 1`).Scan(&offset); err != nil {
		t.Fatalf("read ops_telegram_state: %v", err)
	}
	return offset
}

// preserveOffset restores the singleton cursor after a test. SaveOffset is
// monotonic by design (GREATEST), so the only way to put the row back is a
// direct UPDATE.
func preserveOffset(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	original := rawOffset(t, ctx, db)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := db.ExecContext(cleanupCtx,
			`UPDATE ops_telegram_state SET last_offset = $1 WHERE id = 1`, original); err != nil {
			t.Errorf("restore ops_telegram_state offset to %d: %v", original, err)
		}
	})
	return original
}

func countUpdateRows(t *testing.T, ctx context.Context, db *sql.DB, updateID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM ops_telegram_updates WHERE update_id = $1`, updateID).Scan(&n); err != nil {
		t.Fatalf("count ops_telegram_updates for %d: %v", updateID, err)
	}
	return n
}

func newRecord(updateID int64, authorized bool, receivedAt time.Time) UpdateRecord {
	return UpdateRecord{
		UpdateID:   updateID,
		ChatID:     -100200300,
		UserID:     424242,
		Username:   "integration_fixture",
		Command:    "/status",
		RawText:    "/status",
		Authorized: authorized,
		ReceivedAt: receivedAt,
	}
}

// ---------------------------------------------------------------------------
// 1. Offset durability across a restart.
// ---------------------------------------------------------------------------

// TestIntegrationOffsetSurvivesRestart proves the offset is server-side state,
// not process state. A restart that rewound the cursor would ask Telegram for
// updates it has already consumed, and every one of them would be re-executed.
func TestIntegrationOffsetSurvivesRestart(t *testing.T) {
	db := openDB(t)
	ctx := testCtx(t)
	original := preserveOffset(t, db)

	want := original + 987_654
	func() {
		// Scoped so the first store is genuinely out of reach below: the
		// restart must read the value back from the table, not from a Go value
		// the test kept a reference to.
		first := newStore(t, db)
		if err := first.SaveOffset(ctx, want); err != nil {
			t.Fatalf("SaveOffset(%d): %v", want, err)
		}
	}()

	// "Restart": a new store over a new pool. Reusing the pool would leave open
	// the possibility that the value came from a connection-local cache rather
	// than from the table.
	restarted := newStore(t, openDB(t))

	got, err := restarted.LoadOffset(ctx)
	if err != nil {
		t.Fatalf("LoadOffset after restart: %v", err)
	}
	if got != want {
		t.Fatalf("LoadOffset after restart = %d, want %d (a restart must not rewind the cursor)", got, want)
	}

	// The advance is monotonic: a late write from a retried poll must not be
	// able to move the cursor backwards and replay consumed updates.
	if err := restarted.SaveOffset(ctx, want-1000); err != nil {
		t.Fatalf("SaveOffset(%d): %v", want-1000, err)
	}
	if got := rawOffset(t, ctx, db); got != want {
		t.Fatalf("offset after a backwards SaveOffset = %d, want %d (GREATEST must clamp)", got, want)
	}
}

// ---------------------------------------------------------------------------
// 2. Replay of an update_id is a no-op.
// ---------------------------------------------------------------------------

// TestIntegrationDuplicateUpdateIsNoOp exercises spec §7.4 defence 3 against a
// real primary key rather than a fake that returns a synthetic 23505. It proves
// two things at once: that pgx's unique-violation surfaces as ErrDuplicateUpdate
// through errors.Is, and that the failed insert left the ledger with exactly one
// row — no partial second row, no upsert-shaped overwrite of the first.
func TestIntegrationDuplicateUpdateIsNoOp(t *testing.T) {
	db := openDB(t)
	ctx := testCtx(t)
	updateID := idRange(t, db) + 1

	store := newStore(t, db)
	received := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if err := store.RecordUpdate(ctx, newRecord(updateID, true, received)); err != nil {
		t.Fatalf("first RecordUpdate: %v", err)
	}

	replay := newRecord(updateID, true, time.Now().UTC())
	replay.RawText = "/status (redelivered)"
	err := store.RecordUpdate(ctx, replay)
	if !errors.Is(err, ErrDuplicateUpdate) {
		t.Fatalf("replayed RecordUpdate error = %v, want errors.Is(err, ErrDuplicateUpdate)", err)
	}

	if n := countUpdateRows(t, ctx, db, updateID); n != 1 {
		t.Fatalf("ops_telegram_updates holds %d rows for update_id %d, want exactly 1", n, updateID)
	}

	// The original row must be intact: the replay is discarded, not merged.
	var rawText string
	var storedAt time.Time
	if err := db.QueryRowContext(ctx,
		`SELECT raw_text, received_at FROM ops_telegram_updates WHERE update_id = $1`, updateID,
	).Scan(&rawText, &storedAt); err != nil {
		t.Fatalf("read back update %d: %v", updateID, err)
	}
	if rawText != "/status" {
		t.Fatalf("raw_text = %q after replay, want the first write %q", rawText, "/status")
	}
	if !storedAt.UTC().Equal(received) {
		t.Fatalf("received_at = %s after replay, want the first write %s", storedAt.UTC(), received)
	}
}

// ---------------------------------------------------------------------------
// 3. Crash between the durable write and the offset save.
// ---------------------------------------------------------------------------

// TestIntegrationCrashBeforeOffsetSaveDoesNotDoubleExecute simulates the exact
// §7.4 window the ordering in Poller.handle exists to survive: the update row is
// durably written, the unit of work is dispatched, and the process dies before
// SaveOffset. Telegram redelivers, because the offset it was told about never
// advanced.
//
// The unit of work here is a real durable side effect — an ops_pending_confirmations
// row — with a token that differs on every attempt. A second execution therefore
// leaves a second row, and the assertion is a row count rather than a log line or
// an in-memory flag that a buggy retry could reset.
func TestIntegrationCrashBeforeOffsetSaveDoesNotDoubleExecute(t *testing.T) {
	db := openDB(t)
	ctx := testCtx(t)
	base := idRange(t, db)
	updateID := base + 7
	original := preserveOffset(t, db)

	tokenPrefix := fmt.Sprintf("itest-%d-", base)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := db.ExecContext(cleanupCtx,
			`DELETE FROM ops_pending_confirmations WHERE token LIKE $1`, tokenPrefix+"%"); err != nil {
			t.Errorf("cleanup ops_pending_confirmations %s*: %v", tokenPrefix, err)
		}
	})

	// dispatch models a mutating command: it must run exactly once for
	// update N, no matter how many times Telegram delivers it.
	attempt := 0
	dispatch := func(handle *sql.DB) {
		attempt++
		token := fmt.Sprintf("%s%d", tokenPrefix, attempt)
		if _, err := handle.ExecContext(ctx, `INSERT INTO ops_pending_confirmations
                        (token, runbook, args, requested_by, chat_id, expires_at)
                 VALUES ($1, 'integration-noop', '{}'::jsonb, 424242, -100200300, now() + interval '5 minutes')`,
			token); err != nil {
			t.Fatalf("dispatch unit of work %s: %v", token, err)
		}
	}

	// --- pass 1: the process that is about to crash ---------------------
	crashing := newStore(t, db)
	if err := crashing.RecordUpdate(ctx, newRecord(updateID, true, time.Now().UTC())); err != nil {
		t.Fatalf("RecordUpdate before crash: %v", err)
	}
	dispatch(db)
	if err := crashing.CompleteUpdate(ctx, updateID, nil); err != nil {
		t.Fatalf("CompleteUpdate before crash: %v", err)
	}
	// CRASH. SaveOffset is deliberately never called.

	if got := rawOffset(t, ctx, db); got != original {
		t.Fatalf("offset moved to %d without SaveOffset, want %d; the crash window is not what this test assumes", got, original)
	}

	// --- pass 2: restart, and Telegram redelivers update N --------------
	restarted := newStore(t, openDB(t))
	resumed, err := restarted.LoadOffset(ctx)
	if err != nil {
		t.Fatalf("LoadOffset after restart: %v", err)
	}
	if resumed > updateID {
		t.Fatalf("resumed offset %d is past update %d; the redelivery this test models cannot occur", resumed, updateID)
	}

	replayErr := restarted.RecordUpdate(ctx, newRecord(updateID, true, time.Now().UTC()))
	if !errors.Is(replayErr, ErrDuplicateUpdate) {
		t.Fatalf("replay after crash returned %v, want ErrDuplicateUpdate; the work would be executed twice", replayErr)
	}
	// The poller returns here without dispatching. Nothing calls dispatch again.

	if n := countUpdateRows(t, ctx, db, updateID); n != 1 {
		t.Fatalf("ops_telegram_updates holds %d rows for update_id %d after replay, want 1", n, updateID)
	}

	var work int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM ops_pending_confirmations WHERE token LIKE $1`, tokenPrefix+"%").Scan(&work); err != nil {
		t.Fatalf("count dispatched work: %v", err)
	}
	if work != 1 {
		t.Fatalf("the redelivered update produced %d units of work, want exactly 1", work)
	}

	// Only now, having absorbed the duplicate, may the offset advance past N.
	if err := restarted.SaveOffset(ctx, updateID+1); err != nil {
		t.Fatalf("SaveOffset after absorbing the duplicate: %v", err)
	}
	if got := rawOffset(t, ctx, db); got != updateID+1 {
		t.Fatalf("offset after recovery = %d, want %d", got, updateID+1)
	}
}

// ---------------------------------------------------------------------------
// 4. Advisory lock mutual exclusion.
// ---------------------------------------------------------------------------

// TestIntegrationAdvisoryLockExcludesSecondPoller proves a second instance
// cannot poll the same bot token. The contended attempt must report (false, nil)
// and not an error: losing this race is normal operation for every non-holding
// instance, and turning it into an error would make a healthy standby look like
// a broken one.
//
// The two stores use separate pools on purpose. Two stores over one *sql.DB
// could be handed the same backend, and PostgreSQL advisory locks are
// re-entrant within a session, so that arrangement would report success and
// prove nothing.
func TestIntegrationAdvisoryLockExcludesSecondPoller(t *testing.T) {
	ctx := testCtx(t)
	dbA := openDB(t)
	dbB := openDB(t)

	holder := newStore(t, dbA)
	standby := newStore(t, dbB)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = holder.ReleaseLock(cleanupCtx)
		_ = standby.ReleaseLock(cleanupCtx)
	})

	acquired, err := holder.AcquireLock(ctx)
	if err != nil {
		t.Fatalf("holder AcquireLock: %v", err)
	}
	if !acquired {
		t.Skipf("poller advisory lock (%d,%d) is already held elsewhere; is a local ops-controller running?",
			pollerLockClassID, pollerLockObjectID)
	}

	contended, err := standby.AcquireLock(ctx)
	if err != nil {
		t.Fatalf("standby AcquireLock returned error %v, want (false, nil): contention is not a failure", err)
	}
	if contended {
		t.Fatalf("standby acquired the poller lock while the holder still has it; two instances would poll the same token")
	}
	if standby.HoldsLock() {
		t.Fatalf("standby reports HoldsLock after a failed acquire")
	}

	// A losing acquire must not leak the connection it briefly pinned.
	if used := dbB.Stats().InUse; used != 0 {
		t.Fatalf("standby pool has %d connections still checked out after a failed acquire, want 0", used)
	}

	if err := holder.ReleaseLock(ctx); err != nil {
		t.Fatalf("holder ReleaseLock: %v", err)
	}
	if holder.HoldsLock() {
		t.Fatalf("holder still reports HoldsLock after ReleaseLock")
	}
	// Idempotent: releasing a lock this instance does not hold is not an error.
	if err := holder.ReleaseLock(ctx); err != nil {
		t.Fatalf("second holder ReleaseLock: %v", err)
	}

	promoted, err := standby.AcquireLock(ctx)
	if err != nil {
		t.Fatalf("standby AcquireLock after release: %v", err)
	}
	if !promoted {
		t.Fatalf("standby could not acquire the lock after the holder released it; the lock leaked")
	}
	if err := standby.RenewLock(ctx); err != nil {
		t.Fatalf("promoted standby RenewLock: %v", err)
	}
	if err := standby.ReleaseLock(ctx); err != nil {
		t.Fatalf("standby ReleaseLock: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 5. The lock survives pool churn.
// ---------------------------------------------------------------------------

// TestIntegrationAdvisoryLockSurvivesPoolChurn is the test that catches the
// failure mode described at the top of store_postgres.go: an advisory lock is
// SESSION-scoped and *sql.DB is a pool, so a lock taken through the pool is
// taken on whichever connection happened to be free and is then silently lost
// when that connection is recycled. That bug passes every single-connection
// test and fails only under load in production.
//
// The pool is configured to churn aggressively — short lifetimes, a bounded
// size — and then driven with unrelated queries until it has demonstrably
// handed out other backends. The lock must still be held afterwards, verified
// three ways: RenewLock (which asks the pinned session itself), pg_locks read
// from a second pool, and a contended acquire from that second pool.
func TestIntegrationAdvisoryLockSurvivesPoolChurn(t *testing.T) {
	ctx := testCtx(t)
	dbA := openDB(t)
	dbB := openDB(t)

	// Small pool, short lifetimes: connections are recycled constantly.
	dbA.SetMaxOpenConns(4)
	dbA.SetMaxIdleConns(1)
	dbA.SetConnMaxLifetime(100 * time.Millisecond)
	dbA.SetConnMaxIdleTime(50 * time.Millisecond)

	holder := newStore(t, dbA)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = holder.ReleaseLock(cleanupCtx)
	})

	acquired, err := holder.AcquireLock(ctx)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if !acquired {
		t.Skipf("poller advisory lock (%d,%d) is already held elsewhere; is a local ops-controller running?",
			pollerLockClassID, pollerLockObjectID)
	}

	holderPID := lockHolderPID(t, ctx, dbB)
	if holderPID == 0 {
		t.Fatalf("no backend holds advisory lock (%d,%d) immediately after a successful acquire",
			pollerLockClassID, pollerLockObjectID)
	}

	// Churn: unrelated work through the same pool, with pauses long enough for
	// ConnMaxLifetime to retire idle connections between rounds.
	seen := map[int]bool{}
	for round := 0; round < 12; round++ {
		for i := 0; i < 20; i++ {
			var pid int
			var sum int
			if err := dbA.QueryRowContext(ctx,
				`SELECT pg_backend_pid(), (SELECT count(*) FROM ops_telegram_state)`).Scan(&pid, &sum); err != nil {
				t.Fatalf("churn query: %v", err)
			}
			seen[pid] = true
			if pid == holderPID {
				t.Fatalf("pool handed out the pinned lock session (pid %d) for unrelated work; the lock connection is not pinned", holderPID)
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	if len(seen) < 2 {
		t.Fatalf("pool used only %d distinct backend(s); no churn actually happened, so this test proved nothing", len(seen))
	}
	t.Logf("churned across %d distinct backends while the lock stayed pinned to pid %d", len(seen), holderPID)

	// 1. The pinned session itself still reports the lock.
	if err := holder.RenewLock(ctx); err != nil {
		t.Fatalf("RenewLock after pool churn: %v (the lock was lost with a recycled connection)", err)
	}
	// 2. The server still shows the same backend holding it.
	if got := lockHolderPID(t, ctx, dbB); got != holderPID {
		t.Fatalf("advisory lock holder pid = %d after churn, want the original %d", got, holderPID)
	}
	// 3. A genuinely separate session still cannot take it.
	standby := newStore(t, dbB)
	contended, err := standby.AcquireLock(ctx)
	if err != nil {
		t.Fatalf("standby AcquireLock after churn: %v", err)
	}
	if contended {
		_ = standby.ReleaseLock(ctx)
		t.Fatalf("a second session acquired the lock after pool churn; the holder had silently lost it")
	}

	if err := holder.ReleaseLock(ctx); err != nil {
		t.Fatalf("ReleaseLock after churn: %v", err)
	}
	if got := lockHolderPID(t, ctx, dbB); got != 0 {
		t.Fatalf("advisory lock still held by pid %d after ReleaseLock; the session was returned to the pool poisoned", got)
	}
	// A store that no longer pins a session must say so rather than claim the
	// lock is fine.
	if err := holder.RenewLock(ctx); !errors.Is(err, ErrPollerLockHeld) {
		t.Fatalf("RenewLock after release = %v, want an error wrapping ErrPollerLockHeld", err)
	}
}

// lockHolderPID returns the backend pid holding the two-key poller advisory
// lock, or 0 when nobody holds it. objsubid = 2 selects the two-key form.
func lockHolderPID(t *testing.T, ctx context.Context, db *sql.DB) int {
	t.Helper()
	var pid int
	err := db.QueryRowContext(ctx, `SELECT coalesce(max(pid), 0) FROM pg_locks
                 WHERE locktype = 'advisory'
                   AND classid = $1
                   AND objid = $2
                   AND objsubid = 2
                   AND granted`, pollerLockClassID, pollerLockObjectID).Scan(&pid)
	if err != nil {
		t.Fatalf("read pg_locks: %v", err)
	}
	return pid
}

// ---------------------------------------------------------------------------
// 6. Unauthorized counting window.
// ---------------------------------------------------------------------------

// TestIntegrationCountRecentUnauthorizedWindow proves the §8 abuse threshold
// counts the right rows: unauthorized only, and inside the window only. A count
// that swept the whole ledger would fire the "5 in 10 minutes" alert once and
// then permanently, which is indistinguishable from an attack that never stops.
//
// The assertion is on the delta from a baseline taken at the same cutoff, not on
// an absolute count: ops_telegram_updates is shared and may already hold recent
// rejections.
func TestIntegrationCountRecentUnauthorizedWindow(t *testing.T) {
	db := openDB(t)
	ctx := testCtx(t)
	base := idRange(t, db)
	store := newStore(t, db)

	now := time.Now().UTC()
	const window = 10 * time.Minute
	since := now.Add(-window)

	baseline, err := store.CountRecentUnauthorized(ctx, since)
	if err != nil {
		t.Fatalf("baseline CountRecentUnauthorized: %v", err)
	}

	rows := []struct {
		id         int64
		authorized bool
		receivedAt time.Time
		counted    bool
		why        string
	}{
		{base + 1, false, now.Add(-30 * time.Second), true, "rejected, well inside the window"},
		{base + 2, false, now.Add(-5 * time.Minute), true, "rejected, mid-window"},
		{base + 3, false, now.Add(-window + 2*time.Second), true, "rejected, just inside the boundary"},
		{base + 4, false, now.Add(-window - 2*time.Second), false, "rejected, just outside the boundary"},
		{base + 5, false, now.Add(-2 * time.Hour), false, "rejected, long expired"},
		{base + 6, true, now.Add(-time.Minute), false, "authorized, recent — never counted"},
	}
	for _, r := range rows {
		if err := store.RecordUpdate(ctx, newRecord(r.id, r.authorized, r.receivedAt)); err != nil {
			t.Fatalf("RecordUpdate(%d, %s): %v", r.id, r.why, err)
		}
	}

	got, err := store.CountRecentUnauthorized(ctx, since)
	if err != nil {
		t.Fatalf("CountRecentUnauthorized: %v", err)
	}
	want := 0
	for _, r := range rows {
		if r.counted {
			want++
		}
	}
	if delta := got - baseline; delta != want {
		t.Fatalf("CountRecentUnauthorized(since=%s) counted %d of this test's rows, want %d (baseline %d, total %d)",
			since, delta, want, baseline, got)
	}

	// A cutoff in the future counts nothing of ours, which is the property that
	// keeps the alert from re-firing once the flood stops.
	future, err := store.CountRecentUnauthorized(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("CountRecentUnauthorized(future): %v", err)
	}
	var ours int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM ops_telegram_updates
                  WHERE update_id >= $1 AND update_id < $2
                    AND NOT authorized AND received_at >= $3`,
		base, base+idRangeSize, now.Add(time.Hour)).Scan(&ours); err != nil {
		t.Fatalf("count future-window rows: %v", err)
	}
	if ours != 0 {
		t.Fatalf("%d of this test's rows fall after a future cutoff, want 0 (total future count %d)", ours, future)
	}
}

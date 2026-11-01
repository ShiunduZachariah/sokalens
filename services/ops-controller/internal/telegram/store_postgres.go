package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrStoreUnavailable marks a persistence failure the poller must treat as
// "stop polling" rather than "retry harder" (spec §20).
var ErrStoreUnavailable = errors.New("telegram: store unavailable")

// ---------------------------------------------------------------------------
// Advisory lock identity.
//
// The poller lock uses the two-key form, pg_try_advisory_lock(int4, int4),
// deliberately. PostgreSQL keeps the one-key (bigint) and two-key (int4, int4)
// advisory spaces separate, and internal/incidents already takes single-key
// locks derived from hashtextextended(fingerprint). Living in the other space
// makes a collision between an incident fingerprint hash and the poller lock
// structurally impossible rather than merely unlikely.
//
// Both values are frozen. Changing either during a rolling restart would let
// the old and the new binary poll the same bot token simultaneously, which
// Telegram answers with a 409 to whichever consumer it likes.
// ---------------------------------------------------------------------------
const (
	// pollerLockClassID namespaces ops-controller's two-key advisory locks.
	pollerLockClassID = 5847
	// pollerLockObjectID identifies the Telegram poller lock within that class.
	pollerLockObjectID = 1
)

// Storage limits. Telegram bounds a message at 4096 characters, but the ledger
// is also read from a phone and an error string from a driver or an upstream
// provider has no bound at all.
const (
	maxStoredErrorLength    = 500
	maxStoredTextLength     = 4096
	maxStoredCommandLength  = 64
	maxStoredUsernameLength = 64
)

// lockReleaseTimeout bounds the unlock round trip on the shutdown path, where
// the caller's context is usually already cancelled.
const lockReleaseTimeout = 5 * time.Second

const (
	loadOffsetSQL = `SELECT last_offset FROM ops_telegram_state WHERE id = 1`
	// GREATEST makes the offset monotonic in SQL rather than in a read-modify-
	// write the poller would have to serialize itself: a late write from a
	// retried poll cannot rewind the cursor and replay a consumed update.
	saveOffsetSQL = `UPDATE ops_telegram_state
SET last_offset = GREATEST(last_offset, $1),
    last_poll_at = now(),
    updated_at = now()
WHERE id = 1`
	recordUpdateSQL = `INSERT INTO ops_telegram_updates (
  update_id, chat_id, user_id, username, command, raw_text, authorized, received_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
	completeUpdateSQL = `UPDATE ops_telegram_updates
SET processed_at = $2,
    error = $3
WHERE update_id = $1`
	countUnauthorizedSQL = `SELECT count(*) FROM ops_telegram_updates
WHERE NOT authorized AND received_at >= $1`

	acquireLockSQL = `SELECT pg_try_advisory_lock($1, $2)`
	// checkLockSQL asks the server, on the pinned session itself, whether that
	// session still holds the lock. objsubid = 2 selects the two-key advisory
	// form; the one-key form records objsubid = 1.
	checkLockSQL = `SELECT EXISTS (
  SELECT 1 FROM pg_locks
  WHERE locktype = 'advisory'
    AND classid = $1
    AND objid = $2
    AND objsubid = 2
    AND pid = pg_backend_pid()
    AND granted
)`
	releaseLockSQL     = `SELECT pg_advisory_unlock($1, $2)`
	releaseAllLocksSQL = `SELECT pg_advisory_unlock_all()`
)

// ---------------------------------------------------------------------------
// Persistence seams. DB, Conn and Row are database/sql-shaped so production
// wiring can hand over a *sql.DB while tests need no driver, matching
// internal/incidents and internal/durablebudget.
//
// Conn is the part that is not merely convention. See AcquireLock.
// ---------------------------------------------------------------------------

// DB is the pooled handle used for every stateless statement.
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) Row
	// Conn checks out one connection and pins it until Close. The advisory
	// lock cannot be taken on the pool.
	Conn(ctx context.Context) (Conn, error)
}

// Conn is a single pinned session.
type Conn interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) Row
	// Close returns the session to the pool. It must not be called while a
	// session-scoped lock is still held on it.
	Close() error
}

// Row is a single-row result.
type Row interface{ Scan(dest ...any) error }

// SQLDB adapts *sql.DB to DB.
type SQLDB struct{ DB *sql.DB }

// ExecContext implements DB.
func (d SQLDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if d.DB == nil {
		return nil, ErrStoreUnavailable
	}
	return d.DB.ExecContext(ctx, query, args...)
}

// QueryRowContext implements DB.
func (d SQLDB) QueryRowContext(ctx context.Context, query string, args ...any) Row {
	if d.DB == nil {
		return errorRow{err: ErrStoreUnavailable}
	}
	return d.DB.QueryRowContext(ctx, query, args...)
}

// Conn implements DB.
func (d SQLDB) Conn(ctx context.Context) (Conn, error) {
	if d.DB == nil {
		return nil, ErrStoreUnavailable
	}
	conn, err := d.DB.Conn(ctx)
	if err != nil {
		return nil, err
	}
	return sqlConn{Conn: conn}, nil
}

type sqlConn struct{ Conn *sql.Conn }

func (c sqlConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.Conn.ExecContext(ctx, query, args...)
}

func (c sqlConn) QueryRowContext(ctx context.Context, query string, args ...any) Row {
	return c.Conn.QueryRowContext(ctx, query, args...)
}

func (c sqlConn) Close() error { return c.Conn.Close() }

type errorRow struct{ err error }

func (r errorRow) Scan(...any) error { return r.err }

// ---------------------------------------------------------------------------
// Store implementation.
// ---------------------------------------------------------------------------

// PostgresStore is the Postgres-backed Store. It is safe for concurrent use:
// the poller renews the lock from its own loop while command handlers record
// and complete updates.
type PostgresStore struct {
	db  DB
	now func() time.Time

	// mu guards lockConn. Every advisory-lock operation is serialized so two
	// goroutines cannot check out two sessions and each believe it is the
	// holder.
	mu       sync.Mutex
	lockConn Conn
}

var _ Store = (*PostgresStore)(nil)

// NewPostgresStore builds a Store over db.
func NewPostgresStore(db DB) (*PostgresStore, error) {
	if db == nil {
		return nil, ErrStoreUnavailable
	}
	return &PostgresStore{db: db, now: time.Now}, nil
}

// LoadOffset reads the singleton ops_telegram_state row.
func (s *PostgresStore) LoadOffset(ctx context.Context) (int64, error) {
	var offset int64
	if err := s.db.QueryRowContext(ctx, loadOffsetSQL).Scan(&offset); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Migration 000057 seeds this row. Its absence means the migration
			// has not been applied. Defaulting to 0 would ask Telegram for its
			// entire retained backlog, so fail closed instead.
			return 0, fmt.Errorf("%w: ops_telegram_state row is missing", ErrStoreUnavailable)
		}
		return 0, err
	}
	return offset, nil
}

// SaveOffset advances the persisted cursor. The advance is monotonic; a value
// at or below the stored offset is accepted and changes nothing.
func (s *PostgresStore) SaveOffset(ctx context.Context, offset int64) error {
	if offset < 0 {
		return fmt.Errorf("%w: negative offset %d", ErrStoreUnavailable, offset)
	}
	result, err := s.db.ExecContext(ctx, saveOffsetSQL, offset)
	if err != nil {
		return err
	}
	return requireSingleRow(result, "ops_telegram_state")
}

// RecordUpdate writes one forensic ledger row. A redelivered update collides on
// the update_id primary key and returns ErrDuplicateUpdate, which is the whole
// replay defence: the caller turns that into a no-op rather than a second
// execution (spec §7.4).
func (s *PostgresStore) RecordUpdate(ctx context.Context, rec UpdateRecord) error {
	if rec.UpdateID <= 0 {
		return fmt.Errorf("%w: invalid update id %d", ErrStoreUnavailable, rec.UpdateID)
	}
	receivedAt := rec.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = s.now()
	}
	_, err := s.db.ExecContext(ctx, recordUpdateSQL,
		rec.UpdateID,
		rec.ChatID,
		rec.UserID,
		nullableText(rec.Username, maxStoredUsernameLength),
		nullableText(rec.Command, maxStoredCommandLength),
		nullableText(rec.RawText, maxStoredTextLength),
		rec.Authorized,
		receivedAt.UTC(),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicateUpdate
		}
		return err
	}
	return nil
}

// CompleteUpdate stamps processed_at, and error when handling failed.
func (s *PostgresStore) CompleteUpdate(ctx context.Context, updateID int64, handlerErr error) error {
	if updateID <= 0 {
		return fmt.Errorf("%w: invalid update id %d", ErrStoreUnavailable, updateID)
	}
	var stored any
	if handlerErr != nil {
		// Truncate before writing, not after reading: a driver error can carry
		// a whole query and an upstream error a whole response body, and this
		// column is rendered to a phone.
		text := truncateText(strings.TrimSpace(handlerErr.Error()), maxStoredErrorLength)
		if text == "" {
			// The column's CHECK rejects a blank string, and a failure that
			// records no reason is worse than a placeholder.
			text = "unspecified handler failure"
		}
		stored = text
	}
	result, err := s.db.ExecContext(ctx, completeUpdateSQL, updateID, s.now().UTC(), stored)
	if err != nil {
		return err
	}
	return requireSingleRow(result, "ops_telegram_updates")
}

// CountRecentUnauthorized counts rejections since a cutoff, backing the §8
// "more than 5 in 10 minutes" one-shot notification.
func (s *PostgresStore) CountRecentUnauthorized(ctx context.Context, since time.Time) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, countUnauthorizedSQL, since.UTC()).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// ---------------------------------------------------------------------------
// Advisory lock.
//
// PostgreSQL advisory locks are SESSION-scoped, and *sql.DB is a connection
// pool. Taking the lock through the pool would work exactly once and then fail
// in a way that looks like success: the acquire lands on connection A, the
// renew a minute later lands on connection B and sees nothing, and the release
// on connection C unlocks nothing at all — leaving connection A poisoned in the
// pool holding a lock no code can reach. From then on no instance can ever
// acquire the lock again, and the symptom is "the poller silently stopped".
//
// So the lock's whole lifetime is pinned to one checked-out *sql.Conn:
// AcquireLock checks it out, RenewLock and ReleaseLock issue their statements
// on that same connection, and only ReleaseLock hands it back.
//
// The second failure mode is the pinned session dying — a server restart, an
// idle-session timeout, a network reset. The server drops the advisory lock the
// instant that backend exits, and nothing informs the client. A RenewLock that
// merely re-ran pg_try_advisory_lock, or that just returned nil, would keep
// reporting a lock this instance no longer holds while another instance is
// already polling the same token. RenewLock therefore asks the pinned session
// itself whether it still holds the lock, and reports loss rather than success.
// ---------------------------------------------------------------------------

// AcquireLock takes the poller's advisory lock on a freshly pinned connection.
// Contention returns (false, nil): every non-holding instance is expected to
// lose this race on every attempt, and that is normal operation, not an error.
func (s *PostgresStore) AcquireLock(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lockConn != nil {
		// Already held on a live pinned session. Re-acquiring on a second
		// connection would leak the first one.
		return true, nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	var acquired bool
	if err := conn.QueryRowContext(ctx, acquireLockSQL, pollerLockClassID, pollerLockObjectID).Scan(&acquired); err != nil {
		_ = conn.Close()
		return false, err
	}
	if !acquired {
		// pg_try_advisory_lock took nothing, so this session is clean and goes
		// straight back to the pool.
		_ = conn.Close()
		return false, nil
	}
	s.lockConn = conn
	return true, nil
}

// RenewLock verifies the lock is still held by the pinned session. A session
// that died, or a lock that is no longer ours, returns an error wrapping
// ErrPollerLockHeld so the caller stops polling; the pinned connection is
// dropped so a later AcquireLock starts from a clean session.
func (s *PostgresStore) RenewLock(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lockConn == nil {
		return fmt.Errorf("%w: this instance holds no advisory lock", ErrPollerLockHeld)
	}
	var held bool
	err := s.lockConn.QueryRowContext(ctx, checkLockSQL, pollerLockClassID, pollerLockObjectID).Scan(&held)
	if err != nil && ctx.Err() != nil {
		// The caller is shutting down or timed out. That says nothing about the
		// lock, so leave the pinned session alone for ReleaseLock to close.
		return err
	}
	if err != nil {
		s.releaseLockConnLocked(ctx)
		return fmt.Errorf("%w: pinned lock session lost: %v", ErrPollerLockHeld, err)
	}
	if !held {
		s.releaseLockConnLocked(ctx)
		return fmt.Errorf("%w: advisory lock is no longer held by this session", ErrPollerLockHeld)
	}
	return nil
}

// ReleaseLock drops the advisory lock and returns the pinned connection. It is
// idempotent: releasing a lock this instance does not hold is not an error.
func (s *PostgresStore) ReleaseLock(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lockConn == nil {
		return nil
	}
	return s.releaseLockConnLocked(ctx)
}

// releaseLockConnLocked unlocks and closes the pinned connection. Callers must
// hold s.mu.
//
// pg_advisory_unlock_all is a deliberate second attempt, not redundancy: a
// *sql.Conn that is Closed goes back into the pool, and a session-scoped lock
// left behind on it would be invisible and unreleasable for the rest of the
// process's life. A genuinely dead session fails both statements, and
// database/sql then discards it instead of pooling it, which is also correct.
func (s *PostgresStore) releaseLockConnLocked(ctx context.Context) error {
	conn := s.lockConn
	s.lockConn = nil
	if conn == nil {
		return nil
	}
	// Shutdown usually arrives with an already-cancelled context, and an
	// unlock that never reaches the server is exactly the leak above.
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lockReleaseTimeout)
	defer cancel()

	var released bool
	err := conn.QueryRowContext(releaseCtx, releaseLockSQL, pollerLockClassID, pollerLockObjectID).Scan(&released)
	if err != nil || !released {
		_, _ = conn.ExecContext(releaseCtx, releaseAllLocksSQL)
	}
	if closeErr := conn.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	return err
}

// HoldsLock reports whether this instance currently pins a lock session. It is
// an observability helper for the poller's readiness surface; it does not ask
// the server, so it can be stale after the session dies. RenewLock is the
// authoritative check.
func (s *PostgresStore) HoldsLock() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lockConn != nil
}

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

// sqlStateError is implemented by pgconn.PgError (github.com/jackc/pgx/v5).
// Matching the interface rather than the concrete type keeps this file free of
// a driver import and lets tests inject a violation without a database, which
// is the same approach internal/incidents takes.
type sqlStateError interface{ SQLState() string }

// uniqueViolationSQLState is Postgres 23505, raised by the ops_telegram_updates
// update_id primary key on a redelivered update.
const uniqueViolationSQLState = "23505"

func isUniqueViolation(err error) bool {
	var state sqlStateError
	return errors.As(err, &state) && state.SQLState() == uniqueViolationSQLState
}

func requireSingleRow(result sql.Result, table string) error {
	if result == nil {
		return fmt.Errorf("%w: %s write returned no result", ErrStoreUnavailable, table)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%w: %s write affected %d rows, want 1", ErrStoreUnavailable, table, affected)
	}
	return nil
}

// nullableText stores NULL rather than an empty string, so "absent" and "empty"
// stay distinguishable in the ledger.
func nullableText(value string, limit int) any {
	text := truncateText(value, limit)
	if text == "" {
		return nil
	}
	return text
}

// truncateText cuts on a rune boundary. Telegram text is UTF-8 and Postgres
// rejects an invalid byte sequence outright, so a byte-wise cut through a
// multi-byte character would fail the whole insert.
func truncateText(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// countStrandedSQL finds updates that were recorded but never completed.
//
// The replay defence in §7.4 trades at-least-once for at-most-once, which is
// right for a bot that can mutate production: a redelivered update is rejected
// as a duplicate rather than run twice. The cost is that a crash landing
// between RecordUpdate and dispatch strands the row — the command is dropped,
// and the redelivery is correctly refused. Without this count, a dropped
// command looks exactly like one that was never sent.
const countStrandedSQL = `SELECT count(*) FROM ops_telegram_updates
WHERE processed_at IS NULL AND received_at < $1`

// CountStranded reports updates recorded before the cutoff that never reached
// a terminal state. A non-zero value means at least one command was accepted
// and silently dropped, and warrants a look at the audit trail.
func (s *PostgresStore) CountStranded(ctx context.Context, before time.Time) (int64, error) {
	var count int64
	if err := s.db.QueryRowContext(ctx, countStrandedSQL, before.UTC()).Scan(&count); err != nil {
		return 0, fmt.Errorf("count stranded updates: %w", err)
	}
	return count, nil
}

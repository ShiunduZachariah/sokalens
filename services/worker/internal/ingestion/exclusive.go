package ingestion

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

type LockRelease func() error

type AdvisoryLocker interface {
	TryLock(context.Context, string) (LockRelease, bool, error)
}

type PostgresAdvisoryLocker struct {
	DB *sql.DB
}

func (l PostgresAdvisoryLocker) TryLock(ctx context.Context, key string) (LockRelease, bool, error) {
	if l.DB == nil {
		return nil, false, errors.New("advisory lock DB is nil")
	}
	if key == "" {
		return nil, false, errors.New("advisory lock key is required")
	}
	conn, err := l.DB.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	var acquired bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, key).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, false, err
	}
	if !acquired {
		_ = conn.Close()
		return nil, false, nil
	}
	release := func() error {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		err := conn.QueryRowContext(releaseCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, key).Scan(&unlocked)
		if err == nil && unlocked {
			// The lock is provably gone, so the session is clean and the pool
			// can have it back.
			return conn.Close()
		}
		// We could not prove the lock was released, and this is the case that
		// took live ingestion down for fifteen hours on 2026-09-22.
		//
		// (*sql.Conn).Close() returns the session to the pool; it does not
		// close it. A PostgreSQL advisory lock is held by the session, so
		// handing back a connection whose unlock failed leaves an idle pooled
		// connection holding pipeline:api-football:fixtures forever. Every
		// later TryLock then sees the lock held and every live and fixtures
		// job is rejected with "job already running for lock" -- 703 of them
		// had queued up, no match had changed status since midnight, and
		// nothing was in a `running` state to point at.
		//
		// So the connection is destroyed rather than reused. Postgres releases
		// every advisory lock a session holds when that session ends, which
		// makes killing the connection the one release path that cannot
		// itself fail.
		discardErr := discard(conn)
		switch {
		case err != nil:
			return fmt.Errorf("release advisory lock %q (connection discarded: %v): %w", key, discardErr, err)
		default:
			return fmt.Errorf("advisory lock %q was not held by the connection (connection discarded: %v)", key, discardErr)
		}
	}
	return release, true, nil
}

// discard removes a connection from the pool instead of returning it.
//
// Returning driver.ErrBadConn from Raw is what marks the session unusable;
// database/sql then closes it rather than reusing it. Close() is still called
// so the pool slot is freed either way.
func discard(conn *sql.Conn) error {
	rawErr := conn.Raw(func(any) error { return driver.ErrBadConn })
	closeErr := conn.Close()
	if rawErr != nil && !errors.Is(rawErr, driver.ErrBadConn) {
		return rawErr
	}
	if closeErr != nil && !errors.Is(closeErr, sql.ErrConnDone) {
		return closeErr
	}
	return nil
}

type ExclusiveHandler struct {
	Handler    JobHandler
	Locker     AdvisoryLocker
	LockKey    string
	RetryAfter time.Duration
	Logger     *slog.Logger
}

func (h ExclusiveHandler) Run(ctx context.Context, job jobs.Job) error {
	if h.Handler == nil {
		return errors.New("exclusive job handler is required")
	}
	if h.Locker == nil {
		return errors.New("exclusive job locker is required")
	}
	release, acquired, err := h.Locker.TryLock(ctx, h.LockKey)
	if err != nil {
		return fmt.Errorf("acquire exclusive job lock: %w", err)
	}
	if !acquired {
		return overlapError{key: h.LockKey, delay: contentionBackoff(h.RetryAfter)}
	}
	// Deferred, so a panic in the handler still releases the lock. Calling it
	// after the handler returns is not enough: an unwinding goroutine skips
	// the call, and the lock outlives the process's attempt to use it.
	var releaseErr error
	released := false
	releaseOnce := func() {
		if released {
			return
		}
		released = true
		releaseErr = release()
		if releaseErr != nil {
			logger := h.Logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.ErrorContext(ctx, "release exclusive job lock failed",
				"job_id", job.ID, "job_key", job.JobKey, "lock_key", h.LockKey, "error", releaseErr)
		}
	}
	defer releaseOnce()

	handlerErr := h.Handler.Run(ctx, job)
	releaseOnce()
	if releaseErr != nil && handlerErr == nil {
		return fmt.Errorf("release exclusive job lock: %w", releaseErr)
	}
	return handlerErr
}

type overlapError struct {
	key   string
	delay time.Duration
}

func (e overlapError) Error() string {
	return "job already running for lock " + e.key
}

func (e overlapError) RetryAfter() time.Duration {
	return e.delay
}

func (e overlapError) Retryable() bool {
	return true
}

// Deferrable reports that a job blocked on this lock has not run at all.
//
// This is the difference between a job waiting its turn and a job dying. A
// retry consumes an attempt, and attempts are consumed at claim time, so three
// collisions killed a job whose handler never executed -- the work was never
// tried, let alone found wanting. A defer requeues it and gives the attempt
// back.
//
// The failure was not hypothetical. The first production run of results
// reconciliation queued 25 batches against this lock; one ran and 23 died
// against a lock that was working exactly as designed, and the backlog they
// were meant to clear did not move.
//
// With this, contention is bounded only by patience: any number of contenders
// park and come back, the holder proceeds, and nothing fails. The lock keeps
// doing its job -- serialising fixture convergence, which is what #163 added
// it for -- without that serialisation being able to destroy work.
func (e overlapError) Deferrable() bool {
	return true
}

// Losing the race for a lock is not a reason to wait a minute.
//
// The deferral used to be a flat minute, which is fine when two jobs collide
// and pathological when hundreds do. After the 2026-09-22 ingestion outage the
// provider-fixtures queue held 1,153 jobs and drained at about five a minute,
// a recovery tail of roughly four hours -- because every contender that lost
// the lock slept a minute regardless of the holder finishing in a second or
// two. Throughput was set by the deferral, not by the work.
//
// That tail is not cosmetic. The reaper reconciles a finished match by
// enqueuing a fixtures job, so while the queue crawls, matches that ended
// stay on the in-play board: on that evening Bayern Munich W vs Manchester
// City W showed as second half, 2-2, for 93 minutes after it was last seen
// live.
//
// A second or two matches how long the holder actually needs, so the queue
// moves at roughly the speed of the work. The jitter matters as much as the
// interval: hundreds of contenders woken on the same schedule would retry in
// lockstep and collide again, so each waits a different amount.
const (
	defaultContentionBackoff = 1500 * time.Millisecond
	contentionBackoffJitter  = 1500 * time.Millisecond
)

func contentionBackoff(configured time.Duration) time.Duration {
	base := configured
	if base <= 0 {
		base = defaultContentionBackoff
	}
	// #nosec G404 -- spreading retries, not generating a secret.
	return base + time.Duration(rand.Int63n(int64(contentionBackoffJitter)))
}

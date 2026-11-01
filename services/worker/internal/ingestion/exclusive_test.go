package ingestion

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

type fakeAdvisoryLocker struct {
	acquired bool
	err      error
	released bool
}

func (f *fakeAdvisoryLocker) TryLock(context.Context, string) (LockRelease, bool, error) {
	return func() error {
		f.released = true
		return nil
	}, f.acquired, f.err
}

func TestExclusiveHandlerRunsAndReleases(t *testing.T) {
	locker := &fakeAdvisoryLocker{acquired: true}
	handler := &fakeHandler{}
	exclusive := ExclusiveHandler{Handler: handler, Locker: locker, LockKey: "predictions:generate"}
	if err := exclusive.Run(context.Background(), jobs.Job{ID: 42}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if handler.called != 1 || !locker.released {
		t.Fatalf("handler calls=%d released=%v", handler.called, locker.released)
	}
}

func TestExclusiveHandlerDefersOverlapWithoutRunning(t *testing.T) {
	locker := &fakeAdvisoryLocker{}
	handler := &fakeHandler{}
	exclusive := ExclusiveHandler{Handler: handler, Locker: locker, LockKey: "predictions:settle", RetryAfter: 2 * time.Minute}
	err := exclusive.Run(context.Background(), jobs.Job{ID: 43})
	if err == nil || handler.called != 0 {
		t.Fatalf("error=%v handler calls=%d", err, handler.called)
	}
	// This test was named for deferral but only ever asserted retryability,
	// which is how the gap survived: contention took the retry path, consumed
	// an attempt at claim time for a handler that never ran, and killed the
	// job after three collisions. Twenty-three of twenty-five reconciliation
	// batches died that way on their first production run.
	//
	// Deferring gives the attempt back, so a contended job waits its turn
	// instead of being spent.
	if !errorIsDeferrable(err) {
		t.Fatalf("overlap must defer, not retry: a contended handler never ran, so the attempt is not spent")
	}
	if !errorIsRetryable(err) {
		t.Fatalf("overlap must still be retryable: %v", err)
	}
	var directive retryAfterError
	if !errors.As(err, &directive) {
		t.Fatalf("overlap carried no retry directive: %v", err)
	}
	// Not an exact value any more. The configured delay is a floor and jitter
	// is added on top, because hundreds of contenders waking on the same
	// schedule retry in lockstep and collide again.
	got := directive.RetryAfter()
	if got < 2*time.Minute || got >= 2*time.Minute+contentionBackoffJitter {
		t.Fatalf("retry delay %s is outside [%s, %s)", got, 2*time.Minute, 2*time.Minute+contentionBackoffJitter)
	}
}

// Contention must not be priced at a flat minute.
//
// This is the whole point of the change. After the 2026-09-22 outage the
// provider-fixtures queue held 1,153 jobs and drained at about five a minute
// -- roughly a four-hour tail -- because every contender that lost the lock
// slept a minute while the holder finished in a second or two. Throughput was
// set by the deferral rather than by the work, and while the queue crawled,
// matches that had ended stayed on the in-play board for over an hour.
func TestLockContentionBacksOffInSecondsNotMinutes(t *testing.T) {
	locker := &fakeAdvisoryLocker{}
	handler := &fakeHandler{}
	// As wired in the production adapter.
	exclusive := ExclusiveHandler{Handler: handler, Locker: locker,
		LockKey: "pipeline:api-football:fixtures", RetryAfter: defaultContentionBackoff}

	var directive retryAfterError
	err := exclusive.Run(context.Background(), jobs.Job{ID: 1})
	if !errors.As(err, &directive) {
		t.Fatalf("overlap carried no retry directive: %v", err)
	}
	if got := directive.RetryAfter(); got > 5*time.Second {
		t.Fatalf("a contended job waits %s; at that pace a backlog takes hours "+
			"and finished matches stay on the in-play board", got)
	}
}

// Contenders must not all wake at the same instant.
func TestLockContentionIsJittered(t *testing.T) {
	seen := map[time.Duration]bool{}
	for range 40 {
		seen[contentionBackoff(defaultContentionBackoff)] = true
	}
	if len(seen) < 2 {
		t.Fatal("every contender was given an identical delay, so they will " +
			"retry in lockstep and collide again")
	}
	for delay := range seen {
		if delay < defaultContentionBackoff {
			t.Fatalf("delay %s is below the configured floor %s", delay, defaultContentionBackoff)
		}
	}
}

// panickingHandler stands in for a handler that dies mid-run.
type panickingHandler struct{}

func (panickingHandler) Run(context.Context, jobs.Job) error {
	panic("handler exploded")
}

// A panicking handler must still release the lock.
//
// pipeline:api-football:fixtures is a PostgreSQL advisory lock, held by the
// session rather than the transaction, so a lock this process fails to release
// is not released by anything else either. On 2026-09-22 one leaked and every
// live and fixtures job queued behind it for fifteen hours -- 703 of them --
// with no match changing status and nothing in a `running` state to point at.
//
// Releasing after the handler returns does not cover this: an unwinding
// goroutine never reaches that line.
func TestExclusiveHandlerReleasesLockWhenHandlerPanics(t *testing.T) {
	locker := &fakeAdvisoryLocker{acquired: true}
	handler := ExclusiveHandler{Handler: panickingHandler{}, Locker: locker, LockKey: "pipeline:test"}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected the panic to propagate to the caller")
			}
		}()
		_ = handler.Run(context.Background(), jobs.Job{ID: 1, JobKey: "test"})
	}()

	if !locker.released {
		t.Fatal("lock was not released after the handler panicked -- it would be held " +
			"by an idle pooled session and every later job would be rejected")
	}
}

// The lock must not be released twice: the deferred call and the normal call
// are the same release, and a double unlock would report a lock that is not
// held and log a spurious error on every successful job.
func TestExclusiveHandlerReleasesExactlyOnceOnTheHappyPath(t *testing.T) {
	var releases int
	locker := &countingLocker{releases: &releases}
	handler := ExclusiveHandler{Handler: &fakeHandler{}, Locker: locker, LockKey: "pipeline:test"}

	if err := handler.Run(context.Background(), jobs.Job{ID: 1, JobKey: "test"}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if releases != 1 {
		t.Fatalf("release called %d times, want exactly 1", releases)
	}
}

type countingLocker struct{ releases *int }

func (c *countingLocker) TryLock(context.Context, string) (LockRelease, bool, error) {
	return func() error { *c.releases++; return nil }, true, nil
}

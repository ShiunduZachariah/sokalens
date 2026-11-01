package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStore is a minimal in-memory JobStore. It records what the worker did so
// the tests assert on outcomes rather than on logs.
type fakeStore struct {
	mu sync.Mutex

	pending   []Job
	completed []LeaseRequest
	failed    []Failure
	renewals  atomic.Int64
	claims    atomic.Int64

	claimErr error
}

func (f *fakeStore) Claim(context.Context, ClaimRequest) (Job, error) {
	f.claims.Add(1)
	if f.claimErr != nil {
		return Job{}, f.claimErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pending) == 0 {
		return Job{}, ErrNotFound
	}
	job := f.pending[0]
	f.pending = f.pending[1:]
	return job, nil
}

func (f *fakeStore) Renew(context.Context, RenewRequest) (Job, error) {
	f.renewals.Add(1)
	return Job{}, nil
}

func (f *fakeStore) Complete(_ context.Context, req LeaseRequest) (Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completed = append(f.completed, req)
	return Job{}, nil
}

func (f *fakeStore) Fail(_ context.Context, failure Failure) (Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failed = append(f.failed, failure)
	return Job{}, nil
}

func (f *fakeStore) ReclaimExpired(context.Context, int) (int64, error) { return 0, nil }

func (f *fakeStore) outcomes() (completed []LeaseRequest, failed []Failure) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]LeaseRequest(nil), f.completed...), append([]Failure(nil), f.failed...)
}

func quietConfig(queue string) WorkerConfig {
	return WorkerConfig{
		Queue:          queue,
		Name:           "test-worker",
		Concurrency:    1,
		Lease:          3 * time.Second,
		PollInterval:   5 * time.Millisecond,
		HandlerTimeout: time.Second,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// runUntil runs the worker until fn reports done or the deadline passes.
func runUntil(t *testing.T, w *Worker, fn func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = w.Run(ctx) }()

	deadline := time.After(3 * time.Second)
	for !fn() {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("worker did not reach the expected state in time")
		default:
			time.Sleep(2 * time.Millisecond)
		}
	}
	cancel()
	<-done
}

// TestWorkerExecutesAndCompletes is the core claim: a job placed on the queue
// actually runs. Without this the whole runbook surface would enqueue work
// that nothing executes, and report success for it.
func TestWorkerExecutesAndCompletes(t *testing.T) {
	store := &fakeStore{pending: []Job{{ID: "job-1", Type: "runbook", LeaseToken: "tok-1"}}}
	var handled atomic.Int64

	w, err := NewWorker(store, HandlerFunc(func(context.Context, Job) error {
		handled.Add(1)
		return nil
	}), quietConfig("runbooks"))
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}

	runUntil(t, w, func() bool { return handled.Load() == 1 })

	completed, failed := store.outcomes()
	if len(completed) != 1 {
		t.Fatalf("completed %d jobs, want 1", len(completed))
	}
	if completed[0].JobID != "job-1" || completed[0].LeaseToken != "tok-1" {
		t.Fatalf("completed with the wrong lease: %+v", completed[0])
	}
	if len(failed) != 0 {
		t.Fatalf("unexpected failures: %+v", failed)
	}
}

// TestWorkerClassifiesFailures: an unclassified error must be retryable. The
// dangerous default is the other way round — silently marking work permanent
// drops it forever.
func TestWorkerClassifiesFailures(t *testing.T) {
	cases := map[string]struct {
		err   error
		class FailureClass
	}{
		"unclassified is retryable": {errors.New("boom"), FailureRetryable},
		"explicit permanent":        {ErrPermanent, FailurePermanent},
		"wrapped permanent":         {errors.Join(errors.New("ctx"), ErrPermanent), FailurePermanent},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{pending: []Job{{ID: "job-1", LeaseToken: "tok"}}}
			var handled atomic.Int64
			w, err := NewWorker(store, HandlerFunc(func(context.Context, Job) error {
				handled.Add(1)
				return tc.err
			}), quietConfig("runbooks"))
			if err != nil {
				t.Fatalf("NewWorker: %v", err)
			}
			runUntil(t, w, func() bool {
				_, failed := store.outcomes()
				return len(failed) == 1
			})
			_, failed := store.outcomes()
			if failed[0].Class != tc.class {
				t.Fatalf("class = %q, want %q", failed[0].Class, tc.class)
			}
		})
	}
}

// TestWorkerBoundsFailureMessage stops a verbose handler error from writing an
// unbounded string into the database.
func TestWorkerBoundsFailureMessage(t *testing.T) {
	huge := make([]byte, 5000)
	for i := range huge {
		huge[i] = 'x'
	}
	store := &fakeStore{pending: []Job{{ID: "job-1", LeaseToken: "tok"}}}
	w, err := NewWorker(store, HandlerFunc(func(context.Context, Job) error {
		return errors.New(string(huge))
	}), quietConfig("runbooks"))
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	runUntil(t, w, func() bool {
		_, failed := store.outcomes()
		return len(failed) == 1
	})
	_, failed := store.outcomes()
	if len(failed[0].Message) > 600 {
		t.Fatalf("failure message was not bounded: %d bytes", len(failed[0].Message))
	}
}

// TestWorkerIsBounded: the pool must not spawn a goroutine per job. Concurrency
// 1 means at most one handler runs at a time, so an alert storm grows the queue
// rather than memory.
func TestWorkerIsBounded(t *testing.T) {
	jobs := make([]Job, 0, 20)
	for i := 0; i < 20; i++ {
		jobs = append(jobs, Job{ID: "job", LeaseToken: "tok"})
	}
	store := &fakeStore{pending: jobs}

	var inFlight, maxInFlight atomic.Int64
	var handled atomic.Int64

	w, err := NewWorker(store, HandlerFunc(func(context.Context, Job) error {
		cur := inFlight.Add(1)
		for {
			prev := maxInFlight.Load()
			if cur <= prev || maxInFlight.CompareAndSwap(prev, cur) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		inFlight.Add(-1)
		handled.Add(1)
		return nil
	}), quietConfig("runbooks"))
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}

	runUntil(t, w, func() bool { return handled.Load() == 20 })

	if got := maxInFlight.Load(); got > 1 {
		t.Fatalf("max concurrent handlers = %d, want at most 1 for concurrency 1", got)
	}
}

// TestWorkerSurvivesClaimErrors: a failing claim must not spin or exit the
// pool. Storage blips are transient.
func TestWorkerSurvivesClaimErrors(t *testing.T) {
	store := &fakeStore{claimErr: errors.New("database unavailable")}
	w, err := NewWorker(store, HandlerFunc(func(context.Context, Job) error { return nil }), quietConfig("runbooks"))
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	runUntil(t, w, func() bool { return store.claims.Load() >= 3 })
	if store.claims.Load() < 3 {
		t.Fatal("worker stopped claiming after an error")
	}
}

// TestWorkerConfigKeepsHandlerInsideLease: a handler must never be allowed to
// outlive its lease, or two workers could run the same job concurrently.
func TestWorkerConfigKeepsHandlerInsideLease(t *testing.T) {
	cases := []WorkerConfig{
		{Lease: 30 * time.Second, HandlerTimeout: 60 * time.Second},
		{Lease: 30 * time.Second, HandlerTimeout: 30 * time.Second},
		{Lease: 30 * time.Second},
	}
	for _, cfg := range cases {
		cfg.applyDefaults()
		if cfg.HandlerTimeout >= cfg.Lease {
			t.Fatalf("handler timeout %s is not inside lease %s", cfg.HandlerTimeout, cfg.Lease)
		}
	}
}

func TestNewWorkerValidates(t *testing.T) {
	if _, err := NewWorker(nil, HandlerFunc(func(context.Context, Job) error { return nil }), WorkerConfig{}); err == nil {
		t.Fatal("expected an error for a nil store")
	}
	if _, err := NewWorker(&fakeStore{}, nil, WorkerConfig{}); err == nil {
		t.Fatal("expected an error for a nil handler")
	}
}

package ingestion

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

type fakeJobStore struct {
	job       jobs.Job
	hasJob    bool
	claimErr  error
	completed int64
	retried   int64
	deferred  int64
	retryAt   time.Time
	failed    int64
	renewed   int64
	renewErr  error
	// renewals, when set, is signalled after each recorded renewal. It needs a
	// buffer: the send never blocks, so the lease goroutine is never held up.
	renewals chan struct{}
}

func (f *fakeJobStore) ClaimNext(context.Context, string, string, time.Time) (jobs.Job, bool, error) {
	return f.job, f.hasJob, f.claimErr
}

func (f *fakeJobStore) RenewLease(_ context.Context, id int64, _ string, _ time.Time) error {
	f.renewed = id
	select {
	case f.renewals <- struct{}{}:
	default:
	}
	return f.renewErr
}

func (f *fakeJobStore) Complete(_ context.Context, id int64, _ string, _ time.Time) error {
	f.completed = id
	return nil
}

func (f *fakeJobStore) Retry(_ context.Context, id int64, _, _ string, runAfter time.Time) error {
	f.retried = id
	f.retryAt = runAfter
	return nil
}

func (f *fakeJobStore) Defer(_ context.Context, id int64, _, _ string, runAfter time.Time) error {
	f.deferred = id
	f.retryAt = runAfter
	return nil
}

func (f *fakeJobStore) Fail(_ context.Context, id int64, _, _ string, _ time.Time) error {
	f.failed = id
	return nil
}

type blockingHandler struct {
	started  chan struct{}
	released chan struct{}
	canceled chan struct{}
}

func (h *blockingHandler) Run(ctx context.Context, _ jobs.Job) error {
	close(h.started)
	select {
	case <-h.released:
		return nil
	case <-ctx.Done():
		close(h.canceled)
		return ctx.Err()
	}
}

type fakeHandler struct {
	called int
	err    error
	job    jobs.Job
}

type permanentTestError struct{ message string }

func (e permanentTestError) Error() string   { return e.message }
func (e permanentTestError) Retryable() bool { return false }

type retryAfterTestError struct {
	message string
	delay   time.Duration
}

func (e retryAfterTestError) Error() string             { return e.message }
func (e retryAfterTestError) RetryAfter() time.Duration { return e.delay }

type deferredTestError struct {
	message string
	delay   time.Duration
}

func (e deferredTestError) Error() string             { return e.message }
func (e deferredTestError) Deferrable() bool          { return true }
func (e deferredTestError) RetryAfter() time.Duration { return e.delay }

func (f *fakeHandler) Run(_ context.Context, job jobs.Job) error {
	f.called++
	f.job = job
	return f.err
}

func TestProcessorCompletesHandledJob(t *testing.T) {
	store := &fakeJobStore{hasJob: true, job: jobs.Job{ID: 7, JobKey: CountriesJobKey(), Attempts: 1, MaxAttempts: 3}}
	handler := &fakeHandler{}
	processor := NewProcessor(ProcessorOptions{
		Jobs:     store,
		Queue:    "provider-reference",
		WorkerID: "worker-1",
		Handlers: map[string]JobHandler{CountriesJobKey(): handler},
	})

	processed, err := processor.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("ProcessOnce returned error: %v", err)
	}
	if !processed {
		t.Fatal("processed = false, want true")
	}
	if handler.called != 1 || store.completed != 7 {
		t.Fatalf("handler called %d completed %d, want 1 and 7", handler.called, store.completed)
	}
	if handler.job.ID != 7 {
		t.Fatalf("handler job id = %d, want 7", handler.job.ID)
	}
}

func TestProcessorRetriesTemporaryHandlerError(t *testing.T) {
	store := &fakeJobStore{hasJob: true, job: jobs.Job{ID: 7, JobKey: CountriesJobKey(), Attempts: 1, MaxAttempts: 3}}
	handler := &fakeHandler{err: errors.New("temporary")}
	processor := NewProcessor(ProcessorOptions{
		Jobs:     store,
		Queue:    "provider-reference",
		WorkerID: "worker-1",
		Handlers: map[string]JobHandler{CountriesJobKey(): handler},
	})

	processed, err := processor.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("ProcessOnce returned error: %v", err)
	}
	if !processed || store.retried != 7 || store.failed != 0 {
		t.Fatalf("processed=%v retried=%d failed=%d, want retry only", processed, store.retried, store.failed)
	}
}

func TestProcessorFailsPermanentHandlerErrorWithoutRetry(t *testing.T) {
	store := &fakeJobStore{hasJob: true, job: jobs.Job{ID: 8, JobKey: CountriesJobKey(), Attempts: 1, MaxAttempts: 3}}
	handler := &fakeHandler{err: permanentTestError{message: "provider plan rejects date"}}
	processor := NewProcessor(ProcessorOptions{
		Jobs: store, Queue: "provider-reference", WorkerID: "worker-1",
		Handlers: map[string]JobHandler{CountriesJobKey(): handler},
	})

	processed, err := processor.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("ProcessOnce returned error: %v", err)
	}
	if !processed || store.failed != 8 || store.retried != 0 {
		t.Fatalf("processed=%v failed=%d retried=%d, want immediate failure", processed, store.failed, store.retried)
	}
}

func TestProcessorUsesRetryAfterAndExponentialBackoff(t *testing.T) {
	now := time.Date(2026, 7, 14, 20, 0, 0, 0, time.UTC)
	store := &fakeJobStore{hasJob: true, job: jobs.Job{ID: 9, JobKey: CountriesJobKey(), Attempts: 3, MaxAttempts: 5}}
	handler := &fakeHandler{err: retryAfterTestError{message: "rate limited", delay: 10 * time.Minute}}
	processor := NewProcessor(ProcessorOptions{
		Jobs: store, Queue: "provider-reference", WorkerID: "worker-1",
		Handlers:   map[string]JobHandler{CountriesJobKey(): handler},
		RetryDelay: time.Minute,
		Now:        func() time.Time { return now },
	})

	_, err := processor.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("ProcessOnce returned error: %v", err)
	}
	if store.retryAt != now.Add(10*time.Minute) {
		t.Fatalf("retryAt=%s, want Retry-After deadline %s", store.retryAt, now.Add(10*time.Minute))
	}

	store.retryAt = time.Time{}
	handler.err = errors.New("temporary")
	_, err = processor.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("ProcessOnce returned error: %v", err)
	}
	// Exponential backoff (4m at attempt 3) is jittered ±10%.
	expected := now.Add(4 * time.Minute)
	delta := 4 * time.Minute / 10
	if store.retryAt.Before(expected.Add(-delta)) || store.retryAt.After(expected.Add(delta)) {
		t.Fatalf("retryAt=%s, want exponential deadline near %s (±%s)", store.retryAt, expected, delta)
	}
}

func TestProcessorLogsJobLifecycleAndError(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	store := &fakeJobStore{hasJob: true, job: jobs.Job{ID: 10, JobKey: CountriesJobKey(), Queue: "provider-reference", Attempts: 1, MaxAttempts: 3}}
	handler := &fakeHandler{err: permanentTestError{message: "unsupported plan"}}
	processor := NewProcessor(ProcessorOptions{
		Jobs: store, Queue: "provider-reference", WorkerID: "worker-1", Logger: logger,
		Handlers: map[string]JobHandler{CountriesJobKey(): handler},
	})

	_, err := processor.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("ProcessOnce returned error: %v", err)
	}
	logs := output.String()
	for _, want := range []string{"job started", "job failed", "unsupported plan", "provider-reference"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("logs=%s, want %q", logs, want)
		}
	}
}

func TestProcessorFailsAfterMaxAttempts(t *testing.T) {
	store := &fakeJobStore{hasJob: true, job: jobs.Job{ID: 7, JobKey: CountriesJobKey(), Attempts: 3, MaxAttempts: 3}}
	handler := &fakeHandler{err: errors.New("permanent")}
	processor := NewProcessor(ProcessorOptions{
		Jobs:     store,
		Queue:    "provider-reference",
		WorkerID: "worker-1",
		Handlers: map[string]JobHandler{CountriesJobKey(): handler},
	})

	processed, err := processor.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("ProcessOnce returned error: %v", err)
	}
	if !processed || store.failed != 7 || store.retried != 0 {
		t.Fatalf("processed=%v failed=%d retried=%d, want final failure only", processed, store.failed, store.retried)
	}
}

func TestProcessorDefersCapacityWithoutConsumingFinalAttempt(t *testing.T) {
	now := time.Date(2026, 9, 15, 20, 0, 0, 0, time.UTC)
	store := &fakeJobStore{hasJob: true, job: jobs.Job{ID: 17, JobKey: CountriesJobKey(), Attempts: 6, MaxAttempts: 6}}
	handler := &fakeHandler{err: deferredTestError{message: "provider capacity unavailable", delay: time.Minute}}
	processor := NewProcessor(ProcessorOptions{
		Jobs: store, Queue: "provider-reference", WorkerID: "worker-1",
		Handlers: map[string]JobHandler{CountriesJobKey(): handler},
		Now:      func() time.Time { return now },
	})

	processed, err := processor.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("ProcessOnce returned error: %v", err)
	}
	if !processed || store.deferred != 17 || store.retried != 0 || store.failed != 0 {
		t.Fatalf("processed=%v deferred=%d retried=%d failed=%d, want defer only", processed, store.deferred, store.retried, store.failed)
	}
	if store.retryAt != now.Add(time.Minute) {
		t.Fatalf("defer deadline=%s, want exact deadline %s", store.retryAt, now.Add(time.Minute))
	}
}

func TestProcessorReturnsFalseWhenNoJobIsAvailable(t *testing.T) {
	store := &fakeJobStore{}
	processor := NewProcessor(ProcessorOptions{Jobs: store, Queue: "provider-reference", WorkerID: "worker-1"})

	processed, err := processor.ProcessOnce(context.Background())
	if err != nil {
		t.Fatalf("ProcessOnce returned error: %v", err)
	}
	if processed {
		t.Fatal("processed = true, want false")
	}
}

func TestProcessorRenewsLeaseWhileHandlerRuns(t *testing.T) {
	store := &fakeJobStore{hasJob: true, renewals: make(chan struct{}, 1), job: jobs.Job{ID: 12, JobKey: CountriesJobKey(), Attempts: 1, MaxAttempts: 3}}
	handler := &blockingHandler{started: make(chan struct{}), released: make(chan struct{}), canceled: make(chan struct{})}
	processor := NewProcessor(ProcessorOptions{
		Jobs: store, Queue: "provider-reference", WorkerID: "worker-1",
		Handlers: map[string]JobHandler{CountriesJobKey(): handler}, LeaseRefreshInterval: time.Millisecond,
	})
	done := make(chan error, 1)
	go func() {
		_, err := processor.ProcessOnce(context.Background())
		done <- err
	}()
	<-handler.started
	// Hold the handler open until a renewal is seen. A fixed sleep can overrun on
	// a busy runner, leaving the stop signal and the tick both ready in
	// maintainLease's select, which may then stop without renewing at all. The
	// deadline only bounds the wait when renewal is genuinely broken.
	select {
	case <-store.renewals:
	case <-time.After(30 * time.Second):
		close(handler.released)
		<-done
		t.Fatal("no lease renewal while the handler was running")
	}
	close(handler.released)
	// ProcessOnce joins the lease goroutine before returning, so reading the
	// store's fields after this receive is race-free.
	if err := <-done; err != nil {
		t.Fatalf("ProcessOnce returned error: %v", err)
	}
	if store.renewed != 12 || store.completed != 12 {
		t.Fatalf("renewed=%d completed=%d, want 12 and 12", store.renewed, store.completed)
	}
}

func TestProcessorCancelsHandlerWhenLeaseRenewalFails(t *testing.T) {
	store := &fakeJobStore{hasJob: true, renewErr: jobs.ErrLeaseLost, job: jobs.Job{ID: 13, JobKey: CountriesJobKey(), Attempts: 1, MaxAttempts: 3}}
	handler := &blockingHandler{started: make(chan struct{}), released: make(chan struct{}), canceled: make(chan struct{})}
	processor := NewProcessor(ProcessorOptions{
		Jobs: store, Queue: "provider-reference", WorkerID: "worker-1",
		Handlers: map[string]JobHandler{CountriesJobKey(): handler}, LeaseRefreshInterval: time.Millisecond,
	})
	_, err := processor.ProcessOnce(context.Background())
	if !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("ProcessOnce error=%v, want lease lost", err)
	}
	select {
	case <-handler.canceled:
	default:
		t.Fatal("handler context was not canceled after lease loss")
	}
	if store.completed != 0 || store.retried != 0 || store.failed != 0 {
		t.Fatalf("stale worker transitioned job: completed=%d retried=%d failed=%d", store.completed, store.retried, store.failed)
	}
}

package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// ErrPermanent marks a failure that retrying cannot fix. A handler returning
// anything else is treated as retryable, because the safe default for an
// unclassified failure is to try again rather than silently drop the work.
var ErrPermanent = errors.New("jobs: permanent failure")

// JobStore is the subset of Repository the worker needs. Narrowing it here
// keeps the worker testable without a database and documents exactly which
// repository operations the pool depends on.
type JobStore interface {
	Claim(ctx context.Context, request ClaimRequest) (Job, error)
	Renew(ctx context.Context, request RenewRequest) (Job, error)
	Complete(ctx context.Context, request LeaseRequest) (Job, error)
	Fail(ctx context.Context, failure Failure) (Job, error)
	ReclaimExpired(ctx context.Context, limit int) (int64, error)
}

// Repository satisfies JobStore.
var _ JobStore = (*Repository)(nil)

// Handler executes one claimed job. Implementations must be idempotent: a
// lease can expire mid-execution and the job will then be claimed again.
type Handler interface {
	Handle(ctx context.Context, job Job) error
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, job Job) error

// Handle implements Handler.
func (f HandlerFunc) Handle(ctx context.Context, job Job) error { return f(ctx, job) }

// WorkerConfig bounds the pool. Every field has a safe default.
type WorkerConfig struct {
	// Queue is the single queue this pool drains.
	Queue string
	// Name identifies the pool in lease ownership and logs.
	Name string
	// Concurrency is the fixed number of goroutines. There is deliberately no
	// goroutine-per-job beyond this: a job storm must grow the queue, not
	// memory.
	Concurrency int
	// Lease is how long a claim is held before it can be reclaimed.
	Lease time.Duration
	// PollInterval is the idle wait when the queue is empty.
	PollInterval time.Duration
	// HandlerTimeout bounds one execution. It must be shorter than Lease, so
	// a stuck handler loses its lease rather than holding a job forever.
	HandlerTimeout time.Duration

	Logger *slog.Logger
}

const (
	defaultWorkerConcurrency = 2
	defaultWorkerLease       = 60 * time.Second
	defaultWorkerPoll        = 2 * time.Second
	defaultHandlerTimeout    = 45 * time.Second
	// renewFraction of the lease elapses before a renewal is attempted.
	renewFraction = 3
)

func (c *WorkerConfig) applyDefaults() {
	if c.Queue == "" {
		c.Queue = "default"
	}
	if c.Name == "" {
		c.Name = "ops-controller"
	}
	if c.Concurrency <= 0 {
		c.Concurrency = defaultWorkerConcurrency
	}
	if c.Lease <= 0 {
		c.Lease = defaultWorkerLease
	}
	if c.PollInterval <= 0 {
		c.PollInterval = defaultWorkerPoll
	}
	if c.HandlerTimeout <= 0 || c.HandlerTimeout >= c.Lease {
		// Always leave headroom so the lease outlives the handler.
		c.HandlerTimeout = c.Lease - c.Lease/renewFraction
		if c.HandlerTimeout <= 0 {
			c.HandlerTimeout = defaultHandlerTimeout
		}
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// Worker drains one queue with a fixed pool.
//
// This is the piece that makes a dispatched job actually run. Without it the
// queue accepts work and nothing executes it, so any command that enqueues a
// job would report success and do nothing.
type Worker struct {
	repo    JobStore
	handler Handler
	config  WorkerConfig
}

// NewWorker validates and returns a pool.
func NewWorker(repo JobStore, handler Handler, config WorkerConfig) (*Worker, error) {
	if repo == nil {
		return nil, errors.New("jobs: worker requires a repository")
	}
	if handler == nil {
		return nil, errors.New("jobs: worker requires a handler")
	}
	config.applyDefaults()
	return &Worker{repo: repo, handler: handler, config: config}, nil
}

// Run blocks until ctx is cancelled, then waits for in-flight jobs to finish.
func (w *Worker) Run(ctx context.Context) error {
	done := make(chan struct{}, w.config.Concurrency)
	for i := 0; i < w.config.Concurrency; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			w.loop(ctx)
		}()
	}
	w.config.Logger.Info("job worker started",
		"queue", w.config.Queue, "concurrency", w.config.Concurrency, "lease", w.config.Lease.String())

	for i := 0; i < w.config.Concurrency; i++ {
		<-done
	}
	w.config.Logger.Info("job worker stopped", "queue", w.config.Queue)
	return ctx.Err()
}

func (w *Worker) loop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		job, err := w.repo.Claim(ctx, ClaimRequest{
			Queue:  w.config.Queue,
			Worker: w.config.Name,
			Lease:  w.config.Lease,
		})
		switch {
		case errors.Is(err, ErrNotFound):
			// Empty queue. Idle politely.
			if !sleep(ctx, w.config.PollInterval) {
				return
			}
			continue
		case err != nil:
			w.config.Logger.Error("claim job", "queue", w.config.Queue, "error", err)
			if !sleep(ctx, w.config.PollInterval) {
				return
			}
			continue
		}
		w.execute(ctx, job)
	}
}

// execute runs one job and records its outcome.
func (w *Worker) execute(parent context.Context, job Job) {
	lease := LeaseRequest{JobID: job.ID, Worker: w.config.Name, LeaseToken: job.LeaseToken}

	// Detach from cancellation so a shutdown mid-job still records an outcome
	// rather than abandoning the lease to expire.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), w.config.HandlerTimeout)
	defer cancel()

	renewDone := make(chan struct{})
	go w.renewUntil(ctx, lease, renewDone)

	err := w.handler.Handle(ctx, job)
	close(renewDone)

	if err == nil {
		if _, cerr := w.repo.Complete(context.WithoutCancel(parent), lease); cerr != nil {
			w.config.Logger.Error("complete job", "job_id", job.ID, "error", cerr)
		}
		return
	}

	class := FailureRetryable
	if errors.Is(err, ErrPermanent) {
		class = FailurePermanent
	}
	failure := Failure{
		LeaseRequest: lease,
		Class:        class,
		Code:         string(class),
		// Bound the stored message: a handler error can carry a long body.
		Message: truncate(err.Error(), 500),
	}
	if _, ferr := w.repo.Fail(context.WithoutCancel(parent), failure); ferr != nil {
		w.config.Logger.Error("fail job", "job_id", job.ID, "error", ferr)
	}
	w.config.Logger.Warn("job failed", "job_id", job.ID, "type", job.Type, "class", class, "error", err)
}

// renewUntil keeps the lease alive while the handler runs.
func (w *Worker) renewUntil(ctx context.Context, lease LeaseRequest, done <-chan struct{}) {
	interval := w.config.Lease / renewFraction
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := w.repo.Renew(ctx, RenewRequest{LeaseRequest: lease, Lease: w.config.Lease}); err != nil {
				// The lease is gone; another worker may already hold the job.
				// Stop renewing and let the handler's own timeout end it.
				return
			}
		}
	}
}

// Reclaimer periodically returns expired leases to the queue. Without it a
// worker that dies mid-job leaves that job leased until something intervenes.
func (w *Worker) Reclaim(ctx context.Context, every time.Duration, limit int) {
	if every <= 0 {
		every = w.config.Lease
	}
	if limit <= 0 {
		limit = 50
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := w.repo.ReclaimExpired(ctx, limit)
			if err != nil {
				w.config.Logger.Error("reclaim expired leases", "error", err)
				continue
			}
			if n > 0 {
				w.config.Logger.Info("reclaimed expired job leases", "count", n)
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return fmt.Sprintf("%s… (%d bytes truncated)", s[:limit], len(s)-limit)
}

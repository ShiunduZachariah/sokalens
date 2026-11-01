package ingestion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

const DefaultProviderQueue = "provider-reference"

type JobHandler interface {
	Run(context.Context, jobs.Job) error
}

type jobStore interface {
	ClaimNext(context.Context, string, string, time.Time) (jobs.Job, bool, error)
	RenewLease(context.Context, int64, string, time.Time) error
	Complete(context.Context, int64, string, time.Time) error
	Retry(context.Context, int64, string, string, time.Time) error
	Defer(context.Context, int64, string, string, time.Time) error
	Fail(context.Context, int64, string, string, time.Time) error
}

type ProcessorOptions struct {
	Jobs                 jobStore
	Queue                string
	WorkerID             string
	Handlers             map[string]JobHandler
	RetryDelay           time.Duration
	LeaseRefreshInterval time.Duration
	Now                  func() time.Time
	Logger               *slog.Logger
}

type Processor struct {
	opts ProcessorOptions
}

func NewProcessor(opts ProcessorOptions) Processor {
	if opts.Queue == "" {
		opts.Queue = DefaultProviderQueue
	}
	if opts.RetryDelay <= 0 {
		opts.RetryDelay = time.Minute
	}
	if opts.LeaseRefreshInterval <= 0 || opts.LeaseRefreshInterval >= jobs.LeaseDuration {
		opts.LeaseRefreshInterval = jobs.LeaseDuration / 3
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return Processor{opts: opts}
}

func (p Processor) ProcessOnce(ctx context.Context) (bool, error) {
	if p.opts.Jobs == nil {
		return false, errors.New("processor jobs repository is required")
	}
	if p.opts.WorkerID == "" {
		return false, errors.New("processor worker ID is required")
	}
	now := p.opts.Now().UTC()
	job, ok, err := p.opts.Jobs.ClaimNext(ctx, p.opts.Queue, p.opts.WorkerID, now)
	if err != nil || !ok {
		return ok, err
	}
	startedAt := time.Now()
	p.opts.Logger.Info(
		"job started",
		"job_id", job.ID,
		"job_key", job.JobKey,
		"queue", job.Queue,
		"attempt", job.Attempts,
		"max_attempts", job.MaxAttempts,
	)

	handler := p.opts.Handlers[job.JobKey]
	if handler == nil {
		reason := "unsupported job key: " + job.JobKey
		err := p.opts.Jobs.Fail(ctx, job.ID, p.opts.WorkerID, reason, p.opts.Now().UTC())
		if err == nil {
			p.logFailed(job, reason, time.Since(startedAt))
		}
		return true, err
	}
	handlerCtx, cancelHandler := context.WithCancel(ctx)
	leaseDone, stopLease := p.maintainLease(ctx, cancelHandler, job.ID)
	handlerErr := handler.Run(handlerCtx, job)
	stopLease()
	leaseErr := <-leaseDone
	cancelHandler()
	if leaseErr != nil {
		return true, fmt.Errorf("renew lease for job %d: %w", job.ID, leaseErr)
	}
	now = p.opts.Now().UTC()
	if handlerErr != nil {
		if errorIsDeferrable(handlerErr) {
			delay := p.opts.RetryDelay
			var directive retryAfterError
			if errors.As(handlerErr, &directive) && directive.RetryAfter() > 0 {
				delay = directive.RetryAfter()
			}
			deferAt := now.Add(delay)
			repoErr := p.opts.Jobs.Defer(ctx, job.ID, p.opts.WorkerID, handlerErr.Error(), deferAt)
			if repoErr == nil {
				p.opts.Logger.Warn("job deferred for provider capacity", "job_id", job.ID, "job_key", job.JobKey, "queue", job.Queue, "attempt", job.Attempts, "defer_at", deferAt, "error", handlerErr, "duration_ms", time.Since(startedAt).Milliseconds())
			}
			return true, repoErr
		}
		if jobs.ShouldRetry(job) && errorIsRetryable(handlerErr) {
			delay := retryDelay(p.opts.RetryDelay, job.Attempts, handlerErr)
			retryAt := now.Add(delay)
			repoErr := p.opts.Jobs.Retry(ctx, job.ID, p.opts.WorkerID, handlerErr.Error(), retryAt)
			if repoErr == nil {
				p.opts.Logger.Warn(
					"job retry scheduled",
					"job_id", job.ID,
					"job_key", job.JobKey,
					"queue", job.Queue,
					"attempt", job.Attempts,
					"retry_at", retryAt,
					"error", handlerErr,
					"duration_ms", time.Since(startedAt).Milliseconds(),
				)
			}
			return true, repoErr
		}
		repoErr := p.opts.Jobs.Fail(ctx, job.ID, p.opts.WorkerID, handlerErr.Error(), now)
		if repoErr == nil {
			p.logFailed(job, handlerErr.Error(), time.Since(startedAt))
		}
		return true, repoErr
	}
	err = p.opts.Jobs.Complete(ctx, job.ID, p.opts.WorkerID, now)
	if err == nil {
		p.opts.Logger.Info(
			"job completed",
			"job_id", job.ID,
			"job_key", job.JobKey,
			"queue", job.Queue,
			"attempt", job.Attempts,
			"duration_ms", time.Since(startedAt).Milliseconds(),
		)
	}
	return true, err
}

type retryableError interface {
	Retryable() bool
}

type retryAfterError interface {
	RetryAfter() time.Duration
}

type deferrableError interface {
	Deferrable() bool
}

func errorIsDeferrable(err error) bool {
	var directive deferrableError
	return errors.As(err, &directive) && directive.Deferrable()
}

func errorIsRetryable(err error) bool {
	var directive retryableError
	if errors.As(err, &directive) {
		return directive.Retryable()
	}
	return true
}

func retryDelay(base time.Duration, attempt int, err error) time.Duration {
	if base <= 0 {
		base = time.Minute
	}
	delay := base
	for exponent := 1; exponent < attempt && delay < 30*time.Minute; exponent++ {
		delay *= 2
		if delay > 30*time.Minute {
			delay = 30 * time.Minute
		}
	}
	var directive retryAfterError
	if errors.As(err, &directive) && directive.RetryAfter() > delay {
		// Retry-After from the provider is authoritative — honor it exactly.
		return directive.RetryAfter()
	}
	// Jitter the exponential backoff (±10%) so independent workers and retries
	// do not synchronize on the same tick and burst the provider.
	return time.Duration(float64(delay) * (0.9 + 0.2*rand.Float64()))
}

func (p Processor) maintainLease(ctx context.Context, cancelHandler context.CancelFunc, jobID int64) (<-chan error, context.CancelFunc) {
	leaseCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(p.opts.LeaseRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				done <- nil
				return
			case <-ticker.C:
				renewCtx, cancelRenew := context.WithTimeout(leaseCtx, leaseRenewalTimeout(p.opts.LeaseRefreshInterval))
				err := p.opts.Jobs.RenewLease(renewCtx, jobID, p.opts.WorkerID, p.opts.Now().UTC())
				cancelRenew()
				if err != nil {
					cancelHandler()
					done <- err
					return
				}
			}
		}
	}()
	return done, stop
}

func leaseRenewalTimeout(interval time.Duration) time.Duration {
	timeout := interval / 2
	if timeout < time.Second {
		return time.Second
	}
	if timeout > 30*time.Second {
		return 30 * time.Second
	}
	return timeout
}

func (p Processor) logFailed(job jobs.Job, reason string, duration time.Duration) {
	p.opts.Logger.Error(
		"job failed",
		"job_id", job.ID,
		"job_key", job.JobKey,
		"queue", job.Queue,
		"attempt", job.Attempts,
		"error", reason,
		"duration_ms", duration.Milliseconds(),
	)
}

package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// QueueWorker is one queue's drain loop.
//
// Each queue runs independently. That independence is the point: the previous
// runtime ran every queue inside one barrier and waited for all of them before
// starting the next cycle, so a single fifteen-minute job stopped every other
// queue and stalled the heartbeat with it.
type QueueWorker struct {
	// Queue names the queue, for logging only.
	Queue string

	// Concurrency is how many handlers this queue may run at once. Zero or
	// negative means one.
	Concurrency int

	// ProcessOnce claims and runs at most one job. It reports whether it did
	// any work, which is what lets the loop drain a backlog at full speed and
	// then go quiet instead of spinning.
	ProcessOnce func(context.Context) (bool, error)

	// Reserved is how many slots this queue is guaranteed, regardless of what
	// every other queue is doing.
	//
	// Without this the shared budget is first-come-first-served, and a queue
	// with a standing backlog always comes first. On 2026-09-21 a restart left
	// 4,076 team-history backfill jobs queued; the backfill loops claimed
	// thirteen of the in-flight slots within seconds and live match polling
	// got none. Every queue was healthy, every worker was busy, and the
	// product's core feature stopped for forty minutes.
	//
	// A reservation is not extra capacity. It is capacity this queue may take
	// back from the shared pool at any moment, which is the only property that
	// makes starvation impossible rather than unlikely.
	Reserved int
}

type RuntimeOptions struct {
	// PollInterval is how often recurring work is planned. It is not how often
	// jobs are executed: queue workers run continuously.
	PollInterval time.Duration

	// IdleBackoff is how long a queue waits after finding nothing to do.
	//
	// This is the floor on how long a newly enqueued job waits, so it is
	// deliberately short. The old runtime executed one job per queue per
	// PollInterval, which at thirty seconds meant a job could sit for half a
	// minute behind an empty queue and far longer behind a busy one.
	IdleBackoff time.Duration

	// HeartbeatInterval is how often liveness is recorded. Independent of job
	// execution: a worker that is busy is alive, and the previous runtime wrote
	// the heartbeat only after all work returned, so a long job was
	// indistinguishable from a dead process.
	HeartbeatInterval time.Duration

	// MaxConcurrentJobs bounds handlers in flight across every queue.
	//
	// Per-queue concurrency alone is not a safe budget: twelve queues each
	// allowed four handlers is forty-eight handlers competing for a connection
	// pool of twelve, and the excess does not wait politely -- it holds a
	// claimed job while blocking on a connection the job needs. This is the
	// ceiling that keeps the pool from being the new bottleneck.
	MaxConcurrentJobs int

	MaxConsecutiveFailures int
	Heartbeat              func(time.Time) error
	Schedule               func(context.Context, time.Time) error
	Workers                []QueueWorker
	Now                    func() time.Time
	Logger                 *slog.Logger
}

const (
	defaultIdleBackoff       = time.Second
	defaultHeartbeatInterval = 15 * time.Second
)

func Run(ctx context.Context, opts RuntimeOptions) error {
	if opts.PollInterval <= 0 {
		return errors.New("poll interval must be positive")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	opts.Logger = scheduleLogger(opts.Logger)
	if opts.MaxConsecutiveFailures <= 0 {
		opts.MaxConsecutiveFailures = 3
	}
	if opts.IdleBackoff <= 0 {
		opts.IdleBackoff = defaultIdleBackoff
	}
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = defaultHeartbeatInterval
	}
	if opts.MaxConcurrentJobs < 1 {
		opts.MaxConcurrentJobs = totalWorkerSlots(opts.Workers)
	}
	if opts.HeartbeatInterval > opts.PollInterval {
		// Never let liveness reporting be slower than planning, or a healthy
		// worker can look stale purely because its planner is slow.
		opts.HeartbeatInterval = opts.PollInterval
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		background sync.WaitGroup
		fatalOnce  sync.Once
		fatalErr   error
	)
	fail := func(err error) {
		fatalOnce.Do(func() {
			fatalErr = err
			cancel()
		})
	}

	// Heartbeat first and immediately, so a worker is reported alive before it
	// has claimed anything. A slow first schedule used to look like a dead
	// process.
	if err := writeHeartbeat(opts); err != nil {
		return err
	}

	background.Add(1)
	go func() {
		defer background.Done()
		runHeartbeatLoop(runCtx, opts, fail)
	}()

	// Admission control. A shared budget plus a reserved pool per queue, so a
	// queue with a standing backlog cannot take every slot and leave a
	// latency-critical queue with none.
	admission := newAdmission(opts.MaxConcurrentJobs, opts.Workers)

	for _, worker := range opts.Workers {
		concurrency := worker.Concurrency
		if concurrency < 1 {
			concurrency = 1
		}
		for slot := 0; slot < concurrency; slot++ {
			background.Add(1)
			go func(worker QueueWorker, slot int) {
				defer background.Done()
				runQueueLoop(runCtx, opts, worker, slot, admission)
			}(worker, slot)
		}
	}

	scheduleErr := runScheduleLoop(runCtx, opts)

	cancel()
	background.Wait()

	if fatalErr != nil {
		return fatalErr
	}
	return scheduleErr
}

// runScheduleLoop plans recurring work. It owns the consecutive-failure budget,
// because a planner that cannot reach the database is the failure worth
// restarting the process over; a single queue handler failing is not.
func runScheduleLoop(ctx context.Context, opts RuntimeOptions) error {
	consecutiveFailures := 0
	run := func() error {
		if err := runScheduleIteration(ctx, opts); err != nil {
			consecutiveFailures++
			if consecutiveFailures >= opts.MaxConsecutiveFailures {
				return fmt.Errorf("worker infrastructure failed %d consecutive iterations: %w", consecutiveFailures, err)
			}
			return nil
		}
		consecutiveFailures = 0
		return nil
	}

	if err := run(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}

	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := run(); err != nil {
				return err
			}
			if ctx.Err() != nil {
				return nil
			}
		}
	}
}

func runScheduleIteration(ctx context.Context, opts RuntimeOptions) error {
	startedAt := time.Now()
	now := opts.Now().UTC()
	// The completion line is emitted even with nothing to schedule. It is the
	// signal that the loop itself is alive, and suppressing it for an idle
	// planner would make a healthy worker look silent.
	if opts.Schedule != nil {
		if err := opts.Schedule(ctx, now); err != nil {
			opts.Logger.Error("worker schedule iteration failed", "error", err, "scheduled_at", now)
			opts.Logger.Error("worker iteration completed", "status", "failed", "duration_ms", time.Since(startedAt).Milliseconds(), "error_count", 1)
			return fmt.Errorf("schedule: %w", err)
		}
	}
	opts.Logger.Info("worker iteration completed", "status", "success", "duration_ms", time.Since(startedAt).Milliseconds(), "error_count", 0)
	return nil
}

// runQueueLoop drains one queue.
//
// It claims repeatedly while there is work, and only backs off once a claim
// comes back empty. A backlog therefore drains at handler speed rather than at
// one job per scheduler tick.
func runQueueLoop(ctx context.Context, opts RuntimeOptions, worker QueueWorker, slot int, admission *admission) {
	if worker.ProcessOnce == nil {
		return
	}
	logger := opts.Logger.With("queue", worker.Queue, "slot", slot)
	for {
		if ctx.Err() != nil {
			return
		}
		worked, err := processOnceBounded(ctx, worker, admission)
		switch {
		case err != nil && ctx.Err() != nil:
			return
		case err != nil:
			// A handler failure is the job's problem, already recorded against
			// the job row by the processor. It must not take the queue down,
			// and it must not spin: back off exactly as an empty queue does.
			logger.Error("queue iteration failed", "error", err)
			if !sleepOrDone(ctx, opts.IdleBackoff) {
				return
			}
		case worked:
			// Claim again immediately. This is the difference between draining
			// a backlog and sipping at it.
			continue
		default:
			if !sleepOrDone(ctx, opts.IdleBackoff) {
				return
			}
		}
	}
}

// processOnceBounded takes a slot from the shared budget for the duration of
// one job. Taking it around the claim as well as the handler is deliberate: a
// claim that succeeds while no slot is free would leave a job marked running
// with nothing executing it until the lease expired.
func processOnceBounded(ctx context.Context, worker QueueWorker, admission *admission) (bool, error) {
	release, err := admission.acquire(ctx, worker.Queue)
	if err != nil {
		return false, err
	}
	defer release()
	return worker.ProcessOnce(ctx)
}

func totalWorkerSlots(workers []QueueWorker) int {
	total := 0
	for _, worker := range workers {
		if worker.Concurrency < 1 {
			total++
			continue
		}
		total += worker.Concurrency
	}
	if total < 1 {
		return 1
	}
	return total
}

func runHeartbeatLoop(ctx context.Context, opts RuntimeOptions, fail func(error)) {
	ticker := time.NewTicker(opts.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := writeHeartbeat(opts); err != nil {
				fail(err)
				return
			}
		}
	}
}

func writeHeartbeat(opts RuntimeOptions) error {
	if opts.Heartbeat == nil {
		return nil
	}
	if err := opts.Heartbeat(opts.Now().UTC()); err != nil {
		return fmt.Errorf("update worker heartbeat: %w", err)
	}
	return nil
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func WriteHeartbeat(path string, now time.Time) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("heartbeat path is required")
	}
	now = normalizedNow(now)
	return os.WriteFile(path, []byte(now.Format(time.RFC3339Nano)+"\n"), 0o600)
}

func CheckHeartbeat(path string, maxAge time.Duration, now time.Time) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("heartbeat path is required")
	}
	if maxAge <= 0 {
		return errors.New("heartbeat max age must be positive")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read worker heartbeat: %w", err)
	}
	lastBeat, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(body)))
	if err != nil {
		return fmt.Errorf("parse worker heartbeat: %w", err)
	}
	now = normalizedNow(now)
	if age := now.Sub(lastBeat); age > maxAge {
		return fmt.Errorf("worker heartbeat is stale: age %s exceeds %s", age.Round(time.Second), maxAge)
	}
	return nil
}

package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/quota"
)

type Enqueuer interface {
	Enqueue(context.Context, jobs.NewJob) (int64, error)
}

// QuotaDecisionFunc returns the current quota planner decision for a cycle.
type QuotaDecisionFunc func(ctx context.Context, now time.Time) (quota.Decision, error)

type Scheduler struct {
	planner   Planner
	jobs      Enqueuer
	logger    *slog.Logger
	quotaFn   QuotaDecisionFunc
	quotaOpts QuotaOptions
	mu        sync.Mutex
	seen      map[string]struct{}
}

func New(planner Planner, enqueuer Enqueuer, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		planner: planner,
		jobs:    enqueuer,
		logger:  scheduleLogger(logger),
		seen:    map[string]struct{}{},
	}
}

// WithQuotaPlanning attaches the quota planner decision hook and options so the
// schedule adapts to fixture demand, remaining quota, and priority pressure.
func (s *Scheduler) WithQuotaPlanning(fn QuotaDecisionFunc, opts QuotaOptions) *Scheduler {
	s.quotaFn = fn
	s.quotaOpts = opts
	return s
}

func (s *Scheduler) Schedule(ctx context.Context, now time.Time) error {
	if s == nil || s.jobs == nil {
		return errors.New("scheduler job enqueuer is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	plan := s.planner.Plan(now)
	if s.quotaFn != nil {
		if decision, err := s.quotaFn(ctx, now); err == nil {
			plan = ApplyQuotaPlan(ctx, plan, decision, s.quotaOpts, now, s.logger)
		} else {
			s.logger.Warn("quota planner decision failed; scheduling base plan", "error", err)
		}
	}
	currentKeys := make(map[string]struct{}, len(plan))
	for _, scheduled := range plan {
		currentKeys[scheduled.Job.IdempotencyKey] = struct{}{}
	}
	for key := range s.seen {
		if _, current := currentKeys[key]; !current {
			delete(s.seen, key)
		}
	}
	var scheduleErrors []error
	for _, scheduled := range plan {
		key := scheduled.Job.IdempotencyKey
		if _, ok := s.seen[key]; ok {
			continue
		}
		jobID, err := s.jobs.Enqueue(ctx, scheduled.Job)
		if err != nil {
			s.logger.Error(
				"schedule job failed",
				"error", err,
				"job_key", scheduled.Job.JobKey,
				"queue", scheduled.Job.Queue,
				"provider", scheduled.Provider,
				"call_mode", scheduled.CallMode,
				"schedule", scheduled.Schedule,
				"bucket", scheduled.Bucket,
				"idempotency_key", key,
			)
			scheduleErrors = append(scheduleErrors, err)
			continue
		}
		s.seen[key] = struct{}{}
		s.logger.Info(
			"schedule job ensured",
			"job_id", jobID,
			"job_key", scheduled.Job.JobKey,
			"queue", scheduled.Job.Queue,
			"provider", scheduled.Provider,
			"call_mode", scheduled.CallMode,
			"schedule", scheduled.Schedule,
			"bucket", scheduled.Bucket,
			"idempotency_key", key,
			"run_after", scheduled.Job.RunAfter,
		)
	}
	return errors.Join(scheduleErrors...)
}

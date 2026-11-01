package main

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/scheduler"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	cfg, err := config.Load(nil)
	if err != nil {
		slog.Error("invalid enqueue configuration", "error", err)
		os.Exit(1)
	}
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	repo := jobs.NewRepository(db)
	now := time.Now().UTC()
	jobsToQueue, err := providerJobsToQueue(cfg, now)
	if err != nil {
		slog.Error("plan provider jobs", "error", err)
		os.Exit(1)
	}
	for _, job := range jobsToQueue {
		id, err := repo.Enqueue(ctx, job)
		if err != nil {
			slog.Error("enqueue provider job", "job_key", job.JobKey, "error", err)
			os.Exit(1)
		}
		slog.Info("provider job queued", "id", id, "job_key", job.JobKey, "queue", job.Queue)
	}
}

func providerJobsToQueue(cfg config.Config, now time.Time) ([]jobs.NewJob, error) {
	planner, err := scheduler.NewPlanner(scheduler.PlanOptions{
		ProviderMode:              cfg.FootballProvider,
		EnableRealAPIFootball:     cfg.EnableRealAPIFootballCalls,
		EnableMockAPIFootball:     cfg.EnableMockAPIFootballCalls,
		EnableRealFootballDataOrg: cfg.EnableRealFootballDataOrgCalls,
		ProviderInterval:          cfg.ProviderScheduleInterval,
		PredictionInterval:        cfg.PredictionScheduleInterval,
		OddsDelay:                 2 * cfg.PollInterval,
	})
	if err != nil {
		return nil, err
	}
	plan := planner.Plan(now)
	jobsToQueue := make([]jobs.NewJob, 0, len(plan))
	for _, scheduled := range plan {
		if scheduled.Provider != "" {
			jobsToQueue = append(jobsToQueue, scheduled.Job)
		}
	}
	return jobsToQueue, nil
}

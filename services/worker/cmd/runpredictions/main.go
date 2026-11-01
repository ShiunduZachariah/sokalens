package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/ingestion"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/predictionenginepython"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/predictions"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	cfg, err := config.Load(nil)
	if err != nil {
		slog.Error("invalid worker configuration", "error", err)
		os.Exit(1)
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	pythonEngineConfig := predictionenginepython.LoadConfigFromEnv()
	if pythonEngineConfig.Env["DATABASE_URL"] == "" {
		pythonEngineConfig.Env["DATABASE_URL"] = cfg.DatabaseURL
	}
	processor := ingestion.NewProcessor(ingestion.ProcessorOptions{
		Jobs:       jobs.NewRepository(db),
		Queue:      "predictions",
		WorkerID:   oneShotWorkerID(),
		RetryDelay: time.Minute,
		Handlers: map[string]ingestion.JobHandler{
			predictions.GenerateUpcomingJobKey: predictionenginepython.NewJobHandler(pythonEngineConfig, nil),
			predictions.SettleFinishedJobKey:   predictionenginepython.NewSettlementJobHandler(pythonEngineConfig, nil),
			predictions.SnapshotAccuracyJobKey: predictionenginepython.NewAccuracySnapshotJobHandler(pythonEngineConfig, nil),
		},
	})

	ctx := context.Background()
	processedCount := 0
	for {
		processed, err := processor.ProcessOnce(ctx)
		if err != nil {
			slog.Error("prediction job failed", "error", err)
			os.Exit(1)
		}
		if !processed {
			break
		}
		processedCount++
	}

	fmt.Printf("processed_prediction_jobs=%d\n", processedCount)
}

func oneShotWorkerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "prediction-one-shot"
	}
	return "prediction-one-shot-" + host
}

package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/ingestion"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/metrics"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/footballdata"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	queueFlag := flag.String("queues", "", "comma-separated provider queues to process")
	maxJobs := flag.Int("max-jobs", 20, "maximum provider jobs to process")
	timeout := flag.Duration("timeout", 2*time.Minute, "maximum run duration")
	flag.Parse()

	if *maxJobs <= 0 {
		slog.Error("max-jobs must be positive")
		os.Exit(1)
	}

	cfg, err := config.Load(nil)
	if err != nil {
		slog.Error("invalid provider runner configuration", "error", err)
		os.Exit(1)
	}
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	providerBaseURL := cfg.MockProviderURL
	if cfg.EnableRealAPIFootballCalls {
		providerBaseURL = cfg.APIFootballBaseURL
	}
	providerClient := apifootball.NewClient(apifootball.Config{
		BaseURL:       providerBaseURL,
		APIKey:        cfg.APIFootballKey,
		DailyLimit:    cfg.ProviderDailyLimit,
		DailyReserve:  cfg.ProviderDailyReserve,
		MinuteReserve: cfg.ProviderMinuteReserve,
		HTTPClient:    &http.Client{Timeout: 20 * time.Second},
	})
	footballDataClient := footballdata.NewClient(footballdata.Config{
		BaseURL:     cfg.FootballDataOrgBaseURL,
		APIKey:      cfg.FootballDataOrgAPIKey,
		MinuteLimit: cfg.ProviderMinuteLimit,
		HTTPClient:  &http.Client{Timeout: 20 * time.Second},
	})
	jobRepo := jobs.NewRepository(db)
	providerRegistry, err := ingestion.NewProviderRegistry(
		// Neither the live history nor the event timeline is written by a
		// one-shot provider run: both belong to the scheduled live poll.
		ingestion.NewAPIFootballAdapter(db, providerClient, jobRepo, ingestion.NewRequestDedupe(db, cfg.RequestDedupeTTL), oneShotWorkerID(), metrics.New(), nil, nil, cfg.InPlayStatisticsPollEnabled),
		ingestion.NewFootballDataAdapter(db, footballDataClient, jobRepo),
	)
	if err != nil {
		slog.Error("register provider adapters", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	processed, err := processProviderQueues(ctx, jobRepo, providerRegistry, parseQueues(*queueFlag), *maxJobs)
	if err != nil {
		slog.Error("provider job failed", "error", err)
		os.Exit(1)
	}
	fmt.Printf("processed_provider_jobs=%d\n", processed)
}

func processProviderQueues(
	ctx context.Context,
	jobRepo jobs.Repository,
	registry ingestion.ProviderRegistry,
	queues []string,
	maxJobs int,
) (int, error) {
	processedCount := 0
	processors := make([]ingestion.Processor, 0, len(queues))
	for _, queue := range queues {
		handlers := registry.Handlers(queue)
		if len(handlers) == 0 {
			return 0, fmt.Errorf("provider queue %q has no registered handlers", queue)
		}
		processors = append(processors, ingestion.NewProcessor(ingestion.ProcessorOptions{
			Jobs:       jobRepo,
			Queue:      queue,
			WorkerID:   oneShotWorkerID(),
			RetryDelay: time.Minute,
			Handlers:   handlers,
		}))
	}

	for processedCount < maxJobs {
		processedAny := false
		for _, processor := range processors {
			if processedCount >= maxJobs {
				break
			}
			processed, err := processor.ProcessOnce(ctx)
			if err != nil {
				return processedCount, err
			}
			if processed {
				processedAny = true
				processedCount++
			}
		}
		if !processedAny {
			break
		}
	}
	return processedCount, nil
}

func parseQueues(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{ingestion.DefaultProviderQueue, "provider-fixtures", "provider-odds"}
	}
	parts := strings.Split(raw, ",")
	queues := make([]string, 0, len(parts))
	for _, part := range parts {
		queue := strings.TrimSpace(part)
		if queue != "" {
			queues = append(queues, queue)
		}
	}
	return queues
}

func oneShotWorkerID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "provider-one-shot"
	}
	return "provider-one-shot-" + host
}

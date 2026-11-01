package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/ingestion"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const statisticsQueue = "provider-fixtures"

type statisticsPayload struct {
	FixtureID int64 `json:"fixture_id"`
}

type statisticsCandidate struct {
	FixtureID         int64
	ProviderFixtureID int64
}

func main() {
	limit := flag.Int("limit", 500, "max fixture statistics jobs to queue")
	flag.Parse()

	if *limit <= 0 {
		slog.Error("limit must be positive", "limit", *limit)
		os.Exit(1)
	}

	cfg, err := config.Load(nil)
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		slog.Error("open db", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	candidates, skipped, err := listStatisticsCandidates(ctx, db, *limit)
	if err != nil {
		slog.Error("list statistics candidates", "error", err)
		os.Exit(1)
	}

	now := time.Now().UTC()
	repo := jobs.NewRepository(db)
	queued := 0
	for _, candidate := range candidates {
		payload, err := json.Marshal(statisticsPayload{FixtureID: candidate.ProviderFixtureID})
		if err != nil {
			slog.Error("marshal statistics payload", "error", err)
			os.Exit(1)
		}
		job := jobs.NewJob{
			JobKey:               ingestion.StatisticsJobKey(),
			Queue:                statisticsQueue,
			Priority:             40,
			Payload:              payload,
			IdempotencyKey:       fmt.Sprintf("enqueue:statistics:%d", candidate.ProviderFixtureID),
			DeduplicateCompleted: true,
			RunAfter:             now,
		}
		if _, err := repo.Enqueue(ctx, job); err != nil {
			slog.Error("enqueue statistics job", "error", err, "idempotency_key", job.IdempotencyKey)
			os.Exit(1)
		}
		queued++
	}

	slog.Info("fixture statistics jobs queued",
		"queued", queued,
		"candidates", len(candidates),
		"skipped_invalid_provider_id", skipped,
		"limit", *limit,
	)
	fmt.Printf("queued_fixture_statistics_jobs=%d candidates=%d skipped_invalid_provider_id=%d limit=%d\n", queued, len(candidates), skipped, *limit)
}

// listStatisticsCandidates returns finished fixtures that have no
// fixture_statistics row yet and are mapped to an API-Football provider fixture
// id, most recent kickoff first. The provider fixture id lives in
// provider_entity_maps (entity_type 'match'), not on the fixtures row.
func listStatisticsCandidates(ctx context.Context, db *sql.DB, limit int) ([]statisticsCandidate, int, error) {
	rows, err := db.QueryContext(ctx, `
SELECT
  f.id,
  pem.provider_entity_id
FROM fixtures AS f
JOIN provider_entity_maps AS pem
  ON pem.provider = 'api-football'
 AND pem.entity_type = 'match'
 AND pem.canonical_entity_type = 'match'
 AND pem.canonical_entity_id = f.id::text
 AND pem.retired_at IS NULL
WHERE lower(f.status) IN ('ft', 'aet', 'pen', 'finished')
  AND f.home_goals_ft IS NOT NULL
  AND f.away_goals_ft IS NOT NULL
  AND NOT EXISTS (
    SELECT 1
    FROM fixture_statistics AS fs
    WHERE fs.fixture_id = f.id
  )
ORDER BY f.kickoff_at DESC
LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	candidates := []statisticsCandidate{}
	skipped := 0
	for rows.Next() {
		var candidate statisticsCandidate
		var providerID string
		if err := rows.Scan(&candidate.FixtureID, &providerID); err != nil {
			return nil, 0, err
		}
		parsed, err := strconv.ParseInt(strings.TrimSpace(providerID), 10, 64)
		if err != nil || parsed <= 0 {
			skipped++
			continue
		}
		candidate.ProviderFixtureID = parsed
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return candidates, skipped, nil
}

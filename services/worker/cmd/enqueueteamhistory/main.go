package main

import (
	"context"
	"database/sql"
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
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	providerFixturesQueue = "provider-fixtures"
	teamHistoryPriority   = 10
)

type candidateTeam struct {
	CanonicalTeamID     int64
	Slug                string
	TeamName            string
	FixtureID           int64
	Side                string
	KickoffAt           time.Time
	PriorCompletedCount int
}

func main() {
	last := flag.Int("last", 15, "number of recent team fixtures to fetch from API-Football")
	maxTeams := flag.Int("max-teams", 50, "maximum API-Football team-history jobs to queue")
	minHistory := flag.Int("min-history", 5, "minimum completed same-league-season matches needed before no backfill is queued")
	flag.Parse()

	if *last <= 0 {
		slog.Error("last must be positive", "last", *last)
		os.Exit(1)
	}
	if *maxTeams <= 0 {
		slog.Error("max-teams must be positive", "max_teams", *maxTeams)
		os.Exit(1)
	}
	if *minHistory <= 0 {
		slog.Error("min-history must be positive", "min_history", *minHistory)
		os.Exit(1)
	}

	cfg, err := config.Load(nil)
	if err != nil {
		slog.Error("invalid team-history enqueue configuration", "error", err)
		os.Exit(1)
	}
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	candidates, err := listCandidateTeams(ctx, db, *maxTeams*3, *minHistory)
	if err != nil {
		slog.Error("list team-history candidates", "error", err)
		os.Exit(1)
	}

	now := time.Now().UTC()
	jobsToQueue, skipped := buildTeamHistoryJobs(candidates, *last, *maxTeams, now)
	repo := jobs.NewRepository(db)
	queued := 0
	for _, job := range jobsToQueue {
		if _, err := repo.Enqueue(ctx, job); err != nil {
			slog.Error("enqueue team-history job", "error", err, "idempotency_key", job.IdempotencyKey)
			os.Exit(1)
		}
		queued++
	}

	slog.Info(
		"team-history jobs queued",
		"queued", queued,
		"candidates", len(candidates),
		"skipped_invalid_or_duplicate", skipped,
		"last", *last,
		"max_teams", *maxTeams,
		"min_history", *minHistory,
	)
	fmt.Printf("queued_team_history_jobs=%d candidates=%d skipped_invalid_or_duplicate=%d last=%d max_teams=%d min_history=%d\n", queued, len(candidates), skipped, *last, *maxTeams, *minHistory)
}

func listCandidateTeams(ctx context.Context, db *sql.DB, limit int, minHistory int) ([]candidateTeam, error) {
	rows, err := db.QueryContext(ctx, `
WITH odds_backed_upcoming AS (
  SELECT
    f.id AS fixture_id,
    f.league_id,
    f.season_id,
    f.home_team_id,
    f.away_team_id,
    f.kickoff_at
  FROM fixtures AS f
  WHERE f.kickoff_at >= now()
    AND f.kickoff_at < now() + interval '7 days'
    AND lower(f.status) NOT IN ('ft', 'aet', 'pen', 'finished', 'cancelled', 'canceled', 'postponed', 'abandoned')
    AND EXISTS (
      SELECT 1
      FROM odds AS o
      WHERE o.fixture_id = f.id
        AND lower(o.market) IN ('1x2', 'match winner', 'match_winner')
    )
),
fixture_teams AS (
  SELECT fixture_id, league_id, season_id, kickoff_at, home_team_id AS team_id, 'home' AS side
  FROM odds_backed_upcoming
  UNION ALL
  SELECT fixture_id, league_id, season_id, kickoff_at, away_team_id AS team_id, 'away' AS side
  FROM odds_backed_upcoming
),
history_counts AS (
  SELECT
    ft.fixture_id,
    ft.team_id,
    ft.side,
    ft.kickoff_at,
    COUNT(p.id)::integer AS prior_completed_count
  FROM fixture_teams AS ft
  LEFT JOIN fixtures AS p
    ON p.league_id = ft.league_id
   AND p.season_id = ft.season_id
   AND p.kickoff_at < ft.kickoff_at
   AND p.home_goals_ft IS NOT NULL
   AND p.away_goals_ft IS NOT NULL
   AND (p.home_team_id = ft.team_id OR p.away_team_id = ft.team_id)
  GROUP BY ft.fixture_id, ft.team_id, ft.side, ft.kickoff_at
),
mapped AS (
  SELECT
    COALESCE(pem.provider_entity_id, t.slug) AS provider_team_ref,
    h.team_id AS canonical_team_id,
    t.name AS team_name,
    h.fixture_id,
    h.side,
    h.kickoff_at,
    h.prior_completed_count
  FROM history_counts AS h
  JOIN teams AS t ON t.id = h.team_id
  LEFT JOIN provider_entity_maps AS pem
    ON pem.provider = 'api-football'
   AND pem.entity_type = 'team'
   AND pem.canonical_entity_type = 'team'
   AND pem.canonical_entity_id = h.team_id::text
   AND pem.retired_at IS NULL
  WHERE h.prior_completed_count < $2::integer
),
ranked AS (
  SELECT
    *,
    ROW_NUMBER() OVER (
      PARTITION BY provider_team_ref
      ORDER BY prior_completed_count ASC, kickoff_at ASC, fixture_id ASC
    ) AS row_num
  FROM mapped
  WHERE provider_team_ref IS NOT NULL
)
SELECT
  provider_team_ref,
  canonical_team_id,
  team_name,
  fixture_id,
  side,
  kickoff_at,
  prior_completed_count
FROM ranked
WHERE row_num = 1
ORDER BY prior_completed_count ASC, kickoff_at ASC, provider_team_ref ASC
LIMIT $1::integer`,
		limit,
		minHistory,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	candidates := []candidateTeam{}
	for rows.Next() {
		var candidate candidateTeam
		if err := rows.Scan(
			&candidate.Slug,
			&candidate.CanonicalTeamID,
			&candidate.TeamName,
			&candidate.FixtureID,
			&candidate.Side,
			&candidate.KickoffAt,
			&candidate.PriorCompletedCount,
		); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}

func buildTeamHistoryJobs(candidates []candidateTeam, last int, maxTeams int, now time.Time) ([]jobs.NewJob, int) {
	if maxTeams <= 0 {
		return nil, len(candidates)
	}
	seen := map[int]struct{}{}
	jobsToQueue := make([]jobs.NewJob, 0, maxTeams)
	skipped := 0
	for _, candidate := range candidates {
		providerTeamID, ok := providerTeamIDFromSlug(candidate.Slug)
		if !ok {
			skipped++
			continue
		}
		if _, ok := seen[providerTeamID]; ok {
			skipped++
			continue
		}
		seen[providerTeamID] = struct{}{}
		if len(jobsToQueue) >= maxTeams {
			continue
		}
		jobsToQueue = append(jobsToQueue, newTeamHistoryJob(providerTeamID, last, now))
	}
	return jobsToQueue, skipped
}

func newTeamHistoryJob(providerTeamID int, last int, now time.Time) jobs.NewJob {
	return jobs.NewJob{
		JobKey:               ingestion.FixturesJobKey(),
		Queue:                providerFixturesQueue,
		Priority:             teamHistoryPriority,
		Payload:              []byte(fmt.Sprintf(`{"endpoint":"fixtures","team_id":%d,"last":%d}`, providerTeamID, last)),
		IdempotencyKey:       ingestion.ProviderIdempotencyKey(apifootball.ProviderName, "fixtures", teamHistoryParamsKey(providerTeamID, last)),
		DeduplicateCompleted: true,
		RunAfter:             now,
	}
}

func teamHistoryParamsKey(providerTeamID int, last int) string {
	return fmt.Sprintf(`{"last":%d,"team":%d}`, last, providerTeamID)
}

func providerTeamIDFromSlug(value string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if id, err := strconv.Atoi(value); err == nil && id > 0 {
		return id, true
	}
	if !strings.HasPrefix(value, "team-") {
		return 0, false
	}
	remainder := strings.TrimPrefix(value, "team-")
	nextDash := strings.IndexByte(remainder, '-')
	if nextDash >= 0 {
		remainder = remainder[:nextDash]
	}
	id, err := strconv.Atoi(remainder)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

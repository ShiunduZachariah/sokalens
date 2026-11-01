package scheduler

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/ingestion"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

// Statistics backfill is deliberately one-shot. Only teams in upcoming
// fixtures without an engine prediction create demand; each team contributes
// at most twenty recent finished histories. A previously fetched resource is
// excluded even if its provider response was empty. Reopening such resources
// needs a separate, measured delayed-data policy.
const statisticsBackfillCandidatesSQL = `
WITH targets AS MATERIALIZED (
  SELECT fixture.id, fixture.home_team_id, fixture.away_team_id, fixture.kickoff_at
  FROM fixtures AS fixture
  WHERE fixture.kickoff_at > now()
    AND fixture.kickoff_at <= now() + interval '7 days'
    AND lower(fixture.status) NOT IN
      ('ft', 'aet', 'pen', 'finished', 'canceled', 'cancelled', 'abandoned', 'postponed')
    AND NOT EXISTS (
      SELECT 1 FROM engine_predictions AS prediction
      WHERE prediction.fixture_id = fixture.id
    )
), teams AS MATERIALIZED (
  SELECT team_id, min(kickoff_at) AS target_at
  FROM (
    SELECT home_team_id AS team_id, kickoff_at FROM targets
    UNION ALL
    SELECT away_team_id AS team_id, kickoff_at FROM targets
  ) AS target_teams
  GROUP BY team_id
), recent AS MATERIALIZED (
  SELECT history.id, min(team.target_at) AS target_at,
         max(history.kickoff_at) AS history_at
  FROM teams AS team
  CROSS JOIN LATERAL (
    SELECT candidate.id, candidate.kickoff_at, candidate.status,
           candidate.home_goals_ft, candidate.away_goals_ft
    FROM (
      (SELECT id, kickoff_at, status, home_goals_ft, away_goals_ft
       FROM fixtures WHERE home_team_id = team.team_id
         AND kickoff_at < team.target_at
         AND kickoff_at >= team.target_at - interval '18 months'
       ORDER BY kickoff_at DESC LIMIT 30)
      UNION ALL
      (SELECT id, kickoff_at, status, home_goals_ft, away_goals_ft
       FROM fixtures WHERE away_team_id = team.team_id
         AND kickoff_at < team.target_at
         AND kickoff_at >= team.target_at - interval '18 months'
       ORDER BY kickoff_at DESC LIMIT 30)
    ) AS candidate
    ORDER BY candidate.kickoff_at DESC LIMIT 20
  ) AS history
  WHERE lower(history.status) IN ('ft', 'aet', 'pen', 'finished')
    AND history.home_goals_ft IS NOT NULL
    AND history.away_goals_ft IS NOT NULL
  GROUP BY history.id
)
SELECT map.provider_entity_id
FROM recent
JOIN fixtures AS historical ON historical.id = recent.id
JOIN (
  SELECT DISTINCT fixture.league_id, fixture.season_id
  FROM fixture_statistics AS stored
  JOIN fixtures AS fixture ON fixture.id = stored.fixture_id
) AS covered
  ON covered.league_id = historical.league_id
 AND covered.season_id = historical.season_id
JOIN provider_entity_maps AS map
  ON map.canonical_entity_id = recent.id::text
 AND map.canonical_entity_type = 'match'
 AND map.provider = 'api-football'
 AND map.entity_type = 'match'
 AND map.retired_at IS NULL
LEFT JOIN provider_resource_state AS resource
  ON resource.provider = 'api-football'
 AND resource.resource_key = 'fixture_stats:' || map.provider_entity_id
WHERE resource.resource_key IS NULL
  AND map.provider_entity_id ~ '^[0-9]+$'
  AND length(map.provider_entity_id) <= 18
  AND NOT EXISTS (
    SELECT 1 FROM fixture_statistics AS stored
    WHERE stored.fixture_id = recent.id
  )
  AND NOT EXISTS (
    SELECT 1 FROM jobs AS previous
    WHERE previous.idempotency_key =
      'schedule:provider:api-football:stats-demand:' || map.provider_entity_id
  )
ORDER BY recent.history_at DESC, recent.target_at ASC, recent.id ASC
LIMIT $1`

func statisticsBackfillJobs(ctx context.Context, opts QuotaOptions, max int, now time.Time, logger *slog.Logger) []ScheduledJob {
	if opts.DB == nil || max <= 0 {
		return nil
	}
	rows, err := opts.DB.QueryContext(ctx, statisticsBackfillCandidatesSQL, max)
	if err != nil {
		logger.Warn("statistics backfill candidates unavailable", "error", err)
		return nil
	}
	defer rows.Close()
	plan := make([]ScheduledJob, 0, max)
	for rows.Next() {
		var providerID string
		if err := rows.Scan(&providerID); err != nil {
			logger.Warn("statistics backfill candidate scan failed", "error", err)
			continue
		}
		scheduled, ok := newStatisticsBackfillJob(providerID, now)
		if !ok {
			logger.Warn("statistics backfill provider fixture ID invalid", "provider_fixture_id", providerID)
			continue
		}
		plan = append(plan, scheduled)
	}
	if err := rows.Err(); err != nil {
		logger.Warn("statistics backfill candidate iteration failed", "error", err)
		return nil
	}
	return plan
}

func newStatisticsBackfillJob(providerID string, now time.Time) (ScheduledJob, bool) {
	fixtureID, err := strconv.ParseInt(providerID, 10, 64)
	if err != nil || fixtureID <= 0 {
		return ScheduledJob{}, false
	}
	return ScheduledJob{
		Job: jobs.NewJob{
			JobKey: ingestion.StatisticsJobKey(), Queue: "provider-fixtures", Priority: 40,
			Payload:              []byte(`{"fixture_id":` + strconv.FormatInt(fixtureID, 10) + `}`),
			IdempotencyKey:       "schedule:provider:api-football:stats-demand:" + strconv.FormatInt(fixtureID, 10),
			DeduplicateCompleted: true, RunAfter: now,
		},
		Provider: "api-football", CallMode: "real", Schedule: "statistics-backfill",
		Bucket: now.Truncate(6 * time.Hour).Format("20060102T150405Z"),
	}, true
}

package ingestion

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/footballdata"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/thesportsdb"
)

type SQLBackfillRepository struct {
	db *sql.DB
}

func NewSQLBackfillRepository(db *sql.DB) SQLBackfillRepository {
	return SQLBackfillRepository{db: db}
}

// ExpireElapsedRequests closes requests after their 24-hour window, including
// future fixtures whose coordinator job failed and will never run again.
// Start cannot claim an elapsed request. A fresh engine demand can reopen an
// expired request for a fixture that has not kicked off yet.
// A bounded batch and SKIP LOCKED keep this safe alongside normal workers.
func (r SQLBackfillRepository) ExpireElapsedRequests(ctx context.Context, limit int) (int64, error) {
	if r.db == nil {
		return 0, errors.New("backfill repository DB is nil")
	}
	if limit <= 0 {
		return 0, nil
	}
	result, err := r.db.ExecContext(ctx, `
WITH stale AS (
  SELECT request.id
  FROM data_backfill_requests AS request
  WHERE request.status IN ('pending', 'in_progress')
    AND request.expires_at <= now()
  ORDER BY request.expires_at, request.id
  LIMIT $1
  FOR UPDATE OF request SKIP LOCKED
)
UPDATE data_backfill_requests AS request
SET status = 'expired', completed_at = now(), updated_at = now(),
    last_error = COALESCE(
      (SELECT job.last_error FROM jobs AS job
       WHERE job.job_key = 'backfill.team_history'
         AND job.idempotency_key = 'backfill.team_history:' || request.id::text
         AND job.status = 'failed'
       ORDER BY job.id DESC LIMIT 1),
      request.last_error,
      'REQUEST_EXPIRED')
FROM stale WHERE request.id = stale.id`, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (r SQLBackfillRepository) Start(ctx context.Context, requestID int64) (DataBackfillRequest, error) {
	if r.db == nil {
		return DataBackfillRequest{}, errors.New("backfill repository DB is nil")
	}
	var request DataBackfillRequest
	err := r.db.QueryRowContext(ctx, `
UPDATE data_backfill_requests AS request
SET status = 'in_progress',
    -- A request remains in_progress while a provider/quota error retries the
    -- same coordinator job. Count the request lifecycle once, not every job
    -- delivery; repeated deliveries previously overflowed the <=12 check.
    attempts = CASE
      WHEN request.status = 'pending' THEN request.attempts + 1
      ELSE request.attempts
    END,
    updated_at = now()
FROM fixtures AS target
WHERE request.id = $1
  AND request.target_fixture_id = target.id
  AND request.status IN ('pending', 'in_progress')
  AND request.next_attempt_at <= now()
  AND request.expires_at > now()
RETURNING request.id, request.target_fixture_id, request.team_id,
          request.competition_id, request.feature_group, request.matches_needed,
          request.attempts, target.season_id, target.kickoff_at`, requestID).Scan(
		&request.ID,
		&request.TargetFixtureID,
		&request.TeamID,
		&request.CompetitionID,
		&request.FeatureGroup,
		&request.MatchesNeeded,
		&request.Attempts,
		&request.Season,
		&request.TargetKickoffAt,
	)
	return request, err
}

func (r SQLBackfillRepository) Count(ctx context.Context, request DataBackfillRequest) (int, error) {
	if r.db == nil {
		return 0, errors.New("backfill repository DB is nil")
	}
	var count int
	if request.FeatureGroup == "rich_statistics" {
		err := r.db.QueryRowContext(ctx, `
SELECT COUNT(DISTINCT stats.fixture_id)
FROM fixture_statistics AS stats
JOIN fixtures AS history ON history.id = stats.fixture_id
WHERE stats.team_id = $1
  AND history.kickoff_at < $2
  AND history.season_id IN ($3, $3 - 1)`, request.TeamID, request.TargetKickoffAt, request.Season).Scan(&count)
		return count, err
	}
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM fixtures AS history
WHERE history.kickoff_at < $1
  AND history.season_id IN ($2, $2 - 1)
  AND lower(history.status) IN ('ft', 'aet', 'pen', 'finished')
  AND history.home_goals_ft IS NOT NULL
  AND history.away_goals_ft IS NOT NULL
  AND (history.home_team_id = $3 OR history.away_team_id = $3)`, request.TargetKickoffAt, request.Season, request.TeamID).Scan(&count)
	return count, err
}

func (r SQLBackfillRepository) RecordAttempt(
	ctx context.Context,
	requestID int64,
	provider string,
	available bool,
) error {
	if r.db == nil {
		return errors.New("backfill repository DB is nil")
	}
	_, err := r.db.ExecContext(ctx, `
UPDATE data_backfill_requests
SET provider_attempts = provider_attempts || jsonb_build_object(
      $2::text,
      jsonb_build_object('available', $3::boolean, 'attempted_at', now())
    ),
    updated_at = now()
WHERE id = $1`, requestID, provider, available)
	return err
}

func (r SQLBackfillRepository) Complete(ctx context.Context, request DataBackfillRequest) error {
	if r.db == nil {
		return errors.New("backfill repository DB is nil")
	}
	payload, err := json.Marshal(map[string]any{"fixture_ids": []int64{request.TargetFixtureID}})
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
WITH completed AS (
  UPDATE data_backfill_requests
  SET status = 'completed', completed_at = now(), updated_at = now(), last_error = NULL
  WHERE id = $1
    AND status IN ('pending', 'in_progress')
  RETURNING target_fixture_id
)
INSERT INTO jobs (job_key, status, payload, queue, priority, run_after, idempotency_key, max_attempts)
SELECT 'predictions.generate.upcoming', 'queued', $2::jsonb, 'predictions', 5, now(),
       'backfill:regenerate:' || target_fixture_id::text, 5
FROM completed
ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL AND status IN ('queued', 'running')
DO UPDATE SET updated_at = jobs.updated_at`, request.ID, string(payload))
	return err
}

func (r SQLBackfillRepository) ConfirmUnavailable(ctx context.Context, request DataBackfillRequest) error {
	if r.db == nil {
		return errors.New("backfill repository DB is nil")
	}
	_, err := r.db.ExecContext(ctx, `
UPDATE data_backfill_requests
SET status = 'confirmed_unavailable',
    completed_at = now(),
    updated_at = now(),
    last_error = 'PROVIDER_CONFIRMED_NO_HISTORY'
WHERE id = $1
  AND status IN ('pending', 'in_progress')`, request.ID)
	return err
}

func (r SQLBackfillRepository) BuildProviderPayloads(
	ctx context.Context,
	provider string,
	request DataBackfillRequest,
) ([][]byte, error) {
	switch provider {
	case footballdata.ProviderName:
		if request.FeatureGroup != "results" {
			return nil, nil
		}
		competitionID, season, err := r.actualPriorCompetition(ctx, request)
		if err != nil {
			return nil, err
		}
		providerCompetitionID, ok, err := r.providerEntityID(ctx, provider, "league", competitionID)
		if err != nil || !ok {
			// The target competition remains a last-resort fallback when the actual
			// prior competition has no provider mapping.
			providerCompetitionID, ok, err = r.providerEntityID(ctx, provider, "league", request.CompetitionID)
			season = request.Season - 1
			if err != nil || !ok {
				return nil, err
			}
		}
		return marshalBackfillPayloads(map[string]any{
			"competition": providerCompetitionID,
			"season":      strconv.Itoa(season),
			"status":      "FINISHED",
		})
	case thesportsdb.ProviderName:
		if request.FeatureGroup != "results" {
			return nil, nil
		}
		teamName, ok, err := r.canonicalTeamName(ctx, request.TeamID)
		if err != nil || !ok {
			return nil, err
		}
		return marshalBackfillPayloads(map[string]any{
			"team_id":        request.TeamID,
			"team_name":      teamName,
			"competition_id": request.CompetitionID,
			"matches_needed": request.MatchesNeeded,
		})
	case apifootball.ProviderName:
		if request.FeatureGroup == "rich_statistics" {
			return r.statisticsPayloads(ctx, request)
		}
		providerTeamID, ok, err := r.providerEntityID(ctx, provider, "team", request.TeamID)
		if err != nil || !ok {
			return nil, err
		}
		teamID, err := strconv.Atoi(providerTeamID)
		if err != nil || teamID <= 0 {
			return nil, nil
		}
		last := request.MatchesNeeded * 2
		if last < 10 {
			last = 10
		}
		if last > 30 {
			last = 30
		}
		return marshalBackfillPayloads(map[string]any{"team_id": teamID, "last": last})
	default:
		return nil, fmt.Errorf("unsupported backfill provider %q", provider)
	}
}

func (r SQLBackfillRepository) actualPriorCompetition(ctx context.Context, request DataBackfillRequest) (int64, int, error) {
	var competitionID int64
	var season int
	err := r.db.QueryRowContext(ctx, `
SELECT history.league_id, history.season_id
FROM fixtures AS history
WHERE history.kickoff_at < $1
  AND history.season_id IN ($2, $2 - 1)
  AND (history.home_team_id = $3 OR history.away_team_id = $3)
  AND lower(history.status) IN ('ft', 'aet', 'pen', 'finished')
ORDER BY history.kickoff_at DESC, history.id DESC
LIMIT 1`, request.TargetKickoffAt, request.Season, request.TeamID).Scan(&competitionID, &season)
	if errors.Is(err, sql.ErrNoRows) {
		return request.CompetitionID, request.Season - 1, nil
	}
	return competitionID, season, err
}

func (r SQLBackfillRepository) canonicalTeamName(ctx context.Context, teamID int64) (string, bool, error) {
	var name string
	err := r.db.QueryRowContext(ctx, `SELECT name FROM teams WHERE id = $1`, teamID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return name, err == nil, err
}

func (r SQLBackfillRepository) providerEntityID(
	ctx context.Context,
	provider string,
	entityType string,
	canonicalID int64,
) (string, bool, error) {
	if r.db == nil {
		return "", false, errors.New("backfill repository DB is nil")
	}
	var providerID string
	err := r.db.QueryRowContext(ctx, `
SELECT provider_entity_id
FROM provider_entity_maps
WHERE provider = $1
  AND entity_type = $2
  AND canonical_entity_type = $2
  AND canonical_entity_id = $3
  AND retired_at IS NULL
ORDER BY last_seen_at DESC
LIMIT 1`, provider, entityType, strconv.FormatInt(canonicalID, 10)).Scan(&providerID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return providerID, err == nil, err
}

const statisticsPayloadsSQL = `
SELECT maps.provider_entity_id
FROM fixtures AS history
JOIN provider_entity_maps AS maps
  ON maps.provider = 'api-football'
 AND maps.entity_type = 'match'
 AND maps.canonical_entity_type = 'match'
 AND maps.canonical_entity_id = history.id::text
 AND maps.retired_at IS NULL
LEFT JOIN provider_resource_state AS resource
  ON resource.provider = 'api-football'
 AND resource.resource_key = 'fixture_stats:' || maps.provider_entity_id
WHERE history.kickoff_at < $1
  AND history.season_id IN ($2, $2 - 1)
  AND (history.home_team_id = $3 OR history.away_team_id = $3)
  AND lower(history.status) IN ('ft', 'aet', 'pen', 'finished')
  AND NOT EXISTS (
    SELECT 1 FROM fixture_statistics AS stats
    WHERE stats.fixture_id = history.id AND stats.team_id = $3
  )
  -- The existing scheduler already fetched and marked this settled fixture
  -- immutable. Delayed provider fills need a separate bounded retry policy.
  AND COALESCE(resource.immutable, FALSE) = FALSE
ORDER BY history.kickoff_at DESC
LIMIT $4`

func (r SQLBackfillRepository) statisticsPayloads(
	ctx context.Context,
	request DataBackfillRequest,
) ([][]byte, error) {
	rows, err := r.db.QueryContext(ctx, statisticsPayloadsSQL, request.TargetKickoffAt, request.Season, request.TeamID, request.MatchesNeeded)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	payloads := make([][]byte, 0, request.MatchesNeeded)
	for rows.Next() {
		var providerID string
		if err := rows.Scan(&providerID); err != nil {
			return nil, err
		}
		fixtureID, err := strconv.ParseInt(providerID, 10, 64)
		if err != nil || fixtureID <= 0 {
			continue
		}
		payload, err := json.Marshal(map[string]int64{"fixture_id": fixtureID})
		if err != nil {
			return nil, err
		}
		payloads = append(payloads, payload)
	}
	return payloads, rows.Err()
}

func marshalBackfillPayloads(value map[string]any) ([][]byte, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return [][]byte{payload}, nil
}

var _ BackfillRequestStore = SQLBackfillRepository{}
var _ BackfillPayloadBuilder = SQLBackfillRepository{}

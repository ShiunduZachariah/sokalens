package ingestion

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
)

// ProviderPredictionsDB is the narrow database surface for provider-prediction
// storage.
type ProviderPredictionsDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// ProviderPredictionRecord is one API-Football native prediction for a fixture.
type ProviderPredictionRecord struct {
	FixtureID       int64
	PredictedWinner string
	WinOrDraw       bool
	UnderOver       string
	PercentHome     float64
	PercentDraw     float64
	PercentAway     float64
	Advice          string
}

type ProviderPredictionsRepository struct {
	db ProviderPredictionsDB
}

func NewProviderPredictionsRepository(db ProviderPredictionsDB) ProviderPredictionsRepository {
	return ProviderPredictionsRepository{db: db}
}

func (r ProviderPredictionsRepository) Upsert(ctx context.Context, record ProviderPredictionRecord) error {
	if r.db == nil {
		return errors.New("provider predictions repository DB is nil")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO api_predictions (
  fixture_id, predicted_winner, win_or_draw, under_over,
  percent_home, percent_draw, percent_away, advice
) VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, NULLIF($8, ''))
ON CONFLICT (fixture_id) DO UPDATE SET
  predicted_winner = EXCLUDED.predicted_winner,
  win_or_draw = EXCLUDED.win_or_draw,
  under_over = EXCLUDED.under_over,
  percent_home = EXCLUDED.percent_home,
  percent_draw = EXCLUDED.percent_draw,
  percent_away = EXCLUDED.percent_away,
  advice = EXCLUDED.advice,
  captured_at = now()`,
		record.FixtureID,
		record.PredictedWinner,
		record.WinOrDraw,
		record.UnderOver,
		record.PercentHome,
		record.PercentDraw,
		record.PercentAway,
		record.Advice,
	)
	return err
}

type providerPredictionsEnqueuer interface {
	Enqueue(context.Context, jobs.NewJob) (int64, error)
}

// PendingProviderPrediction is one fixture that needs a fresh provider
// prediction (missing or stale api_predictions row).
type PendingProviderPrediction struct {
	ProviderFixtureID string
}

// ProviderPredictionsPlanner selects fixtures that need fresh provider
// predictions. It never calls the provider itself — it only enqueues the
// per-fixture fetch jobs (which are quota-governed by the client).
type ProviderPredictionsPlanner struct {
	db *sql.DB
}

func NewProviderPredictionsPlanner(db *sql.DB) *ProviderPredictionsPlanner {
	return &ProviderPredictionsPlanner{db: db}
}

// Pending returns upcoming fixtures (kickoff within horizonHours) whose
// provider prediction is missing or older than staleAfter, capped at limit.
func (p ProviderPredictionsPlanner) Pending(ctx context.Context, provider string, horizonHours int, staleAfter time.Duration, limit int) ([]PendingProviderPrediction, error) {
	if p.db == nil {
		return nil, errors.New("provider predictions planner DB is nil")
	}
	if horizonHours <= 0 {
		horizonHours = 48
	}
	if staleAfter <= 0 {
		staleAfter = 12 * time.Hour
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := p.db.QueryContext(ctx, `
SELECT pem.provider_entity_id
FROM matches m
JOIN provider_entity_maps pem
  ON pem.provider = $1
 AND pem.entity_type = 'match'
 AND pem.canonical_entity_type = 'match'
 AND pem.canonical_entity_id = m.id::text
 AND pem.retired_at IS NULL
LEFT JOIN api_predictions ap ON ap.fixture_id = m.id
WHERE m.kickoff_at > now()
  AND m.kickoff_at <= now() + ($2 * interval '1 hour')
  AND m.status IN ('upcoming', 'live', 'halftime', 'second_half')
  AND (ap.fixture_id IS NULL OR ap.captured_at < now() - ($3 * interval '1 hour'))
ORDER BY m.kickoff_at ASC
LIMIT $4`, provider, horizonHours, staleAfter.Hours(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pending := make([]PendingProviderPrediction, 0, 16)
	for rows.Next() {
		var item PendingProviderPrediction
		if err := rows.Scan(&item.ProviderFixtureID); err != nil {
			return nil, err
		}
		pending = append(pending, item)
	}
	return pending, rows.Err()
}

type ProviderPredictionsJobOptions struct {
	Client      ProviderClient
	RequestLogs requestLogger
	EntityMaps  entityMapResolver
	RateLimits  rateLimitWriter
	Predictions ProviderPredictionsRepository
	Planner     *ProviderPredictionsPlanner
	Jobs        providerPredictionsEnqueuer
	Now         func() time.Time
}

type ProviderPredictionsJob struct {
	opts ProviderPredictionsJobOptions
}

type providerPredictionsJobPayload struct {
	FixtureID int `json:"fixture_id"`
}

func NewProviderPredictionsJob(opts ProviderPredictionsJobOptions) ProviderPredictionsJob {
	return ProviderPredictionsJob{opts: opts}
}

func (j ProviderPredictionsJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Client == nil {
		return errors.New("provider predictions job client is required")
	}
	if j.opts.RequestLogs == nil {
		return errors.New("provider predictions job request log repository is required")
	}
	if j.opts.EntityMaps == nil {
		return errors.New("provider predictions job entity map repository is required")
	}
	if j.opts.Predictions.db == nil {
		return errors.New("provider predictions repository is required")
	}
	now := time.Now().UTC
	if j.opts.Now != nil {
		now = j.opts.Now
	}

	payload := providerPredictionsJobPayload{}
	if len(job.Payload) > 0 && string(job.Payload) != "{}" {
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return err
		}
	}
	if payload.FixtureID <= 0 {
		// Batch mode: find fixtures that need a fresh provider prediction and
		// enqueue one per-fixture fetch job each. The batch job itself makes no
		// provider call (no quota cost); the per-fixture jobs are governed by
		// the client quota governors.
		return j.enqueuePending(ctx, job, now)
	}

	canonicalID, ok, err := j.opts.EntityMaps.LookupCanonicalEntityID(ctx, apifootball.ProviderName, "match", strconv.Itoa(payload.FixtureID))
	if err != nil {
		return err
	}
	if !ok {
		slog.Warn("provider prediction skipped: unmapped fixture", "fixture_id", payload.FixtureID)
		return nil
	}
	matchID, err := ParseCanonicalID(canonicalID)
	if err != nil {
		return err
	}

	params := url.Values{}
	params.Set("fixture", strconv.Itoa(payload.FixtureID))
	resp, err := j.opts.Client.Get(ctx, "predictions", params)
	paramsHash := hashParams(params)
	log := RequestLog{
		JobID:         job.ID,
		Provider:      nonEmpty(resp.Provider, apifootball.ProviderName),
		Endpoint:      nonEmpty(resp.Endpoint, "predictions"),
		Method:        "GET",
		ParamsHash:    paramsHash,
		StatusCode:    resp.StatusCode,
		QuotaCost:     providerQuotaCost(resp),
		Latency:       resp.Latency,
		ResponseBytes: resp.ResponseBytes,
		RateLimit:     resp.RateLimit,
		RequestedAt:   now(),
	}
	if err != nil {
		log.ErrorCategory = categorizeProviderError(resp, err)
		_ = j.opts.RequestLogs.Insert(ctx, log)
		return err
	}
	if err := j.opts.RequestLogs.Insert(ctx, log); err != nil {
		return err
	}
	if j.opts.RateLimits != nil {
		if err := j.opts.RateLimits.UpdateFromHeaders(ctx, ProviderRateLimitUpdate{
			Provider:        apifootball.ProviderName,
			DailyLimit:      resp.RateLimit.DailyLimit,
			DailyRemaining:  resp.RateLimit.DailyRemaining,
			MinuteLimit:     resp.RateLimit.MinuteLimit,
			MinuteRemaining: resp.RateLimit.MinuteRemaining,
			UpdatedAt:       now(),
		}); err != nil {
			return err
		}
	}

	items, err := apifootball.ParsePredictions(resp.Body)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		slog.Info("provider prediction returned no rows", "fixture_id", payload.FixtureID)
		return nil
	}
	item := items[0]
	home, err := apifootball.ParsePercent(item.Predictions.Percent.Home)
	if err != nil {
		return err
	}
	draw, err := apifootball.ParsePercent(item.Predictions.Percent.Draw)
	if err != nil {
		return err
	}
	away, err := apifootball.ParsePercent(item.Predictions.Percent.Away)
	if err != nil {
		return err
	}
	if err := j.opts.Predictions.Upsert(ctx, ProviderPredictionRecord{
		FixtureID:       matchID,
		PredictedWinner: strings.TrimSpace(item.Predictions.Winner.Name),
		WinOrDraw:       item.Predictions.WinOrDraw,
		UnderOver:       strings.TrimSpace(item.Predictions.UnderOver),
		PercentHome:     home,
		PercentDraw:     draw,
		PercentAway:     away,
		Advice:          strings.TrimSpace(item.Predictions.Advice),
	}); err != nil {
		return err
	}
	slog.Info("provider prediction stored", "fixture_id", matchID, "provider_fixture_id", payload.FixtureID)
	return nil
}

func ProviderPredictionsJobKey() string {
	return ProviderJobKey(apifootball.ProviderName, "predictions")
}

// enqueuePending implements batch mode: select fixtures missing a fresh
// provider prediction and enqueue a per-fixture job for each.
func (j ProviderPredictionsJob) enqueuePending(ctx context.Context, job jobs.Job, now func() time.Time) error {
	if j.opts.Planner == nil {
		return errors.New("provider predictions batch mode requires a planner")
	}
	if j.opts.Jobs == nil {
		return errors.New("provider predictions batch mode requires a job enqueuer")
	}
	pending, err := j.opts.Planner.Pending(ctx, apifootball.ProviderName, 48, 12*time.Hour, 200)
	if err != nil {
		return err
	}
	enqueued, err := planProviderPredictionJobs(ctx, now, pending, j.opts.Jobs)
	if err != nil {
		return err
	}
	slog.Info("provider prediction batch planned",
		"job_id", job.ID, "job_key", job.JobKey,
		"pending", len(pending), "enqueued", enqueued)
	return nil
}

// planProviderPredictionJobs enqueues one per-fixture fetch job per pending
// prediction. Extracted for testability.
func planProviderPredictionJobs(ctx context.Context, now func() time.Time, pending []PendingProviderPrediction, enqueuer providerPredictionsEnqueuer) (int, error) {
	if enqueuer == nil {
		return 0, errors.New("provider predictions job enqueuer is required")
	}
	if now == nil {
		now = time.Now
	}
	enqueued := 0
	for _, item := range pending {
		payload, err := json.Marshal(providerPredictionsJobPayload{FixtureID: parseIntOrZero(item.ProviderFixtureID)})
		if err != nil {
			return enqueued, err
		}
		_, err = enqueuer.Enqueue(ctx, jobs.NewJob{
			JobKey:               ProviderPredictionsJobKey(),
			Queue:                "provider-fixtures",
			Priority:             33,
			Payload:              payload,
			IdempotencyKey:       "provider:api-football:predictions:" + item.ProviderFixtureID,
			DeduplicateCompleted: false,
			RunAfter:             now().UTC(),
		})
		if err != nil {
			return enqueued, err
		}
		enqueued++
	}
	return enqueued, nil
}

func parseIntOrZero(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return value
}

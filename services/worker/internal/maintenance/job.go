package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

const (
	PruneJobKey              = "maintenance.prune-retained-data"
	RawPredictionPruneJobKey = "maintenance.prune-raw-predictions"
	Queue                    = "maintenance"
)

type Payload struct {
	EnableDestructiveRetention   bool `json:"enable_destructive_retention"`
	DryRun                       bool `json:"dry_run"`
	JobRetentionDays             int  `json:"job_retention_days"`
	NotificationRetentionDays    int  `json:"notification_retention_days"`
	EventRetentionDays           int  `json:"event_retention_days"`
	SessionRetentionDays         int  `json:"session_retention_days"`
	RawPredictionRetentionHours  int  `json:"raw_prediction_retention_hours"`
	ModelEvidenceRetentionDays   int  `json:"model_evidence_retention_days"`
	AccuracyRetentionDays        int  `json:"accuracy_retention_days"`
	SavedReferenceRetentionHours int  `json:"saved_reference_retention_hours"`
	StatisticsMatchesPerTeam     int  `json:"statistics_matches_per_team"`
	ProviderPayloadRetentionDays int  `json:"provider_payload_retention_days"`
	ProviderRequestRetentionDays int  `json:"provider_request_retention_days"`
	BatchSize                    int  `json:"batch_size"`
	MaxBatches                   int  `json:"max_batches"`
	// provider_request_logs needs its own allowance: it arrives at roughly
	// 1.25 million rows a day, which MaxBatches cannot drain at any value that
	// is still sane for the other tables.
	ProviderRequestMaxBatches int `json:"provider_request_max_batches"`
}

type Metrics struct {
	Jobs                        int64
	NotificationOutbox          int64
	RecommendationEvents        int64
	RecommendationCandidates    int64
	RecommendationDismissals    int64
	AuthTokens                  int64
	UserSessions                int64
	SavedPredictions            int64
	SavedPredictionFavorites    int64
	SavedFixtures               int64
	PredictionHashesNormalized  int64
	PredictionDuplicatesDeleted int64
	PredictionDuplicatesBlocked int64
	RawPredictions              int64
	RawPredictionsExamined      int64
	RawPredictionsBlocked       int64
	ModelEvidence               int64
	AccuracyAggregates          int64
	FixtureStatistics           int64
	ProviderPayloadReferences   int64
	ProviderPayloads            int64
	ProviderRequestLogs         int64
}

func (m Metrics) Total() int64 {
	return m.Jobs + m.NotificationOutbox + m.RecommendationEvents + m.RecommendationCandidates + m.RecommendationDismissals + m.AuthTokens + m.UserSessions +
		m.SavedPredictions + m.SavedPredictionFavorites + m.SavedFixtures + m.PredictionHashesNormalized + m.PredictionDuplicatesDeleted + m.RawPredictions + m.ModelEvidence + m.AccuracyAggregates + m.FixtureStatistics +
		m.ProviderPayloadReferences + m.ProviderPayloads + m.ProviderRequestLogs
}

type Store interface {
	Prune(context.Context, Payload, time.Time) (Metrics, error)
}

type Job struct {
	Store  Store
	Now    func() time.Time
	Logger *slog.Logger
	Policy Payload
}

func (j Job) Run(ctx context.Context, source jobs.Job) error {
	if j.Store == nil {
		return errors.New("maintenance store is required")
	}
	payload := j.Policy
	if len(source.Payload) > 0 {
		if err := json.Unmarshal(source.Payload, &payload); err != nil {
			return err
		}
	}
	payload = payload.withDefaults()
	now := time.Now().UTC()
	if j.Now != nil {
		now = j.Now().UTC()
	}
	started := time.Now()
	metrics, err := j.Store.Prune(ctx, payload, now)
	logger := j.Logger
	if logger == nil {
		logger = slog.Default()
	}
	attrs := []any{
		"job_id", source.ID,
		"rows_total", metrics.Total(),
		"jobs", metrics.Jobs,
		"notification_outbox", metrics.NotificationOutbox,
		"recommendation_events", metrics.RecommendationEvents,
		"recommendation_candidates", metrics.RecommendationCandidates,
		"recommendation_dismissals", metrics.RecommendationDismissals,
		"auth_tokens", metrics.AuthTokens,
		"user_sessions", metrics.UserSessions,
		"saved_predictions", metrics.SavedPredictions,
		"saved_prediction_favorites", metrics.SavedPredictionFavorites,
		"saved_fixtures", metrics.SavedFixtures,
		"prediction_hashes_normalized", metrics.PredictionHashesNormalized,
		"prediction_duplicates_deleted", metrics.PredictionDuplicatesDeleted,
		"prediction_duplicates_blocked", metrics.PredictionDuplicatesBlocked,
		"raw_predictions", metrics.RawPredictions,
		"raw_predictions_blocked", metrics.RawPredictionsBlocked,
		"model_evidence", metrics.ModelEvidence,
		"accuracy_aggregates", metrics.AccuracyAggregates,
		"fixture_statistics", metrics.FixtureStatistics,
		"provider_payload_references", metrics.ProviderPayloadReferences,
		"provider_payloads", metrics.ProviderPayloads,
		"provider_request_logs", metrics.ProviderRequestLogs,
		"duration_ms", time.Since(started).Milliseconds(),
		"destructive_retention_enabled", payload.EnableDestructiveRetention,
		"dry_run", payload.EnableDestructiveRetention && payload.DryRun,
	}
	if payload.EnableDestructiveRetention && payload.DryRun {
		attrs = append(attrs, "eligible_total", metrics.Total())
	} else {
		attrs = append(attrs, "deleted_total", metrics.Total())
	}
	if err != nil {
		logger.ErrorContext(ctx, "retention pruning failed", append(attrs, "error", err)...)
		return err
	}
	message := "retention pruning completed"
	if payload.EnableDestructiveRetention && payload.DryRun {
		message = "retention dry-run completed"
	}
	logger.InfoContext(ctx, message, attrs...)
	return nil
}

func (p Payload) withDefaults() Payload {
	if p.JobRetentionDays <= 0 {
		p.JobRetentionDays = 30
	}
	if p.NotificationRetentionDays <= 0 {
		p.NotificationRetentionDays = 90
	}
	if p.EventRetentionDays <= 0 {
		p.EventRetentionDays = 90
	}
	if p.SessionRetentionDays <= 0 {
		p.SessionRetentionDays = 30
	}
	if p.RawPredictionRetentionHours <= 0 {
		p.RawPredictionRetentionHours = 24
	}
	if p.ModelEvidenceRetentionDays <= 0 {
		p.ModelEvidenceRetentionDays = 365
	}
	if p.AccuracyRetentionDays <= 0 {
		p.AccuracyRetentionDays = 365
	}
	if p.SavedReferenceRetentionHours <= 0 {
		p.SavedReferenceRetentionHours = 6
	}
	if p.StatisticsMatchesPerTeam <= 0 || p.StatisticsMatchesPerTeam > 100 {
		p.StatisticsMatchesPerTeam = 10
	}
	if p.ProviderPayloadRetentionDays <= 0 {
		p.ProviderPayloadRetentionDays = 7
	}
	// One day, down from thirty on 2026-09-22.
	//
	// provider_request_logs is a log of calls we made. It held 9,055,224 rows
	// at 5,655 MB, and 6,263,581 of those were under five days old -- the table
	// is not growing from a long tail, it is growing from volume. Thirty days
	// of it is 5.7 GB of a 12 GB database.
	//
	// That size is paid for on every release, not just in disk: the deploy
	// takes a restore-verified backup, dumping and restoring the whole database
	// into a scratch copy before it proceeds, and that step now takes over an
	// hour. It is a good safety property and the way to keep it is to stop the
	// database growing, not to weaken the check.
	//
	// A day is the right default because nobody reads these unless something
	// went wrong. When something has, the window is widened for the
	// investigation rather than carried all year for it: the job payload's
	// provider_request_retention_days overrides this, so an incident run is
	//
	//   {"provider_request_retention_days": 14}
	//
	// and the next scheduled run returns to a day. Rows referenced by a
	// retained payload are excluded by the prune's own predicate regardless of
	// age, so this cannot orphan a payload that is still being kept.
	if p.ProviderRequestRetentionDays <= 0 {
		p.ProviderRequestRetentionDays = 1
	}
	if p.BatchSize <= 0 || p.BatchSize > 5000 {
		p.BatchSize = 1000
	}
	if p.MaxBatches <= 0 || p.MaxBatches > 100 {
		p.MaxBatches = 10
	}
	// 4,000 batches of 1,000 is 4 million rows, against an arrival rate near
	// 1.25 million a day. That is enough to hold the table steady and still
	// claw back the backlog over a few runs, rather than only keeping pace.
	//
	// It is an upper bound, not a target: the loop stops as soon as a batch
	// comes back short, so a drained table costs one query. The wall clock,
	// not this number, is what actually bounds a run -- see providerPruneBudget.
	if p.ProviderRequestMaxBatches <= 0 || p.ProviderRequestMaxBatches > 20000 {
		p.ProviderRequestMaxBatches = 4000
	}
	return p
}

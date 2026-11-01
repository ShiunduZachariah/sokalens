package ingestion

import (
	"context"
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

const foundationModelKey = "foundation-baseline"

type predictionJobEnqueuer interface {
	Enqueue(context.Context, jobs.NewJob) (int64, error)
}

type oddsWriter interface {
	LookupMarketID(context.Context, string) (int64, error)
	LookupModelID(context.Context, string) (int64, error)
	InsertOddsSnapshot(context.Context, OddsSnapshotRecord) error
	UpsertFeatureSnapshot(context.Context, PreparedSignal) error
	RecordGenerationFailure(context.Context, int64, int64, int64, string, string) error
	ClearGenerationFailure(context.Context, int64, int64, int64) error
}

type OddsJobOptions struct {
	Client      ProviderClient
	RequestLogs requestLogger
	EntityMaps  entityMapResolver
	RawPayloads rawPayloadWriter
	RateLimits  rateLimitWriter
	Odds        oddsWriter
	Jobs        predictionJobEnqueuer
	Now         func() time.Time
}

type OddsJob struct {
	opts OddsJobOptions
}

type oddsPricePayload struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Trend string `json:"trend"`
}

type oddsJobPayload struct {
	Date string `json:"date"`
	Page int    `json:"page"`
}

func NewOddsJob(opts OddsJobOptions) OddsJob {
	return OddsJob{opts: opts}
}

func (j OddsJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Client == nil {
		return errors.New("odds job provider client is required")
	}
	if j.opts.RequestLogs == nil {
		return errors.New("odds job request log repository is required")
	}
	if j.opts.EntityMaps == nil {
		return errors.New("odds job entity map repository is required")
	}
	if j.opts.Odds == nil {
		return errors.New("odds job repository is required")
	}
	now := time.Now().UTC
	if j.opts.Now != nil {
		now = j.opts.Now
	}
	payload := oddsJobPayload{Page: 1}
	if len(job.Payload) > 0 && string(job.Payload) != "{}" {
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return err
		}
	}
	if payload.Date == "" {
		payload.Date = now().Format("2006-01-02")
	}
	if payload.Page <= 0 {
		payload.Page = 1
	}

	params := url.Values{}
	params.Set("date", payload.Date)
	params.Set("page", strconv.Itoa(payload.Page))
	resp, err := j.opts.Client.Get(ctx, "odds", params)
	paramsHash := hashParams(params)
	log := RequestLog{
		JobID:         job.ID,
		Provider:      nonEmpty(resp.Provider, apifootball.ProviderName),
		Endpoint:      nonEmpty(resp.Endpoint, "odds"),
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
		if logErr := j.opts.RequestLogs.Insert(ctx, log); logErr != nil {
			return logErr
		}
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
	if j.opts.RawPayloads != nil {
		if err := j.opts.RawPayloads.InsertMetadata(ctx, RawPayloadMetadata{
			Provider:      apifootball.ProviderName,
			EndpointKey:   "odds.pre_match",
			ParamsHash:    paramsHash,
			StatusCode:    resp.StatusCode,
			ResponseHash:  ResponseHash(resp.Body),
			ResponseBytes: resp.ResponseBytes,
			Headers:       resp.RateLimit.Raw,
			StoragePolicy: "metadata_only",
			FetchedAt:     now(),
		}); err != nil {
			return err
		}
	}

	marketID, err := j.opts.Odds.LookupMarketID(ctx, "1x2")
	if err != nil {
		return err
	}
	modelID, err := j.opts.Odds.LookupModelID(ctx, foundationModelKey)
	if err != nil {
		return err
	}

	items, err := apifootball.ParseOdds(resp.Body)
	if err != nil {
		return err
	}
	refreshedPredictions := false
	unmappedFixtures := 0
	unavailableMarkets := 0
	invalidPrices := 0
	snapshotsStored := 0
	featuresUpdated := 0
	for _, item := range items {
		canonicalID, ok, err := j.opts.EntityMaps.LookupCanonicalEntityID(ctx, apifootball.ProviderName, "match", strconv.Itoa(item.Fixture.ID))
		if err != nil || !ok {
			if err != nil {
				return err
			}
			unmappedFixtures++
			continue
		}
		matchID, err := ParseCanonicalID(canonicalID)
		if err != nil {
			return err
		}
		prices, bookmaker, capturedAt, ok := extractOneXTwoOdds(item)
		if !ok {
			unavailableMarkets++
			continue
		}
		homeOdds, err := parsePriceValue(prices[0].Value)
		if err != nil {
			_ = j.opts.Odds.RecordGenerationFailure(ctx, matchID, marketID, modelID, "insufficient_data", err.Error())
			invalidPrices++
			continue
		}
		drawOdds, err := parsePriceValue(prices[1].Value)
		if err != nil {
			_ = j.opts.Odds.RecordGenerationFailure(ctx, matchID, marketID, modelID, "insufficient_data", err.Error())
			invalidPrices++
			continue
		}
		awayOdds, err := parsePriceValue(prices[2].Value)
		if err != nil {
			_ = j.opts.Odds.RecordGenerationFailure(ctx, matchID, marketID, modelID, "insufficient_data", err.Error())
			invalidPrices++
			continue
		}
		if err := j.opts.Odds.InsertOddsSnapshot(ctx, OddsSnapshotRecord{
			MatchID:  matchID,
			MarketID: marketID,
			Provider: apifootball.ProviderName,
			Payload: map[string]any{
				"bookmaker": bookmaker,
				"prices":    prices,
			},
			CapturedAt: capturedAt,
		}); err != nil {
			return err
		}
		snapshotsStored++
		signals, err := buildSignalsFromOdds(homeOdds, drawOdds, awayOdds)
		if err != nil {
			_ = j.opts.Odds.RecordGenerationFailure(ctx, matchID, marketID, modelID, "insufficient_data", err.Error())
			continue
		}
		signals.MatchID = matchID
		signals.MarketID = marketID
		signals.ModelID = modelID
		if err := j.opts.Odds.UpsertFeatureSnapshot(ctx, signals); err != nil {
			_ = j.opts.Odds.RecordGenerationFailure(ctx, matchID, marketID, modelID, "failed_generation", err.Error())
			continue
		}
		featuresUpdated++
		if err := j.opts.Odds.ClearGenerationFailure(ctx, matchID, marketID, modelID); err != nil {
			return err
		}
		refreshedPredictions = true
	}
	if refreshedPredictions && j.opts.Jobs != nil {
		if err := enqueuePredictionGeneration(ctx, j.opts.Jobs, now()); err != nil {
			return err
		}
	}
	slog.Info("provider normalization completed",
		"provider", apifootball.ProviderName,
		"endpoint", "odds",
		"job_id", job.ID,
		"records_fetched", len(items),
		"snapshots_stored", snapshotsStored,
		"features_updated", featuresUpdated,
		"unmapped_fixtures", unmappedFixtures,
		"unavailable_markets", unavailableMarkets,
		"invalid_prices", invalidPrices,
	)
	return nil
}

func OddsJobKey() string {
	return ProviderJobKey(apifootball.ProviderName, "odds")
}

func extractOneXTwoOdds(item apifootball.OddsItem) ([]oddsPricePayload, string, string, bool) {
	for _, bookmaker := range item.Bookmakers {
		for _, bet := range bookmaker.Bets {
			if bet.ID != 1 && !strings.EqualFold(strings.TrimSpace(bet.Name), "Match Winner") {
				continue
			}
			prices := map[string]string{}
			for _, value := range bet.Values {
				switch strings.ToLower(strings.TrimSpace(value.Value)) {
				case "home":
					prices["Home"] = value.Odd
				case "draw":
					prices["Draw"] = value.Odd
				case "away":
					prices["Away"] = value.Odd
				}
			}
			if prices["Home"] == "" || prices["Draw"] == "" || prices["Away"] == "" {
				return nil, "", "", false
			}
			capturedAt := item.Update
			if capturedAt == "" {
				capturedAt = item.Fixture.Date
			}
			return []oddsPricePayload{
				{Label: "Home", Value: prices["Home"], Trend: "flat"},
				{Label: "Draw", Value: prices["Draw"], Trend: "flat"},
				{Label: "Away", Value: prices["Away"], Trend: "flat"},
			}, bookmaker.Name, capturedAt, true
		}
	}
	return nil, "", "", false
}

func parsePriceValue(value string) (float64, error) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || parsed <= 0 {
		return 0, errors.New("invalid odds value")
	}
	return parsed, nil
}

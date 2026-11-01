package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
)

// PlayersJobOptions wires the players-reference ingestion.
type PlayersJobOptions struct {
	Client      ProviderClient
	RequestLogs requestLogger
	EntityMaps  entityMapResolver
	Catalog     playerCatalogWriter
	RawPayloads rawPayloadWriter
	RateLimits  rateLimitWriter
	Jobs        referenceJobEnqueuer
	Now         func() time.Time
}

type playerCatalogWriter interface {
	UpsertPlayer(context.Context, PlayerRecord) (int64, error)
}

// PlayersJobKey is the per-team players backfill (players?team&season).
func PlayersJobKey() string {
	return ProviderJobKey(apifootball.ProviderName, "players")
}

// VenuesJobKey is the venues reference backfill (venues or venues?id).
func VenuesJobKey() string {
	return ProviderJobKey(apifootball.ProviderName, "venues")
}

type playersJobPayload struct {
	TeamID         int64  `json:"team_id"`
	ProviderTeamID string `json:"provider_team_id"`
	Season         int    `json:"season"`
	Page           int    `json:"page"`
}

type PlayersJob struct {
	opts PlayersJobOptions
}

func NewPlayersJob(opts PlayersJobOptions) PlayersJob {
	return PlayersJob{opts: opts}
}

func (j PlayersJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Client == nil {
		return errors.New("players job provider client is required")
	}
	if j.opts.RequestLogs == nil {
		return errors.New("players job request log repository is required")
	}
	if j.opts.Catalog == nil {
		return errors.New("players job catalog repository is required")
	}
	now := time.Now().UTC
	if j.opts.Now != nil {
		now = j.opts.Now
	}

	payload := playersJobPayload{}
	if len(job.Payload) > 0 && string(job.Payload) != "{}" {
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return err
		}
	}
	if payload.TeamID <= 0 || payload.ProviderTeamID == "" || payload.Season <= 0 {
		return errors.New("players team_id, provider_team_id, and season are required")
	}
	if payload.Page <= 0 {
		payload.Page = 1
	}

	params := url.Values{}
	params.Set("team", payload.ProviderTeamID)
	params.Set("season", strconv.Itoa(payload.Season))
	params.Set("page", strconv.Itoa(payload.Page))
	resp, err := j.opts.Client.Get(ctx, "players", params)
	paramsHash := hashParams(params)
	log := RequestLog{
		JobID:         job.ID,
		Provider:      nonEmpty(resp.Provider, apifootball.ProviderName),
		Endpoint:      nonEmpty(resp.Endpoint, "players"),
		Method:        "GET",
		ParamsHash:    paramsHash,
		StatusCode:    resp.StatusCode,
		QuotaCost:     providerQuotaCost(resp),
		Latency:       resp.Latency,
		ResponseBytes: resp.ResponseBytes,
		RateLimit:     resp.RateLimit,
		Reason:        "reference",
		Priority:      20,
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
			EndpointKey:   "players",
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

	players, err := apifootball.ParsePlayers(resp.Body)
	if err != nil {
		return err
	}
	normalized := 0
	for _, item := range players {
		teamID := payload.TeamID
		if j.opts.EntityMaps != nil {
			mapped, ok, err := j.opts.EntityMaps.LookupCanonicalEntityID(
				ctx, apifootball.ProviderName, "team", strconv.FormatInt(item.ProviderTeamID, 10))
			if err != nil {
				return err
			}
			if ok {
				if parsed, parseErr := strconv.ParseInt(mapped, 10, 64); parseErr == nil && parsed > 0 {
					teamID = parsed
				}
			}
		}
		if _, err := j.opts.Catalog.UpsertPlayer(ctx, PlayerRecord{
			ID:          item.ID,
			TeamID:      teamID,
			Name:        item.Name,
			Age:         item.Age,
			Number:      item.Number,
			Position:    item.Position,
			PhotoURL:    item.PhotoURL,
			Nationality: item.Nationality,
		}); err != nil {
			return err
		}
		normalized++
	}
	if j.opts.Jobs != nil {
		nextPage := payload.Page + 1
		if totalPages, ok := playerPagingTotal(resp.Body); ok && nextPage <= totalPages {
			followUp := playersJobPayload{
				TeamID:         payload.TeamID,
				ProviderTeamID: payload.ProviderTeamID,
				Season:         payload.Season,
				Page:           nextPage,
			}
			raw, err := json.Marshal(followUp)
			if err != nil {
				return err
			}
			if _, err := j.opts.Jobs.Enqueue(ctx, jobs.NewJob{
				JobKey:               PlayersJobKey(),
				Queue:                DefaultProviderQueue,
				Priority:             20,
				Payload:              raw,
				IdempotencyKey:       fmt.Sprintf("enqueue:players:%d:%d:page:%d", payload.TeamID, payload.Season, nextPage),
				DeduplicateCompleted: true,
				RunAfter:             now(),
			}); err != nil {
				return err
			}
		}
	}
	slog.Info("provider normalization completed",
		"provider", apifootball.ProviderName,
		"endpoint", "players",
		"job_id", job.ID,
		"team_id", payload.TeamID,
		"season", payload.Season,
		"page", payload.Page,
		"records_fetched", len(players),
		"records_normalized", normalized,
	)
	return nil
}

// playerPagingTotal returns the provider paging.total for a players response.
func playerPagingTotal(body []byte) (int, bool) {
	var envelope struct {
		Paging struct {
			Total int `json:"total"`
		} `json:"paging"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return 0, false
	}
	return envelope.Paging.Total, envelope.Paging.Total > 0
}

// VenuesJobOptions wires the venues-reference ingestion.
type VenuesJobOptions struct {
	Client      ProviderClient
	RequestLogs requestLogger
	Catalog     venueCatalogWriter
	RawPayloads rawPayloadWriter
	RateLimits  rateLimitWriter
	Now         func() time.Time
}

type venueCatalogWriter interface {
	UpsertVenue(context.Context, VenueRecord) (int64, error)
}

type venuesJobPayload struct {
	VenueID int64  `json:"venue_id"`
	Country string `json:"country"`
}

type VenuesJob struct {
	opts VenuesJobOptions
}

func NewVenuesJob(opts VenuesJobOptions) VenuesJob {
	return VenuesJob{opts: opts}
}

func (j VenuesJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Client == nil {
		return errors.New("venues job provider client is required")
	}
	if j.opts.RequestLogs == nil {
		return errors.New("venues job request log repository is required")
	}
	if j.opts.Catalog == nil {
		return errors.New("venues job catalog repository is required")
	}
	now := time.Now().UTC
	if j.opts.Now != nil {
		now = j.opts.Now
	}

	payload := venuesJobPayload{}
	if len(job.Payload) > 0 && string(job.Payload) != "{}" {
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return err
		}
	}

	params := url.Values{}
	if payload.VenueID > 0 {
		params.Set("id", strconv.FormatInt(payload.VenueID, 10))
	} else if strings.TrimSpace(payload.Country) != "" {
		params.Set("country", strings.TrimSpace(payload.Country))
	} else {
		return errors.New("venues job requires venue_id or country")
	}
	resp, err := j.opts.Client.Get(ctx, "venues", params)
	paramsHash := hashParams(params)
	log := RequestLog{
		JobID:         job.ID,
		Provider:      nonEmpty(resp.Provider, apifootball.ProviderName),
		Endpoint:      nonEmpty(resp.Endpoint, "venues"),
		Method:        "GET",
		ParamsHash:    paramsHash,
		StatusCode:    resp.StatusCode,
		QuotaCost:     providerQuotaCost(resp),
		Latency:       resp.Latency,
		ResponseBytes: resp.ResponseBytes,
		RateLimit:     resp.RateLimit,
		Reason:        "reference",
		Priority:      20,
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
			EndpointKey:   "venues",
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

	venues, err := apifootball.ParseVenues(resp.Body)
	if err != nil {
		return err
	}
	normalized := 0
	for _, item := range venues {
		if _, err := j.opts.Catalog.UpsertVenue(ctx, VenueRecord{
			ID:       item.ID,
			Name:     item.Name,
			Address:  item.Address,
			City:     item.City,
			Country:  item.Country,
			Capacity: item.Capacity,
			Surface:  item.Surface,
			ImageURL: item.ImageURL,
		}); err != nil {
			return err
		}
		normalized++
	}
	slog.Info("provider normalization completed",
		"provider", apifootball.ProviderName,
		"endpoint", "venues",
		"job_id", job.ID,
		"records_fetched", len(venues),
		"records_normalized", normalized,
	)
	return nil
}

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
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/footballdata"
)

type FootballDataCompetitionsJobOptions struct {
	Client      ProviderClient
	RequestLogs requestLogger
	EntityMaps  entityMapWriter
	RawPayloads rawPayloadWriter
	RateLimits  rateLimitWriter
	Catalog     catalogWriter
	Jobs        predictionJobEnqueuer
	Now         func() time.Time
}

type FootballDataCompetitionsJob struct {
	opts FootballDataCompetitionsJobOptions
}

func NewFootballDataCompetitionsJob(opts FootballDataCompetitionsJobOptions) FootballDataCompetitionsJob {
	return FootballDataCompetitionsJob{opts: opts}
}

func (j FootballDataCompetitionsJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Client == nil {
		return errors.New("football-data competitions job provider client is required")
	}
	if j.opts.RequestLogs == nil {
		return errors.New("football-data competitions job request log repository is required")
	}
	if j.opts.EntityMaps == nil {
		return errors.New("football-data competitions job entity map repository is required")
	}
	now := utcNow(j.opts.Now)

	resp, err := j.opts.Client.Get(ctx, "competitions", url.Values{})
	log := RequestLog{
		JobID:         job.ID,
		Provider:      nonEmpty(resp.Provider, footballdata.ProviderName),
		Endpoint:      nonEmpty(resp.Endpoint, "competitions"),
		Method:        "GET",
		StatusCode:    resp.StatusCode,
		QuotaCost:     providerQuotaCost(resp),
		Latency:       resp.Latency,
		ResponseBytes: resp.ResponseBytes,
		RateLimit:     resp.RateLimit,
		RequestedAt:   now,
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
	if err := persistProviderResponseMetadata(ctx, persistMetadataOptions{
		Provider:    footballdata.ProviderName,
		Endpoint:    "competitions",
		Response:    resp,
		ParamsHash:  "",
		RateLimits:  j.opts.RateLimits,
		RawPayloads: j.opts.RawPayloads,
		Now:         now,
	}); err != nil {
		return err
	}

	competitions, err := footballdata.ParseCompetitions(resp.Body)
	if err != nil {
		return err
	}
	childrenEnqueued := 0
	for _, item := range competitions {
		providerCompetitionID := strconv.Itoa(item.ID)
		canonicalLeagueID := ""
		if j.opts.Catalog != nil {
			leagueID, err := j.opts.Catalog.UpsertLeague(ctx, LeagueRecord{
				SportSlug:   "football",
				SportName:   "Football",
				CountryCode: "",
				CountryName: item.Area.Name,
				Slug:        canonicalLeagueSlug(item.Area.Name, item.Name),
				Name:        item.Name,
			})
			if err != nil {
				return err
			}
			canonicalLeagueID = strconv.FormatInt(leagueID, 10)
		}
		mapping := ProviderEntityMap{
			Provider:          footballdata.ProviderName,
			EntityType:        "league",
			ProviderEntityID:  providerCompetitionID,
			ProviderEntityKey: item.Name,
			CanonicalType:     "league",
			CanonicalID:       canonicalLeagueID,
			MatchStatus:       "resolved",
			Metadata: map[string]any{
				"name":             item.Name,
				"code":             item.Code,
				"type":             item.Type,
				"plan":             item.Plan,
				"area_name":        item.Area.Name,
				"area_code":        item.Area.Code,
				"current_matchday": item.CurrentSeason.CurrentMatchday,
				"data_source":      "football-data.org",
			},
			SeenAt: now,
		}
		if mapping.CanonicalID == "" {
			mapping.CanonicalType = ""
			mapping.MatchStatus = "unresolved"
		}
		if err := j.opts.EntityMaps.Upsert(ctx, mapping); err != nil {
			return err
		}
		if j.opts.Jobs != nil && strings.EqualFold(strings.TrimSpace(item.Plan), "TIER_ONE") {
			season, ok := footballDataSeasonStartYear(item.CurrentSeason.StartDate)
			if ok {
				competition := firstNonEmpty(item.Code, strconv.Itoa(item.ID))
				payload, err := json.Marshal(footballDataMatchesPayload{
					Competition: competition,
					Season:      season,
				})
				if err != nil {
					return err
				}
				_, err = j.opts.Jobs.Enqueue(ctx, jobs.NewJob{
					JobKey:               FootballDataMatchesJobKey(),
					Queue:                "provider-fixtures",
					Priority:             30,
					Payload:              payload,
					IdempotencyKey:       fmt.Sprintf("%s:matches:%s:%s:%s", footballdata.ProviderName, competition, season, now.Format("2006-01-02")),
					DeduplicateCompleted: true,
					RunAfter:             now,
				})
				if err != nil {
					return err
				}
				childrenEnqueued++
			}
		}
	}
	slog.Info("provider normalization completed",
		"provider", footballdata.ProviderName,
		"endpoint", "competitions",
		"job_id", job.ID,
		"records_fetched", len(competitions),
		"records_normalized", len(competitions),
		"child_jobs_enqueued", childrenEnqueued,
	)
	return nil
}

type FootballDataMatchesJobOptions struct {
	Client      ProviderClient
	RequestLogs requestLogger
	EntityMaps  entityMapResolver
	RawPayloads rawPayloadWriter
	RateLimits  rateLimitWriter
	Catalog     catalogWriter
	Jobs        predictionJobEnqueuer
	Now         func() time.Time
}

type FootballDataTeamsJobOptions struct {
	Client      ProviderClient
	RequestLogs requestLogger
	EntityMaps  entityMapWriter
	RawPayloads rawPayloadWriter
	RateLimits  rateLimitWriter
	Catalog     catalogWriter
	Now         func() time.Time
}

type FootballDataTeamsJob struct {
	opts FootballDataTeamsJobOptions
}

type footballDataTeamsPayload struct {
	Competition string `json:"competition"`
	Season      string `json:"season"`
}

func NewFootballDataTeamsJob(opts FootballDataTeamsJobOptions) FootballDataTeamsJob {
	return FootballDataTeamsJob{opts: opts}
}

func (j FootballDataTeamsJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Client == nil {
		return errors.New("football-data teams job provider client is required")
	}
	if j.opts.RequestLogs == nil {
		return errors.New("football-data teams job request log repository is required")
	}
	if j.opts.EntityMaps == nil {
		return errors.New("football-data teams job entity map repository is required")
	}
	if j.opts.Catalog == nil {
		return errors.New("football-data teams job catalog repository is required")
	}
	now := utcNow(j.opts.Now)
	payload := footballDataTeamsPayload{}
	if len(job.Payload) > 0 && string(job.Payload) != "{}" {
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return err
		}
	}
	competition := strings.Trim(strings.TrimSpace(payload.Competition), "/")
	if competition == "" {
		return errors.New("football-data teams job competition is required")
	}
	endpoint := "competitions/" + url.PathEscape(competition) + "/teams"
	params := url.Values{}
	if payload.Season != "" {
		params.Set("season", strings.TrimSpace(payload.Season))
	}
	resp, err := j.opts.Client.Get(ctx, endpoint, params)
	paramsHash := hashParams(params)
	log := RequestLog{
		JobID:         job.ID,
		Provider:      nonEmpty(resp.Provider, footballdata.ProviderName),
		Endpoint:      nonEmpty(resp.Endpoint, endpoint),
		Method:        "GET",
		ParamsHash:    paramsHash,
		StatusCode:    resp.StatusCode,
		QuotaCost:     providerQuotaCost(resp),
		Latency:       resp.Latency,
		ResponseBytes: resp.ResponseBytes,
		RateLimit:     resp.RateLimit,
		RequestedAt:   now,
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
	if err := persistProviderResponseMetadata(ctx, persistMetadataOptions{
		Provider:    footballdata.ProviderName,
		Endpoint:    "competition.teams",
		Response:    resp,
		ParamsHash:  paramsHash,
		RateLimits:  j.opts.RateLimits,
		RawPayloads: j.opts.RawPayloads,
		Now:         now,
	}); err != nil {
		return err
	}
	teams, err := footballdata.ParseTeams(resp.Body)
	if err != nil {
		return err
	}
	for _, team := range teams {
		teamID, err := j.opts.Catalog.UpsertTeam(ctx, TeamRecord{
			CountryCode: "",
			Slug:        canonicalTeamSlug(team.Area.Name, team.Name),
			Name:        team.Name,
		})
		if err != nil {
			return err
		}
		if err := j.opts.EntityMaps.Upsert(ctx, ProviderEntityMap{
			Provider:          footballdata.ProviderName,
			EntityType:        "team",
			ProviderEntityID:  strconv.Itoa(team.ID),
			ProviderEntityKey: team.Name,
			CanonicalType:     "team",
			CanonicalID:       strconv.FormatInt(teamID, 10),
			MatchStatus:       "resolved",
			Metadata: map[string]any{
				"name":        team.Name,
				"short_name":  team.ShortName,
				"tla":         team.TLA,
				"area_name":   team.Area.Name,
				"area_code":   team.Area.Code,
				"venue":       team.Venue,
				"data_source": "football-data.org",
			},
			SeenAt: now,
		}); err != nil {
			return err
		}
	}
	slog.Info("provider normalization completed",
		"provider", footballdata.ProviderName,
		"endpoint", "competition.teams",
		"job_id", job.ID,
		"records_fetched", len(teams),
		"records_normalized", len(teams),
	)
	return nil
}

type FootballDataMatchesJob struct {
	opts FootballDataMatchesJobOptions
}

type footballDataMatchesPayload struct {
	Competition string `json:"competition"`
	Date        string `json:"date"`
	DateFrom    string `json:"date_from"`
	DateTo      string `json:"date_to"`
	Status      string `json:"status"`
	Season      string `json:"season"`
	Matchday    int    `json:"matchday"`
}

func NewFootballDataMatchesJob(opts FootballDataMatchesJobOptions) FootballDataMatchesJob {
	return FootballDataMatchesJob{opts: opts}
}

func (j FootballDataMatchesJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Client == nil {
		return errors.New("football-data matches job provider client is required")
	}
	if j.opts.RequestLogs == nil {
		return errors.New("football-data matches job request log repository is required")
	}
	if j.opts.EntityMaps == nil {
		return errors.New("football-data matches job entity map repository is required")
	}
	if j.opts.Catalog == nil {
		return errors.New("football-data matches job catalog repository is required")
	}
	now := utcNow(j.opts.Now)

	payload := footballDataMatchesPayload{}
	if len(job.Payload) > 0 && string(job.Payload) != "{}" {
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return err
		}
	}
	endpoint, params := footballDataMatchesEndpointAndParams(payload, now)
	resp, err := j.opts.Client.Get(ctx, endpoint, params)
	paramsHash := hashParams(params)
	log := RequestLog{
		JobID:         job.ID,
		Provider:      nonEmpty(resp.Provider, footballdata.ProviderName),
		Endpoint:      nonEmpty(resp.Endpoint, endpoint),
		Method:        "GET",
		ParamsHash:    paramsHash,
		StatusCode:    resp.StatusCode,
		QuotaCost:     providerQuotaCost(resp),
		Latency:       resp.Latency,
		ResponseBytes: resp.ResponseBytes,
		RateLimit:     resp.RateLimit,
		RequestedAt:   now,
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
	if err := persistProviderResponseMetadata(ctx, persistMetadataOptions{
		Provider:    footballdata.ProviderName,
		Endpoint:    "matches",
		Response:    resp,
		ParamsHash:  paramsHash,
		RateLimits:  j.opts.RateLimits,
		RawPayloads: j.opts.RawPayloads,
		Now:         now,
	}); err != nil {
		return err
	}

	matches, err := footballdata.ParseMatches(resp.Body)
	if err != nil {
		return err
	}
	normalized := 0
	skipped := 0
	for _, match := range matches {
		if !match.HasCompleteTeams() {
			skipped++
			slog.Warn("provider record skipped",
				"provider", footballdata.ProviderName,
				"endpoint", endpoint,
				"job_id", job.ID,
				"provider_match_id", match.ID,
				"status", match.Status,
				"stage", match.Stage,
				"reason", "unresolved_participants",
			)
			continue
		}
		if err := j.upsertMatch(ctx, match, now); err != nil {
			return err
		}
		normalized++
	}
	if normalized > 0 {
		if err := enqueuePredictionGeneration(ctx, j.opts.Jobs, now); err != nil {
			return err
		}
	}
	slog.Info("provider normalization completed",
		"provider", footballdata.ProviderName,
		"endpoint", endpoint,
		"job_id", job.ID,
		"records_fetched", len(matches),
		"records_normalized", normalized,
		"records_skipped", skipped,
	)
	return nil
}

func (j FootballDataMatchesJob) upsertMatch(ctx context.Context, match footballdata.Match, now time.Time) error {
	kickoffAt, err := time.Parse(time.RFC3339, match.UTCDate)
	if err != nil {
		return fmt.Errorf("parse football-data match kickoff %d: %w", match.ID, err)
	}
	leagueID, err := j.opts.Catalog.UpsertLeague(ctx, LeagueRecord{
		SportSlug:   "football",
		SportName:   "Football",
		CountryName: firstNonEmpty(match.Area.Name, match.Competition.Area.Name),
		Slug:        canonicalLeagueSlug(firstNonEmpty(match.Area.Name, match.Competition.Area.Name), match.Competition.Name),
		Name:        match.Competition.Name,
	})
	if err != nil {
		return err
	}
	homeTeamID, err := j.opts.Catalog.UpsertTeam(ctx, TeamRecord{
		Slug: canonicalTeamSlug(firstNonEmpty(match.Area.Name, match.Competition.Area.Name), match.HomeTeam.Name),
		Name: match.HomeTeam.Name,
	})
	if err != nil {
		return err
	}
	awayTeamID, err := j.opts.Catalog.UpsertTeam(ctx, TeamRecord{
		Slug: canonicalTeamSlug(firstNonEmpty(match.Area.Name, match.Competition.Area.Name), match.AwayTeam.Name),
		Name: match.AwayTeam.Name,
	})
	if err != nil {
		return err
	}
	matchRecord := MatchRecord{
		Source:            footballdata.ProviderName,
		LeagueID:          leagueID,
		SeasonID:          footballDataSeasonYear(match.Season.StartDate, kickoffAt),
		HomeTeamID:        homeTeamID,
		AwayTeamID:        awayTeamID,
		KickoffAt:         kickoffAt,
		Status:            normalizeFootballDataMatchStatus(match.Status).Status,
		RawStatus:         match.Status,
		RawStatusLabel:    match.Status,
		StatusElapsed:     match.Minute,
		VenueName:         match.Venue,
		HomeScore:         match.Score.FullTime.Home,
		AwayScore:         match.Score.FullTime.Away,
		HalfTimeHomeScore: match.Score.HalfTime.Home,
		HalfTimeAwayScore: match.Score.HalfTime.Away,
	}
	providerMatchID := strconv.Itoa(match.ID)
	canonicalID, ok, err := j.opts.EntityMaps.LookupCanonicalEntityID(ctx, footballdata.ProviderName, "match", providerMatchID)
	if err != nil {
		return err
	}
	if ok {
		mappedMatchID, err := ParseCanonicalID(canonicalID)
		if err != nil {
			return err
		}
		matchID, err := j.opts.Catalog.ReconcileMatch(ctx, mappedMatchID, matchRecord)
		if err != nil {
			return err
		}
		canonicalID = strconv.FormatInt(matchID, 10)
	} else {
		matchID, err := j.opts.Catalog.InsertMatch(ctx, matchRecord)
		if err != nil {
			return err
		}
		canonicalID = strconv.FormatInt(matchID, 10)
	}
	return j.opts.EntityMaps.Upsert(ctx, ProviderEntityMap{
		Provider:          footballdata.ProviderName,
		EntityType:        "match",
		ProviderEntityID:  providerMatchID,
		ProviderEntityKey: strings.TrimSpace(match.HomeTeam.Name + " vs " + match.AwayTeam.Name),
		CanonicalType:     "match",
		CanonicalID:       canonicalID,
		MatchStatus:       "resolved",
		Metadata: map[string]any{
			"status":      match.Status,
			"competition": match.Competition.Code,
			"league_id":   match.Competition.ID,
			"matchday":    match.Matchday,
			"stage":       match.Stage,
			"data_source": "football-data.org",
		},
		SeenAt: now,
	})
}

func footballDataSeasonYear(startDate string, kickoffAt time.Time) int {
	if parsed, err := time.Parse("2006-01-02", strings.TrimSpace(startDate)); err == nil {
		return parsed.Year()
	}
	return kickoffAt.UTC().Year()
}

func footballDataSeasonStartYear(startDate string) (string, bool) {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(startDate))
	if err != nil {
		return "", false
	}
	return strconv.Itoa(parsed.Year()), true
}

func footballDataMatchesEndpointAndParams(payload footballDataMatchesPayload, now time.Time) (string, url.Values) {
	params := url.Values{}
	competition := strings.Trim(strings.TrimSpace(payload.Competition), "/")
	endpoint := "matches"
	if competition != "" {
		endpoint = "competitions/" + url.PathEscape(competition) + "/matches"
	}
	if payload.Date != "" {
		params.Set("date", payload.Date)
	} else if payload.DateFrom != "" || payload.DateTo != "" {
		if payload.DateFrom != "" {
			params.Set("dateFrom", payload.DateFrom)
		}
		if payload.DateTo != "" {
			params.Set("dateTo", payload.DateTo)
		}
	} else if competition == "" {
		params.Set("date", now.Format("2006-01-02"))
	}
	if payload.Status != "" {
		params.Set("status", strings.ToUpper(strings.TrimSpace(payload.Status)))
	}
	if payload.Season != "" {
		params.Set("season", strings.TrimSpace(payload.Season))
	}
	if payload.Matchday > 0 {
		params.Set("matchday", strconv.Itoa(payload.Matchday))
	}
	return endpoint, params
}

func normalizeFootballDataMatchStatus(status string) normalizedFixtureStatus {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "SCHEDULED", "TIMED":
		return normalizedFixtureStatus{Status: "upcoming"}
	case "IN_PLAY":
		return normalizedFixtureStatus{Status: "live"}
	case "PAUSED":
		return normalizedFixtureStatus{Status: "halftime"}
	case "FINISHED":
		return normalizedFixtureStatus{Status: "finished"}
	case "POSTPONED":
		return normalizedFixtureStatus{Status: "postponed"}
	case "CANCELLED":
		return normalizedFixtureStatus{Status: "canceled"}
	case "SUSPENDED":
		return normalizedFixtureStatus{Status: "suspended"}
	case "AWARDED":
		return normalizedFixtureStatus{Status: "awarded"}
	default:
		return normalizedFixtureStatus{Status: "upcoming"}
	}
}

func FootballDataCompetitionsJobKey() string {
	return ProviderJobKey(footballdata.ProviderName, "competitions")
}

func FootballDataMatchesJobKey() string {
	return ProviderJobKey(footballdata.ProviderName, "matches")
}

func FootballDataTeamsJobKey() string {
	return ProviderJobKey(footballdata.ProviderName, "teams")
}

type persistMetadataOptions struct {
	Provider    string
	Endpoint    string
	Response    provider.Response
	ParamsHash  string
	RateLimits  rateLimitWriter
	RawPayloads rawPayloadWriter
	Now         time.Time
}

func persistProviderResponseMetadata(ctx context.Context, opts persistMetadataOptions) error {
	if opts.RateLimits != nil {
		if err := opts.RateLimits.UpdateFromHeaders(ctx, ProviderRateLimitUpdate{
			Provider:        opts.Provider,
			DailyLimit:      opts.Response.RateLimit.DailyLimit,
			DailyRemaining:  opts.Response.RateLimit.DailyRemaining,
			MinuteLimit:     opts.Response.RateLimit.MinuteLimit,
			MinuteRemaining: opts.Response.RateLimit.MinuteRemaining,
			UpdatedAt:       opts.Now,
		}); err != nil {
			return err
		}
	}
	if opts.RawPayloads != nil {
		if err := opts.RawPayloads.InsertMetadata(ctx, RawPayloadMetadata{
			Provider:      opts.Provider,
			EndpointKey:   opts.Endpoint,
			ParamsHash:    opts.ParamsHash,
			StatusCode:    opts.Response.StatusCode,
			ResponseHash:  ResponseHash(opts.Response.Body),
			ResponseBytes: opts.Response.ResponseBytes,
			Headers:       opts.Response.RateLimit.Raw,
			StoragePolicy: "metadata_only",
			FetchedAt:     opts.Now,
		}); err != nil {
			return err
		}
	}
	return nil
}

func utcNow(now func() time.Time) time.Time {
	if now == nil {
		now = time.Now
	}
	return now().UTC()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

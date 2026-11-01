package ingestion

import (
	"context"
	"database/sql"
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

// ReferenceSyncRepository persists display attributes (flags/logos) on the
// canonical catalogue tables. Reference entities are low-churn: these methods
// only write when a non-empty value arrives, so partial feeds never blank out
// existing logos.
type ReferenceSyncRepository struct {
	db referenceSyncDB
}

type referenceSyncDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func NewReferenceSyncRepository(db referenceSyncDB) ReferenceSyncRepository {
	return ReferenceSyncRepository{db: db}
}

// ApplyCountryFlag stores the flag URL on the canonical country row.
func (r ReferenceSyncRepository) ApplyCountryFlag(ctx context.Context, code, flagURL string) error {
	if r.db == nil {
		return errors.New("reference sync DB is nil")
	}
	if code = strings.ToUpper(strings.TrimSpace(code)); len(code) != 2 {
		return errors.New("country code is required")
	}
	flagURL = strings.TrimSpace(flagURL)
	if flagURL == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `
UPDATE countries SET flag_url = $2 WHERE code = $1`, code, flagURL)
	return err
}

// ApplyTeamAttributes stores the logo and country on the canonical team row.
func (r ReferenceSyncRepository) ApplyTeamAttributes(ctx context.Context, teamID int64, countryCode, logoURL string) error {
	if r.db == nil {
		return errors.New("reference sync DB is nil")
	}
	if teamID <= 0 {
		return errors.New("team id is required")
	}
	countryCode = strings.ToUpper(strings.TrimSpace(countryCode))
	if len(countryCode) != 2 {
		countryCode = ""
	}
	logoURL = strings.TrimSpace(logoURL)
	if countryCode == "" && logoURL == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `
UPDATE teams
SET country_code = COALESCE(NULLIF($2, ''), country_code),
    logo_url = COALESCE(NULLIF($3, ''), logo_url)
WHERE id = $1`, teamID, countryCode, logoURL)
	return err
}

// ApplyLeagueLogo stores the logo on the canonical league row.
func (r ReferenceSyncRepository) ApplyLeagueLogo(ctx context.Context, leagueID int64, logoURL string) error {
	if r.db == nil {
		return errors.New("reference sync DB is nil")
	}
	if leagueID <= 0 {
		return errors.New("league id is required")
	}
	logoURL = strings.TrimSpace(logoURL)
	if logoURL == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `
UPDATE leagues SET logo_url = $2 WHERE id = $1`, leagueID, logoURL)
	return err
}

// ActiveLeagueSelection is one canonical league with its provider league id and
// season, selected for reference backfill (teams/logos).
type ActiveLeagueSelection struct {
	CanonicalLeagueID int64
	ProviderLeagueID  string
	Season            int
	LogoURL           string
}

// SelectActiveLeagues returns leagues with upcoming/live fixtures plus their
// API-Football identity and logo from the entity map metadata.
func (r ReferenceSyncRepository) SelectActiveLeagues(ctx context.Context, provider string, horizonHours int, limit int) ([]ActiveLeagueSelection, error) {
	if r.db == nil {
		return nil, errors.New("reference sync DB is nil")
	}
	if horizonHours <= 0 {
		horizonHours = 24 * 7
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT
  l.id,
  pem.provider_entity_id,
  COALESCE((pem.metadata->>'season')::int, date_part('year', now())::int, 0),
  COALESCE(pem.metadata->>'logo', '')
FROM matches m
JOIN leagues l ON l.id = m.league_id
JOIN provider_entity_maps pem
  ON pem.provider = $1
 AND pem.entity_type = 'league'
 AND pem.canonical_entity_type = 'league'
 AND pem.canonical_entity_id = l.id::text
 AND pem.retired_at IS NULL
WHERE m.kickoff_at > now()
  AND m.kickoff_at <= now() + ($2 * interval '1 hour')
  AND lower(COALESCE(m.status, 'scheduled')) NOT IN ('finished','ft','aet','pen','canceled','cancelled','abandoned')
  -- Crests and club names do not change, so a league whose teams are already
  -- populated has nothing left to fetch. Without this the job spent one
  -- provider request per active league per day re-reading settled reference
  -- data forever. The TheSportsDB sibling has always gated this way.
  AND EXISTS (
    SELECT 1
    FROM matches lm
    JOIN teams lt ON lt.id IN (lm.home_team_id, lm.away_team_id)
    WHERE lm.league_id = l.id
      AND COALESCE(lt.logo_url, '') = ''
  )
GROUP BY l.id, pem.provider_entity_id, pem.metadata
ORDER BY MAX(m.kickoff_at) ASC
LIMIT $3`, provider, horizonHours, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	selections := make([]ActiveLeagueSelection, 0, 16)
	for rows.Next() {
		var item ActiveLeagueSelection
		if err := rows.Scan(&item.CanonicalLeagueID, &item.ProviderLeagueID, &item.Season, &item.LogoURL); err != nil {
			return nil, err
		}
		selections = append(selections, item)
	}
	return selections, rows.Err()
}

// TeamsReferenceJobOptions wires the teams-reference ingestion.
type TeamsReferenceJobOptions struct {
	Client      ProviderClient
	RequestLogs requestLogger
	EntityMaps  entityMapResolver
	RawPayloads rawPayloadWriter
	RateLimits  rateLimitWriter
	Catalog     teamReferenceCatalogWriter
	Reference   ReferenceSyncRepository
	Jobs        referenceJobEnqueuer
	Now         func() time.Time
}

type teamReferenceCatalogWriter interface {
	UpsertTeam(context.Context, TeamRecord) (int64, error)
}

type referenceJobEnqueuer interface {
	Enqueue(context.Context, jobs.NewJob) (int64, error)
}

// TeamsReferenceJobKey is the per-league teams backfill (teams?league&season).
func TeamsReferenceJobKey() string {
	return ProviderJobKey(apifootball.ProviderName, "teams")
}

// ReferenceSyncJobKey is the daily batch planner that enqueues per-league
// teams-reference jobs and applies league logos from stored metadata.
func ReferenceSyncJobKey() string {
	return ProviderJobKey(apifootball.ProviderName, "reference-sync")
}

type teamsReferenceJobPayload struct {
	LeagueID int `json:"league_id"`
	Season   int `json:"season"`
}

type TeamsReferenceJob struct {
	opts TeamsReferenceJobOptions
}

func NewTeamsReferenceJob(opts TeamsReferenceJobOptions) TeamsReferenceJob {
	return TeamsReferenceJob{opts: opts}
}

func (j TeamsReferenceJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Client == nil {
		return errors.New("teams reference job provider client is required")
	}
	if j.opts.RequestLogs == nil {
		return errors.New("teams reference job request log repository is required")
	}
	if j.opts.Reference.db == nil {
		return errors.New("teams reference job reference repository is required")
	}
	now := time.Now().UTC
	if j.opts.Now != nil {
		now = j.opts.Now
	}

	payload := teamsReferenceJobPayload{}
	if len(job.Payload) > 0 && string(job.Payload) != "{}" {
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return err
		}
	}
	if payload.LeagueID <= 0 || payload.Season <= 0 {
		return errors.New("teams reference league_id and season are required")
	}

	params := url.Values{}
	params.Set("league", strconv.Itoa(payload.LeagueID))
	params.Set("season", strconv.Itoa(payload.Season))
	resp, err := j.opts.Client.Get(ctx, "teams", params)
	paramsHash := hashParams(params)
	log := RequestLog{
		JobID:         job.ID,
		Provider:      nonEmpty(resp.Provider, apifootball.ProviderName),
		Endpoint:      nonEmpty(resp.Endpoint, "teams"),
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
			EndpointKey:   "teams",
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

	teams, err := apifootball.ParseTeams(resp.Body)
	if err != nil {
		return err
	}
	normalized := 0
	for _, item := range teams {
		// Reference enrichment must retain the identity fixtures already use.
		// A country changing from World to Wales changes the fallback slug,
		// but does not make the provider's team a different canonical team.
		canonicalID, found, err := j.opts.EntityMaps.LookupCanonicalEntityID(ctx, apifootball.ProviderName, "team", strconv.Itoa(item.Team.ID))
		if err != nil {
			return err
		}
		var teamID int64
		if found {
			teamID, err = strconv.ParseInt(canonicalID, 10, 64)
			if err != nil || teamID <= 0 {
				return fmt.Errorf("invalid canonical team mapping for provider team %d", item.Team.ID)
			}
		} else {
			teamID, err = j.opts.Catalog.UpsertTeam(ctx, TeamRecord{
				CountryCode: item.Team.Country,
				Slug:        canonicalTeamSlug(item.Team.Country, item.Team.Name),
				Name:        item.Team.Name,
			})
			if err != nil {
				return err
			}
		}
		if err := j.opts.Reference.ApplyTeamAttributes(ctx, teamID, item.Team.Country, item.Team.Logo); err != nil {
			return err
		}
		if err := j.opts.EntityMaps.Upsert(ctx, ProviderEntityMap{
			Provider:          apifootball.ProviderName,
			EntityType:        "team",
			ProviderEntityID:  strconv.Itoa(item.Team.ID),
			ProviderEntityKey: item.Team.Name,
			CanonicalType:     "team",
			CanonicalID:       strconv.FormatInt(teamID, 10),
			MatchStatus:       "resolved",
			Metadata: map[string]any{
				"name":    item.Team.Name,
				"code":    item.Team.Code,
				"country": item.Team.Country,
				"logo":    item.Team.Logo,
				"founded": item.Team.Founded,
			},
			SeenAt: now(),
		}); err != nil {
			return err
		}
		normalized++
	}
	slog.Info("provider normalization completed",
		"provider", apifootball.ProviderName,
		"endpoint", "teams",
		"job_id", job.ID,
		"league_id", payload.LeagueID,
		"season", payload.Season,
		"records_fetched", len(teams),
		"records_normalized", normalized,
	)
	return nil
}

// ReferenceSyncJob is the daily batch planner: it applies league logos from
// stored entity-map metadata (no provider call) and enqueues one per-league
// teams-reference fetch job each (quota-governed by the client).
type ReferenceSyncJob struct {
	opts TeamsReferenceJobOptions
}

func NewReferenceSyncJob(opts TeamsReferenceJobOptions) ReferenceSyncJob {
	return ReferenceSyncJob{opts: opts}
}

func (j ReferenceSyncJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Reference.db == nil {
		return errors.New("reference sync reference repository is required")
	}
	if j.opts.Jobs == nil {
		return errors.New("reference sync job enqueuer is required")
	}
	now := time.Now().UTC
	if j.opts.Now != nil {
		now = j.opts.Now
	}
	selections, err := j.opts.Reference.SelectActiveLeagues(ctx, apifootball.ProviderName, 24*7, 100)
	if err != nil {
		return err
	}
	enqueued := 0
	appliedLogos := 0
	for _, selection := range selections {
		if strings.TrimSpace(selection.LogoURL) != "" {
			if err := j.opts.Reference.ApplyLeagueLogo(ctx, selection.CanonicalLeagueID, selection.LogoURL); err != nil {
				return err
			}
			appliedLogos++
		}
		if selection.ProviderLeagueID == "" || selection.Season <= 0 {
			continue
		}
		payload, err := json.Marshal(teamsReferenceJobPayload{LeagueID: parseIntOrZero(selection.ProviderLeagueID), Season: selection.Season})
		if err != nil {
			return err
		}
		_, err = j.opts.Jobs.Enqueue(ctx, jobs.NewJob{
			JobKey:               TeamsReferenceJobKey(),
			Queue:                DefaultProviderQueue,
			Priority:             25,
			Payload:              payload,
			IdempotencyKey:       "provider:api-football:teams:" + selection.ProviderLeagueID + ":" + strconv.Itoa(selection.Season) + ":" + now().Format("2006-01-02"),
			DeduplicateCompleted: false,
			RunAfter:             now().UTC(),
		})
		if err != nil {
			return err
		}
		enqueued++
	}
	slog.Info("reference catalogue sync planned",
		"job_id", job.ID, "job_key", job.JobKey,
		"active_leagues", len(selections), "teams_jobs_enqueued", enqueued, "league_logos_applied", appliedLogos)
	return nil
}

package ingestion

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/thesportsdb"
)

const (
	defaultTheSportsDBMaxPerCycle = 20
	maxTheSportsDBMaxPerCycle     = 100
)

// TheSportsDBReferenceSyncJobKey is the daily logo backfill for canonical teams
// and leagues that lack a logo, using TheSportsDB's free-tier image URLs.
func TheSportsDBReferenceSyncJobKey() string {
	return ProviderJobKey(thesportsdb.ProviderName, "reference-sync")
}

// TheSportsDBReferenceSyncJobOptions wires the TheSportsDB logo backfill.
type TheSportsDBReferenceSyncJobOptions struct {
	Client      ProviderClient
	RequestLogs requestLogger
	Reference   ReferenceSyncRepository
	// Selector returns canonical teams/leagues missing logos and gates the
	// daily all_leagues.php fetch.
	Selector    theSportsDBSelector
	MaxPerCycle int
	Now         func() time.Time
}

// theSportsDBSelector is the repository surface the logo backfill needs.
type theSportsDBSelector interface {
	SelectTeamsWithoutLogos(context.Context, int) ([]TheSportsDBTeamSelection, error)
	SelectLeaguesWithoutLogos(context.Context, int) ([]TheSportsDBLeagueSelection, error)
	AllLeaguesFetchedToday(context.Context, time.Time) (bool, error)
}

// theSportsDBQueryer is the query surface of the PostgreSQL implementation.
type theSportsDBQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// TheSportsDBTeamSelection is a canonical team missing a logo.
type TheSportsDBTeamSelection struct {
	ID      int64
	Name    string
	Country string
}

// TheSportsDBLeagueSelection is a canonical league missing a logo.
type TheSportsDBLeagueSelection struct {
	ID   int64
	Name string
}

// theSportsDBRepository implements theSportsDBSelector against PostgreSQL.
type theSportsDBRepository struct {
	db theSportsDBQueryer
}

// NewTheSportsDBRepository builds the TheSportsDB logo-backfill selector.
func NewTheSportsDBRepository(db theSportsDBQueryer) theSportsDBRepository {
	return theSportsDBRepository{db: db}
}

// SelectTeamsWithoutLogos returns canonical teams with no logo, oldest first,
// capped at limit.
func (r theSportsDBRepository) SelectTeamsWithoutLogos(ctx context.Context, limit int) ([]TheSportsDBTeamSelection, error) {
	if r.db == nil {
		return nil, errors.New("thesportsdb selector DB is nil")
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, COALESCE(country_code, '')
FROM teams
WHERE logo_url IS NULL OR logo_url = ''
ORDER BY id
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	selections := make([]TheSportsDBTeamSelection, 0, limit)
	for rows.Next() {
		var selection TheSportsDBTeamSelection
		if err := rows.Scan(&selection.ID, &selection.Name, &selection.Country); err != nil {
			return nil, err
		}
		selections = append(selections, selection)
	}
	return selections, rows.Err()
}

// SelectLeaguesWithoutLogos returns canonical leagues with no logo, oldest
// first, capped at limit.
func (r theSportsDBRepository) SelectLeaguesWithoutLogos(ctx context.Context, limit int) ([]TheSportsDBLeagueSelection, error) {
	if r.db == nil {
		return nil, errors.New("thesportsdb selector DB is nil")
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name
FROM leagues
WHERE logo_url IS NULL OR logo_url = ''
ORDER BY id
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	selections := make([]TheSportsDBLeagueSelection, 0, limit)
	for rows.Next() {
		var selection TheSportsDBLeagueSelection
		if err := rows.Scan(&selection.ID, &selection.Name); err != nil {
			return nil, err
		}
		selections = append(selections, selection)
	}
	return selections, rows.Err()
}

// AllLeaguesFetchedToday reports whether a successful all_leagues.php fetch was
// already recorded for the current UTC day, so the daily fetch runs at most
// once per day even if the job is re-run manually.
func (r theSportsDBRepository) AllLeaguesFetchedToday(ctx context.Context, now time.Time) (bool, error) {
	if r.db == nil {
		return false, errors.New("thesportsdb selector DB is nil")
	}
	dayStart := now.UTC().Truncate(24 * time.Hour)
	rows, err := r.db.QueryContext(ctx, `
SELECT 1
FROM provider_request_logs
WHERE provider = $1
  AND endpoint = 'all_leagues.php'
  AND status_code >= 200
  AND status_code < 300
  AND requested_at >= $2
LIMIT 1`, thesportsdb.ProviderName, dayStart)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), rows.Err()
}

// TheSportsDBReferenceSyncJob is a self-contained batch logo backfill: it picks
// a bounded set of canonical teams lacking logos, resolves each via
// searchteams.php, and applies the best matching logo; it then runs the daily
// all_leagues.php fetch (once per day) and applies logos to canonical leagues
// lacking one. It runs directly rather than enqueueing sub-jobs, keeping the
// free tier's footprint small and polite.
type TheSportsDBReferenceSyncJob struct {
	opts TheSportsDBReferenceSyncJobOptions
}

// NewTheSportsDBReferenceSyncJob builds the TheSportsDB logo backfill job.
func NewTheSportsDBReferenceSyncJob(opts TheSportsDBReferenceSyncJobOptions) TheSportsDBReferenceSyncJob {
	return TheSportsDBReferenceSyncJob{opts: opts}
}

// Run applies TheSportsDB logos to canonical teams and leagues missing one.
// All provider requests go through the client (reason "reference", priority
// 20) and are recorded in provider_request_logs; single-team lookup failures
// are logged and skipped so one bad hit cannot abort the whole batch.
func (j TheSportsDBReferenceSyncJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Client == nil {
		return errors.New("thesportsdb reference sync job provider client is required")
	}
	if j.opts.RequestLogs == nil {
		return errors.New("thesportsdb reference sync job request log repository is required")
	}
	if j.opts.Selector == nil {
		return errors.New("thesportsdb reference sync job selector is required")
	}
	if j.opts.Reference.db == nil {
		return errors.New("thesportsdb reference sync job reference repository is required")
	}
	now := utcNow(j.opts.Now)
	maxPerCycle := normalizeTheSportsDBMaxPerCycle(j.opts.MaxPerCycle)

	teams, err := j.opts.Selector.SelectTeamsWithoutLogos(ctx, maxPerCycle)
	if err != nil {
		return err
	}
	teamLogosApplied := 0
	for _, team := range teams {
		logo, err := j.lookupTeamLogo(ctx, job, now, team)
		if err != nil {
			return err
		}
		if logo == "" {
			continue
		}
		if err := j.opts.Reference.ApplyTeamAttributes(ctx, team.ID, team.Country, logo); err != nil {
			return err
		}
		teamLogosApplied++
	}

	leaguesChecked, leagueLogosApplied, err := j.applyLeagueLogosOncePerDay(ctx, job, now, maxPerCycle)
	if err != nil {
		return err
	}

	slog.Info("thesportsdb reference logo backfill completed",
		"provider", thesportsdb.ProviderName,
		"job_id", job.ID,
		"job_key", job.JobKey,
		"teams_checked", len(teams),
		"logos_applied", teamLogosApplied+leagueLogosApplied,
		"leagues_checked", leaguesChecked,
	)
	return nil
}

// lookupTeamLogo resolves one canonical team via searchteams.php and returns
// the best matching logo URL ("" when nothing usable came back).
func (j TheSportsDBReferenceSyncJob) lookupTeamLogo(ctx context.Context, job jobs.Job, now time.Time, team TheSportsDBTeamSelection) (string, error) {
	params := url.Values{}
	params.Set("t", team.Name)
	resp, err := j.opts.Client.Get(ctx, "searchteams.php", params)
	log := RequestLog{
		JobID:         job.ID,
		Provider:      nonEmpty(resp.Provider, thesportsdb.ProviderName),
		Endpoint:      nonEmpty(resp.Endpoint, "searchteams.php"),
		Method:        "GET",
		ParamsHash:    hashParams(params),
		StatusCode:    resp.StatusCode,
		QuotaCost:     providerQuotaCost(resp),
		Latency:       resp.Latency,
		ResponseBytes: resp.ResponseBytes,
		Reason:        "reference",
		Priority:      20,
		RequestedAt:   now,
	}
	if err != nil {
		log.ErrorCategory = categorizeProviderError(resp, err)
		if logErr := j.opts.RequestLogs.Insert(ctx, log); logErr != nil {
			return "", logErr
		}
		slog.Warn("thesportsdb team search failed",
			"provider", thesportsdb.ProviderName,
			"job_id", job.ID,
			"team_id", team.ID,
			"error", err,
		)
		return "", nil
	}
	if err := j.opts.RequestLogs.Insert(ctx, log); err != nil {
		return "", err
	}
	items, parseErr := thesportsdb.ParseTeams(resp.Body)
	if parseErr != nil {
		slog.Warn("thesportsdb team search parse failed",
			"provider", thesportsdb.ProviderName,
			"job_id", job.ID,
			"team_id", team.ID,
			"error", parseErr,
		)
		return "", nil
	}
	return bestTeamLogo(team.Name, items), nil
}

// applyLeagueLogosOncePerDay runs the daily all_leagues.php fetch — gated so a
// manual re-run the same day does not hit the endpoint again — and applies
// logos to canonical leagues missing one (bounded by maxPerCycle).
func (j TheSportsDBReferenceSyncJob) applyLeagueLogosOncePerDay(ctx context.Context, job jobs.Job, now time.Time, maxPerCycle int) (checked int, applied int, err error) {
	fetchedToday, err := j.opts.Selector.AllLeaguesFetchedToday(ctx, now)
	if err != nil {
		return 0, 0, err
	}
	if fetchedToday {
		return 0, 0, nil
	}
	resp, err := j.opts.Client.Get(ctx, "all_leagues.php", url.Values{})
	log := RequestLog{
		JobID:         job.ID,
		Provider:      nonEmpty(resp.Provider, thesportsdb.ProviderName),
		Endpoint:      nonEmpty(resp.Endpoint, "all_leagues.php"),
		Method:        "GET",
		StatusCode:    resp.StatusCode,
		QuotaCost:     providerQuotaCost(resp),
		Latency:       resp.Latency,
		ResponseBytes: resp.ResponseBytes,
		Reason:        "reference",
		Priority:      20,
		RequestedAt:   now,
	}
	if err != nil {
		log.ErrorCategory = categorizeProviderError(resp, err)
		if logErr := j.opts.RequestLogs.Insert(ctx, log); logErr != nil {
			return 0, 0, logErr
		}
		return 0, 0, err
	}
	if err := j.opts.RequestLogs.Insert(ctx, log); err != nil {
		return 0, 0, err
	}
	items, parseErr := thesportsdb.ParseLeagues(resp.Body)
	if parseErr != nil {
		slog.Warn("thesportsdb all_leagues parse failed",
			"provider", thesportsdb.ProviderName,
			"job_id", job.ID,
			"error", parseErr,
		)
		return 0, 0, nil
	}
	selections, err := j.opts.Selector.SelectLeaguesWithoutLogos(ctx, maxPerCycle)
	if err != nil {
		return 0, 0, err
	}
	for _, selection := range selections {
		logo := matchLeagueLogo(selection.Name, items)
		if logo == "" {
			continue
		}
		if err := j.opts.Reference.ApplyLeagueLogo(ctx, selection.ID, logo); err != nil {
			return len(selections), applied, err
		}
		applied++
	}
	return len(selections), applied, nil
}

func normalizeTheSportsDBMaxPerCycle(value int) int {
	if value <= 0 {
		return defaultTheSportsDBMaxPerCycle
	}
	if value > maxTheSportsDBMaxPerCycle {
		return maxTheSportsDBMaxPerCycle
	}
	return value
}

// bestTeamLogo picks the logo for the canonical team: an exact normalized name
// match first (strTeamLogo preferred over strTeamBadge), then a lone search
// result as a defensive fallback. Ambiguous multi-result searches without an
// exact match yield no logo rather than risking another team's crest.
func bestTeamLogo(canonicalName string, items []thesportsdb.TeamItem) string {
	canonical := canonicalNameSlug(canonicalName, true)
	for _, item := range items {
		if canonicalNameSlug(item.Name, true) == canonical {
			if logo := preferredLogo(item.Logo, item.Badge); logo != "" {
				return logo
			}
		}
	}
	if len(items) == 1 {
		return preferredLogo(items[0].Logo, items[0].Badge)
	}
	return ""
}

// matchLeagueLogo matches a canonical league against the daily all_leagues
// list. Soccer entries are preferred; an exact normalized name match is
// required so another sport's league never gets applied.
func matchLeagueLogo(canonicalName string, items []thesportsdb.LeagueItem) string {
	canonical := canonicalNameSlug(canonicalName, false)
	for _, item := range items {
		if canonicalNameSlug(item.Name, false) == canonical && isSoccerLeague(item.Sport) {
			if logo := preferredLogo(item.Logo, item.Badge); logo != "" {
				return logo
			}
		}
	}
	for _, item := range items {
		if canonicalNameSlug(item.Name, false) == canonical {
			if logo := preferredLogo(item.Logo, item.Badge); logo != "" {
				return logo
			}
		}
	}
	return ""
}

func isSoccerLeague(sport string) bool {
	switch strings.ToLower(strings.TrimSpace(sport)) {
	case "soccer", "football":
		return true
	default:
		return false
	}
}

func preferredLogo(logo, badge string) string {
	logo = strings.TrimSpace(logo)
	if logo != "" {
		return logo
	}
	return strings.TrimSpace(badge)
}

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
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/thesportsdb"
)

func TheSportsDBTeamHistoryJobKey() string {
	return ProviderJobKey(thesportsdb.ProviderName, "team-history")
}

type TheSportsDBTeamHistoryJobOptions struct {
	Client      ProviderClient
	RequestLogs requestLogger
	Catalog     CatalogRepository
	EntityMaps  ProviderEntityMapRepository
	Now         func() time.Time
}

type TheSportsDBTeamHistoryJob struct {
	opts TheSportsDBTeamHistoryJobOptions
}

func NewTheSportsDBTeamHistoryJob(opts TheSportsDBTeamHistoryJobOptions) TheSportsDBTeamHistoryJob {
	return TheSportsDBTeamHistoryJob{opts: opts}
}

type theSportsDBTeamHistoryPayload struct {
	TeamID        int64  `json:"team_id"`
	TeamName      string `json:"team_name"`
	CompetitionID int64  `json:"competition_id"`
	MatchesNeeded int    `json:"matches_needed"`
}

func (j TheSportsDBTeamHistoryJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Client == nil || j.opts.RequestLogs == nil || j.opts.Catalog.db == nil || j.opts.EntityMaps.db == nil {
		return errors.New("thesportsdb team history dependencies are required")
	}
	var payload theSportsDBTeamHistoryPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return fmt.Errorf("decode thesportsdb team history payload: %w", err)
	}
	if payload.TeamID <= 0 || payload.CompetitionID <= 0 || strings.TrimSpace(payload.TeamName) == "" {
		return errors.New("thesportsdb team history requires team_id, team_name, and competition_id")
	}
	now := utcNow(j.opts.Now)
	teams, err := j.fetchTeams(ctx, job, now, payload.TeamName)
	if err != nil || len(teams) == 0 {
		return err
	}
	providerTeam, ok := bestTheSportsDBTeam(payload.TeamName, teams)
	if !ok {
		slog.Info("thesportsdb team history skipped: unresolved team mapping recorded",
			"provider", thesportsdb.ProviderName,
			"job_id", job.ID,
			"canonical_team_id", payload.TeamID,
			"team_name", payload.TeamName,
			"candidates", len(teams),
			"reason", "no_exact_name_match",
		)
		if mapErr := j.opts.EntityMaps.Upsert(ctx, ProviderEntityMap{
			Provider:          thesportsdb.ProviderName,
			EntityType:        "team",
			ProviderEntityID:  "unresolved:" + strconv.FormatInt(payload.TeamID, 10),
			ProviderEntityKey: payload.TeamName,
			CanonicalType:     "team",
			CanonicalID:       strconv.FormatInt(payload.TeamID, 10),
			MatchStatus:       "unresolved",
			SeenAt:            now,
			Metadata: map[string]any{
				"reason":     "no_exact_name_match",
				"candidates": len(teams),
				"terminal":   true,
			},
		}); mapErr != nil {
			return mapErr
		}
		// The provider returned a valid response, but none of its candidates can
		// be attributed safely. Retrying the identical lookup cannot change that
		// result and previously turned an expected coverage gap into six worker
		// errors plus repeated provider spend. The unresolved map is the durable
		// terminal record; the coordinator can continue to the next provider or
		// mark this history request confirmed unavailable.
		return nil
	}
	events, err := j.fetchEvents(ctx, job, now, providerTeam.ID)
	if err != nil {
		return err
	}
	limit := payload.MatchesNeeded
	if limit <= 0 || limit > len(events) {
		limit = len(events)
	}
	for _, event := range events[:limit] {
		if err := j.persistEvent(ctx, payload, providerTeam, event, now); err != nil {
			return err
		}
	}
	return nil
}

func (j TheSportsDBTeamHistoryJob) fetchTeams(ctx context.Context, job jobs.Job, now time.Time, name string) ([]thesportsdb.TeamItem, error) {
	params := url.Values{"t": []string{name}}
	resp, err := j.fetch(ctx, job, now, "searchteams.php", params)
	if err != nil {
		return nil, err
	}
	return thesportsdb.ParseTeams(resp.Body)
}

func (j TheSportsDBTeamHistoryJob) fetchEvents(ctx context.Context, job jobs.Job, now time.Time, teamID int) ([]thesportsdb.EventItem, error) {
	params := url.Values{"id": []string{strconv.Itoa(teamID)}}
	resp, err := j.fetch(ctx, job, now, "eventslast.php", params)
	if err != nil {
		return nil, err
	}
	return thesportsdb.ParseEvents(resp.Body)
}

func (j TheSportsDBTeamHistoryJob) fetch(ctx context.Context, job jobs.Job, now time.Time, endpoint string, params url.Values) (provider.Response, error) {
	resp, err := j.opts.Client.Get(ctx, endpoint, params)
	requestLog := RequestLog{
		JobID: job.ID, Provider: nonEmpty(resp.Provider, thesportsdb.ProviderName),
		Endpoint: nonEmpty(resp.Endpoint, endpoint), Method: "GET", ParamsHash: hashParams(params),
		StatusCode: resp.StatusCode, QuotaCost: providerQuotaCost(resp), Latency: resp.Latency,
		ResponseBytes: resp.ResponseBytes, Reason: "backfill", Priority: 10, RequestedAt: now,
	}
	if err != nil {
		requestLog.ErrorCategory = categorizeProviderError(resp, err)
	}
	if logErr := j.opts.RequestLogs.Insert(ctx, requestLog); logErr != nil {
		return resp, logErr
	}
	return resp, err
}

func (j TheSportsDBTeamHistoryJob) persistEvent(
	ctx context.Context,
	payload theSportsDBTeamHistoryPayload,
	providerTeam thesportsdb.TeamItem,
	event thesportsdb.EventItem,
	now time.Time,
) error {
	if event.ID <= 0 || event.Home == "" || event.Away == "" || event.Date == "" {
		return nil
	}
	kickoff, err := time.Parse("2006-01-02 15:04:05", event.Date+" "+nonEmpty(event.Time, "00:00:00"))
	if err != nil {
		return nil
	}
	kickoff = kickoff.UTC()
	homeID, awayID := payload.TeamID, payload.TeamID
	if equalTeamName(event.Home, providerTeam.Name) {
		awayID, err = j.opts.Catalog.UpsertTeam(ctx, TeamRecord{Slug: canonicalTeamSlug("", event.Away), Name: event.Away})
	} else if equalTeamName(event.Away, providerTeam.Name) {
		homeID, err = j.opts.Catalog.UpsertTeam(ctx, TeamRecord{Slug: canonicalTeamSlug("", event.Home), Name: event.Home})
	} else {
		return nil
	}
	if err != nil {
		return err
	}
	originalHomeID, originalAwayID := homeID, awayID
	if err := j.opts.Catalog.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT canonical_id FROM team_identity_aliases WHERE old_id = $1 AND revoked_at IS NULL), $1)`, homeID).Scan(&homeID); err != nil {
		return fmt.Errorf("resolve reviewed home team alias: %w", err)
	}
	if err := j.opts.Catalog.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT canonical_id FROM team_identity_aliases WHERE old_id = $1 AND revoked_at IS NULL), $1)`, awayID).Scan(&awayID); err != nil {
		return fmt.Errorf("resolve reviewed away team alias: %w", err)
	}
	homeScore, awayScore := event.HomeScore, event.AwayScore
	matchID, err := j.reviewedHistoryMatch(ctx, payload.CompetitionID, homeID, awayID, kickoff,
		homeScore, awayScore, homeID != originalHomeID || awayID != originalAwayID)
	if err != nil {
		return err
	}
	if matchID == 0 {
		matchID, err = j.opts.Catalog.InsertMatch(ctx, MatchRecord{
			LeagueID: payload.CompetitionID, SeasonID: kickoff.Year(), HomeTeamID: homeID, AwayTeamID: awayID,
			KickoffAt: kickoff, Status: "finished", RawStatus: event.Status, RawStatusLabel: event.Status,
			VenueName: event.Venue, HomeScore: &homeScore, AwayScore: &awayScore, Source: thesportsdb.ProviderName,
		})
		if err != nil {
			return err
		}
	}
	return j.opts.EntityMaps.Upsert(ctx, ProviderEntityMap{
		Provider: thesportsdb.ProviderName, EntityType: "match", ProviderEntityID: strconv.Itoa(event.ID),
		ProviderEntityKey: event.Name, CanonicalType: "match", CanonicalID: strconv.FormatInt(matchID, 10),
		MatchStatus: "resolved", SeenAt: now,
		Metadata: map[string]any{"source_fidelity": "basic_results", "tier_eligibility": "A"},
	})
}

// A reviewed team alias must not let reference history republish the old
// fixture or overwrite the canonical finished score. The provider mapping is
// written only after the exact canonical row passes score parity.
func (j TheSportsDBTeamHistoryJob) reviewedHistoryMatch(
	ctx context.Context, leagueID, homeID, awayID int64, kickoff time.Time,
	homeScore, awayScore int, teamAliased bool,
) (int64, error) {
	var existingID, targetID int64
	err := j.opts.Catalog.db.QueryRowContext(ctx, `
SELECT m.id, COALESCE(fia.canonical_id, m.id)
FROM matches m
LEFT JOIN fixture_identity_aliases fia ON fia.old_id = m.id AND fia.revoked_at IS NULL
WHERE m.league_id = $1 AND m.home_team_id = $2 AND m.away_team_id = $3 AND m.kickoff_at = $4`,
		leagueID, homeID, awayID, kickoff).Scan(&existingID, &targetID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("find reviewed history match: %w", err)
	}
	if !teamAliased && targetID == existingID {
		return 0, nil // retain the ordinary reference-history update path
	}
	var status string
	var storedHome, storedAway sql.NullInt64
	if err := j.opts.Catalog.db.QueryRowContext(ctx, `SELECT status, home_score, away_score FROM matches WHERE id = $1`, targetID).
		Scan(&status, &storedHome, &storedAway); err != nil {
		return 0, fmt.Errorf("read reviewed history target %d: %w", targetID, err)
	}
	if !isFinishedHistoryStatus(status) {
		if targetID != existingID {
			return 0, fmt.Errorf("reviewed history target %d is not finished", targetID)
		}
		return 0, nil
	}
	if !storedHome.Valid || !storedAway.Valid || storedHome.Int64 != int64(homeScore) || storedAway.Int64 != int64(awayScore) {
		return 0, fmt.Errorf("reviewed history score differs from finished target %d", targetID)
	}
	return targetID, nil
}

func isFinishedHistoryStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "finished", "ft", "aet", "pen":
		return true
	default:
		return false
	}
}

// bestTheSportsDBTeam returns the search result whose name matches the
// canonical team name exactly, first case/whitespace-insensitively and then
// through the same conservative accent/designator normalization used for
// canonical team slugs. It deliberately never falls back to an arbitrary
// candidate: an unresolved match must be recorded, not silently attributed to
// the wrong team.
func bestTheSportsDBTeam(canonicalName string, items []thesportsdb.TeamItem) (thesportsdb.TeamItem, bool) {
	for _, item := range items {
		if equalTeamName(canonicalName, item.Name) {
			return item, true
		}
	}
	canonical := canonicalNameSlug(canonicalName, true)
	if canonical == "" {
		return thesportsdb.TeamItem{}, false
	}
	var matched thesportsdb.TeamItem
	matches := 0
	for _, item := range items {
		if canonicalNameSlug(item.Name, true) == canonical {
			matched = item
			matches++
		}
	}
	if matches == 1 {
		return matched, true
	}
	return thesportsdb.TeamItem{}, false
}

func equalTeamName(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

var _ JobHandler = TheSportsDBTeamHistoryJob{}

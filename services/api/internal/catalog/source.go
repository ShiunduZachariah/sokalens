// Package catalog exposes league and team read models for discovery pages.
package catalog

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/httpserver"
)

type Source struct {
	db *sql.DB
}

func NewSource(db *sql.DB) Source {
	return Source{db: db}
}

// A league is a durable entity with its own indexable page, so the directory
// has to be able to ask for the whole catalogue. The old ceiling silently
// rewrote any larger request to 100, which is how 891 leagues with content came
// to have 100 internal links between them. 0 means "everything".
const leagueCatalogCeiling = 5000

func (s Source) ListLeagues(ctx context.Context, limit int) ([]httpserver.LeagueSummary, error) {
	if limit <= 0 || limit > leagueCatalogCeiling {
		limit = leagueCatalogCeiling
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT l.id, l.slug, l.name, COALESCE(c.name, ''), COALESCE(sp.name, ''), COALESCE(l.priority_score, 0), COALESCE(l.logo_url, ''),
       EXISTS (
         SELECT 1 FROM fixtures f
         WHERE f.league_id = l.id
           AND NOT EXISTS (SELECT 1 FROM fixture_identity_aliases fia WHERE fia.old_id = f.id AND fia.revoked_at IS NULL)
           AND (
             f.kickoff_at > now() - interval '2 hours'
             OR lower(COALESCE(f.status, 'scheduled')) IN ('finished', 'ft', 'aet', 'pen')
           )
       )
FROM leagues l
LEFT JOIN countries c ON c.code = l.country_code
LEFT JOIN sports sp ON sp.id = l.sport_id
ORDER BY l.priority_score DESC NULLS LAST, l.name ASC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	leagues := []httpserver.LeagueSummary{}
	for rows.Next() {
		var league httpserver.LeagueSummary
		if err := rows.Scan(&league.ID, &league.Slug, &league.Name, &league.Country, &league.Sport, &league.PriorityScore, &league.LogoURL, &league.HasContent); err != nil {
			return nil, err
		}
		leagues = append(leagues, league)
	}
	return leagues, rows.Err()
}

func (s Source) GetLeague(ctx context.Context, slug string) (httpserver.LeagueDetail, error) {
	var detail httpserver.LeagueDetail
	err := s.db.QueryRowContext(ctx, `
SELECT l.id, l.slug, l.name, COALESCE(c.name, ''), COALESCE(sp.name, ''), COALESCE(l.priority_score, 0), COALESCE(l.logo_url, '')
FROM leagues l
LEFT JOIN countries c ON c.code = l.country_code
LEFT JOIN sports sp ON sp.id = l.sport_id
WHERE l.slug = $1`, slug).Scan(
		&detail.ID, &detail.Slug, &detail.Name, &detail.Country, &detail.Sport, &detail.PriorityScore, &detail.LogoURL,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return httpserver.LeagueDetail{}, httpserver.ErrLeagueNotFound
		}
		return httpserver.LeagueDetail{}, err
	}
	matches, err := s.leagueMatches(ctx, detail.ID, 30)
	if err != nil {
		return httpserver.LeagueDetail{}, err
	}
	detail.Upcoming = matches
	recent, err := s.leagueRecentMatches(ctx, detail.ID)
	if err != nil {
		return httpserver.LeagueDetail{}, err
	}
	detail.Recent = recent
	detail.HasContent = len(detail.Upcoming) > 0 || len(detail.Recent) > 0
	teams, err := s.leagueTeams(ctx, detail.ID)
	if err != nil {
		return httpserver.LeagueDetail{}, err
	}
	detail.Teams = teams
	return detail, nil
}

func (s Source) GetTeam(ctx context.Context, slug string) (httpserver.TeamDetail, error) {
	detail, err := s.lookupTeam(ctx, slug)
	if err != nil {
		return httpserver.TeamDetail{}, err
	}
	if err := s.db.QueryRowContext(ctx, `
SELECT COALESCE(l.name, ''), COALESCE(l.slug, '')
FROM fixtures f
LEFT JOIN leagues l ON l.id = f.league_id
WHERE (f.home_team_id = $1 OR f.away_team_id = $1)
  AND NOT EXISTS (SELECT 1 FROM fixture_identity_aliases fia WHERE fia.old_id = f.id AND fia.revoked_at IS NULL)
ORDER BY f.kickoff_at DESC
LIMIT 1`, detail.ID).Scan(&detail.LeagueName, &detail.LeagueSlug); err != nil && err != sql.ErrNoRows {
		return httpserver.TeamDetail{}, err
	}
	next, err := s.teamMatch(ctx, detail.ID, true)
	if err != nil {
		return httpserver.TeamDetail{}, err
	}
	recent, err := s.teamMatches(ctx, detail.ID, 20)
	if err != nil {
		return httpserver.TeamDetail{}, err
	}
	detail.NextMatch = next
	detail.Recent = recent
	return detail, nil
}

// lookupTeam maps only explicit, active identity decisions. The old slug
// remains addressable, but returns the canonical slug for the web redirect.
func (s Source) lookupTeam(ctx context.Context, slug string) (httpserver.TeamDetail, error) {
	var detail httpserver.TeamDetail
	err := s.db.QueryRowContext(ctx, `
SELECT t.id, t.slug, t.name, COALESCE(c.name, ''),
       COALESCE(NULLIF(t.logo_url, ''), (SELECT NULLIF(alias_team.logo_url, '') FROM teams alias_team WHERE lower(alias_team.name) = lower(t.name) AND NULLIF(alias_team.logo_url, '') IS NOT NULL LIMIT 1), ''),
       COALESCE(c.flag_url, '')
FROM teams requested
LEFT JOIN team_identity_aliases tia ON tia.old_id = requested.id AND tia.revoked_at IS NULL
JOIN teams t ON t.id = COALESCE(tia.canonical_id, requested.id)
LEFT JOIN countries c ON c.code = t.country_code
WHERE requested.slug = $1`, slug).Scan(&detail.ID, &detail.Slug, &detail.Name, &detail.Country, &detail.LogoURL, &detail.FlagURL)
	if err != nil {
		if err == sql.ErrNoRows {
			return httpserver.TeamDetail{}, httpserver.ErrTeamNotFound
		}
		return httpserver.TeamDetail{}, err
	}
	return detail, nil
}

// ListCountryMatches returns only live or future fixtures for a saved country.
// The identifier may be either the country code stored on match feeds or the
// human country name shown in catalog pages.
func (s Source) ListCountryMatches(ctx context.Context, country string, limit int) ([]httpserver.MatchSummary, error) {
	country = strings.TrimSpace(country)
	if country == "" {
		return []httpserver.MatchSummary{}, nil
	}
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT f.id, COALESCE(home.name,'Home'), COALESCE(away.name,'Away'), COALESCE(l.name,'Competition'), f.kickoff_at, COALESCE(f.status,'scheduled'), f.home_goals_ft, f.away_goals_ft,
       COALESCE(NULLIF(home.logo_url, ''), ''), COALESCE(NULLIF(away.logo_url, ''), '')
FROM fixtures f
JOIN teams home ON home.id = f.home_team_id
JOIN teams away ON away.id = f.away_team_id
LEFT JOIN leagues l ON l.id = f.league_id
LEFT JOIN countries c ON c.code = l.country_code
WHERE (lower(COALESCE(c.code, l.country_code, '')) = lower($1) OR lower(COALESCE(c.name, '')) = lower($1))
  AND NOT EXISTS (SELECT 1 FROM fixture_identity_aliases fia WHERE fia.old_id = f.id AND fia.revoked_at IS NULL)
  AND f.kickoff_at > now() - interval '4 hours'
  AND lower(COALESCE(f.status, 'scheduled')) NOT IN ('finished', 'ft', 'aet', 'pen', 'awarded', 'walkover', 'cancelled', 'canceled', 'postponed', 'abandoned')
ORDER BY f.kickoff_at ASC
LIMIT $2`, country, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMatches(rows)
}

func (s Source) GetTeamNextMatch(ctx context.Context, slug string) (*httpserver.MatchSummary, error) {
	var teamID int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(tia.canonical_id, t.id) FROM teams t LEFT JOIN team_identity_aliases tia ON tia.old_id = t.id AND tia.revoked_at IS NULL WHERE t.slug = $1`, strings.TrimSpace(slug)).Scan(&teamID); err != nil {
		if err == sql.ErrNoRows {
			return nil, httpserver.ErrTeamNotFound
		}
		return nil, err
	}
	return s.teamMatch(ctx, teamID, true)
}

func (s Source) ListLeagueMatches(ctx context.Context, slug string, limit int) ([]httpserver.MatchSummary, error) {
	var leagueID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM leagues WHERE slug = $1`, strings.TrimSpace(slug)).Scan(&leagueID); err != nil {
		if err == sql.ErrNoRows {
			return nil, httpserver.ErrLeagueNotFound
		}
		return nil, err
	}
	if limit <= 0 || limit > 20 {
		limit = 6
	}
	return s.leagueMatches(ctx, leagueID, limit)
}

func (s Source) leagueMatches(ctx context.Context, leagueID int64, limit int) ([]httpserver.MatchSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT f.id, COALESCE(home.name,'Home'), COALESCE(away.name,'Away'), COALESCE(l.name,'Competition'), f.kickoff_at, COALESCE(f.status,'scheduled'), f.home_goals_ft, f.away_goals_ft,
       COALESCE(NULLIF(home.logo_url, ''), (SELECT NULLIF(alias_home.logo_url, '') FROM teams alias_home WHERE lower(alias_home.name) = lower(home.name) AND NULLIF(alias_home.logo_url, '') IS NOT NULL LIMIT 1), ''),
       COALESCE(NULLIF(away.logo_url, ''), (SELECT NULLIF(alias_away.logo_url, '') FROM teams alias_away WHERE lower(alias_away.name) = lower(away.name) AND NULLIF(alias_away.logo_url, '') IS NOT NULL LIMIT 1), '')
FROM fixtures f
JOIN teams home ON home.id = f.home_team_id
JOIN teams away ON away.id = f.away_team_id
LEFT JOIN leagues l ON l.id = f.league_id
WHERE f.league_id = $1
  AND NOT EXISTS (SELECT 1 FROM fixture_identity_aliases fia WHERE fia.old_id = f.id AND fia.revoked_at IS NULL)
  AND f.kickoff_at > now() - interval '4 hours'
  AND lower(COALESCE(f.status, 'scheduled')) NOT IN ('finished', 'ft', 'aet', 'pen', 'awarded', 'walkover', 'cancelled', 'canceled', 'postponed', 'abandoned')
ORDER BY f.kickoff_at ASC
LIMIT $2`, leagueID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMatches(rows)
}

// leagueRecentMatches returns the most recent finished fixtures for a league
// (results), newest first.
func (s Source) leagueRecentMatches(ctx context.Context, leagueID int64) ([]httpserver.MatchSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT f.id, COALESCE(home.name,'Home'), COALESCE(away.name,'Away'), COALESCE(l.name,'Competition'), f.kickoff_at, COALESCE(f.status,'scheduled'), f.home_goals_ft, f.away_goals_ft,
       COALESCE(NULLIF(home.logo_url, ''), (SELECT NULLIF(alias_home.logo_url, '') FROM teams alias_home WHERE lower(alias_home.name) = lower(home.name) AND NULLIF(alias_home.logo_url, '') IS NOT NULL LIMIT 1), ''),
       COALESCE(NULLIF(away.logo_url, ''), (SELECT NULLIF(alias_away.logo_url, '') FROM teams alias_away WHERE lower(alias_away.name) = lower(away.name) AND NULLIF(alias_away.logo_url, '') IS NOT NULL LIMIT 1), '')
FROM fixtures f
JOIN teams home ON home.id = f.home_team_id
JOIN teams away ON away.id = f.away_team_id
LEFT JOIN leagues l ON l.id = f.league_id
WHERE f.league_id = $1
  AND NOT EXISTS (SELECT 1 FROM fixture_identity_aliases fia WHERE fia.old_id = f.id AND fia.revoked_at IS NULL)
  AND lower(COALESCE(f.status, 'scheduled')) IN ('finished', 'ft', 'aet', 'pen')
ORDER BY f.kickoff_at DESC
LIMIT 20`, leagueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMatches(rows)
}

// leagueTeams returns the distinct teams that have a fixture in this league
// (any season), ordered by name.
func (s Source) leagueTeams(ctx context.Context, leagueID int64) ([]httpserver.TeamSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT t.id, t.slug, t.name, COALESCE(c.name, ''), COALESCE(t.logo_url, ''), COALESCE(c.flag_url, '')
FROM fixtures f
JOIN teams t ON t.id = COALESCE((SELECT tia.canonical_id FROM team_identity_aliases tia WHERE tia.old_id = f.home_team_id AND tia.revoked_at IS NULL), f.home_team_id)
  OR t.id = COALESCE((SELECT tia.canonical_id FROM team_identity_aliases tia WHERE tia.old_id = f.away_team_id AND tia.revoked_at IS NULL), f.away_team_id)
LEFT JOIN countries c ON c.code = t.country_code
WHERE f.league_id = $1
  AND NOT EXISTS (SELECT 1 FROM fixture_identity_aliases fia WHERE fia.old_id = f.id AND fia.revoked_at IS NULL)
ORDER BY t.name ASC
LIMIT 100`, leagueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	teams := []httpserver.TeamSummary{}
	for rows.Next() {
		var team httpserver.TeamSummary
		if err := rows.Scan(&team.ID, &team.Slug, &team.Name, &team.Country, &team.LogoURL, &team.FlagURL); err != nil {
			return nil, err
		}
		teams = append(teams, team)
	}
	return teams, rows.Err()
}

func (s Source) teamMatch(ctx context.Context, teamID int64, upcoming bool) (*httpserver.MatchSummary, error) {
	comparison := "> now() - interval '2 hours'"
	order := "ASC"
	if !upcoming {
		comparison = "<= now()"
		order = "DESC"
	}
	query := fmt.Sprintf(`
SELECT f.id, COALESCE(home.name,'Home'), COALESCE(away.name,'Away'), COALESCE(l.name,'Competition'), f.kickoff_at, COALESCE(f.status,'scheduled'), f.home_goals_ft, f.away_goals_ft,
       COALESCE(NULLIF(home.logo_url, ''), (SELECT NULLIF(alias_home.logo_url, '') FROM teams alias_home WHERE lower(alias_home.name) = lower(home.name) AND NULLIF(alias_home.logo_url, '') IS NOT NULL LIMIT 1), ''),
       COALESCE(NULLIF(away.logo_url, ''), (SELECT NULLIF(alias_away.logo_url, '') FROM teams alias_away WHERE lower(alias_away.name) = lower(away.name) AND NULLIF(alias_away.logo_url, '') IS NOT NULL LIMIT 1), '')
FROM fixtures f
JOIN teams home ON home.id = f.home_team_id
JOIN teams away ON away.id = f.away_team_id
LEFT JOIN leagues l ON l.id = f.league_id
WHERE (
  f.home_team_id = $1 OR f.away_team_id = $1
  OR EXISTS (SELECT 1 FROM team_identity_aliases tia WHERE tia.canonical_id = $1
    AND tia.revoked_at IS NULL AND tia.old_id IN (f.home_team_id, f.away_team_id))
)
  AND NOT EXISTS (SELECT 1 FROM fixture_identity_aliases fia WHERE fia.old_id = f.id AND fia.revoked_at IS NULL)
  AND f.kickoff_at %s
  AND lower(COALESCE(f.status, 'scheduled')) NOT IN ('finished', 'ft', 'aet', 'pen', 'awarded', 'walkover', 'cancelled', 'canceled', 'postponed', 'abandoned')
ORDER BY f.kickoff_at %s
LIMIT 1`, comparison, order)
	var match httpserver.MatchSummary
	var kickoff any
	var homeScore, awayScore sql.NullInt64
	err := s.db.QueryRowContext(ctx, query, teamID).Scan(&match.ID, &match.HomeTeam, &match.AwayTeam, &match.LeagueName, &kickoff, &match.Status, &homeScore, &awayScore, &match.HomeTeamLogo, &match.AwayTeamLogo)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	match.KickoffAt = parseKickoff(kickoff)
	applyScores(&match, homeScore, awayScore)
	return &match, nil
}

func (s Source) teamMatches(ctx context.Context, teamID int64, limit int) ([]httpserver.MatchSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT f.id, COALESCE(home.name,'Home'), COALESCE(away.name,'Away'), COALESCE(l.name,'Competition'), f.kickoff_at, COALESCE(f.status,'scheduled'), f.home_goals_ft, f.away_goals_ft,
       COALESCE(NULLIF(home.logo_url, ''), (SELECT NULLIF(alias_home.logo_url, '') FROM teams alias_home WHERE lower(alias_home.name) = lower(home.name) AND NULLIF(alias_home.logo_url, '') IS NOT NULL LIMIT 1), ''),
       COALESCE(NULLIF(away.logo_url, ''), (SELECT NULLIF(alias_away.logo_url, '') FROM teams alias_away WHERE lower(alias_away.name) = lower(away.name) AND NULLIF(alias_away.logo_url, '') IS NOT NULL LIMIT 1), '')
FROM fixtures f
JOIN teams home ON home.id = f.home_team_id
JOIN teams away ON away.id = f.away_team_id
LEFT JOIN leagues l ON l.id = f.league_id
WHERE (
  f.home_team_id = $1 OR f.away_team_id = $1
  OR EXISTS (SELECT 1 FROM team_identity_aliases tia WHERE tia.canonical_id = $1
    AND tia.revoked_at IS NULL AND tia.old_id IN (f.home_team_id, f.away_team_id))
) AND f.kickoff_at <= now()
  AND NOT EXISTS (SELECT 1 FROM fixture_identity_aliases fia WHERE fia.old_id = f.id AND fia.revoked_at IS NULL)
ORDER BY f.kickoff_at DESC
LIMIT $2`, teamID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMatches(rows)
}

func scanMatches(rows *sql.Rows) ([]httpserver.MatchSummary, error) {
	matches := []httpserver.MatchSummary{}
	for rows.Next() {
		var match httpserver.MatchSummary
		var kickoff any
		var homeScore, awayScore sql.NullInt64
		if err := rows.Scan(&match.ID, &match.HomeTeam, &match.AwayTeam, &match.LeagueName, &kickoff, &match.Status, &homeScore, &awayScore, &match.HomeTeamLogo, &match.AwayTeamLogo); err != nil {
			return nil, err
		}
		match.KickoffAt = parseKickoff(kickoff)
		applyScores(&match, homeScore, awayScore)
		matches = append(matches, match)
	}
	return matches, rows.Err()
}

func applyScores(match *httpserver.MatchSummary, homeScore, awayScore sql.NullInt64) {
	if homeScore.Valid {
		value := int(homeScore.Int64)
		match.HomeScore = &value
	}
	if awayScore.Valid {
		value := int(awayScore.Int64)
		match.AwayScore = &value
	}
}

func parseKickoff(value any) time.Time {
	switch v := value.(type) {
	case time.Time:
		return v
	case string:
		if parsed, err := time.Parse(time.RFC3339, v); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

package ingestion

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/matchstate"
)

type catalogDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

const MaxFixtureProjectionReconcileBatch = 500

type CatalogRepository struct {
	db catalogDB
}

type CountryRecord struct {
	Code string
	Name string
}

type LeagueRecord struct {
	SportSlug   string
	SportName   string
	CountryCode string
	CountryName string
	Slug        string
	Name        string
}

type TeamRecord struct {
	CountryCode string
	Slug        string
	Name        string
}

type PlayerRecord struct {
	ID          int64
	TeamID      int64
	Name        string
	Age         int
	Number      int
	Position    string
	PhotoURL    string
	Nationality string
}

type VenueRecord struct {
	ID       int64
	Name     string
	Address  string
	City     string
	Country  string
	Capacity int
	Surface  string
	ImageURL string
}

type MatchRecord struct {
	LeagueID          int64
	SeasonID          int
	HomeTeamID        int64
	AwayTeamID        int64
	KickoffAt         time.Time
	Status            string
	RawStatus         string
	RawStatusLabel    string
	StatusElapsed     *int
	VenueName         string
	VenueCity         string
	HomeScore         *int
	AwayScore         *int
	HalfTimeHomeScore *int
	HalfTimeAwayScore *int
	// Source identifies the provider that supplied the score/result history.
	// It is later used to keep supplemental basic data in Tier A eligibility.
	Source string
}

// MatchIdentity is the natural identity of a canonical match row. Fixture
// ingestion uses it to detect provider fixture-id collisions: when a provider
// fixture id already maps to a canonical match, a stored identity that differs
// from the incoming payload means the provider reused the fixture id for a
// different real match (or the same match arrived under two provider ids).
type MatchIdentity struct {
	LeagueID   int64
	HomeTeamID int64
	AwayTeamID int64
	KickoffAt  time.Time
	Status     string
}

func NewCatalogRepository(db catalogDB) CatalogRepository {
	return CatalogRepository{db: db}
}

func (r CatalogRepository) UpsertCountry(ctx context.Context, country CountryRecord) error {
	if r.db == nil {
		return errors.New("catalog DB is nil")
	}
	code := strings.TrimSpace(strings.ToUpper(country.Code))
	name := strings.TrimSpace(country.Name)
	if len(code) != 2 {
		return errors.New("country code is required")
	}
	if name == "" {
		return errors.New("country name is required")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO countries (code, name)
VALUES ($1, $2)
ON CONFLICT (code)
DO UPDATE SET name = EXCLUDED.name`,
		code,
		name,
	)
	return err
}

func (r CatalogRepository) UpsertLeague(ctx context.Context, league LeagueRecord) (int64, error) {
	if r.db == nil {
		return 0, errors.New("catalog DB is nil")
	}
	if strings.TrimSpace(league.SportSlug) == "" {
		return 0, errors.New("sport slug is required")
	}
	if strings.TrimSpace(league.SportName) == "" {
		return 0, errors.New("sport name is required")
	}
	if strings.TrimSpace(league.Slug) == "" {
		return 0, errors.New("league slug is required")
	}
	if strings.TrimSpace(league.Name) == "" {
		return 0, errors.New("league name is required")
	}

	countryCode, err := r.resolveCountryCode(ctx, league.CountryCode, league.CountryName)
	if err != nil {
		return 0, err
	}

	sportID, err := r.ensureSport(ctx, league.SportSlug, league.SportName)
	if err != nil {
		return 0, err
	}

	var leagueID int64
	err = r.db.QueryRowContext(ctx, `
INSERT INTO leagues (sport_id, country_code, slug, name, priority_score)
VALUES ($1, NULLIF($2, ''), $3, $4, 0)
ON CONFLICT (slug)
DO UPDATE SET
  sport_id = EXCLUDED.sport_id,
  country_code = COALESCE(EXCLUDED.country_code, leagues.country_code),
  name = EXCLUDED.name
RETURNING id`,
		sportID,
		countryCode,
		league.Slug,
		league.Name,
	).Scan(&leagueID)
	return leagueID, err
}

func (r CatalogRepository) UpsertTeam(ctx context.Context, team TeamRecord) (int64, error) {
	if r.db == nil {
		return 0, errors.New("catalog DB is nil")
	}
	if strings.TrimSpace(team.Slug) == "" {
		return 0, errors.New("team slug is required")
	}
	if strings.TrimSpace(team.Name) == "" {
		return 0, errors.New("team name is required")
	}
	countryCode, err := r.resolveCountryCode(ctx, team.CountryCode, "")
	if err != nil {
		return 0, err
	}
	var teamID int64
	err = r.db.QueryRowContext(ctx, `
INSERT INTO teams (country_code, slug, name)
VALUES (NULLIF($1, ''), $2, $3)
ON CONFLICT (slug)
DO UPDATE SET
  country_code = COALESCE(EXCLUDED.country_code, teams.country_code),
  name = EXCLUDED.name
RETURNING id`,
		countryCode,
		team.Slug,
		team.Name,
	).Scan(&teamID)
	return teamID, err
}

// TeamExists guards fixture ingestion against a provider map whose canonical
// team was deleted. A stale map must not become a matches foreign-key failure.
func (r CatalogRepository) TeamExists(ctx context.Context, teamID int64) (bool, error) {
	if r.db == nil {
		return false, errors.New("catalog DB is nil")
	}
	var exists bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM teams WHERE id = $1)`, teamID).Scan(&exists)
	return exists, err
}

func (r CatalogRepository) UpsertPlayer(ctx context.Context, player PlayerRecord) (int64, error) {
	if r.db == nil {
		return 0, errors.New("catalog DB is nil")
	}
	if err := validatePlayerRecord(player); err != nil {
		return 0, err
	}
	var playerID int64
	err := r.db.QueryRowContext(ctx, `
INSERT INTO players (id, team_id, name, age, number, position, photo_url, nationality)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (id)
DO UPDATE SET
  team_id = EXCLUDED.team_id,
  name = EXCLUDED.name,
  age = EXCLUDED.age,
  number = EXCLUDED.number,
  position = EXCLUDED.position,
  photo_url = EXCLUDED.photo_url,
  nationality = EXCLUDED.nationality
RETURNING id`,
		player.ID,
		player.TeamID,
		player.Name,
		nullablePositiveInt(player.Age),
		nullablePositiveInt(player.Number),
		nullableText(player.Position),
		nullableText(player.PhotoURL),
		nullableText(player.Nationality),
	).Scan(&playerID)
	return playerID, err
}

func (r CatalogRepository) UpsertVenue(ctx context.Context, venue VenueRecord) (int64, error) {
	if r.db == nil {
		return 0, errors.New("catalog DB is nil")
	}
	if err := validateVenueRecord(venue); err != nil {
		return 0, err
	}
	var venueID int64
	err := r.db.QueryRowContext(ctx, `
INSERT INTO venues (id, name, address, city, country, capacity, surface, image_url)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (id)
DO UPDATE SET
  name = EXCLUDED.name,
  address = EXCLUDED.address,
  city = EXCLUDED.city,
  country = EXCLUDED.country,
  capacity = EXCLUDED.capacity,
  surface = EXCLUDED.surface,
  image_url = EXCLUDED.image_url
RETURNING id`,
		venue.ID,
		venue.Name,
		nullableText(venue.Address),
		nullableText(venue.City),
		nullableText(venue.Country),
		nullablePositiveInt(venue.Capacity),
		nullableText(venue.Surface),
		nullableText(venue.ImageURL),
	).Scan(&venueID)
	return venueID, err
}

// liveStateStamp returns the freshness stamp for an incoming observation.
//
// Only a state that claims the match is happening right now carries one. A
// finished or scheduled match makes no claim about the present moment, so it
// cannot go stale and needs no stamp -- and stamping one would put a live
// freshness signal on a row the reaper should never look at.
//
// Halted states (interrupted, suspended) do carry a stamp: play has started
// and may resume, so they are bounded like an in-play state even though they
// advertise no running clock.
func liveStateStamp(status string) sql.NullTime {
	state := matchstate.Normalize(status)
	if state.InPlay() || state.Halted() {
		return sql.NullTime{Time: time.Now().UTC(), Valid: true}
	}
	return sql.NullTime{}
}

func (r CatalogRepository) InsertMatch(ctx context.Context, match MatchRecord) (int64, error) {
	if r.db == nil {
		return 0, errors.New("catalog DB is nil")
	}
	if err := validateMatchRecord(match); err != nil {
		return 0, err
	}
	var matchID int64
	err := r.db.QueryRowContext(ctx, `
INSERT INTO matches (
  league_id,
  home_team_id,
  away_team_id,
  kickoff_at,
  status,
  provider_status_raw,
  provider_status_label,
  status_elapsed,
  venue_name,
  venue_city,
  home_score,
  away_score,
  half_time_home_score,
  half_time_away_score,
  live_state_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
ON CONFLICT (league_id, home_team_id, away_team_id, kickoff_at)
DO UPDATE SET
  status = CASE
    WHEN lower(matches.status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower(EXCLUDED.status) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN matches.status
    ELSE EXCLUDED.status
  END,
  provider_status_raw = CASE
    WHEN lower(matches.status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower(EXCLUDED.status) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN matches.provider_status_raw
    ELSE COALESCE(EXCLUDED.provider_status_raw, matches.provider_status_raw)
  END,
  provider_status_label = CASE
    WHEN lower(matches.status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower(EXCLUDED.status) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN matches.provider_status_label
    ELSE COALESCE(EXCLUDED.provider_status_label, matches.provider_status_label)
  END,
  status_elapsed = CASE
    WHEN lower(matches.status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower(EXCLUDED.status) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN matches.status_elapsed
    ELSE COALESCE(EXCLUDED.status_elapsed, matches.status_elapsed)
  END,
  venue_name = COALESCE(EXCLUDED.venue_name, matches.venue_name),
  venue_city = COALESCE(EXCLUDED.venue_city, matches.venue_city),
  home_score = CASE
    WHEN lower(matches.status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower(EXCLUDED.status) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN matches.home_score
    ELSE COALESCE(EXCLUDED.home_score, matches.home_score)
  END,
  away_score = CASE
    WHEN lower(matches.status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower(EXCLUDED.status) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN matches.away_score
    ELSE COALESCE(EXCLUDED.away_score, matches.away_score)
  END,
  half_time_home_score = CASE
    WHEN lower(matches.status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower(EXCLUDED.status) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN matches.half_time_home_score
    ELSE COALESCE(EXCLUDED.half_time_home_score, matches.half_time_home_score)
  END,
  half_time_away_score = CASE
    WHEN lower(matches.status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower(EXCLUDED.status) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN matches.half_time_away_score
    ELSE COALESCE(EXCLUDED.half_time_away_score, matches.half_time_away_score)
  END,
  -- Freshness of the live claim (in-play architecture 6). Stamped on every
  -- observation that describes the match as in play, not only when a value
  -- changes: a goalless match sitting on the same minute for three polls has
  -- still been confirmed three times, and treating "unchanged" as "unheard
  -- from" would mark every quiet match stale. A write the terminal guard above
  -- rejects leaves the stamp alone, because that observation was not accepted.
  live_state_at = CASE
    WHEN lower(matches.status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower(EXCLUDED.status) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN matches.live_state_at
    ELSE COALESCE(EXCLUDED.live_state_at, matches.live_state_at)
  END
RETURNING id, status, home_score, away_score, half_time_home_score, half_time_away_score`,
		match.LeagueID,
		match.HomeTeamID,
		match.AwayTeamID,
		match.KickoffAt.UTC(),
		match.Status,
		nullableText(match.RawStatus),
		nullableText(match.RawStatusLabel),
		match.StatusElapsed,
		nullableText(match.VenueName),
		nullableText(match.VenueCity),
		match.HomeScore,
		match.AwayScore,
		match.HalfTimeHomeScore,
		match.HalfTimeAwayScore,
		liveStateStamp(match.Status),
	).Scan(
		&matchID,
		&match.Status,
		&match.HomeScore,
		&match.AwayScore,
		&match.HalfTimeHomeScore,
		&match.HalfTimeAwayScore,
	)
	if err != nil {
		return 0, err
	}
	return matchID, r.upsertEngineFixture(ctx, matchID, match)
}

func (r CatalogRepository) UpdateMatch(ctx context.Context, matchID int64, match MatchRecord) error {
	if r.db == nil {
		return errors.New("catalog DB is nil")
	}
	if matchID <= 0 {
		return errors.New("match id is required")
	}
	if err := validateMatchRecord(match); err != nil {
		return err
	}
	err := r.db.QueryRowContext(ctx, `
UPDATE matches
SET
  league_id = $1,
  home_team_id = $2,
  away_team_id = $3,
  kickoff_at = $4,
  status = CASE
    WHEN lower(status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower($5) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN status
    ELSE $5
  END,
  provider_status_raw = CASE
    WHEN lower(status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower($5) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN provider_status_raw
    ELSE COALESCE($6, provider_status_raw)
  END,
  provider_status_label = CASE
    WHEN lower(status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower($5) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN provider_status_label
    ELSE COALESCE($7, provider_status_label)
  END,
  status_elapsed = CASE
    WHEN lower(status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower($5) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN status_elapsed
    ELSE COALESCE($8, status_elapsed)
  END,
  venue_name = COALESCE($9, venue_name),
  venue_city = COALESCE($10, venue_city),
  home_score = CASE
    WHEN lower(status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower($5) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN home_score
    ELSE COALESCE($11, home_score)
  END,
  away_score = CASE
    WHEN lower(status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower($5) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN away_score
    ELSE COALESCE($12, away_score)
  END,
  half_time_home_score = CASE
    WHEN lower(status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower($5) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN half_time_home_score
    ELSE COALESCE($13, half_time_home_score)
  END,
  half_time_away_score = CASE
    WHEN lower(status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower($5) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN half_time_away_score
    ELSE COALESCE($14, half_time_away_score)
  END,
  live_state_at = CASE
    WHEN lower(status) IN ('finished', 'ft', 'aet', 'pen')
      AND lower($5) NOT IN ('finished', 'ft', 'aet', 'pen')
      THEN live_state_at
    ELSE COALESCE($16, live_state_at)
  END
WHERE id = $15
RETURNING status, home_score, away_score, half_time_home_score, half_time_away_score`,
		match.LeagueID,
		match.HomeTeamID,
		match.AwayTeamID,
		match.KickoffAt.UTC(),
		match.Status,
		nullableText(match.RawStatus),
		nullableText(match.RawStatusLabel),
		match.StatusElapsed,
		nullableText(match.VenueName),
		nullableText(match.VenueCity),
		match.HomeScore,
		match.AwayScore,
		match.HalfTimeHomeScore,
		match.HalfTimeAwayScore,
		matchID,
		liveStateStamp(match.Status),
	).Scan(
		&match.Status,
		&match.HomeScore,
		&match.AwayScore,
		&match.HalfTimeHomeScore,
		&match.HalfTimeAwayScore,
	)
	if err != nil {
		return err
	}
	return r.upsertEngineFixture(ctx, matchID, match)
}

// FetchMatchIdentity returns the stored natural identity of a canonical match.
// It is used by fixture ingestion to compare the stored identity of the match a
// provider fixture id maps to against the incoming payload, so provider
// fixture-id reuse (a different match under the same id) can be signaled.
func (r CatalogRepository) FetchMatchIdentity(ctx context.Context, matchID int64) (MatchIdentity, error) {
	if r.db == nil {
		return MatchIdentity{}, errors.New("catalog DB is nil")
	}
	if matchID <= 0 {
		return MatchIdentity{}, errors.New("match id is required")
	}
	var identity MatchIdentity
	err := r.db.QueryRowContext(ctx, `
SELECT league_id, home_team_id, away_team_id, kickoff_at, status
FROM matches
WHERE id = $1`, matchID).Scan(
		&identity.LeagueID,
		&identity.HomeTeamID,
		&identity.AwayTeamID,
		&identity.KickoffAt,
		&identity.Status,
	)
	if err != nil {
		return MatchIdentity{}, err
	}
	return identity, nil
}

// FindSuspectedDuplicate returns the canonical match id of an existing match
// with the same league, home team, and away team whose kickoff falls within
// window of the incoming record. It surfaces provider reschedules or duplicate
// ingestions (the same teams listed twice on one day under different provider
// fixture ids) without silently merging them. A false bool means none exists.
func (r CatalogRepository) FindSuspectedDuplicate(ctx context.Context, match MatchRecord, window time.Duration) (int64, bool, error) {
	if r.db == nil {
		return 0, false, errors.New("catalog DB is nil")
	}
	if window <= 0 {
		return 0, false, errors.New("suspected duplicate window must be positive")
	}
	start := match.KickoffAt.UTC().Add(-window)
	end := match.KickoffAt.UTC().Add(window)
	var id int64
	err := r.db.QueryRowContext(ctx, `SELECT id FROM matches m WHERE league_id = $1 AND home_team_id = $2 AND away_team_id = $3 AND kickoff_at >= $4 AND kickoff_at <= $5 AND NOT EXISTS (SELECT 1 FROM fixture_identity_aliases fia WHERE fia.old_id = m.id AND fia.revoked_at IS NULL) ORDER BY kickoff_at ASC LIMIT 1`, match.LeagueID, match.HomeTeamID, match.AwayTeamID, start, end).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// ErrAmbiguousExactCanonicalFixture requires a reviewed identity decision.
var ErrAmbiguousExactCanonicalFixture = errors.New("ambiguous exact canonical fixture")

// FindExactCanonicalFixture finds one active real-world fixture using the
// provider-neutral name normalizer and an exact kickoff. It refuses ambiguous
// matches instead of guessing which existing row is canonical.
func (r CatalogRepository) FindExactCanonicalFixture(ctx context.Context, leagueName, homeName, awayName string, kickoff time.Time, excludeMatchID int64) (int64, bool, error) {
	if r.db == nil {
		return 0, false, errors.New("catalog DB is nil")
	}
	wantedLeague := canonicalNameSlug(leagueName, false)
	wantedHome := canonicalNameSlug(homeName, true)
	wantedAway := canonicalNameSlug(awayName, true)
	if wantedLeague == "" || wantedHome == "" || wantedAway == "" || kickoff.IsZero() {
		return 0, false, nil
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT candidate.id, candidate_league.name, candidate_home.name, candidate_away.name
FROM matches AS candidate
JOIN leagues AS candidate_league ON candidate_league.id = candidate.league_id
JOIN teams AS candidate_home ON candidate_home.id = candidate.home_team_id
JOIN teams AS candidate_away ON candidate_away.id = candidate.away_team_id
WHERE candidate.kickoff_at = $1
  AND ($2 = 0 OR candidate.id <> $2)
  AND lower(COALESCE(candidate.status, 'scheduled')) NOT IN ('cancelled', 'canceled')
  AND NOT EXISTS (SELECT 1 FROM fixture_identity_aliases fia WHERE fia.old_id = candidate.id AND fia.revoked_at IS NULL)
ORDER BY candidate.id`, kickoff.UTC(), excludeMatchID)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	matches := make([]int64, 0, 2)
	for rows.Next() {
		var id int64
		var candidateLeague, candidateHome, candidateAway string
		if err := rows.Scan(&id, &candidateLeague, &candidateHome, &candidateAway); err != nil {
			return 0, false, err
		}
		if canonicalNameSlug(candidateLeague, false) == wantedLeague &&
			canonicalNameSlug(candidateHome, true) == wantedHome &&
			canonicalNameSlug(candidateAway, true) == wantedAway {
			matches = append(matches, id)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	if len(matches) == 0 {
		return 0, false, nil
	}
	if len(matches) > 1 {
		return 0, false, fmt.Errorf("%w: %d active matches", ErrAmbiguousExactCanonicalFixture, len(matches))
	}
	return matches[0], true, nil
}

// ReconcileMatch persists the current natural identity for a previously mapped
// provider match and returns the canonical row that owns that identity. The
// old row is retained when the identity changed so historical ingestion data
// remains auditable.
func (r CatalogRepository) ReconcileMatch(ctx context.Context, mappedMatchID int64, match MatchRecord) (int64, error) {
	if r.db == nil {
		return 0, errors.New("catalog DB is nil")
	}
	if mappedMatchID <= 0 {
		return 0, errors.New("mapped match id is required")
	}
	if err := validateMatchRecord(match); err != nil {
		return 0, err
	}

	var previousStatus string
	var previousRawStatus sql.NullString
	var previousRawStatusLabel sql.NullString
	var previousStatusElapsed sql.NullInt64
	var previousVenueName sql.NullString
	var previousVenueCity sql.NullString
	var previousHomeScore sql.NullInt64
	var previousAwayScore sql.NullInt64
	var previousHalfTimeHomeScore sql.NullInt64
	var previousHalfTimeAwayScore sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
SELECT
  status,
  provider_status_raw,
  provider_status_label,
  status_elapsed,
  venue_name,
  venue_city,
  home_score,
  away_score,
  half_time_home_score,
  half_time_away_score
FROM matches
WHERE id = $1`, mappedMatchID).Scan(
		&previousStatus,
		&previousRawStatus,
		&previousRawStatusLabel,
		&previousStatusElapsed,
		&previousVenueName,
		&previousVenueCity,
		&previousHomeScore,
		&previousAwayScore,
		&previousHalfTimeHomeScore,
		&previousHalfTimeAwayScore,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err == nil && isTerminalMatchStatus(previousStatus) {
		if !isTerminalMatchStatus(match.Status) {
			match.Status = previousStatus
			match.RawStatus = previousRawStatus.String
			match.RawStatusLabel = previousRawStatusLabel.String
			match.StatusElapsed = nullableInt(previousStatusElapsed)
			match.HomeScore = nullableInt(previousHomeScore)
			match.AwayScore = nullableInt(previousAwayScore)
			match.HalfTimeHomeScore = nullableInt(previousHalfTimeHomeScore)
			match.HalfTimeAwayScore = nullableInt(previousHalfTimeAwayScore)
		} else {
			if strings.TrimSpace(match.RawStatus) == "" {
				match.RawStatus = previousRawStatus.String
			}
			if strings.TrimSpace(match.RawStatusLabel) == "" {
				match.RawStatusLabel = previousRawStatusLabel.String
			}
			if match.StatusElapsed == nil {
				match.StatusElapsed = nullableInt(previousStatusElapsed)
			}
			if match.HomeScore == nil {
				match.HomeScore = nullableInt(previousHomeScore)
			}
			if match.AwayScore == nil {
				match.AwayScore = nullableInt(previousAwayScore)
			}
			if match.HalfTimeHomeScore == nil {
				match.HalfTimeHomeScore = nullableInt(previousHalfTimeHomeScore)
			}
			if match.HalfTimeAwayScore == nil {
				match.HalfTimeAwayScore = nullableInt(previousHalfTimeAwayScore)
			}
		}
		if strings.TrimSpace(match.VenueName) == "" {
			match.VenueName = previousVenueName.String
		}
		if strings.TrimSpace(match.VenueCity) == "" {
			match.VenueCity = previousVenueCity.String
		}
	}

	return r.InsertMatch(ctx, match)
}

// ReconcileFixtureProjection repairs a bounded batch of missing or divergent
// engine fixture projections from their canonical matches. Existing fixture
// season and source labels are retained because matches does not carry an
// authoritative season identifier and the source label is useful provenance.
// The operation is idempotent and can be called repeatedly until it returns 0.
func (r CatalogRepository) ReconcileFixtureProjection(ctx context.Context, limit int) (int, error) {
	if r.db == nil {
		return 0, errors.New("catalog DB is nil")
	}
	if limit <= 0 || limit > MaxFixtureProjectionReconcileBatch {
		return 0, fmt.Errorf("fixture projection reconciliation limit must be between 1 and %d", MaxFixtureProjectionReconcileBatch)
	}

	rows, err := r.db.QueryContext(ctx, `
SELECT
  m.id,
  m.league_id,
  m.home_team_id,
  m.away_team_id,
  m.kickoff_at,
  m.status,
  m.venue_name,
  m.home_score,
  m.away_score,
  m.half_time_home_score,
  m.half_time_away_score
FROM matches AS m
LEFT JOIN fixtures AS f ON f.id = m.id
WHERE f.id IS NULL
   OR f.league_id IS DISTINCT FROM m.league_id
   OR f.home_team_id IS DISTINCT FROM m.home_team_id
   OR f.away_team_id IS DISTINCT FROM m.away_team_id
   OR f.kickoff_at IS DISTINCT FROM m.kickoff_at
   OR f.status IS DISTINCT FROM m.status
   OR f.venue_name IS DISTINCT FROM NULLIF(trim(m.venue_name), '')
   OR f.home_goals_ft IS DISTINCT FROM m.home_score
   OR f.away_goals_ft IS DISTINCT FROM m.away_score
   OR f.home_goals_ht IS DISTINCT FROM m.half_time_home_score
   OR f.away_goals_ht IS DISTINCT FROM m.half_time_away_score
   OR f.source_match_id IS DISTINCT FROM m.id
ORDER BY m.id
LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}

	type projection struct {
		matchID int64
		match   MatchRecord
	}
	batch := make([]projection, 0, limit)
	for rows.Next() {
		var item projection
		var venue sql.NullString
		var homeScore sql.NullInt64
		var awayScore sql.NullInt64
		var halfTimeHomeScore sql.NullInt64
		var halfTimeAwayScore sql.NullInt64
		if err := rows.Scan(
			&item.matchID,
			&item.match.LeagueID,
			&item.match.HomeTeamID,
			&item.match.AwayTeamID,
			&item.match.KickoffAt,
			&item.match.Status,
			&venue,
			&homeScore,
			&awayScore,
			&halfTimeHomeScore,
			&halfTimeAwayScore,
		); err != nil {
			rows.Close()
			return 0, err
		}
		item.match.SeasonID = item.match.KickoffAt.UTC().Year()
		item.match.VenueName = venue.String
		item.match.HomeScore = nullableInt(homeScore)
		item.match.AwayScore = nullableInt(awayScore)
		item.match.HalfTimeHomeScore = nullableInt(halfTimeHomeScore)
		item.match.HalfTimeAwayScore = nullableInt(halfTimeAwayScore)
		batch = append(batch, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	processed := 0
	for _, item := range batch {
		if err := r.upsertReconciledFixture(ctx, item.matchID, item.match); err != nil {
			return processed, err
		}
		processed++
	}
	return processed, nil
}

func (r CatalogRepository) upsertReconciledFixture(ctx context.Context, matchID int64, match MatchRecord) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO fixtures (
  id,
  league_id,
  season_id,
  home_team_id,
  away_team_id,
  kickoff_at,
  status,
  venue_name,
  home_goals_ft,
  away_goals_ft,
  home_goals_ht,
  away_goals_ht,
  source_match_id,
  source,
  finished_at,
  updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $1, 'matches_reconciliation',
  CASE WHEN lower(trim($7)) IN ('finished','ft','aet','pen','awarded','walkover','canceled','cancelled','abandoned') THEN CURRENT_TIMESTAMP ELSE NULL END,
  CURRENT_TIMESTAMP)
ON CONFLICT (id)
DO UPDATE SET
  league_id = EXCLUDED.league_id,
  home_team_id = EXCLUDED.home_team_id,
  away_team_id = EXCLUDED.away_team_id,
  kickoff_at = EXCLUDED.kickoff_at,
  status = EXCLUDED.status,
  venue_name = EXCLUDED.venue_name,
  home_goals_ft = EXCLUDED.home_goals_ft,
  away_goals_ft = EXCLUDED.away_goals_ft,
  home_goals_ht = EXCLUDED.home_goals_ht,
  away_goals_ht = EXCLUDED.away_goals_ht,
  source_match_id = EXCLUDED.source_match_id,
  finished_at = CASE
    WHEN fixtures.finished_at IS NOT NULL THEN fixtures.finished_at
    WHEN lower(trim(EXCLUDED.status)) IN ('finished','ft','aet','pen','awarded','walkover','canceled','cancelled','abandoned') THEN CURRENT_TIMESTAMP
    ELSE NULL
  END,
  updated_at = CURRENT_TIMESTAMP`,
		matchID,
		match.LeagueID,
		matchSeasonID(match),
		match.HomeTeamID,
		match.AwayTeamID,
		match.KickoffAt.UTC(),
		match.Status,
		nullableText(match.VenueName),
		match.HomeScore,
		match.AwayScore,
		match.HalfTimeHomeScore,
		match.HalfTimeAwayScore,
	)
	return err
}

func isTerminalMatchStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "finished", "ft", "aet", "pen":
		return true
	default:
		return false
	}
}

func nullableInt(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	converted := int(value.Int64)
	return &converted
}

func (r CatalogRepository) upsertEngineFixture(ctx context.Context, matchID int64, match MatchRecord) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO fixtures (
  id,
  league_id,
  season_id,
  home_team_id,
  away_team_id,
  kickoff_at,
  status,
  venue_name,
  home_goals_ft,
  away_goals_ft,
  home_goals_ht,
  away_goals_ht,
  source_match_id,
  source,
  finished_at,
  updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $1, $13,
  CASE WHEN lower(trim($7)) IN ('finished','ft','aet','pen','awarded','walkover','canceled','cancelled','abandoned') THEN CURRENT_TIMESTAMP ELSE NULL END,
  CURRENT_TIMESTAMP)
ON CONFLICT (id)
DO UPDATE SET
  league_id = EXCLUDED.league_id,
  season_id = EXCLUDED.season_id,
  home_team_id = EXCLUDED.home_team_id,
  away_team_id = EXCLUDED.away_team_id,
  kickoff_at = EXCLUDED.kickoff_at,
  status = EXCLUDED.status,
  venue_name = COALESCE(EXCLUDED.venue_name, fixtures.venue_name),
  home_goals_ft = EXCLUDED.home_goals_ft,
  away_goals_ft = EXCLUDED.away_goals_ft,
  home_goals_ht = EXCLUDED.home_goals_ht,
  away_goals_ht = EXCLUDED.away_goals_ht,
  source_match_id = EXCLUDED.source_match_id,
  source = EXCLUDED.source,
  finished_at = CASE
    WHEN fixtures.finished_at IS NOT NULL THEN fixtures.finished_at
    WHEN lower(trim(EXCLUDED.status)) IN ('finished','ft','aet','pen','awarded','walkover','canceled','cancelled','abandoned') THEN CURRENT_TIMESTAMP
    ELSE NULL
  END,
  updated_at = CURRENT_TIMESTAMP`,
		matchID,
		match.LeagueID,
		matchSeasonID(match),
		match.HomeTeamID,
		match.AwayTeamID,
		match.KickoffAt.UTC(),
		match.Status,
		nullableText(match.VenueName),
		match.HomeScore,
		match.AwayScore,
		match.HalfTimeHomeScore,
		match.HalfTimeAwayScore,
		nonEmpty(match.Source, "provider_ingestion"),
	)
	return err
}

func matchSeasonID(match MatchRecord) int {
	if match.SeasonID > 0 {
		return match.SeasonID
	}
	return match.KickoffAt.UTC().Year()
}

func ParseCanonicalID(value string) (int64, error) {
	canonicalID, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || canonicalID <= 0 {
		return 0, fmt.Errorf("invalid canonical entity id %q", value)
	}
	return canonicalID, nil
}

func (r CatalogRepository) ensureSport(ctx context.Context, slug, name string) (int64, error) {
	var sportID int64
	err := r.db.QueryRowContext(ctx, `
INSERT INTO sports (slug, name)
VALUES ($1, $2)
ON CONFLICT (slug)
DO UPDATE SET name = EXCLUDED.name
RETURNING id`,
		slug,
		name,
	).Scan(&sportID)
	return sportID, err
}

func validatePlayerRecord(player PlayerRecord) error {
	if player.ID <= 0 {
		return errors.New("player id is required")
	}
	if player.TeamID <= 0 {
		return errors.New("player team id is required")
	}
	if strings.TrimSpace(player.Name) == "" {
		return errors.New("player name is required")
	}
	return nil
}

func validateVenueRecord(venue VenueRecord) error {
	if venue.ID <= 0 {
		return errors.New("venue id is required")
	}
	if strings.TrimSpace(venue.Name) == "" {
		return errors.New("venue name is required")
	}
	return nil
}

func validateMatchRecord(match MatchRecord) error {
	if match.LeagueID <= 0 {
		return errors.New("league id is required")
	}
	if match.HomeTeamID <= 0 {
		return errors.New("home team id is required")
	}
	if match.AwayTeamID <= 0 {
		return errors.New("away team id is required")
	}
	if match.KickoffAt.IsZero() {
		return errors.New("kickoff time is required")
	}
	if strings.TrimSpace(match.Status) == "" {
		return errors.New("match status is required")
	}
	return nil
}

func (r CatalogRepository) resolveCountryCode(ctx context.Context, code string, name string) (string, error) {
	countryCode := strings.TrimSpace(strings.ToUpper(code))
	countryName := strings.TrimSpace(name)
	if sovereignCode, ok := subdivisionCountryCode(countryCode, countryName); ok {
		var existing string
		err := r.db.QueryRowContext(ctx, `SELECT code FROM countries WHERE code = $1 LIMIT 1`, sovereignCode).Scan(&existing)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return existing, nil
	}
	if len(countryCode) == 2 {
		var existing string
		err := r.db.QueryRowContext(ctx, `SELECT code FROM countries WHERE code = $1 LIMIT 1`, countryCode).Scan(&existing)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return existing, nil
	}
	if countryName == "" {
		return "", nil
	}
	var existing string
	err := r.db.QueryRowContext(ctx, `SELECT code FROM countries WHERE lower(name) = lower($1) LIMIT 1`, countryName).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		alias := normalizeCountryName(countryName)
		if alias == "" || alias == countryName {
			return "", nil
		}
		err = r.db.QueryRowContext(ctx, `SELECT code FROM countries WHERE lower(name) = lower($1) LIMIT 1`, alias).Scan(&existing)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return existing, nil
	}
	if err != nil {
		return "", err
	}
	return existing, nil
}

func normalizeCountryName(value string) string {
	trimmed := strings.TrimSpace(value)
	switch strings.ToLower(trimmed) {
	case "england", "scotland", "wales", "northern-ireland":
		return "United-Kingdom"
	default:
		return trimmed
	}
}

func nullableText(value string) any {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

func nullablePositiveInt(value int) any {
	if value <= 0 {
		return nil
	}
	return value
}

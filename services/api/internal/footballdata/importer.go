package footballdata

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

const (
	providerKey       = "football-data"
	footballSportSlug = "football"
)

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

type Options struct {
	Path            string
	Season          string
	ReplaceExisting bool
}

type Report struct {
	ParsedFiles     int
	ImportedMatches int
	SkippedRows     int
	RowErrors       int
	FailedFiles     []FileError
}

type FileError struct {
	Path  string
	Error string
}

type matchRecord struct {
	ExternalID string
	SourceKey  string

	CountryName string
	CountryCode string
	LeagueCode  string
	LeagueName  string
	SeasonLabel string
	SeasonID    int
	KickoffAt   time.Time

	HomeTeam string
	AwayTeam string

	HomeGoals int
	AwayGoals int
	HomeHT    *int
	AwayHT    *int

	Referee string

	HomeShotsTotal    *int
	AwayShotsTotal    *int
	HomeShotsOnTarget *int
	AwayShotsOnTarget *int
	HomeCorners       *int
	AwayCorners       *int
	HomeFouls         *int
	AwayFouls         *int
	HomeOffsides      *int
	AwayOffsides      *int
	HomeYellowCards   *int
	AwayYellowCards   *int
	HomeRedCards      *int
	AwayRedCards      *int
	HomeCardPoints    *int
	AwayCardPoints    *int

	OddsHome *float64
	OddsDraw *float64
	OddsAway *float64
}

func Import(ctx context.Context, db *sql.DB, opts Options) (Report, error) {
	if db == nil {
		return Report{}, errors.New("football-data import DB is nil")
	}
	if strings.TrimSpace(opts.Path) == "" {
		return Report{}, errors.New("football-data path is required")
	}

	paths, err := csvPaths(opts.Path, opts.Season)
	if err != nil {
		return Report{}, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback()

	if err := ensureProvider(ctx, tx); err != nil {
		return Report{}, err
	}
	if opts.ReplaceExisting {
		if err := cleanupExistingImport(ctx, tx, seasonIDFromLabel(opts.Season, time.Time{})); err != nil {
			return Report{}, err
		}
	}

	report := Report{}
	for _, path := range paths {
		matches, fileReport := parseFile(path)
		if fileReport.Error != "" {
			report.FailedFiles = append(report.FailedFiles, FileError{Path: path, Error: fileReport.Error})
			continue
		}
		report.ParsedFiles++
		report.SkippedRows += fileReport.SkippedRows
		report.RowErrors += fileReport.RowErrors

		for _, match := range matches {
			if opts.Season != "" && match.SeasonLabel != opts.Season {
				continue
			}
			if err := importMatch(ctx, tx, match); err != nil {
				return report, fmt.Errorf("import %s: %w", match.SourceKey, err)
			}
			report.ImportedMatches++
		}
	}

	if err := refreshDerivedTables(ctx, tx); err != nil {
		return report, err
	}
	if err := tx.Commit(); err != nil {
		return report, err
	}
	return report, nil
}

func csvPaths(root string, season string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{root}, nil
	}

	walkRoot := root
	mainRoot := filepath.Join(root, "raw", "main")
	if mainInfo, err := os.Stat(mainRoot); err == nil && mainInfo.IsDir() {
		walkRoot = mainRoot
	}

	var paths []string
	err = filepath.WalkDir(walkRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), "_combined") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".csv") {
			if season != "" && !pathHasSegment(path, season) {
				return nil
			}
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

type parseReport struct {
	Error       string
	SkippedRows int
	RowErrors   int
}

func parseFile(path string) ([]matchRecord, parseReport) {
	rows, err := readRows(path)
	if err != nil {
		return nil, parseReport{Error: err.Error()}
	}

	var matches []matchRecord
	report := parseReport{}
	for rowNumber, row := range rows {
		if emptyRow(row) {
			continue
		}
		match, ok, err := parseRow(path, rowNumber, row)
		if err != nil {
			report.RowErrors++
			continue
		}
		if !ok {
			report.SkippedRows++
			continue
		}
		matches = append(matches, match)
	}
	return matches, report
}

func readRows(path string) ([]map[string]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	reader := io.Reader(bytes.NewReader(body))
	if !utf8.Valid(body) {
		reader = transform.NewReader(bytes.NewReader(body), charmap.Windows1252.NewDecoder())
	}
	csvReader := csv.NewReader(bufio.NewReader(reader))
	csvReader.FieldsPerRecord = -1
	rawRows, err := csvReader.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rawRows) == 0 {
		return nil, nil
	}
	header := rawRows[0]
	rows := make([]map[string]string, 0, len(rawRows)-1)
	for _, raw := range rawRows[1:] {
		row := make(map[string]string, len(header))
		for index, name := range header {
			name = strings.TrimPrefix(strings.TrimSpace(name), "\ufeff")
			if index >= len(raw) {
				row[name] = ""
				continue
			}
			row[name] = strings.TrimSpace(strings.ReplaceAll(raw[index], "\u00a0", " "))
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func pathHasSegment(path string, segment string) bool {
	for _, part := range splitPath(path) {
		if part == segment {
			return true
		}
	}
	return false
}

func parseRow(path string, rowNumber int, row map[string]string) (matchRecord, bool, error) {
	homeGoalsRaw := first(row, "FTHG", "HG")
	awayGoalsRaw := first(row, "FTAG", "AG")
	result := strings.ToUpper(first(row, "FTR", "Res"))
	if homeGoalsRaw == "" || awayGoalsRaw == "" || (result != "H" && result != "D" && result != "A") {
		return matchRecord{}, false, nil
	}

	homeGoals, err := strconv.Atoi(homeGoalsRaw)
	if err != nil {
		return matchRecord{}, false, err
	}
	awayGoals, err := strconv.Atoi(awayGoalsRaw)
	if err != nil {
		return matchRecord{}, false, err
	}
	kickoff, err := parseKickoff(first(row, "Date"), first(row, "Time"))
	if err != nil {
		return matchRecord{}, false, err
	}

	countryName, countryCode, pathSeason := pathContext(path)
	if first(row, "Country") != "" {
		countryName = first(row, "Country")
		countryCode = countryCodeFor(countryName)
	}
	seasonLabel := first(row, "Season")
	if seasonLabel == "" {
		seasonLabel = pathSeason
	}
	seasonID := seasonIDFromLabel(seasonLabel, kickoff)
	leagueCode := first(row, "Div")
	leagueName := first(row, "League")
	if leagueName == "" {
		leagueName = leagueDisplayName(countryName, leagueCode)
	}
	homeTeam := first(row, "HomeTeam", "Home")
	awayTeam := first(row, "AwayTeam", "Away")
	if homeTeam == "" || awayTeam == "" || countryName == "" || leagueName == "" || seasonID == 0 {
		return matchRecord{}, false, fmt.Errorf("missing required football-data fields in %s row %d", path, rowNumber)
	}

	homeYellow := optionalInt(first(row, "HY"))
	awayYellow := optionalInt(first(row, "AY"))
	homeRed := optionalInt(first(row, "HR"))
	awayRed := optionalInt(first(row, "AR"))

	sourceKey := strings.Join([]string{
		countryName, seasonLabel, leagueCode, kickoff.Format("2006-01-02 15:04"), homeTeam, awayTeam,
	}, "|")
	externalID := shortHash(sourceKey)
	homeOdds, drawOdds, awayOdds := extractOdds(row)

	return matchRecord{
		ExternalID: externalID,
		SourceKey:  sourceKey,

		CountryName: countryName,
		CountryCode: countryCode,
		LeagueCode:  leagueCode,
		LeagueName:  leagueName,
		SeasonLabel: seasonLabel,
		SeasonID:    seasonID,
		KickoffAt:   kickoff,

		HomeTeam: homeTeam,
		AwayTeam: awayTeam,

		HomeGoals: homeGoals,
		AwayGoals: awayGoals,
		HomeHT:    optionalInt(first(row, "HTHG")),
		AwayHT:    optionalInt(first(row, "HTAG")),

		Referee: first(row, "Referee"),

		HomeShotsTotal:    optionalInt(first(row, "HS")),
		AwayShotsTotal:    optionalInt(first(row, "AS")),
		HomeShotsOnTarget: optionalInt(first(row, "HST")),
		AwayShotsOnTarget: optionalInt(first(row, "AST")),
		HomeCorners:       optionalInt(first(row, "HC")),
		AwayCorners:       optionalInt(first(row, "AC")),
		HomeFouls:         optionalInt(first(row, "HF")),
		AwayFouls:         optionalInt(first(row, "AF")),
		HomeOffsides:      optionalInt(first(row, "HO")),
		AwayOffsides:      optionalInt(first(row, "AO")),
		HomeYellowCards:   homeYellow,
		AwayYellowCards:   awayYellow,
		HomeRedCards:      homeRed,
		AwayRedCards:      awayRed,
		HomeCardPoints:    cardPoints(homeYellow, homeRed),
		AwayCardPoints:    cardPoints(awayYellow, awayRed),

		OddsHome: homeOdds,
		OddsDraw: drawOdds,
		OddsAway: awayOdds,
	}, true, nil
}

func importMatch(ctx context.Context, tx *sql.Tx, match matchRecord) error {
	countryCode, err := ensureCountry(ctx, tx, match.CountryCode, match.CountryName)
	if err != nil {
		return err
	}
	leagueID, err := ensureLeague(ctx, tx, match, countryCode)
	if err != nil {
		return err
	}
	homeTeamID, err := ensureTeam(ctx, tx, countryCode, match.HomeTeam)
	if err != nil {
		return err
	}
	awayTeamID, err := ensureTeam(ctx, tx, countryCode, match.AwayTeam)
	if err != nil {
		return err
	}
	matchID, found, err := lookupFixtureMapping(ctx, tx, match.ExternalID)
	if err != nil {
		return err
	}
	if found {
		if err := updateBaseMatch(ctx, tx, matchID, leagueID, homeTeamID, awayTeamID, match); err != nil {
			return err
		}
	} else {
		matchID, err = insertBaseMatch(ctx, tx, leagueID, homeTeamID, awayTeamID, match)
		if err != nil {
			return err
		}
		if err := upsertFixtureMapping(ctx, tx, match.ExternalID, match.SourceKey, matchID); err != nil {
			return err
		}
	}
	if err := upsertFixture(ctx, tx, matchID, leagueID, homeTeamID, awayTeamID, match); err != nil {
		return err
	}
	if err := upsertFixtureStats(ctx, tx, matchID, homeTeamID, true, match); err != nil {
		return err
	}
	if err := upsertFixtureStats(ctx, tx, matchID, awayTeamID, false, match); err != nil {
		return err
	}
	return refreshOdds(ctx, tx, matchID, match)
}

func ensureProvider(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO providers (provider, display_name, base_url, enabled)
VALUES ($1, $2, $3, true)
ON CONFLICT (provider) DO UPDATE SET
  display_name = EXCLUDED.display_name,
  base_url = EXCLUDED.base_url,
  enabled = true,
  updated_at = CURRENT_TIMESTAMP`,
		providerKey,
		"Football-Data",
		"https://www.football-data.co.uk",
	)
	return err
}

func cleanupExistingImport(ctx context.Context, tx *sql.Tx, seasonID int) error {
	query := `
SELECT pem.id, pem.canonical_entity_id
FROM provider_entity_maps AS pem
JOIN fixtures AS f ON f.id = CAST(pem.canonical_entity_id AS BIGINT)
WHERE pem.provider = $1
  AND pem.entity_type = 'fixture'
  AND pem.retired_at IS NULL
  AND f.source = 'football_data_import'`
	args := []any{providerKey}
	if seasonID > 0 {
		query += " AND f.season_id = $2"
		args = append(args, seasonID)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	type mappedFixture struct {
		mapID   int64
		matchID int64
	}
	var fixtures []mappedFixture
	for rows.Next() {
		var mapID int64
		var canonicalID string
		if err := rows.Scan(&mapID, &canonicalID); err != nil {
			return err
		}
		matchID, err := strconv.ParseInt(canonicalID, 10, 64)
		if err != nil || matchID <= 0 {
			continue
		}
		fixtures = append(fixtures, mappedFixture{mapID: mapID, matchID: matchID})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, fixture := range fixtures {
		if _, err := tx.ExecContext(ctx, `DELETE FROM provider_entity_maps WHERE id = $1`, fixture.mapID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM matches WHERE id = $1`, fixture.matchID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
DELETE FROM league_coverage
WHERE NOT EXISTS (
  SELECT 1 FROM fixtures
  WHERE fixtures.league_id = league_coverage.league_id
    AND fixtures.season_id = league_coverage.season_id
)`)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM referee_stats WHERE source = 'football_data_import'`)
	return err
}

func ensureCountry(ctx context.Context, tx *sql.Tx, code string, name string) (string, error) {
	countryCode := strings.ToUpper(strings.TrimSpace(code))
	if len(countryCode) != 2 {
		countryCode = "FD"
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO countries (code, name)
VALUES ($1, $2)
ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name`,
		countryCode,
		name,
	)
	return countryCode, err
}

func ensureSport(ctx context.Context, tx *sql.Tx) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
INSERT INTO sports (slug, name)
VALUES ($1, $2)
ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
RETURNING id`,
		footballSportSlug,
		"Football",
	).Scan(&id)
	return id, err
}

func ensureLeague(ctx context.Context, tx *sql.Tx, match matchRecord, countryCode string) (int64, error) {
	sportID, err := ensureSport(ctx, tx)
	if err != nil {
		return 0, err
	}
	if id, found, err := findExistingLeague(ctx, tx, match.LeagueName, countryCode); err != nil {
		return 0, err
	} else if found {
		return id, nil
	}
	slug := "football-data-" + slugPart(match.CountryName) + "-" + slugPart(match.LeagueCode) + "-" + strconv.Itoa(match.SeasonID)
	var id int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO leagues (sport_id, country_code, slug, name, priority_score)
VALUES ($1, NULLIF($2, ''), $3, $4, 0)
ON CONFLICT (slug) DO UPDATE SET
  sport_id = EXCLUDED.sport_id,
  country_code = COALESCE(EXCLUDED.country_code, leagues.country_code),
  name = EXCLUDED.name
RETURNING id`,
		sportID,
		countryCode,
		slug,
		match.LeagueName,
	).Scan(&id)
	return id, err
}

func findExistingLeague(ctx context.Context, tx *sql.Tx, name string, countryCode string) (int64, bool, error) {
	if strings.TrimSpace(countryCode) == "" {
		return 0, false, nil
	}
	var id int64
	err := tx.QueryRowContext(ctx, `
SELECT id
FROM leagues
WHERE lower(name) = lower($1)
  AND ($2 = '' OR country_code = $2 OR country_code IS NULL)
ORDER BY
  CASE
    WHEN country_code = $2 THEN 0
    WHEN country_code IS NULL THEN 1
    ELSE 2
  END,
  id
LIMIT 1`,
		name,
		countryCode,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

func ensureTeam(ctx context.Context, tx *sql.Tx, countryCode string, name string) (int64, error) {
	if id, found, err := findExistingTeam(ctx, tx, name, countryCode); err != nil {
		return 0, err
	} else if found {
		if countryCode != "" {
			_, _ = tx.ExecContext(ctx, `UPDATE teams SET country_code = COALESCE(country_code, $1) WHERE id = $2`, countryCode, id)
		}
		return id, nil
	}
	slug := "football-data-" + slugPart(name) + "-" + shortHash(name)[:8]
	var id int64
	err := tx.QueryRowContext(ctx, `
INSERT INTO teams (country_code, slug, name)
VALUES (NULLIF($1, ''), $2, $3)
ON CONFLICT (slug) DO UPDATE SET
  country_code = COALESCE(EXCLUDED.country_code, teams.country_code),
  name = EXCLUDED.name
RETURNING id`,
		countryCode,
		slug,
		name,
	).Scan(&id)
	return id, err
}

func findExistingTeam(ctx context.Context, tx *sql.Tx, name string, countryCode string) (int64, bool, error) {
	if strings.TrimSpace(countryCode) == "" {
		return 0, false, nil
	}
	var id int64
	err := tx.QueryRowContext(ctx, `
SELECT id
FROM teams
WHERE lower(name) = lower($1)
  AND ($2 = '' OR country_code = $2 OR country_code IS NULL)
ORDER BY
  CASE
    WHEN country_code = $2 THEN 0
    WHEN country_code IS NULL THEN 1
    ELSE 2
  END,
  id
LIMIT 1`,
		name,
		countryCode,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

func lookupFixtureMapping(ctx context.Context, tx *sql.Tx, externalID string) (int64, bool, error) {
	var value sql.NullString
	err := tx.QueryRowContext(ctx, `
SELECT canonical_entity_id
FROM provider_entity_maps
WHERE provider = $1
  AND entity_type = 'fixture'
  AND provider_entity_id = $2
  AND retired_at IS NULL
LIMIT 1`,
		providerKey,
		externalID,
	).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !value.Valid || value.String == "" {
		return 0, false, nil
	}
	id, err := strconv.ParseInt(value.String, 10, 64)
	if err != nil || id <= 0 {
		return 0, false, fmt.Errorf("invalid football-data canonical fixture id %q", value.String)
	}
	return id, true, nil
}

func upsertFixtureMapping(ctx context.Context, tx *sql.Tx, externalID string, sourceKey string, matchID int64) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO provider_entity_maps (
  provider,
  entity_type,
  provider_entity_id,
  provider_entity_key,
  canonical_entity_type,
  canonical_entity_id,
  confidence,
  match_status,
  metadata,
  first_seen_at,
  last_seen_at
) VALUES ($1, 'fixture', $2, $3, 'match', $4, 1.0, 'matched', '{}', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT (provider, entity_type, provider_entity_id) WHERE retired_at IS NULL
DO UPDATE SET
  provider_entity_key = EXCLUDED.provider_entity_key,
  canonical_entity_type = EXCLUDED.canonical_entity_type,
  canonical_entity_id = EXCLUDED.canonical_entity_id,
  confidence = EXCLUDED.confidence,
  match_status = EXCLUDED.match_status,
  last_seen_at = CURRENT_TIMESTAMP`,
		providerKey,
		externalID,
		sourceKey,
		strconv.FormatInt(matchID, 10),
	)
	return err
}

func insertBaseMatch(ctx context.Context, tx *sql.Tx, leagueID int64, homeTeamID int64, awayTeamID int64, match matchRecord) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
INSERT INTO matches (
  league_id, home_team_id, away_team_id, kickoff_at, status,
  provider_status_raw, provider_status_label, venue_name, home_score, away_score,
  half_time_home_score, half_time_away_score
) VALUES ($1, $2, $3, $4, 'finished', 'FT', 'Match Finished', '', $5, $6, $7, $8)
RETURNING id`,
		leagueID,
		homeTeamID,
		awayTeamID,
		match.KickoffAt.UTC(),
		match.HomeGoals,
		match.AwayGoals,
		match.HomeHT,
		match.AwayHT,
	).Scan(&id)
	return id, err
}

func updateBaseMatch(ctx context.Context, tx *sql.Tx, matchID int64, leagueID int64, homeTeamID int64, awayTeamID int64, match matchRecord) error {
	_, err := tx.ExecContext(ctx, `
UPDATE matches
SET
  league_id = $1,
  home_team_id = $2,
  away_team_id = $3,
  kickoff_at = $4,
  status = 'finished',
  provider_status_raw = 'FT',
  provider_status_label = 'Match Finished',
  home_score = $5,
  away_score = $6,
  half_time_home_score = $7,
  half_time_away_score = $8
WHERE id = $9`,
		leagueID,
		homeTeamID,
		awayTeamID,
		match.KickoffAt.UTC(),
		match.HomeGoals,
		match.AwayGoals,
		match.HomeHT,
		match.AwayHT,
		matchID,
	)
	return err
}

func upsertFixture(ctx context.Context, tx *sql.Tx, matchID int64, leagueID int64, homeTeamID int64, awayTeamID int64, match matchRecord) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO fixtures (
  id, league_id, season_id, home_team_id, away_team_id, kickoff_at, status,
  venue_name, referee, round, home_goals_ft, away_goals_ft, home_goals_ht, away_goals_ht,
  source_match_id, source, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, 'finished', NULL, NULLIF($7, ''), NULL, $8, $9, $10, $11, $1, 'football_data_import', CURRENT_TIMESTAMP)
ON CONFLICT (id) DO UPDATE SET
  league_id = EXCLUDED.league_id,
  season_id = EXCLUDED.season_id,
  home_team_id = EXCLUDED.home_team_id,
  away_team_id = EXCLUDED.away_team_id,
  kickoff_at = EXCLUDED.kickoff_at,
  status = EXCLUDED.status,
  referee = EXCLUDED.referee,
  home_goals_ft = EXCLUDED.home_goals_ft,
  away_goals_ft = EXCLUDED.away_goals_ft,
  home_goals_ht = EXCLUDED.home_goals_ht,
  away_goals_ht = EXCLUDED.away_goals_ht,
  source_match_id = EXCLUDED.source_match_id,
  source = EXCLUDED.source,
  updated_at = CURRENT_TIMESTAMP`,
		matchID,
		leagueID,
		match.SeasonID,
		homeTeamID,
		awayTeamID,
		match.KickoffAt.UTC(),
		match.Referee,
		match.HomeGoals,
		match.AwayGoals,
		match.HomeHT,
		match.AwayHT,
	)
	return err
}

func upsertFixtureStats(ctx context.Context, tx *sql.Tx, matchID int64, teamID int64, home bool, match matchRecord) error {
	shotsTotal, shotsOnTarget := match.HomeShotsTotal, match.HomeShotsOnTarget
	corners, fouls, offsides := match.HomeCorners, match.HomeFouls, match.HomeOffsides
	yellow, red, cardPoints := match.HomeYellowCards, match.HomeRedCards, match.HomeCardPoints
	if !home {
		shotsTotal, shotsOnTarget = match.AwayShotsTotal, match.AwayShotsOnTarget
		corners, fouls, offsides = match.AwayCorners, match.AwayFouls, match.AwayOffsides
		yellow, red, cardPoints = match.AwayYellowCards, match.AwayRedCards, match.AwayCardPoints
	}

	_, err := tx.ExecContext(ctx, `
INSERT INTO fixture_statistics (
  fixture_id, team_id, shots_on_target, shots_total, corners, fouls, offsides,
  yellow_cards, red_cards, card_points, source, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'football_data_import', CURRENT_TIMESTAMP)
ON CONFLICT (fixture_id, team_id) DO UPDATE SET
  shots_on_target = EXCLUDED.shots_on_target,
  shots_total = EXCLUDED.shots_total,
  corners = EXCLUDED.corners,
  fouls = EXCLUDED.fouls,
  offsides = EXCLUDED.offsides,
  yellow_cards = EXCLUDED.yellow_cards,
  red_cards = EXCLUDED.red_cards,
  card_points = EXCLUDED.card_points,
  source = EXCLUDED.source,
  updated_at = CURRENT_TIMESTAMP`,
		matchID,
		teamID,
		shotsOnTarget,
		shotsTotal,
		corners,
		fouls,
		offsides,
		yellow,
		red,
		cardPoints,
	)
	return err
}

func refreshOdds(ctx context.Context, tx *sql.Tx, matchID int64, match matchRecord) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM odds WHERE fixture_id = $1 AND bookmaker = 'Football-Data' AND market = '1x2'`, matchID)
	if err != nil {
		return err
	}
	values := []struct {
		outcome string
		value   *float64
	}{
		{outcome: "Home", value: match.OddsHome},
		{outcome: "Draw", value: match.OddsDraw},
		{outcome: "Away", value: match.OddsAway},
	}
	for _, item := range values {
		if item.value == nil || *item.value <= 0 {
			continue
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO odds (fixture_id, bookmaker, market, outcome, value, implied_prob, fetched_at)
VALUES ($1, 'Football-Data', '1x2', $2, $3, $4, $5)`,
			matchID,
			item.outcome,
			*item.value,
			1/(*item.value),
			match.KickoffAt.UTC(),
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func refreshDerivedTables(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range derivedStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

var derivedStatements = []string{
	teamSeasonStatsSQL,
	headToHeadSQL,
	standingsSQL,
	refereeStatsSQL,
	leagueCoverageSQL,
}

const teamSeasonStatsSQL = `
WITH fixture_teams AS (
  SELECT DISTINCT home_team_id AS team_id, league_id, season_id FROM fixtures
  UNION
  SELECT DISTINCT away_team_id AS team_id, league_id, season_id FROM fixtures
),
home_stats AS (
  SELECT
    home_team_id AS team_id,
    league_id,
    season_id,
    COUNT(*) FILTER (WHERE home_goals_ft IS NOT NULL AND away_goals_ft IS NOT NULL) AS played_home,
    COUNT(*) FILTER (WHERE home_goals_ft > away_goals_ft) AS wins_home,
    COUNT(*) FILTER (WHERE home_goals_ft = away_goals_ft AND home_goals_ft IS NOT NULL) AS draws_home,
    COUNT(*) FILTER (WHERE home_goals_ft < away_goals_ft) AS losses_home,
    COALESCE(SUM(home_goals_ft) FILTER (WHERE home_goals_ft IS NOT NULL), 0) AS goals_for_home,
    COALESCE(SUM(away_goals_ft) FILTER (WHERE away_goals_ft IS NOT NULL), 0) AS goals_against_home,
    COUNT(*) FILTER (WHERE away_goals_ft = 0 AND home_goals_ft IS NOT NULL) AS clean_sheets_home,
    COUNT(*) FILTER (WHERE home_goals_ft = 0 AND away_goals_ft IS NOT NULL) AS failed_to_score_home
  FROM fixtures
  GROUP BY home_team_id, league_id, season_id
),
away_stats AS (
  SELECT
    away_team_id AS team_id,
    league_id,
    season_id,
    COUNT(*) FILTER (WHERE home_goals_ft IS NOT NULL AND away_goals_ft IS NOT NULL) AS played_away,
    COUNT(*) FILTER (WHERE away_goals_ft > home_goals_ft) AS wins_away,
    COUNT(*) FILTER (WHERE away_goals_ft = home_goals_ft AND away_goals_ft IS NOT NULL) AS draws_away,
    COUNT(*) FILTER (WHERE away_goals_ft < home_goals_ft) AS losses_away,
    COALESCE(SUM(away_goals_ft) FILTER (WHERE away_goals_ft IS NOT NULL), 0) AS goals_for_away,
    COALESCE(SUM(home_goals_ft) FILTER (WHERE home_goals_ft IS NOT NULL), 0) AS goals_against_away,
    COUNT(*) FILTER (WHERE home_goals_ft = 0 AND away_goals_ft IS NOT NULL) AS clean_sheets_away,
    COUNT(*) FILTER (WHERE away_goals_ft = 0 AND home_goals_ft IS NOT NULL) AS failed_to_score_away
  FROM fixtures
  GROUP BY away_team_id, league_id, season_id
)
INSERT INTO team_season_stats (
  team_id, league_id, season_id,
  played_home, wins_home, draws_home, losses_home, goals_for_home, goals_against_home, clean_sheets_home, failed_to_score_home,
  played_away, wins_away, draws_away, losses_away, goals_for_away, goals_against_away, clean_sheets_away, failed_to_score_away,
  source, updated_at
)
SELECT
  ft.team_id, ft.league_id, ft.season_id,
  COALESCE(hs.played_home, 0), COALESCE(hs.wins_home, 0), COALESCE(hs.draws_home, 0), COALESCE(hs.losses_home, 0),
  COALESCE(hs.goals_for_home, 0), COALESCE(hs.goals_against_home, 0), COALESCE(hs.clean_sheets_home, 0), COALESCE(hs.failed_to_score_home, 0),
  COALESCE(asx.played_away, 0), COALESCE(asx.wins_away, 0), COALESCE(asx.draws_away, 0), COALESCE(asx.losses_away, 0),
  COALESCE(asx.goals_for_away, 0), COALESCE(asx.goals_against_away, 0), COALESCE(asx.clean_sheets_away, 0), COALESCE(asx.failed_to_score_away, 0),
  'derived', CURRENT_TIMESTAMP
FROM fixture_teams AS ft
LEFT JOIN home_stats AS hs ON hs.team_id = ft.team_id AND hs.league_id = ft.league_id AND hs.season_id = ft.season_id
LEFT JOIN away_stats AS asx ON asx.team_id = ft.team_id AND asx.league_id = ft.league_id AND asx.season_id = ft.season_id
WHERE 1 = 1
ON CONFLICT (team_id, league_id, season_id) DO UPDATE SET
  played_home = EXCLUDED.played_home,
  wins_home = EXCLUDED.wins_home,
  draws_home = EXCLUDED.draws_home,
  losses_home = EXCLUDED.losses_home,
  goals_for_home = EXCLUDED.goals_for_home,
  goals_against_home = EXCLUDED.goals_against_home,
  clean_sheets_home = EXCLUDED.clean_sheets_home,
  failed_to_score_home = EXCLUDED.failed_to_score_home,
  played_away = EXCLUDED.played_away,
  wins_away = EXCLUDED.wins_away,
  draws_away = EXCLUDED.draws_away,
  losses_away = EXCLUDED.losses_away,
  goals_for_away = EXCLUDED.goals_for_away,
  goals_against_away = EXCLUDED.goals_against_away,
  clean_sheets_away = EXCLUDED.clean_sheets_away,
  failed_to_score_away = EXCLUDED.failed_to_score_away,
  source = EXCLUDED.source,
  updated_at = CURRENT_TIMESTAMP`

const headToHeadSQL = `
INSERT INTO head_to_head (
  home_team_id, away_team_id, fixture_id, played_at, home_goals, away_goals, venue, source
)
SELECT home_team_id, away_team_id, id, kickoff_at, home_goals_ft, away_goals_ft, venue_name, source
FROM fixtures
WHERE home_goals_ft IS NOT NULL AND away_goals_ft IS NOT NULL
ON CONFLICT (fixture_id) DO UPDATE SET
  home_team_id = EXCLUDED.home_team_id,
  away_team_id = EXCLUDED.away_team_id,
  played_at = EXCLUDED.played_at,
  home_goals = EXCLUDED.home_goals,
  away_goals = EXCLUDED.away_goals,
  venue = EXCLUDED.venue,
  source = EXCLUDED.source`

const standingsSQL = `
WITH totals AS (
  SELECT
    team_id, league_id, season_id,
    played_home + played_away AS played,
    wins_home + wins_away AS wins,
    draws_home + draws_away AS draws,
    losses_home + losses_away AS losses,
    goals_for_home + goals_for_away AS goals_for,
    goals_against_home + goals_against_away AS goals_against
  FROM team_season_stats
),
ranked AS (
  SELECT
    league_id, season_id, team_id,
    RANK() OVER (
      PARTITION BY league_id, season_id
      ORDER BY ((wins * 3) + draws) DESC, (goals_for - goals_against) DESC, goals_for DESC, team_id
    ) AS rank,
    ((wins * 3) + draws) AS points,
    played, wins, draws, losses, goals_for, goals_against, (goals_for - goals_against) AS goal_diff
  FROM totals
)
INSERT INTO standings (
  league_id, season_id, team_id, rank, points, played, wins, draws, losses, goals_for, goals_against, goal_diff, form, source, updated_at
)
SELECT league_id, season_id, team_id, rank, points, played, wins, draws, losses, goals_for, goals_against, goal_diff, NULL, 'derived', CURRENT_TIMESTAMP
FROM ranked
WHERE 1 = 1
ON CONFLICT (league_id, season_id, team_id) DO UPDATE SET
  rank = EXCLUDED.rank,
  points = EXCLUDED.points,
  played = EXCLUDED.played,
  wins = EXCLUDED.wins,
  draws = EXCLUDED.draws,
  losses = EXCLUDED.losses,
  goals_for = EXCLUDED.goals_for,
  goals_against = EXCLUDED.goals_against,
  goal_diff = EXCLUDED.goal_diff,
  form = EXCLUDED.form,
  source = EXCLUDED.source,
  updated_at = CURRENT_TIMESTAMP`

const refereeStatsSQL = `
WITH per_fixture AS (
  SELECT
    f.id,
    f.referee AS name,
    SUM(COALESCE(fs.yellow_cards, 0)) AS yellow_cards,
    SUM(COALESCE(fs.red_cards, 0)) AS red_cards,
    SUM(fs.fouls) AS fouls
  FROM fixtures AS f
  JOIN fixture_statistics AS fs ON fs.fixture_id = f.id
  WHERE f.referee IS NOT NULL AND f.referee <> ''
  GROUP BY f.id, f.referee
)
INSERT INTO referee_stats (
  name, matches_officiated, avg_yellow_cards, avg_red_cards, avg_fouls, source, updated_at
)
SELECT
  name,
  COUNT(*),
  AVG(yellow_cards),
  AVG(red_cards),
  AVG(fouls),
  'football_data_import',
  CURRENT_TIMESTAMP
FROM per_fixture
GROUP BY name
ON CONFLICT (name) DO UPDATE SET
  matches_officiated = EXCLUDED.matches_officiated,
  avg_yellow_cards = EXCLUDED.avg_yellow_cards,
  avg_red_cards = EXCLUDED.avg_red_cards,
  avg_fouls = EXCLUDED.avg_fouls,
  source = EXCLUDED.source,
  updated_at = CURRENT_TIMESTAMP`

const leagueCoverageSQL = `
WITH fixture_league_seasons AS (
  SELECT DISTINCT league_id, season_id
  FROM fixtures
)
INSERT INTO league_coverage (
  league_id, season_id, has_statistics, has_xg, has_injuries, has_odds, has_lineups, source, updated_at
)
SELECT
  fls.league_id,
  fls.season_id,
  EXISTS (
    SELECT 1
    FROM fixtures AS f
    JOIN fixture_statistics AS fs ON fs.fixture_id = f.id
    WHERE f.league_id = fls.league_id
      AND f.season_id = fls.season_id
      AND (
        fs.shots_on_target IS NOT NULL OR fs.shots_total IS NOT NULL OR fs.corners IS NOT NULL
        OR fs.fouls IS NOT NULL OR fs.offsides IS NOT NULL OR fs.possession_pct IS NOT NULL
      )
  ),
  EXISTS (
    SELECT 1
    FROM fixtures AS f
    JOIN fixture_statistics AS fs ON fs.fixture_id = f.id
    WHERE f.league_id = fls.league_id
      AND f.season_id = fls.season_id
      AND fs.xg IS NOT NULL
  ),
  false,
  EXISTS (
    SELECT 1
    FROM fixtures AS f
    JOIN odds AS o ON o.fixture_id = f.id
    WHERE f.league_id = fls.league_id
      AND f.season_id = fls.season_id
  ),
  false,
  'derived',
  CURRENT_TIMESTAMP
FROM fixture_league_seasons AS fls
WHERE 1 = 1
ON CONFLICT (league_id, season_id) DO UPDATE SET
  has_statistics = EXCLUDED.has_statistics,
  has_xg = EXCLUDED.has_xg,
  has_injuries = EXCLUDED.has_injuries,
  has_odds = EXCLUDED.has_odds,
  has_lineups = EXCLUDED.has_lineups,
  source = EXCLUDED.source,
  updated_at = CURRENT_TIMESTAMP`

func first(row map[string]string, names ...string) string {
	for _, name := range names {
		value := strings.TrimSpace(row[name])
		if value != "" {
			return value
		}
	}
	return ""
}

func optionalInt(value string) *int {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	return &parsed
}

func optionalFloat(value string) *float64 {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || parsed <= 1 {
		return nil
	}
	return &parsed
}

// cardPoints is the cards shown to one team: yellow + red. It matches the
// worker's statistics job and the prediction engine, which forecasts and
// settles total_cards lines on this unit. (It was yellow + 2*red while the
// worker wrote yellow*10 + red*25; the mixed scales are why the engine now
// derives the count from the raw columns.)
func cardPoints(yellow *int, red *int) *int {
	if yellow == nil && red == nil {
		return nil
	}
	total := 0
	if yellow != nil {
		total += *yellow
	}
	if red != nil {
		total += *red
	}
	return &total
}

func extractOdds(row map[string]string) (*float64, *float64, *float64) {
	groups := [][3]string{
		{"PSCH", "PSCD", "PSCA"},
		{"AvgCH", "AvgCD", "AvgCA"},
		{"MaxCH", "MaxCD", "MaxCA"},
		{"B365CH", "B365CD", "B365CA"},
		{"PSH", "PSD", "PSA"},
		{"AvgH", "AvgD", "AvgA"},
		{"MaxH", "MaxD", "MaxA"},
		{"B365H", "B365D", "B365A"},
	}
	for _, group := range groups {
		home := optionalFloat(first(row, group[0]))
		draw := optionalFloat(first(row, group[1]))
		away := optionalFloat(first(row, group[2]))
		if home != nil && draw != nil && away != nil {
			return home, draw, away
		}
	}
	return nil, nil, nil
}

func parseKickoff(dateValue string, timeValue string) (time.Time, error) {
	if timeValue == "" {
		timeValue = "00:00"
	}
	value := dateValue + " " + timeValue
	for _, layout := range []string{"02/01/2006 15:04", "02/01/06 15:04"} {
		parsed, err := time.ParseInLocation(layout, value, time.UTC)
		if err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported Football-Data kickoff %q", value)
}

func pathContext(path string) (string, string, string) {
	parts := splitPath(path)
	for index, part := range parts {
		if strings.EqualFold(part, "main") && index+2 < len(parts) {
			country := parts[index+1]
			return country, countryCodeFor(country), parts[index+2]
		}
		if strings.EqualFold(part, "extra") && index+2 < len(parts) {
			country := parts[index+1]
			return country, countryCodeFor(country), ""
		}
	}
	return "", "", ""
}

func splitPath(path string) []string {
	cleaned := filepath.Clean(path)
	parts := strings.FieldsFunc(cleaned, func(r rune) bool {
		return r == os.PathSeparator || r == '/' || r == '\\'
	})
	return parts
}

func seasonIDFromLabel(label string, kickoff time.Time) int {
	if label != "" {
		fields := strings.FieldsFunc(label, func(r rune) bool { return r == '-' || r == '/' })
		if len(fields) > 0 {
			last := fields[len(fields)-1]
			if len(last) == 2 {
				prefix := "20"
				if last >= "70" {
					prefix = "19"
				}
				last = prefix + last
			}
			parsed, err := strconv.Atoi(last)
			if err == nil && parsed > 0 {
				return parsed
			}
		}
	}
	return kickoff.Year()
}

func countryCodeFor(country string) string {
	switch strings.ToLower(strings.TrimSpace(country)) {
	case "belgium":
		return "BE"
	case "england", "scotland", "wales", "northern-ireland":
		return "GB"
	case "france":
		return "FR"
	case "germany":
		return "DE"
	case "greece":
		return "GR"
	case "italy":
		return "IT"
	case "netherlands":
		return "NL"
	case "norway":
		return "NO"
	case "portugal":
		return "PT"
	case "spain":
		return "ES"
	case "turkey":
		return "TR"
	case "usa", "united states":
		return "US"
	default:
		return ""
	}
}

func leagueDisplayName(country string, code string) string {
	names := map[string]string{
		"E0":  "Premier League",
		"E1":  "Championship",
		"E2":  "League One",
		"E3":  "League Two",
		"EC":  "National League",
		"D1":  "Bundesliga",
		"D2":  "2. Bundesliga",
		"SP1": "La Liga",
		"SP2": "Segunda Division",
		"I1":  "Serie A",
		"I2":  "Serie B",
		"F1":  "Ligue 1",
		"F2":  "Ligue 2",
		"N1":  "Eredivisie",
		"P1":  "Primeira Liga",
		"SC0": "Scottish Premiership",
		"SC1": "Scottish Championship",
		"B1":  "Belgian First Division A",
		"G1":  "Super League Greece",
		"T1":  "Super Lig",
	}
	if name, ok := names[strings.ToUpper(code)]; ok {
		return name
	}
	if code != "" {
		return strings.TrimSpace(country + " " + strings.ToUpper(code))
	}
	return country
}

func slugPart(value string) string {
	lower := strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, r := range lower {
		if r > unicode.MaxASCII {
			continue
		}
		builder.WriteRune(r)
	}
	slug := strings.Trim(nonSlug.ReplaceAllString(builder.String(), "-"), "-")
	if slug == "" {
		return "unknown"
	}
	return slug
}

func shortHash(value string) string {
	sum := sha1.Sum([]byte(strings.ToLower(strings.TrimSpace(value))))
	return hex.EncodeToString(sum[:])
}

func emptyRow(row map[string]string) bool {
	for _, value := range row {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}

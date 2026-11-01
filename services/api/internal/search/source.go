// Package search implements all-rounded entity search (teams, leagues,
// countries, competitions, matches) over PostgreSQL canonical data, with the
// result shape designed to be swapped for a Typesense collection later.
package search

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	accadomain "github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/acca"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/httpserver"
)

type Source struct {
	db        *sql.DB
	now       func() time.Time
	typesense *TypesenseClient
	schema    string
	failures  *typesenseFailureLog
}

// typesenseFailureLog rate-limits the "the search index is not answering"
// warning so an outage reports itself without drowning the log.
type typesenseFailureLog struct {
	mu   sync.Mutex
	last time.Time
}

func (s Source) currentTime() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func (s Source) reportTypesenseFailure(err error) {
	if s.failures == nil {
		slog.Warn("search index unavailable; serving the PostgreSQL fallback", "error", err)
		return
	}
	s.failures.mu.Lock()
	defer s.failures.mu.Unlock()
	now := time.Now()
	if !s.failures.last.IsZero() && now.Sub(s.failures.last) < time.Minute {
		return
	}
	s.failures.last = now
	slog.Warn("search index unavailable; serving the PostgreSQL fallback", "error", err)
}

var schemaNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func NewSource(db *sql.DB) Source {
	return Source{db: db, now: time.Now, schema: "public"}
}

// WithSchema selects the schema containing ephemeral ACCA inventory. It is
// intentionally allow-listed because schema names cannot be SQL parameters.
// Invalid or empty values retain the production-safe public default.
func (s Source) WithSchema(schema string) Source {
	schema = strings.TrimSpace(strings.ToLower(schema))
	if schemaNamePattern.MatchString(schema) {
		s.schema = schema
	}
	if s.schema == "" {
		s.schema = "public"
	}
	return s
}

// WithTypesense enables Typesense as the primary search backend with the
// PostgreSQL query as a graceful fallback.
func (s Source) WithTypesense(client *TypesenseClient) Source {
	s.typesense = client
	s.failures = &typesenseFailureLog{}
	return s
}

// Coverage returns reference-catalogue and live-pipeline coverage counters for
// the admin control plane.
func (s Source) Coverage(ctx context.Context) (httpserver.CatalogueCoverage, error) {
	var report httpserver.CatalogueCoverage
	var liveLastSeen any
	var workerLastSeen any
	err := s.db.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM countries),
  (SELECT COUNT(*) FROM countries WHERE flag_url IS NOT NULL AND flag_url <> ''),
  (SELECT COUNT(*) FROM teams),
  (SELECT COUNT(*) FROM teams WHERE logo_url IS NOT NULL AND logo_url <> ''),
  (SELECT COUNT(*) FROM leagues),
  (SELECT COUNT(*) FROM leagues WHERE logo_url IS NOT NULL AND logo_url <> ''),
  (SELECT MAX(requested_at) FROM provider_request_logs WHERE reason = 'live'),
  (SELECT MAX(updated_at) FROM jobs)`).Scan(
		&report.Countries,
		&report.CountriesFlags,
		&report.Teams,
		&report.TeamLogos,
		&report.Leagues,
		&report.LeagueLogos,
		&liveLastSeen,
		&workerLastSeen,
	)
	if err != nil {
		return httpserver.CatalogueCoverage{}, err
	}
	report.LiveLastSeen = parseTimestampValue(liveLastSeen)
	report.WorkerLastSeen = parseTimestampValue(workerLastSeen)
	if s.typesense != nil {
		count, err := s.typesense.CountDocuments(ctx)
		if err == nil {
			report.SearchIndexed = count
		}
	}
	return report, nil
}

func parseTimestampValue(value any) time.Time {
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

// Search returns typed hits ordered by priority (teams, leagues, countries,
// matches) for a query of at least three characters. When Typesense is
// configured it is tried first; any error or empty result falls back to the
// PostgreSQL query.
func (s Source) Search(ctx context.Context, query string, limit int) ([]httpserver.SearchResult, error) {
	return s.search(ctx, query, limit, false)
}

// SearchWithRecommendations adds prediction and ACCA hits for clients that
// explicitly support these result types. Search remains backwards compatible.
func (s Source) SearchWithRecommendations(ctx context.Context, query string, limit int) ([]httpserver.SearchResult, error) {
	return s.search(ctx, query, limit, true)
}

func (s Source) search(ctx context.Context, query string, limit int, includeRecommendations bool) ([]httpserver.SearchResult, error) {
	query = strings.TrimSpace(query)
	if len(query) < 3 {
		return []httpserver.SearchResult{}, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	if s.typesense != nil {
		if results, ok := s.typesenseSearch(ctx, query, limit); ok {
			// Typesense does not contain ephemeral predictions or ACCA inventory.
			// Enrich its entity hits from PostgreSQL when available; failures here
			// must not take down an otherwise healthy search index.
			if includeRecommendations && s.db != nil {
				terms := ParseTerms(query)
				_ = s.appendPredictions(ctx, &results, terms, MatchAll, limit)
				_ = s.appendAccas(ctx, &results, terms, MatchAll, limit)
			}
			return deduplicateEntityResults(results, limit), nil
		}
	}

	// Try the strict reading of the query first and only pay for the looser
	// ones when it found nothing. A one-word search costs exactly one pass.
	var lastErr error
	for _, pass := range Plan(query) {
		results, err := s.searchPass(ctx, query, pass, limit, includeRecommendations)
		if err != nil {
			lastErr = err
			continue
		}
		if len(results) > 0 {
			return deduplicateEntityResults(results, limit), nil
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return []httpserver.SearchResult{}, nil
}

func (s Source) searchPass(ctx context.Context, query string, pass Pass, limit int, includeRecommendations bool) ([]httpserver.SearchResult, error) {
	results := make([]httpserver.SearchResult, 0, limit)
	if err := s.appendTeams(ctx, &results, query, pass.Terms, pass.Mode, limit); err != nil {
		return nil, err
	}
	if err := s.appendPlayers(ctx, &results, pass.Terms, pass.Mode, limit); err != nil {
		return nil, err
	}
	if err := s.appendLeagues(ctx, &results, pass.Terms, pass.Mode, limit); err != nil {
		return nil, err
	}
	if err := s.appendCountries(ctx, &results, pass.Terms, pass.Mode, limit); err != nil {
		return nil, err
	}
	if err := s.appendMatches(ctx, &results, pass.Terms, pass.Mode, limit); err != nil {
		return nil, err
	}
	if includeRecommendations {
		if err := s.appendPredictions(ctx, &results, pass.Terms, pass.Mode, limit); err != nil {
			return nil, err
		}
		if err := s.appendAccas(ctx, &results, pass.Terms, pass.Mode, limit); err != nil {
			return nil, err
		}
	}
	return results, nil
}

func (s Source) appendPredictions(ctx context.Context, results *[]httpserver.SearchResult, terms []Term, mode MatchMode, limit int) error {
	args := make([]any, 0, 8)
	clause := Clause(predictionHaystack, terms, mode, &args)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
SELECT ep.public_id, ep.fixture_id, home.name || ' vs ' || away.name,
       ep.market, ep.prediction, f.kickoff_at
FROM engine_predictions ep
JOIN fixtures f ON f.id=ep.fixture_id
JOIN teams home ON home.id=f.home_team_id
JOIN teams away ON away.id=f.away_team_id
LEFT JOIN leagues l ON l.id=f.league_id
WHERE ep.superseded_at IS NULL AND ep.horizon_eligible=true AND ep.embargo_active=false
  AND f.kickoff_at > now()
  AND %s
ORDER BY COALESCE(l.priority_score, 0) DESC, f.kickoff_at ASC, ep.public_id ASC LIMIT $%d`, clause, len(args)), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r httpserver.SearchResult
		r.Type = "prediction"
		var kickoff time.Time
		if err := rows.Scan(&r.ID, &r.MatchID, &r.Name, &r.MarketKey, &r.Selection, &kickoff); err != nil {
			return err
		}
		r.KickoffAt = kickoff
		r.Subtitle = "Prediction"
		*results = append(*results, r)
	}
	return rows.Err()
}

func (s Source) appendAccas(ctx context.Context, results *[]httpserver.SearchResult, terms []Term, mode MatchMode, limit int) error {
	schema := s.schema
	if schema == "" {
		schema = "public"
	}
	args := make([]any, 0, 8)
	clause := Clause(accaHaystack, terms, mode, &args)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, accaSearchQuery(schema, clause, len(args)), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r httpserver.SearchResult
		var odds float64
		var legCount int
		r.Type = "acca"
		r.Subtitle = "ACCA"
		if err := rows.Scan(&r.Key, &legCount, &odds, &r.KickoffAt); err != nil {
			return err
		}
		r.Name = fmt.Sprintf("%d-leg ACCA (%.2f fair odds)", legCount, odds)
		*results = append(*results, r)
	}
	return rows.Err()
}

func accaSearchQuery(schema, clause string, limitPlaceholder int) string {
	return fmt.Sprintf(`
SELECT DISTINCT s.id::text, s.leg_count, s.combined_fair_odds, s.earliest_kickoff
FROM %s.acca_slips s JOIN %s.acca_slip_legs al ON al.slip_id=s.id
JOIN matches m ON m.id=al.match_id JOIN teams home ON home.id=m.home_team_id JOIN teams away ON away.id=m.away_team_id
JOIN leagues l ON l.id=m.league_id
WHERE s.status='active' AND s.earliest_kickoff > now()
  AND %s
  AND %s
ORDER BY s.earliest_kickoff ASC LIMIT $%d`,
		schema, schema, accadomain.SystemStandardSQLPredicate(schema, "s"), clause, limitPlaceholder)
}

func deduplicateEntityResults(results []httpserver.SearchResult, limit int) []httpserver.SearchResult {
	if limit <= 0 {
		return []httpserver.SearchResult{}
	}
	all := make([]httpserver.SearchResult, 0, len(results))
	teamIndex := make(map[string]int)
	for _, result := range results {
		if result.Type != "team" {
			all = append(all, result)
			continue
		}
		key := strings.ToLower(strings.Join(strings.Fields(result.Name), " "))
		if index, exists := teamIndex[key]; exists {
			if preferredTeamResult(result, all[index]) {
				all[index] = result
			}
			continue
		}
		teamIndex[key] = len(all)
		all = append(all, result)
	}
	if len(all) <= limit {
		return all
	}
	output := append([]httpserver.SearchResult(nil), all[:limit]...)
	for _, requiredType := range []string{"prediction", "acca"} {
		if containsResultType(output, requiredType) {
			continue
		}
		candidate, found := firstResultType(all[limit:], requiredType)
		if !found {
			continue
		}
		for index := len(output) - 1; index >= 0; index-- {
			if output[index].Type != "prediction" && output[index].Type != "acca" {
				output[index] = candidate
				break
			}
		}
	}
	return output
}

func containsResultType(results []httpserver.SearchResult, kind string) bool {
	_, found := firstResultType(results, kind)
	return found
}

func firstResultType(results []httpserver.SearchResult, kind string) (httpserver.SearchResult, bool) {
	for _, result := range results {
		if result.Type == kind {
			return result, true
		}
	}
	return httpserver.SearchResult{}, false
}

func preferredTeamResult(candidate, current httpserver.SearchResult) bool {
	candidateWorld := strings.EqualFold(strings.TrimSpace(candidate.Subtitle), "World") || strings.Contains(strings.ToLower(candidate.Slug), "team-world-")
	currentWorld := strings.EqualFold(strings.TrimSpace(current.Subtitle), "World") || strings.Contains(strings.ToLower(current.Slug), "team-world-")
	if candidateWorld != currentWorld {
		return !candidateWorld
	}
	return current.LogoURL == "" && candidate.LogoURL != ""
}

// Each entity is matched against one "haystack": every field that could
// reasonably carry a word the user typed, folded to lower case and joined.
// Matching field-by-field is what made "sunderland vs manchester city"
// impossible — no single column holds all of it — so the row is matched as a
// whole and the terms may land in different columns.
const (
	teamHaystack       = `lower(t.name || ' ' || coalesce(t.slug, ''))`
	playerHaystack     = `lower(p.name)`
	leagueHaystack     = `lower(l.name || ' ' || coalesce(l.slug, '') || ' ' || coalesce(c.name, ''))`
	countryHaystack    = `lower(name || ' ' || code)`
	matchHaystack      = `lower(home.name || ' ' || away.name || ' ' || coalesce(l.name, '') || ' ' || coalesce(c.name, ''))`
	predictionHaystack = `lower(home.name || ' ' || away.name || ' ' || coalesce(l.name, '') || ' ' || ep.market || ' ' || ep.prediction)`
	accaHaystack       = `lower(home.name || ' ' || away.name || ' ' || coalesce(l.name, '') || ' ' || al.market_key || ' ' || al.selection)`
)

// How long after kickoff a match stays findable. Search used to require
// `kickoff_at > now()`, which meant a match vanished from search the second it
// kicked off — the moment people search for it hardest. The window matches the
// 24h public result-detail window, so everything search returns still resolves
// to a live page.
const matchRecencyWindow = `now() - interval '24 hours'`

func (s Source) appendTeams(ctx context.Context, results *[]httpserver.SearchResult, query string, terms []Term, mode MatchMode, limit int) error {
	args := make([]any, 0, 8)
	clause := Clause(teamHaystack, terms, mode, &args)
	args = append(args, Normalize(query))
	exactPlaceholder := len(args)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
SELECT t.id, t.name, t.slug, COALESCE(c.name, t.country_code, ''), COALESCE(t.logo_url, '')
FROM teams t
LEFT JOIN countries c ON c.code = t.country_code
WHERE %s
ORDER BY (lower(t.name) = $%d) DESC, length(t.name) ASC, t.name ASC
LIMIT $%d`, clause, exactPlaceholder, len(args)), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r httpserver.SearchResult
		r.Type = "team"
		if err := rows.Scan(&r.ID, &r.Name, &r.Slug, &r.Subtitle, &r.LogoURL); err != nil {
			return err
		}
		*results = append(*results, r)
	}
	return rows.Err()
}

func (s Source) appendPlayers(ctx context.Context, results *[]httpserver.SearchResult, terms []Term, mode MatchMode, limit int) error {
	args := make([]any, 0, 8)
	clause := Clause(playerHaystack, terms, mode, &args)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
SELECT p.id, p.name, p.team_id, COALESCE(p.photo_url, '')
FROM players p
WHERE %s
ORDER BY length(p.name) ASC, p.name ASC
LIMIT $%d`, clause, len(args)), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r httpserver.SearchResult
		var teamID int64
		r.Type = "player"
		if err := rows.Scan(&r.ID, &r.Name, &teamID, &r.LogoURL); err != nil {
			return err
		}
		r.Subtitle = "Player"
		*results = append(*results, r)
	}
	return rows.Err()
}

func (s Source) appendLeagues(ctx context.Context, results *[]httpserver.SearchResult, terms []Term, mode MatchMode, limit int) error {
	args := make([]any, 0, 8)
	clause := Clause(leagueHaystack, terms, mode, &args)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
SELECT l.id, l.name, l.slug, COALESCE(c.name, l.country_code, ''), COALESCE(l.logo_url, '')
FROM leagues l
LEFT JOIN countries c ON c.code = l.country_code
WHERE %s
ORDER BY l.priority_score DESC NULLS LAST, l.name ASC
LIMIT $%d`, clause, len(args)), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r httpserver.SearchResult
		r.Type = "league"
		if err := rows.Scan(&r.ID, &r.Name, &r.Slug, &r.Subtitle, &r.LogoURL); err != nil {
			return err
		}
		*results = append(*results, r)
	}
	return rows.Err()
}

func (s Source) appendCountries(ctx context.Context, results *[]httpserver.SearchResult, terms []Term, mode MatchMode, limit int) error {
	args := make([]any, 0, 8)
	clause := Clause(countryHaystack, terms, mode, &args)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
SELECT code, name, COALESCE(flag_url, '')
FROM countries
WHERE %s
ORDER BY name ASC
LIMIT $%d`, clause, len(args)), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r httpserver.SearchResult
		r.Type = "country"
		if err := rows.Scan(&r.Code, &r.Name, &r.LogoURL); err != nil {
			return err
		}
		r.Subtitle = "Leagues · Teams · Matches"
		*results = append(*results, r)
	}
	return rows.Err()
}

func (s Source) appendMatches(ctx context.Context, results *[]httpserver.SearchResult, terms []Term, mode MatchMode, limit int) error {
	args := make([]any, 0, 8)
	clause := Clause(matchHaystack, terms, mode, &args)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
SELECT f.id, home.name, away.name, COALESCE(c.name || ' · ', '') || COALESCE(l.name, 'Competition'), f.kickoff_at, COALESCE(f.status, 'scheduled')
FROM fixtures f
JOIN teams home ON home.id = f.home_team_id
JOIN teams away ON away.id = f.away_team_id
LEFT JOIN leagues l ON l.id = f.league_id
LEFT JOIN countries c ON c.code = l.country_code
WHERE %s
  AND f.kickoff_at > %s
ORDER BY abs(extract(epoch FROM (f.kickoff_at - now()))) ASC, COALESCE(l.priority_score, 0) DESC
LIMIT $%d`, clause, matchRecencyWindow, len(args)), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r httpserver.SearchResult
		var homeName, awayName string
		var kickoff any
		r.Type = "match"
		if err := rows.Scan(&r.ID, &homeName, &awayName, &r.Subtitle, &kickoff, &r.Status); err != nil {
			return err
		}
		r.Name = fmt.Sprintf("%s vs %s", homeName, awayName)
		if parsed, err := parseTimestamp(kickoff); err == nil {
			r.KickoffAt = parsed
		}
		*results = append(*results, r)
	}
	return rows.Err()
}

func (s Source) typesenseSearch(ctx context.Context, query string, limit int) ([]httpserver.SearchResult, bool) {
	// Search must remain useful while the optional index is unavailable. A
	// healthy Typesense answers an autocomplete in single-digit milliseconds,
	// so 150ms is already generous and leaves the PostgreSQL fallback almost
	// the whole request deadline.
	typesenseCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	// Hand the index the expanded query: "sunderland vs mancity" reaches it as
	// "sunderland manchester city" rather than as a request for a document
	// containing the word "vs".
	documents, err := s.typesense.Search(typesenseCtx, TypesenseQuery(query), limit)
	if err != nil {
		// A broken index used to be indistinguishable from an index with no
		// answer: both fell through to PostgreSQL and search quietly degraded
		// to a substring scan with no typo tolerance and nobody could tell.
		// Say so, at most once a minute so a sustained outage cannot flood the
		// log.
		s.reportTypesenseFailure(err)
		return nil, false
	}
	if len(documents) == 0 {
		return nil, false
	}
	results := make([]httpserver.SearchResult, 0, len(documents))
	for _, document := range documents {
		result := httpserver.SearchResult{
			Type:     document.Type,
			ID:       document.EntityID,
			Name:     document.Name,
			Slug:     document.Slug,
			Subtitle: document.Subtitle,
			Status:   document.Status,
		}
		if document.KickoffAt > 0 {
			result.KickoffAt = time.Unix(document.KickoffAt, 0).UTC()
			// Matches stay findable for as long as their detail page resolves.
			// This filter used to be "strictly in the future", which removed a
			// match from search at the exact minute it kicked off.
			if result.Type == "match" && result.KickoffAt.Before(s.currentTime().Add(-24*time.Hour)) {
				continue
			}
		}
		result.LogoURL = document.LogoURL
		results = append(results, result)
	}
	return results, true
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}

func parseTimestamp(value any) (time.Time, error) {
	switch v := value.(type) {
	case time.Time:
		return v, nil
	case string:
		return time.Parse(time.RFC3339, v)
	default:
		return time.Time{}, fmt.Errorf("unsupported timestamp type %T", value)
	}
}

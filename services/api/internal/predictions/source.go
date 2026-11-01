package predictions

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/fixturepolicy"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/httpserver"
)

type Source struct {
	db                       *sql.DB
	now                      func() time.Time
	publicResultDetailWindow time.Duration
}

func NewSource(db *sql.DB) Source {
	return Source{db: db, now: time.Now}
}

func (s Source) ConfigurePublicResultDetailWindow(window time.Duration) Source {
	s.publicResultDetailWindow = window
	return s
}

func (s Source) ListTodayPredictions(ctx context.Context, filter httpserver.PredictionFilter) ([]httpserver.PredictionSummary, error) {
	var minimumConfidence any
	if filter.MinimumConfidence != nil {
		minimumConfidence = *filter.MinimumConfidence
	}
	now := s.now()
	dayStart, dayEnd := s.predictionWindow(filter)
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	if filter.Page <= 0 {
		filter.Page = 1
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT
  ep.public_id,
  ep.fixture_id,
  COALESCE(home.name, 'Home'),
  COALESCE(away.name, 'Away'),
  COALESCE(home.logo_url, ''),
  COALESCE(away.logo_url, ''),
  COALESCE(l.name, 'Competition'),
  COALESCE(l.country_code, 'International'),
  f.kickoff_at,
  COALESCE(f.status, 'scheduled'),
  ep.market,
  ep.prediction,
  COALESCE(ep.confidence, 0) * 100,
  COALESCE(ep.probability, 0),
  COALESCE(ep.is_best_bet, false),
  (
    SELECT COUNT(*)
    FROM engine_predictions AS versioned
    WHERE versioned.fixture_id = ep.fixture_id
      AND versioned.market = ep.market
      AND (
        versioned.generated_at < ep.generated_at
        OR (versioned.generated_at = ep.generated_at AND versioned.public_id <= ep.public_id)
      )
  ) AS published_version,
  ep.generated_at,
  ep.updated_at,
  ep.last_evaluated_at,
  (
    SELECT o.value
    FROM odds AS o
    WHERE o.fixture_id = ep.fixture_id
      AND lower(o.market) = '1x2'
      AND lower(o.outcome) = lower(ep.prediction)
    ORDER BY (lower(COALESCE(o.bookmaker, '')) = 'sokalens-provisional') ASC, o.fetched_at DESC
    LIMIT 1
  ) AS selection_odds
FROM engine_predictions AS ep
JOIN fixtures AS f ON f.id = ep.fixture_id
LEFT JOIN teams home ON home.id = f.home_team_id
LEFT JOIN teams away ON away.id = f.away_team_id
LEFT JOIN leagues l ON l.id = f.league_id
WHERE ($1 = '' OR ep.market = $1)
  AND (CAST($2 AS numeric) IS NULL OR (ep.confidence * 100) >= CAST($2 AS numeric))
  AND f.kickoff_at >= $3
  AND f.kickoff_at < $4
  AND ep.generated_at <= f.kickoff_at
  AND ep.gate_tier IN ('BASIC', 'FULL')
  AND ep.horizon_eligible = true
  AND ep.embargo_active = false
  AND ep.superseded_at IS NULL
`+fixturepolicy.UnresolvedEarlierFixtureClause("f", s.now())+`
  AND lower(COALESCE(f.status, 'scheduled')) NOT IN ('ft', 'aet', 'pen', 'finished', 'canceled', 'cancelled', 'abandoned')
  AND ($5 = false OR (ep.is_best_bet = true AND f.kickoff_at > $6))
  -- No single fixture may fill the page.
  --
  -- The feed is one row per fixture-market, and a Premier League fixture
  -- carries about seventeen markets. Once competitions were ranked, the
  -- eight fixtures of a matchday produced 136 rows and took every slot on
  -- page one: the reader saw Aston Villa vs Nottingham Forest repeated down
  -- the screen and no other league at all. Ranking without this cap trades
  -- one burial for another.
  --
  -- Only applied to the unfiltered feed. A reader who asked for corners wants
  -- every corners market, so a market filter switches this off entirely and
  -- that path keeps its previous behaviour exactly.
  AND (
    $1 <> ''
    OR (
      SELECT COUNT(*)
      FROM engine_predictions AS stronger
      WHERE stronger.fixture_id = ep.fixture_id
        AND stronger.superseded_at IS NULL
        AND stronger.gate_tier IN ('BASIC', 'FULL')
        AND stronger.horizon_eligible = true
        AND stronger.embargo_active = false
        AND (
          COALESCE(stronger.confidence, 0) > COALESCE(ep.confidence, 0)
          OR (COALESCE(stronger.confidence, 0) = COALESCE(ep.confidence, 0) AND stronger.public_id < ep.public_id)
        )
    ) < 3
  )
ORDER BY `+predictionOrder(filter)+`
LIMIT $7 OFFSET $8`, filter.MarketKey, minimumConfidence, dayStart.UTC().Format(time.RFC3339), dayEnd.UTC().Format(time.RFC3339), filter.BestBetsOnly, now.UTC().Format(time.RFC3339), limit, (filter.Page-1)*limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	predictions := []httpserver.PredictionSummary{}
	for rows.Next() {
		var prediction httpserver.PredictionSummary
		var kickoffAt any
		var publishedAt any
		var updatedAt any
		var lastEvaluatedAt any
		var selectionOdds sql.NullFloat64
		if err := rows.Scan(
			&prediction.ID,
			&prediction.MatchID,
			&prediction.HomeTeam,
			&prediction.AwayTeam,
			&prediction.HomeTeamLogo,
			&prediction.AwayTeamLogo,
			&prediction.LeagueName,
			&prediction.Country,
			&kickoffAt,
			&prediction.MatchStatus,
			&prediction.MarketKey,
			&prediction.Selection,
			&prediction.Confidence,
			&prediction.Probability,
			&prediction.IsBestBet,
			&prediction.PublishedVersion,
			&publishedAt,
			&updatedAt,
			&lastEvaluatedAt,
			&selectionOdds,
		); err != nil {
			return nil, err
		}
		parsedKickoffAt, err := parsePredictionTimestamp(kickoffAt)
		if err != nil {
			return nil, err
		}
		parsedPublishedAt, err := parsePredictionTimestamp(publishedAt)
		if err != nil {
			return nil, err
		}
		prediction.KickoffAt = parsedKickoffAt
		prediction.PublishedAt = parsedPublishedAt
		parsedUpdatedAt, err := parsePredictionTimestamp(updatedAt)
		if err != nil {
			return nil, err
		}
		prediction.UpdatedAt = parsedUpdatedAt
		parsedLastEvaluatedAt, err := parsePredictionTimestamp(lastEvaluatedAt)
		if err != nil {
			return nil, err
		}
		prediction.LastEvaluatedAt = parsedLastEvaluatedAt
		if selectionOdds.Valid {
			value := selectionOdds.Float64
			prediction.SelectionOdds = &value
		}
		prediction.MatchTitle = fmt.Sprintf("%s vs %s", prediction.HomeTeam, prediction.AwayTeam)
		predictions = append(predictions, prediction)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := s.attachPredictionMarkets(ctx, predictions); err != nil {
		return nil, err
	}
	return predictions, nil
}

// attachPredictionMarkets performs one bounded query for the fixtures already
// selected by the paginated primary feed. This keeps one row per match while
// exposing every current market for accurate client-side filtering.
func (s Source) attachPredictionMarkets(ctx context.Context, predictions []httpserver.PredictionSummary) error {
	if len(predictions) == 0 {
		return nil
	}
	args := make([]any, 0, len(predictions))
	placeholders := make([]string, 0, len(predictions))
	byFixture := make(map[int64][]int, len(predictions))
	for index, prediction := range predictions {
		args = append(args, prediction.MatchID)
		placeholders = append(placeholders, fmt.Sprintf("$%d", index+1))
		byFixture[prediction.MatchID] = append(byFixture[prediction.MatchID], index)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT ep.fixture_id, ep.market, ep.prediction, COALESCE(ep.confidence, 0) * 100, COALESCE(ep.probability, 0), COALESCE(ep.is_best_bet, false)
FROM engine_predictions AS ep
JOIN fixtures AS f ON f.id = ep.fixture_id
WHERE ep.fixture_id IN (`+strings.Join(placeholders, ",")+`)
  AND ep.generated_at <= f.kickoff_at
  AND ep.gate_tier IN ('BASIC', 'FULL')
  AND ep.horizon_eligible = true
  AND ep.embargo_active = false
  AND ep.superseded_at IS NULL
`+fixturepolicy.UnresolvedEarlierFixtureClause("f", s.now())+`
ORDER BY ep.fixture_id, ep.market`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var fixtureID int64
		var market httpserver.PredictionMarketOption
		if err := rows.Scan(&fixtureID, &market.MarketKey, &market.Selection, &market.Confidence, &market.Probability, &market.IsBestBet); err != nil {
			return err
		}
		for _, index := range byFixture[fixtureID] {
			predictions[index].Markets = append(predictions[index].Markets, market)
		}
	}
	return rows.Err()
}

func truncatePredictionDate(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, value.Location())
}

// predictionWindow returns the [start, end) calendar-day window for a
// predictions query, honoring an explicit date, a relative day (yesterday /
// today / tomorrow), and a timezone. It mirrors the matches feed's date
// handling so the predictions page can offer the same 7-day navigation.
func (s Source) predictionWindow(filter httpserver.PredictionFilter) (time.Time, time.Time) {
	location := time.FixedZone("EAT", 3*60*60)
	if strings.TrimSpace(filter.Timezone) != "" && strings.TrimSpace(filter.Timezone) != "Africa/Nairobi" {
		if loaded, err := time.LoadLocation(filter.Timezone); err == nil {
			location = loaded
		}
	}
	now := s.now().In(location)
	base := truncatePredictionDate(now)
	switch strings.TrimSpace(filter.Day) {
	case "yesterday":
		base = base.AddDate(0, 0, -1)
	case "tomorrow":
		base = base.AddDate(0, 0, 1)
	}
	if strings.TrimSpace(filter.Date) != "" {
		if parsed, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(filter.Date), location); err == nil {
			base = truncatePredictionDate(parsed)
		}
	}
	// Days widens the window forward from the same start. 0 and 1 are both one
	// day, so every existing caller keeps the window it had.
	span := filter.Days
	if span < 1 {
		span = 1
	}
	return base, base.AddDate(0, 0, span)
}

// predictionOrder returns the SQL ORDER BY for a predictions feed: the main
// feed leads with the competitions a reader came for and is sorted by kickoff
// within them, while the best-bets shortlist keeps confidence ordering.
//
// Kickoff alone was not survivable at this catalogue size. The site published
// 11,031 predictions for 2026-09-12 and the page shows the first hundred; by
// clock order those hundred were Western Australia NPL, three Korean divisions
// and a Mexican under-21 league, and the day's eight Premier League fixtures
// were not among them. A reader looking for Liverpool vs Fulham on the day it
// is played could not find it.
//
// `priority_score` is 0 for everything except the competitions seeded in
// migration 000062, so this degrades to the old clock ordering for the rest of
// the catalogue rather than inventing a ranking for it.
func predictionOrder(filter httpserver.PredictionFilter) string {
	if filter.BestBetsOnly {
		return "COALESCE(ep.confidence, 0) DESC, ep.generated_at DESC, ep.public_id DESC"
	}
	// COALESCE because the leagues join is a LEFT JOIN: a fixture whose league
	// row is missing must sort with the unranked, not ahead of everything.
	return "COALESCE(l.priority_score, 0) DESC, f.kickoff_at ASC, ep.public_id ASC"
}

// CountTodayPredictions returns the total number of current predictions in the
// same day window and filter, used for pagination metadata.
func (s Source) CountTodayPredictions(ctx context.Context, filter httpserver.PredictionFilter) (int, error) {
	var minimumConfidence any
	if filter.MinimumConfidence != nil {
		minimumConfidence = *filter.MinimumConfidence
	}
	now := s.now()
	dayStart, dayEnd := s.predictionWindow(filter)

	var total int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM engine_predictions AS ep
JOIN fixtures AS f ON f.id = ep.fixture_id
WHERE ($1 = '' OR ep.market = $1)
  AND (CAST($2 AS numeric) IS NULL OR (ep.confidence * 100) >= CAST($2 AS numeric))
  AND f.kickoff_at >= $3
  AND f.kickoff_at < $4
  AND ep.generated_at <= f.kickoff_at
  AND ep.gate_tier IN ('BASIC', 'FULL')
  AND ep.horizon_eligible = true
  AND ep.embargo_active = false
  AND ep.superseded_at IS NULL
`+fixturepolicy.UnresolvedEarlierFixtureClause("f", s.now())+`
  AND lower(COALESCE(f.status, 'scheduled')) NOT IN ('ft', 'aet', 'pen', 'finished', 'canceled', 'cancelled', 'abandoned')
  AND ($5 = false OR (ep.is_best_bet = true AND f.kickoff_at > $6))
  -- Same cap as the list query: see the comment there. The count must agree,
  -- or the reported total promises rows the feed will never serve.
  AND (
    $1 <> ''
    OR (
      SELECT COUNT(*)
      FROM engine_predictions AS stronger
      WHERE stronger.fixture_id = ep.fixture_id
        AND stronger.superseded_at IS NULL
        AND stronger.gate_tier IN ('BASIC', 'FULL')
        AND stronger.horizon_eligible = true
        AND stronger.embargo_active = false
        AND (
          COALESCE(stronger.confidence, 0) > COALESCE(ep.confidence, 0)
          OR (COALESCE(stronger.confidence, 0) = COALESCE(ep.confidence, 0) AND stronger.public_id < ep.public_id)
        )
    ) < 3
  )`,
		filter.MarketKey, minimumConfidence, dayStart.UTC().Format(time.RFC3339), dayEnd.UTC().Format(time.RFC3339), filter.BestBetsOnly, now.UTC().Format(time.RFC3339)).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total, nil
}

// ListTodayProviderPredictions returns API-Football native predictions for
// today's fixtures that have no engine prediction of our own, so the public
// feed is never empty. They are returned separately and the UI must mark them
// as provider fallbacks.
func (s Source) ListTodayProviderPredictions(ctx context.Context, filter httpserver.PredictionFilter) ([]httpserver.ProviderPredictionSummary, error) {
	dayStart, dayEnd := s.predictionWindow(filter)
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	page := filter.Page
	if page <= 0 {
		page = 1
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT
  f.id,
  COALESCE(home.name, 'Home'),
  COALESCE(away.name, 'Away'),
  -- The crests come from the home/away joins this query already makes for the
  -- names, so a provider fallback row carries the same crest as an engine row
  -- beside it without a second round trip. COALESCE to '' rather than NULL:
  -- the clients key on the empty string to draw the initials shield, and a
  -- JSON null would reach them as the string "null".
  COALESCE(home.logo_url, ''),
  COALESCE(away.logo_url, ''),
  COALESCE(l.name, 'Competition'),
  COALESCE(l.country_code, 'International'),
  f.kickoff_at,
  COALESCE(f.status, 'scheduled'),
  COALESCE(ap.predicted_winner, ''),
  COALESCE(ap.win_or_draw, false),
  COALESCE(ap.under_over, ''),
  COALESCE(ap.percent_home, 0),
  COALESCE(ap.percent_draw, 0),
  COALESCE(ap.percent_away, 0),
  COALESCE(ap.advice, ''),
  ap.captured_at
FROM api_predictions AS ap
JOIN fixtures AS f ON f.id = ap.fixture_id
LEFT JOIN teams AS home ON home.id = f.home_team_id
LEFT JOIN teams AS away ON away.id = f.away_team_id
LEFT JOIN leagues AS l ON l.id = f.league_id
WHERE f.kickoff_at >= $1
  AND f.kickoff_at < $2
  AND lower(COALESCE(f.status, 'scheduled')) NOT IN ('ft', 'aet', 'pen', 'finished', 'canceled', 'cancelled', 'abandoned')
`+fixturepolicy.UnresolvedEarlierFixtureClause("f", s.now())+`
  AND NOT EXISTS (
    SELECT 1
    FROM engine_predictions AS ep
    WHERE ep.fixture_id = f.id
      AND ep.superseded_at IS NULL
      AND ep.generated_at <= f.kickoff_at
      AND ep.gate_tier IN ('BASIC', 'FULL')
      AND ep.horizon_eligible = true
      AND ep.embargo_active = false
  )
-- Ranked competitions lead, then kickoff. See predictionOrder.
ORDER BY COALESCE(l.priority_score, 0) DESC, f.kickoff_at ASC, f.id ASC
LIMIT $3 OFFSET $4`, dayStart.UTC().Format(time.RFC3339), dayEnd.UTC().Format(time.RFC3339), limit, (page-1)*limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	summaries := []httpserver.ProviderPredictionSummary{}
	for rows.Next() {
		var summary httpserver.ProviderPredictionSummary
		var kickoffAt any
		var capturedAt any
		if err := rows.Scan(
			&summary.MatchID,
			&summary.HomeTeam,
			&summary.AwayTeam,
			&summary.HomeTeamLogo,
			&summary.AwayTeamLogo,
			&summary.LeagueName,
			&summary.Country,
			&kickoffAt,
			&summary.MatchStatus,
			&summary.PredictedWinner,
			&summary.WinOrDraw,
			&summary.UnderOver,
			&summary.PercentHome,
			&summary.PercentDraw,
			&summary.PercentAway,
			&summary.Advice,
			&capturedAt,
		); err != nil {
			return nil, err
		}
		parsed, err := parsePredictionTimestamp(kickoffAt)
		if err != nil {
			return nil, err
		}
		summary.KickoffAt = parsed
		parsedCapturedAt, err := parsePredictionTimestamp(capturedAt)
		if err != nil {
			return nil, err
		}
		summary.PublishedAt = parsedCapturedAt
		summary.MatchTitle = fmt.Sprintf("%s vs %s", summary.HomeTeam, summary.AwayTeam)
		httpserver.PopulateProviderPrimitives(&summary)
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// CountTodayProviderPredictions reports every provider fallback match in the
// date window so the public feed can paginate and display the unified total.
func (s Source) CountTodayProviderPredictions(ctx context.Context, filter httpserver.PredictionFilter) (int, error) {
	dayStart, dayEnd := s.predictionWindow(filter)
	var total int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM api_predictions AS ap
JOIN fixtures AS f ON f.id = ap.fixture_id
WHERE f.kickoff_at >= $1
  AND f.kickoff_at < $2
  AND lower(COALESCE(f.status, 'scheduled')) NOT IN ('ft', 'aet', 'pen', 'finished', 'canceled', 'cancelled', 'abandoned')
`+fixturepolicy.UnresolvedEarlierFixtureClause("f", s.now())+`
  AND NOT EXISTS (
    SELECT 1
    FROM engine_predictions AS ep
    WHERE ep.fixture_id = f.id
      AND ep.superseded_at IS NULL
      AND ep.generated_at <= f.kickoff_at
      AND ep.gate_tier IN ('BASIC', 'FULL')
      AND ep.horizon_eligible = true
      AND ep.embargo_active = false
  )`, dayStart.UTC().Format(time.RFC3339), dayEnd.UTC().Format(time.RFC3339)).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total, nil
}

// ListProviderPredictions returns API-Football native predictions as a
// separately-marked fallback for fixtures that have no current engine
// prediction of our own.
func (s Source) ListProviderPredictions(ctx context.Context, limit int) ([]httpserver.ProviderPredictionSummary, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT
  f.id,
  COALESCE(home.name, 'Home'),
  COALESCE(away.name, 'Away'),
  -- The crests come from the home/away joins this query already makes for the
  -- names, so a provider fallback row carries the same crest as an engine row
  -- beside it without a second round trip. COALESCE to '' rather than NULL:
  -- the clients key on the empty string to draw the initials shield, and a
  -- JSON null would reach them as the string "null".
  COALESCE(home.logo_url, ''),
  COALESCE(away.logo_url, ''),
  COALESCE(l.name, 'Competition'),
  COALESCE(l.country_code, 'International'),
  f.kickoff_at,
  COALESCE(f.status, 'scheduled'),
  COALESCE(ap.predicted_winner, ''),
  COALESCE(ap.win_or_draw, false),
  COALESCE(ap.under_over, ''),
  COALESCE(ap.percent_home, 0),
  COALESCE(ap.percent_draw, 0),
  COALESCE(ap.percent_away, 0),
  COALESCE(ap.advice, ''),
  ap.captured_at
FROM api_predictions AS ap
JOIN fixtures AS f ON f.id = ap.fixture_id
LEFT JOIN teams AS home ON home.id = f.home_team_id
LEFT JOIN teams AS away ON away.id = f.away_team_id
LEFT JOIN leagues AS l ON l.id = f.league_id
WHERE f.kickoff_at >= now() - interval '2 hours'
`+fixturepolicy.UnresolvedEarlierFixtureClause("f", s.now())+`
  AND NOT EXISTS (
    SELECT 1
    FROM engine_predictions AS ep
    WHERE ep.fixture_id = f.id
      AND ep.superseded_at IS NULL
      AND ep.generated_at <= f.kickoff_at
      AND ep.gate_tier IN ('BASIC', 'FULL')
      AND ep.horizon_eligible = true
      AND ep.embargo_active = false
  )
-- Ranked competitions lead, then kickoff. See predictionOrder.
ORDER BY COALESCE(l.priority_score, 0) DESC, f.kickoff_at ASC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	summaries := []httpserver.ProviderPredictionSummary{}
	for rows.Next() {
		var summary httpserver.ProviderPredictionSummary
		var kickoffAt any
		var capturedAt any
		if err := rows.Scan(
			&summary.MatchID,
			&summary.HomeTeam,
			&summary.AwayTeam,
			&summary.HomeTeamLogo,
			&summary.AwayTeamLogo,
			&summary.LeagueName,
			&summary.Country,
			&kickoffAt,
			&summary.MatchStatus,
			&summary.PredictedWinner,
			&summary.WinOrDraw,
			&summary.UnderOver,
			&summary.PercentHome,
			&summary.PercentDraw,
			&summary.PercentAway,
			&summary.Advice,
			&capturedAt,
		); err != nil {
			return nil, err
		}
		parsed, err := parsePredictionTimestamp(kickoffAt)
		if err != nil {
			return nil, err
		}
		summary.KickoffAt = parsed
		parsedCapturedAt, err := parsePredictionTimestamp(capturedAt)
		if err != nil {
			return nil, err
		}
		summary.PublishedAt = parsedCapturedAt
		summary.MatchTitle = fmt.Sprintf("%s vs %s", summary.HomeTeam, summary.AwayTeam)
		httpserver.PopulateProviderPrimitives(&summary)
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

func (s Source) GetPrediction(ctx context.Context, predictionID int64) (httpserver.PredictionDetail, error) {
	return s.getPrediction(ctx, predictionID, true)
}

// GetSavedPrediction resolves the exact immutable prediction version a user
// saved. Public detail reads remain current-only; account retention is governed
// separately by the fixture completion boundary.
func (s Source) GetSavedPrediction(ctx context.Context, predictionID int64) (httpserver.PredictionDetail, error) {
	return s.getPrediction(ctx, predictionID, false)
}

func (s Source) getPrediction(ctx context.Context, predictionID int64, currentOnly bool) (httpserver.PredictionDetail, error) {
	var detail httpserver.PredictionDetail
	var publishedAt any
	var updatedAt any
	var lastEvaluatedAt any
	var kickoffAt any
	var finishedAt any
	var probability float64
	cutoffEnabled := s.publicResultDetailWindow > 0
	currentClause := ""
	if currentOnly {
		currentClause = "\n  AND ep.superseded_at IS NULL"
	} else {
		cutoffEnabled = false
	}
	cutoff := predictionResultDetailCutoff(s.now(), s.publicResultDetailWindow)
	legacyKickoffCutoff := predictionResultDetailLegacyKickoffCutoff(cutoff)
	err := s.db.QueryRowContext(ctx, `
SELECT
  ep.public_id,
  ep.fixture_id,
  f.kickoff_at,
  f.finished_at,
  COALESCE(f.status, 'upcoming'),
  ep.market,
  ep.prediction,
  COALESCE(ep.confidence, 0) * 100,
  (
    SELECT COUNT(*)
    FROM engine_predictions AS versioned
    WHERE versioned.fixture_id = ep.fixture_id
      AND versioned.market = ep.market
      AND (
        versioned.generated_at < ep.generated_at
        OR (versioned.generated_at = ep.generated_at AND versioned.public_id <= ep.public_id)
      )
  ) AS published_version,
  ep.generated_at,
  ep.updated_at,
  ep.last_evaluated_at,
  ep.probability,
  COALESCE(ep.model_version, ''),
  CASE
    WHEN pr.prediction_id IS NOT NULL THEN CASE
      WHEN pr.settlement_status IN ('void', 'unsettled_data_unavailable') THEN 'void'
      WHEN pr.settlement_status <> 'settled' THEN 'pending'
      WHEN pr.is_correct THEN 'won'
      ELSE 'lost' END
    WHEN evidence.settlement_classification = 'correct' THEN 'won'
    WHEN evidence.settlement_classification = 'incorrect' THEN 'lost'
    WHEN evidence.settlement_classification = 'void' THEN 'void'
    ELSE 'pending'
  END AS outcome
FROM engine_predictions AS ep
JOIN fixtures AS f ON f.id = ep.fixture_id
LEFT JOIN prediction_results AS pr ON pr.prediction_id = ep.id
LEFT JOIN effective_model_evaluation_facts AS evidence
  ON evidence.prediction_id = ep.id AND ep.market = 'btts_total_goals'
WHERE ep.public_id = $1
  AND ep.generated_at <= f.kickoff_at
  AND ep.gate_tier IN ('BASIC', 'FULL')
  AND ep.horizon_eligible = true
  AND ep.embargo_active = false
`+currentClause+fixturepolicy.UnresolvedEarlierFixtureClause("f", s.now())+`
  AND (
    $2 = false
    OR lower(f.status) NOT IN ('ft', 'aet', 'pen', 'finished')
    OR f.finished_at >= $3
    OR (f.finished_at IS NULL AND f.kickoff_at >= $4)
  )
  `, predictionID, cutoffEnabled, cutoff, legacyKickoffCutoff).Scan(
		&detail.ID,
		&detail.MatchID,
		&kickoffAt,
		&finishedAt,
		&detail.MatchStatus,
		&detail.MarketKey,
		&detail.Selection,
		&detail.Confidence,
		&detail.PublishedVersion,
		&publishedAt,
		&updatedAt,
		&lastEvaluatedAt,
		&probability,
		&detail.InputSnapshotHash,
		&detail.Outcome,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return httpserver.PredictionDetail{}, httpserver.ErrPredictionNotFound
		}
		return httpserver.PredictionDetail{}, err
	}
	parsedPublishedAt, err := parsePredictionTimestamp(publishedAt)
	if err != nil {
		return httpserver.PredictionDetail{}, err
	}
	detail.PublishedAt = parsedPublishedAt
	parsedUpdatedAt, err := parsePredictionTimestamp(updatedAt)
	if err != nil {
		return httpserver.PredictionDetail{}, err
	}
	detail.UpdatedAt = parsedUpdatedAt
	parsedLastEvaluatedAt, err := parsePredictionTimestamp(lastEvaluatedAt)
	if err != nil {
		return httpserver.PredictionDetail{}, err
	}
	detail.LastEvaluatedAt = parsedLastEvaluatedAt
	parsedKickoffAt, err := parsePredictionTimestamp(kickoffAt)
	if err != nil {
		return httpserver.PredictionDetail{}, err
	}
	detail.KickoffAt = parsedKickoffAt
	if finishedAt != nil {
		parsedFinishedAt, err := parsePredictionTimestamp(finishedAt)
		if err != nil {
			return httpserver.PredictionDetail{}, err
		}
		detail.FinishedAt = &parsedFinishedAt
	}

	detail.Probabilities = map[string]float64{detail.Selection: probability}
	return detail, nil
}

func (s Source) GetHitRateReport(ctx context.Context) (httpserver.PredictionHitRateReport, error) {
	rows, err := s.db.QueryContext(ctx, `
WITH ranked_raw_results AS (
  SELECT
    pr.market,
    pr.settlement_status,
    pr.is_correct,
    ROW_NUMBER() OVER (
      PARTITION BY pr.fixture_id, pr.market
      ORDER BY ep.generated_at DESC, ep.public_id DESC
    ) AS final_prediction_rank
  FROM prediction_results AS pr
  JOIN engine_predictions AS ep ON ep.id = pr.prediction_id
  JOIN fixtures AS f ON f.id = pr.fixture_id
  WHERE ep.generated_at <= f.kickoff_at
), accuracy_sources AS (
  SELECT
    raw.market,
    SUM(CASE WHEN raw.settlement_status IN ('settled', 'void', 'unsettled_data_unavailable') THEN 1 ELSE 0 END) AS settled,
    SUM(CASE WHEN raw.settlement_status = 'settled' AND raw.is_correct THEN 1 ELSE 0 END) AS won,
    SUM(CASE WHEN raw.settlement_status = 'settled' AND NOT raw.is_correct THEN 1 ELSE 0 END) AS lost,
    SUM(CASE WHEN raw.settlement_status IN ('void', 'unsettled_data_unavailable') THEN 1 ELSE 0 END) AS voided
  FROM ranked_raw_results AS raw
  WHERE raw.final_prediction_rank = 1
  GROUP BY raw.market

  UNION ALL

  SELECT
    daily.market,
    SUM(daily.correct + daily.incorrect + daily.voided) AS settled,
    SUM(daily.correct) AS won,
    SUM(daily.incorrect) AS lost,
    SUM(daily.voided) AS voided
  FROM prediction_accuracy_daily AS daily
  GROUP BY daily.market
)
SELECT
  market,
  SUM(settled) AS settled,
  SUM(won) AS won,
  SUM(lost) AS lost,
  SUM(voided) AS voided
FROM accuracy_sources
GROUP BY market
ORDER BY market ASC`)
	if err != nil {
		return httpserver.PredictionHitRateReport{}, err
	}
	defer rows.Close()

	report := httpserver.PredictionHitRateReport{}
	for rows.Next() {
		market := httpserver.PredictionMarketHitRate{}
		if err := rows.Scan(&market.MarketKey, &market.Settled, &market.Won, &market.Lost, &market.Void); err != nil {
			return httpserver.PredictionHitRateReport{}, err
		}
		market.HitRate = hitRate(market.Won, market.Lost)
		market.Denominator = market.Won + market.Lost
		report.Markets = append(report.Markets, market)
		report.Settled += market.Settled
		report.Won += market.Won
		report.Lost += market.Lost
		report.Void += market.Void
	}
	if err := rows.Err(); err != nil {
		return httpserver.PredictionHitRateReport{}, err
	}
	report.HitRate = hitRate(report.Won, report.Lost)
	report.Denominator = report.Won + report.Lost
	return report, nil
}

func (s Source) ListPredictionResults(ctx context.Context, filter httpserver.PredictionFilter) ([]httpserver.PredictionResultSummary, error) {
	dayStart, dayEnd := s.predictionWindow(filter)
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	page := filter.Page
	if page <= 0 {
		page = 1
	}
	rows, err := s.db.QueryContext(ctx, `
WITH ranked AS (
  SELECT
    ep.public_id,
    ep.fixture_id,
    COALESCE(home.name, 'Home') || ' vs ' || COALESCE(away.name, 'Away') AS match_title,
    -- The two sides as their own columns. match_title stays: it is what the
    -- share text and the single-line fallback read, and recovering names by
    -- splitting it would be guessing at structure that was already flattened.
    COALESCE(home.name, 'Home') AS home_team,
    COALESCE(away.name, 'Away') AS away_team,
    COALESCE(home.logo_url, '') AS home_team_logo,
    COALESCE(away.logo_url, '') AS away_team_logo,
    COALESCE(l.name, 'Competition') AS league_name,
    COALESCE(l.country_code, 'International') AS country,
    f.kickoff_at,
    COALESCE(f.status, 'finished') AS match_status,
    ep.market,
    ep.prediction,
    CASE WHEN pr.prediction_id IS NOT NULL THEN COALESCE(pr.actual, '')
      ELSE COALESCE(evidence.final_outcome, '') END AS actual,
    CASE
      WHEN pr.prediction_id IS NOT NULL THEN CASE
        WHEN pr.settlement_status IN ('void', 'unsettled_data_unavailable') THEN 'void'
        WHEN pr.settlement_status <> 'settled' THEN 'pending'
        WHEN pr.is_correct THEN 'won'
        ELSE 'lost' END
      WHEN evidence.settlement_classification = 'correct' THEN 'won'
      WHEN evidence.settlement_classification = 'incorrect' THEN 'lost'
      WHEN evidence.settlement_classification = 'void' THEN 'void'
      ELSE 'pending'
    END AS outcome,
    COALESCE(ep.confidence, 0) * 100 AS confidence,
    ROW_NUMBER() OVER (PARTITION BY ep.fixture_id, ep.market ORDER BY ep.generated_at DESC, ep.public_id DESC) AS result_rank
  FROM engine_predictions ep
  LEFT JOIN prediction_results pr ON pr.prediction_id = ep.id
  LEFT JOIN effective_model_evaluation_facts evidence
    ON evidence.prediction_id = ep.id AND ep.market = 'btts_total_goals'
  JOIN fixtures f ON f.id = ep.fixture_id
  LEFT JOIN teams home ON home.id = f.home_team_id
  LEFT JOIN teams away ON away.id = f.away_team_id
  LEFT JOIN leagues l ON l.id = f.league_id
  WHERE f.kickoff_at >= $1 AND f.kickoff_at < $2
    AND ($3 = '' OR ep.market = $3)
    AND ep.generated_at <= f.kickoff_at
    AND (pr.prediction_id IS NOT NULL OR evidence.settlement_classification IN ('correct', 'incorrect', 'void'))
)
SELECT public_id, fixture_id, match_title, home_team, away_team,
       home_team_logo, away_team_logo, league_name, country, kickoff_at,
       match_status, market, prediction, actual, outcome, confidence
FROM ranked
WHERE result_rank = 1
ORDER BY kickoff_at DESC, fixture_id, market
LIMIT $4 OFFSET $5`, dayStart.UTC().Format(time.RFC3339), dayEnd.UTC().Format(time.RFC3339), filter.MarketKey, limit, (page-1)*limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []httpserver.PredictionResultSummary{}
	for rows.Next() {
		var result httpserver.PredictionResultSummary
		var kickoffAt any
		if err := rows.Scan(
			&result.PredictionID, &result.MatchID, &result.MatchTitle,
			&result.HomeTeam, &result.AwayTeam, &result.HomeTeamLogo, &result.AwayTeamLogo,
			&result.LeagueName,
			&result.Country, &kickoffAt, &result.MatchStatus, &result.MarketKey,
			&result.Selection, &result.Actual, &result.Outcome, &result.Confidence,
		); err != nil {
			return nil, err
		}
		parsed, err := parsePredictionTimestamp(kickoffAt)
		if err != nil {
			return nil, err
		}
		result.KickoffAt = parsed
		results = append(results, result)
	}
	return results, rows.Err()
}

func (s Source) CountPredictionResults(ctx context.Context, filter httpserver.PredictionFilter) (int, error) {
	dayStart, dayEnd := s.predictionWindow(filter)
	var count int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM (
  SELECT DISTINCT ep.fixture_id, ep.market
  FROM engine_predictions ep
  LEFT JOIN prediction_results pr ON pr.prediction_id = ep.id
  LEFT JOIN effective_model_evaluation_facts evidence
    ON evidence.prediction_id = ep.id AND ep.market = 'btts_total_goals'
  JOIN fixtures f ON f.id = ep.fixture_id
  WHERE f.kickoff_at >= $1 AND f.kickoff_at < $2
    AND ($3 = '' OR ep.market = $3)
    AND ep.generated_at <= f.kickoff_at
    AND (pr.prediction_id IS NOT NULL OR evidence.settlement_classification IN ('correct', 'incorrect', 'void'))
) settled`, dayStart.UTC().Format(time.RFC3339), dayEnd.UTC().Format(time.RFC3339), filter.MarketKey).Scan(&count)
	return count, err
}

func predictionResultDetailCutoff(now time.Time, window time.Duration) string {
	if window <= 0 {
		return time.Time{}.UTC().Format(time.RFC3339)
	}
	location := time.FixedZone("EAT", 3*60*60)
	threshold := now.In(location).Add(-window)
	dayStart := time.Date(threshold.Year(), threshold.Month(), threshold.Day(), 0, 0, 0, 0, location)
	return dayStart.UTC().Format(time.RFC3339)
}

func predictionResultDetailLegacyKickoffCutoff(cutoff string) string {
	parsed, err := time.Parse(time.RFC3339, cutoff)
	if err != nil {
		return cutoff
	}
	return parsed.Add(-2 * time.Hour).UTC().Format(time.RFC3339)
}

func parseProbabilities(value any) (map[string]float64, error) {
	switch v := value.(type) {
	case []byte:
		return parseProbabilityJSON(v)
	case string:
		return parseProbabilityJSON([]byte(v))
	case map[string]float64:
		return v, nil
	case nil:
		return map[string]float64{}, nil
	default:
		return nil, fmt.Errorf("unsupported probabilities type %T", value)
	}
}

func parseProbabilityJSON(body []byte) (map[string]float64, error) {
	values := map[string]float64{}
	if len(body) == 0 {
		return values, nil
	}
	if err := json.Unmarshal(body, &values); err != nil {
		return nil, err
	}
	return values, nil
}

func hitRate(won int, lost int) float64 {
	denominator := won + lost
	if denominator == 0 {
		return 0
	}
	return math.Round((float64(won)/float64(denominator))*10000) / 100
}

func parsePredictionTimestamp(value any) (time.Time, error) {
	switch v := value.(type) {
	case time.Time:
		return v, nil
	case string:
		return time.Parse(time.RFC3339, v)
	case []byte:
		return time.Parse(time.RFC3339, string(v))
	default:
		return time.Time{}, fmt.Errorf("unsupported timestamp type %T", value)
	}
}

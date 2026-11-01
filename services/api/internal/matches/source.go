package matches

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

type Query = httpserver.MatchQuery

const minimumHistoryMatchesPerTeam = 5

func NewSource(db *sql.DB) Source {
	return Source{db: db, now: time.Now}
}

func (s Source) ConfigurePublicResultDetailWindow(window time.Duration) Source {
	s.publicResultDetailWindow = window
	return s
}

func (s Source) ListMatches(ctx context.Context, query httpserver.MatchQuery) ([]httpserver.MatchSummary, error) {
	if s.db == nil {
		return nil, errors.New("matches source DB is nil")
	}
	where, args, err := s.matchWhere(query)
	if err != nil {
		return nil, err
	}
	limit := query.Limit
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	page := query.Page
	if page <= 0 {
		page = 1
	}
	limitClause := ""
	if query.IncludeUnavailable {
		args = append(args, limit, (page-1)*limit)
		limitClause = fmt.Sprintf("LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	}

	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
SELECT
  m.id,
  COALESCE(l.name, 'Competition'),
  COALESCE(l.country_code, 'International'),
  COALESCE(l.slug, ''),
  COALESCE(home.name, 'Home'),
  COALESCE(away.name, 'Away'),
  COALESCE(home.logo_url, ''),
  COALESCE(away.logo_url, ''),
  COALESCE(home.slug, ''),
  COALESCE(away.slug, ''),
  m.kickoff_at,
  fixture_lifecycle.finished_at,
  COALESCE(m.status, 'upcoming'),
  COALESCE(m.provider_status_raw, ''),
  COALESCE(m.provider_status_label, ''),
  m.status_elapsed,
  m.live_state_at,
  COALESCE(m.venue_name, ''),
  COALESCE(m.venue_city, ''),
  m.home_score,
  m.away_score,
  m.half_time_home_score,
  m.half_time_away_score,
  (
    SELECT os.payload
    FROM odds_snapshots os
    JOIN odds_markets om ON om.id = os.market_id
    WHERE os.match_id = m.id
      AND om.market_key = '1x2'
    ORDER BY os.captured_at DESC, os.id DESC
    LIMIT 1
  ) AS odds_payload,
  (
    SELECT COUNT(*)
    FROM matches history_home
    WHERE history_home.kickoff_at < m.kickoff_at
      AND lower(history_home.status) IN ('ft', 'aet', 'pen', 'finished')
      AND history_home.home_score IS NOT NULL
      AND history_home.away_score IS NOT NULL
      AND (history_home.home_team_id = m.home_team_id OR history_home.away_team_id = m.home_team_id)
  ) AS home_history_count,
  (
    SELECT COUNT(*)
    FROM matches history_away
    WHERE history_away.kickoff_at < m.kickoff_at
      AND lower(history_away.status) IN ('ft', 'aet', 'pen', 'finished')
      AND history_away.home_score IS NOT NULL
      AND history_away.away_score IS NOT NULL
      AND (history_away.home_team_id = m.away_team_id OR history_away.away_team_id = m.away_team_id)
  ) AS away_history_count,
  (
    SELECT COALESCE(string_agg(letter, ' ' ORDER BY kickoff_at DESC), '')
    FROM (
      SELECT
        kickoff_at,
        CASE
          WHEN home_team_id = m.home_team_id THEN
            (CASE WHEN home_score > away_score THEN 'W' WHEN home_score < away_score THEN 'L' ELSE 'D' END)
          ELSE
            (CASE WHEN away_score > home_score THEN 'W' WHEN away_score < home_score THEN 'L' ELSE 'D' END)
        END AS letter
      FROM matches
      WHERE (home_team_id = m.home_team_id OR away_team_id = m.home_team_id)
        AND kickoff_at < m.kickoff_at
        AND lower(status) IN ('ft', 'aet', 'pen', 'finished')
        AND home_score IS NOT NULL
        AND away_score IS NOT NULL
      ORDER BY kickoff_at DESC
      LIMIT 5
    ) recent
  ) AS home_form,
  (
    SELECT COALESCE(string_agg(letter, ' ' ORDER BY kickoff_at DESC), '')
    FROM (
      SELECT
        kickoff_at,
        CASE
          WHEN home_team_id = m.away_team_id THEN
            (CASE WHEN home_score > away_score THEN 'W' WHEN home_score < away_score THEN 'L' ELSE 'D' END)
          ELSE
            (CASE WHEN away_score > home_score THEN 'W' WHEN away_score < home_score THEN 'L' ELSE 'D' END)
        END AS letter
      FROM matches
      WHERE (home_team_id = m.away_team_id OR away_team_id = m.away_team_id)
        AND kickoff_at < m.kickoff_at
        AND lower(status) IN ('ft', 'aet', 'pen', 'finished')
        AND home_score IS NOT NULL
        AND away_score IS NOT NULL
      ORDER BY kickoff_at DESC
      LIMIT 5
    ) recent
  ) AS away_form,
  (
    SELECT pgf.reason_code
    FROM prediction_generation_failures pgf
    JOIN odds_markets om ON om.id = pgf.market_id
    WHERE pgf.match_id = m.id
      AND om.market_key = '1x2'
    ORDER BY pgf.updated_at DESC, pgf.id DESC
    LIMIT 1
  ) AS failure_reason_code,
  ep.public_id AS prediction_id,
  ep.prediction AS prediction_selection,
  ep.confidence * 100 AS prediction_confidence,
  (
    SELECT COUNT(*)
    FROM engine_predictions versioned
    WHERE versioned.fixture_id = ep.fixture_id
      AND versioned.market = ep.market
      AND (
        versioned.generated_at < ep.generated_at
        OR (
          versioned.generated_at = ep.generated_at
          AND versioned.public_id <= ep.public_id
        )
      )
  ) AS prediction_version,
  ep.generated_at AS prediction_published_at,
  ep.updated_at AS prediction_updated_at,
  ep.last_evaluated_at AS prediction_last_evaluated_at,
  -- How the headline pick is doing right now, from the SAME table and the
  -- same join the match page reads. Not derived here and not recomputed: a
  -- list and a match page that each worked out "on track" for themselves
  -- would eventually disagree about one pick, which is the whole reason the
  -- verdict is computed once and stored.
  ts.state AS prediction_tracker_state
FROM matches m
LEFT JOIN fixtures fixture_lifecycle ON fixture_lifecycle.id = m.id
LEFT JOIN leagues l ON l.id = m.league_id
LEFT JOIN teams home ON home.id = m.home_team_id
LEFT JOIN teams away ON away.id = m.away_team_id
LEFT JOIN engine_predictions ep ON ep.public_id = (
  SELECT current_ep.public_id
  FROM engine_predictions current_ep
  WHERE current_ep.fixture_id = m.id
    AND current_ep.market = '1x2'
    AND current_ep.generated_at <= m.kickoff_at
  ORDER BY current_ep.generated_at DESC, current_ep.public_id DESC
  LIMIT 1
)
  AND ep.gate_tier IN ('BASIC', 'FULL')
  AND ep.horizon_eligible = true
  AND ep.embargo_active = false
LEFT JOIN prediction_tracker_state ts ON ts.prediction_id = ep.id
`+fixturepolicy.UnresolvedEarlierFixtureClause("m", s.now())+`
%s
-- Ranked competitions lead, then kickoff: a truncated day list ordered by
-- clock alone buries the Premier League behind thousands of earlier
-- lower-division fixtures. Unranked leagues keep their kickoff order.
ORDER BY COALESCE(l.priority_score, 0) DESC, m.kickoff_at ASC, m.id ASC
%s`, where, limitClause), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	matches := []httpserver.MatchSummary{}
	skip := 0
	if !query.IncludeUnavailable {
		skip = (page - 1) * limit
	}
	for rows.Next() {
		match, err := scanMatchSummary(rows)
		if err != nil {
			return nil, err
		}
		if !matchesStatusFilter(match.Status, query.Statuses) {
			continue
		}
		if !query.IncludeUnavailable && !matchActionable(match) {
			continue
		}
		if skip > 0 {
			skip--
			continue
		}
		matches = append(matches, match)
		if len(matches) >= limit {
			break
		}
	}
	return matches, rows.Err()
}

// CountMatches returns the total number of matches matching the query's date
// window and status filter, used for pagination metadata.
func (s Source) CountMatches(ctx context.Context, query httpserver.MatchQuery) (int, error) {
	if s.db == nil {
		return 0, errors.New("matches source DB is nil")
	}
	where, args, err := s.matchWhere(query)
	if err != nil {
		return 0, err
	}
	var total int
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM matches m %s`, where), args...).Scan(&total); err != nil {
		return 0, err
	}
	return total, nil
}

func (s Source) matchWhere(query httpserver.MatchQuery) (string, []any, error) {
	selectedDate, err := normalizeQueryDate(query, s.now)
	if err != nil {
		return "", nil, err
	}
	location, err := loadTimezone(query.Timezone)
	if err != nil {
		return "", nil, err
	}
	dayStart := selectedDate.In(location)
	dayEnd := dayStart.AddDate(0, 0, 1)
	if query.ResultDetailWindow > 0 {
		cutoff := matchResultDetailCutoff(s.now(), location, query.ResultDetailWindow)
		if cutoff.After(dayStart) {
			dayStart = cutoff
		}
		if !dayStart.Before(dayEnd) {
			return "WHERE 1 = 0", nil, nil
		}
	}
	args := []any{
		dayStart.UTC().Format(time.RFC3339),
		dayEnd.UTC().Format(time.RFC3339),
	}
	statusClause := ""
	if statuses := databaseMatchStatuses(query.Statuses); len(statuses) > 0 {
		placeholders := make([]string, 0, len(statuses))
		for _, status := range statuses {
			args = append(args, status)
			placeholders = append(placeholders, fmt.Sprintf("$%d", len(args)))
		}
		statusClause = fmt.Sprintf("AND lower(COALESCE(m.status, 'upcoming')) IN (%s)", strings.Join(placeholders, ", "))
	}
	return fmt.Sprintf("WHERE m.kickoff_at >= $1 AND m.kickoff_at < $2 AND NOT EXISTS (SELECT 1 FROM fixture_identity_aliases fia WHERE fia.old_id = m.id AND fia.revoked_at IS NULL) %s", statusClause), args, nil
}

func (s Source) GetMatch(ctx context.Context, matchID int64) (httpserver.MatchSummary, error) {
	if s.db == nil {
		return httpserver.MatchSummary{}, errors.New("matches source DB is nil")
	}
	// An approved alias changes the public identity, never the original match
	// or prediction records. Returning the target ID lets the public page use
	// its existing one-hop canonical URL redirect.
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(
  (SELECT canonical_id FROM fixture_identity_aliases WHERE old_id = $1 AND revoked_at IS NULL), $1
)`, matchID).Scan(&matchID); err != nil {
		return httpserver.MatchSummary{}, err
	}
	match, err := scanMatchSummary(s.db.QueryRowContext(ctx, `
SELECT
  m.id,
  COALESCE(l.name, 'Competition'),
  COALESCE(l.country_code, 'International'),
  COALESCE(l.slug, ''),
  COALESCE(home.name, 'Home'),
  COALESCE(away.name, 'Away'),
  COALESCE(home.logo_url, ''),
  COALESCE(away.logo_url, ''),
  COALESCE(home.slug, ''),
  COALESCE(away.slug, ''),
  m.kickoff_at,
  fixture_lifecycle.finished_at,
  COALESCE(m.status, 'upcoming'),
  COALESCE(m.provider_status_raw, ''),
  COALESCE(m.provider_status_label, ''),
  m.status_elapsed,
  m.live_state_at,
  COALESCE(m.venue_name, ''),
  COALESCE(m.venue_city, ''),
  m.home_score,
  m.away_score,
  m.half_time_home_score,
  m.half_time_away_score,
  (
    SELECT os.payload
    FROM odds_snapshots os
    JOIN odds_markets om ON om.id = os.market_id
    WHERE os.match_id = m.id
      AND om.market_key = '1x2'
    ORDER BY os.captured_at DESC, os.id DESC
    LIMIT 1
  ) AS odds_payload,
  (
    SELECT COUNT(*)
    FROM matches history_home
    WHERE history_home.kickoff_at < m.kickoff_at
      AND lower(history_home.status) IN ('ft', 'aet', 'pen', 'finished')
      AND history_home.home_score IS NOT NULL
      AND history_home.away_score IS NOT NULL
      AND (history_home.home_team_id = m.home_team_id OR history_home.away_team_id = m.home_team_id)
  ) AS home_history_count,
  (
    SELECT COUNT(*)
    FROM matches history_away
    WHERE history_away.kickoff_at < m.kickoff_at
      AND lower(history_away.status) IN ('ft', 'aet', 'pen', 'finished')
      AND history_away.home_score IS NOT NULL
      AND history_away.away_score IS NOT NULL
      AND (history_away.home_team_id = m.away_team_id OR history_away.away_team_id = m.away_team_id)
  ) AS away_history_count,
  (
    SELECT COALESCE(string_agg(letter, ' ' ORDER BY kickoff_at DESC), '')
    FROM (
      SELECT
        kickoff_at,
        CASE
          WHEN home_team_id = m.home_team_id THEN
            (CASE WHEN home_score > away_score THEN 'W' WHEN home_score < away_score THEN 'L' ELSE 'D' END)
          ELSE
            (CASE WHEN away_score > home_score THEN 'W' WHEN away_score < home_score THEN 'L' ELSE 'D' END)
        END AS letter
      FROM matches
      WHERE (home_team_id = m.home_team_id OR away_team_id = m.home_team_id)
        AND kickoff_at < m.kickoff_at
        AND lower(status) IN ('ft', 'aet', 'pen', 'finished')
        AND home_score IS NOT NULL
        AND away_score IS NOT NULL
      ORDER BY kickoff_at DESC
      LIMIT 5
    ) recent
  ) AS home_form,
  (
    SELECT COALESCE(string_agg(letter, ' ' ORDER BY kickoff_at DESC), '')
    FROM (
      SELECT
        kickoff_at,
        CASE
          WHEN home_team_id = m.away_team_id THEN
            (CASE WHEN home_score > away_score THEN 'W' WHEN home_score < away_score THEN 'L' ELSE 'D' END)
          ELSE
            (CASE WHEN away_score > home_score THEN 'W' WHEN away_score < home_score THEN 'L' ELSE 'D' END)
        END AS letter
      FROM matches
      WHERE (home_team_id = m.away_team_id OR away_team_id = m.away_team_id)
        AND kickoff_at < m.kickoff_at
        AND lower(status) IN ('ft', 'aet', 'pen', 'finished')
        AND home_score IS NOT NULL
        AND away_score IS NOT NULL
      ORDER BY kickoff_at DESC
      LIMIT 5
    ) recent
  ) AS away_form,
  (
    SELECT pgf.reason_code
    FROM prediction_generation_failures pgf
    JOIN odds_markets om ON om.id = pgf.market_id
    WHERE pgf.match_id = m.id
      AND om.market_key = '1x2'
    ORDER BY pgf.updated_at DESC, pgf.id DESC
    LIMIT 1
  ) AS failure_reason_code,
  ep.public_id AS prediction_id,
  ep.prediction AS prediction_selection,
  ep.confidence * 100 AS prediction_confidence,
  (
    SELECT COUNT(*)
    FROM engine_predictions versioned
    WHERE versioned.fixture_id = ep.fixture_id
      AND versioned.market = ep.market
      AND (
        versioned.generated_at < ep.generated_at
        OR (
          versioned.generated_at = ep.generated_at
          AND versioned.public_id <= ep.public_id
        )
      )
  ) AS prediction_version,
  ep.generated_at AS prediction_published_at,
  ep.updated_at AS prediction_updated_at,
  ep.last_evaluated_at AS prediction_last_evaluated_at,
  -- How the headline pick is doing right now, from the SAME table and the
  -- same join the match page reads. Not derived here and not recomputed: a
  -- list and a match page that each worked out "on track" for themselves
  -- would eventually disagree about one pick, which is the whole reason the
  -- verdict is computed once and stored.
  ts.state AS prediction_tracker_state
FROM matches m
LEFT JOIN fixtures fixture_lifecycle ON fixture_lifecycle.id = m.id
LEFT JOIN leagues l ON l.id = m.league_id
LEFT JOIN teams home ON home.id = m.home_team_id
LEFT JOIN teams away ON away.id = m.away_team_id
LEFT JOIN engine_predictions ep ON ep.public_id = (
  SELECT current_ep.public_id
  FROM engine_predictions current_ep
  WHERE current_ep.fixture_id = m.id
    AND current_ep.market = '1x2'
    AND current_ep.generated_at <= m.kickoff_at
  ORDER BY current_ep.generated_at DESC, current_ep.public_id DESC
  LIMIT 1
)
  AND ep.gate_tier IN ('BASIC', 'FULL')
  AND ep.horizon_eligible = true
  AND ep.embargo_active = false
LEFT JOIN prediction_tracker_state ts ON ts.prediction_id = ep.id
`+fixturepolicy.UnresolvedEarlierFixtureClause("m", s.now())+`
WHERE m.id = $1
`, matchID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return httpserver.MatchSummary{}, httpserver.ErrMatchNotFound
		}
		return httpserver.MatchSummary{}, err
	}
	// Match detail must resolve every canonical fixture the UI links to —
	// league/team "recent results" include finished fixtures older than the
	// public result-detail window, so detail is deliberately not window-gated.
	predictions, err := s.matchPredictions(ctx, matchID)
	if err != nil {
		return httpserver.MatchSummary{}, err
	}
	if len(predictions) == 0 {
		predictions, err = s.providerMatchPredictions(ctx, matchID)
		if err != nil {
			return httpserver.MatchSummary{}, err
		}
	}
	match.Predictions = predictions
	if match.Prediction == nil && len(predictions) > 0 {
		primary := predictions[0]
		match.Prediction = &primary
		match.PredictionState = "published"
	} else if len(predictions) == 0 {
		state, reason, err := s.matchBackfillState(ctx, matchID)
		if err != nil {
			return httpserver.MatchSummary{}, err
		}
		if state != "" {
			match.PredictionState = state
			match.PredictionStateReason = reason
		}
	}
	// Only for fixtures that can have events. Detail views are dominated by
	// matches that have not kicked off, and reading a timeline for one is a
	// guaranteed-empty query on every such request.
	if timelineWorthReading(match.Status) {
		timeline, err := s.matchTimeline(ctx, matchID)
		if err != nil {
			// Degraded, not failed. The timeline is an addition to a page that
			// was complete without it, so a timeline that cannot be read costs
			// the reader the events -- not the match, the prediction and the
			// odds as well.
			slog.Error("match timeline unavailable", "match_id", matchID, "error", err)
		} else {
			match.Timeline = timeline
		}
	}
	// Same rule as the timeline and for the same reason: statistics are an
	// addition to a page that is complete without them, and only exist once a
	// match has started.
	if timelineWorthReading(match.Status) {
		statistics, err := s.matchStatistics(ctx, matchID)
		if err != nil {
			slog.Error("match statistics unavailable", "match_id", matchID, "error", err)
		} else {
			match.Statistics = statistics
		}
	}
	// Degraded, not failed, for the same reason as the timeline: card figures
	// add to a page that is complete without them.
	discipline, err := s.matchDiscipline(ctx, matchID)
	if err != nil {
		slog.Error("match discipline unavailable", "match_id", matchID, "error", err)
	} else {
		match.Discipline = discipline
	}
	return match, nil
}

// matchStatistics loads both sides' in-match statistics.
//
// Returns nil, not an empty struct, when the provider has reported nothing for
// this fixture. The caller omits the whole block in that case, so an upcoming
// match does not carry sixteen absent fields.
//
// Which row is home and which is away is decided by joining team_id against the
// match, not by row order. fixture_statistics has no ordering guarantee, and
// trusting one would silently swap the two columns on some fixtures -- a defect
// that looks like bad data rather than a bug.
func (s Source) matchStatistics(ctx context.Context, matchID int64) (*httpserver.MatchStatistics, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
  fs.team_id = m.home_team_id AS is_home,
  fs.possession_pct,
  fs.shots_total,
  fs.shots_on_target,
  fs.corners,
  fs.fouls,
  fs.offsides,
  fs.yellow_cards,
  fs.red_cards
FROM fixture_statistics fs
JOIN matches m ON m.id = fs.fixture_id
WHERE fs.fixture_id = $1
  AND fs.team_id IN (m.home_team_id, m.away_team_id)`, matchID)
	if err != nil {
		return nil, fmt.Errorf("query match statistics: %w", err)
	}
	defer rows.Close()

	var (
		stats httpserver.MatchStatistics
		found bool
	)
	for rows.Next() {
		var (
			isHome                          bool
			possession                      sql.NullFloat64
			shots, onTarget, corners, fouls sql.NullInt64
			offsides, yellow, red           sql.NullInt64
		)
		if err := rows.Scan(&isHome, &possession, &shots, &onTarget, &corners,
			&fouls, &offsides, &yellow, &red); err != nil {
			return nil, fmt.Errorf("scan match statistics: %w", err)
		}
		side := &stats.Away
		if isHome {
			side = &stats.Home
		}
		if possession.Valid {
			value := possession.Float64
			side.PossessionPct = &value
		}
		for target, value := range map[**int]sql.NullInt64{
			&side.ShotsTotal:    shots,
			&side.ShotsOnTarget: onTarget,
			&side.Corners:       corners,
			&side.Fouls:         fouls,
			&side.Offsides:      offsides,
			&side.YellowCards:   yellow,
			&side.RedCards:      red,
		} {
			if value.Valid {
				converted := int(value.Int64)
				*target = &converted
			}
		}
		found = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate match statistics: %w", err)
	}
	if !found {
		return nil, nil
	}
	return &stats, nil
}

// timelineWorthReading is true for matches that have started.
//
// Postponed and cancelled fixtures are excluded for the same reason upcoming
// ones are: no ball was kicked, so there is nothing to read.
func timelineWorthReading(status string) bool {
	if isLiveLikeStatus(status) {
		return true
	}
	switch status {
	case "finished", "interrupted", "suspended", "abandoned", "awarded", "walkover":
		return true
	default:
		return false
	}
}

func (s Source) matchBackfillState(ctx context.Context, matchID int64) (string, string, error) {
	var scheduleEligible bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM matches m WHERE m.id = $1
`+fixturepolicy.UnresolvedEarlierFixtureClause("m", s.now())+`
)`, matchID).Scan(&scheduleEligible); err != nil {
		return "", "", err
	}
	if !scheduleEligible {
		return "pending", "earlier_fixture_pending", nil
	}
	var active int
	var unavailable int
	err := s.db.QueryRowContext(ctx, `
SELECT
  COALESCE(SUM(CASE WHEN status IN ('pending', 'in_progress') THEN 1 ELSE 0 END), 0),
  COALESCE(SUM(CASE WHEN status = 'confirmed_unavailable' AND feature_group = 'results' THEN 1 ELSE 0 END), 0)
FROM data_backfill_requests
WHERE target_fixture_id = $1`, matchID).Scan(&active, &unavailable)
	if err != nil {
		return "", "", err
	}
	if active > 0 {
		return "pending", "backfill_pending", nil
	}
	if unavailable > 0 {
		return "insufficient_history", "PROVIDER_CONFIRMED_NO_HISTORY", nil
	}
	return "", "", nil
}

// matchPredictions returns all current (non-superseded) predictions for a
// match — one per market, generated at or before kickoff.
func (s Source) matchPredictions(ctx context.Context, matchID int64) ([]httpserver.MatchPredictionSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
  ep.market,
  ep.prediction,
  COALESCE(ep.confidence, 0) * 100,
  ep.public_id,
  ep.generated_at,
  CASE
    WHEN pr.prediction_id IS NOT NULL THEN CASE
      WHEN pr.settlement_status IN ('void', 'unsettled_data_unavailable') THEN 'void'
      WHEN pr.settlement_status = 'settled' AND pr.is_correct THEN 'won'
      WHEN pr.settlement_status = 'settled' AND pr.is_correct = false THEN 'lost'
      ELSE 'pending' END
    WHEN ef.settlement_classification = 'correct' THEN 'won'
    WHEN ef.settlement_classification = 'incorrect' THEN 'lost'
    WHEN ef.settlement_classification = 'void' THEN 'void'
    ELSE 'pending'
  END,
  CASE WHEN pr.prediction_id IS NOT NULL THEN COALESCE(pr.actual, '')
    WHEN ef.settlement_classification IN ('correct', 'incorrect', 'void') THEN COALESCE(ef.final_outcome, '')
    ELSE '' END,
  -- Only surfaced for a void the market rule did not produce. A NULL here on
  -- a void row is the historical "drawn draw-no-bet" case and needs no
  -- explanation; a value means no trustworthy result ever existed.
  COALESCE(pr.void_reason,
    CASE WHEN pr.prediction_id IS NULL AND ef.settlement_classification = 'void' THEN ef.exclusion_reason END, ''),
  -- How the pick is doing right now, computed by the engine so the two client
  -- apps cannot judge it differently. Empty when nothing has computed it -- a
  -- fixture the in-play pass has never covered -- and a client then shows no
  -- tracker rather than guessing.
  COALESCE(ts.state, '')
FROM engine_predictions ep
JOIN matches m ON m.id = ep.fixture_id
LEFT JOIN prediction_results pr ON pr.prediction_id = ep.id
-- Retention can prune the mutable result row. The corrected, revision-aware
-- fact projection is retained; only BTTS uses this fallback for now.
LEFT JOIN effective_model_evaluation_facts ef ON ef.prediction_id = ep.id AND ep.market = 'btts_total_goals'
LEFT JOIN prediction_tracker_state ts ON ts.prediction_id = ep.id
WHERE ep.fixture_id = $1
  AND ep.generated_at <= m.kickoff_at
  AND ep.gate_tier IN ('BASIC', 'FULL')
  AND ep.horizon_eligible = true
  AND ep.embargo_active = false
`+fixturepolicy.UnresolvedEarlierFixtureClause("m", s.now())+`
  AND NOT EXISTS (
    SELECT 1
    FROM engine_predictions newer
    WHERE newer.fixture_id = ep.fixture_id
      AND newer.market = ep.market
      AND newer.generated_at <= m.kickoff_at
      AND (
        newer.generated_at > ep.generated_at
        OR (newer.generated_at = ep.generated_at AND newer.public_id > ep.public_id)
      )
  )
ORDER BY ep.market ASC`, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	predictions := []httpserver.MatchPredictionSummary{}
	for rows.Next() {
		var prediction httpserver.MatchPredictionSummary
		var publishedAt any
		if err := rows.Scan(&prediction.MarketKey, &prediction.Selection, &prediction.Confidence, &prediction.ID, &publishedAt, &prediction.Outcome, &prediction.Actual, &prediction.VoidReason, &prediction.TrackerState); err != nil {
			return nil, err
		}
		if parsed, err := parseTimestamp(publishedAt); err == nil {
			prediction.PublishedAt = parsed
			prediction.UpdatedAt = parsed
			prediction.LastEvaluatedAt = parsed
		}
		predictions = append(predictions, prediction)
	}
	return predictions, rows.Err()
}

func (s Source) providerMatchPredictions(ctx context.Context, matchID int64) ([]httpserver.MatchPredictionSummary, error) {
	var summary httpserver.ProviderPredictionSummary
	var capturedAt any
	err := s.db.QueryRowContext(ctx, `
SELECT
  m.id,
  COALESCE(home.name, 'Home'),
  COALESCE(away.name, 'Away'),
  COALESCE(ap.predicted_winner, ''),
  COALESCE(ap.under_over, ''),
  COALESCE(ap.percent_home, 0),
  COALESCE(ap.percent_draw, 0),
  COALESCE(ap.percent_away, 0),
  ap.captured_at
FROM api_predictions AS ap
JOIN matches AS m ON m.id = ap.fixture_id
LEFT JOIN teams AS home ON home.id = m.home_team_id
LEFT JOIN teams AS away ON away.id = m.away_team_id
WHERE ap.fixture_id = $1
`+fixturepolicy.UnresolvedEarlierFixtureClause("m", s.now())+`
ORDER BY ap.captured_at DESC
LIMIT 1`, matchID).Scan(
		&summary.MatchID,
		&summary.HomeTeam,
		&summary.AwayTeam,
		&summary.PredictedWinner,
		&summary.UnderOver,
		&summary.PercentHome,
		&summary.PercentDraw,
		&summary.PercentAway,
		&capturedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if summary.PublishedAt, err = parseTimestamp(capturedAt); err != nil {
		return nil, err
	}
	return httpserver.CanonicalProviderMatchPredictions(summary), nil
}

func matchResultDetailCutoff(now time.Time, location *time.Location, window time.Duration) time.Time {
	threshold := now.In(location).Add(-window)
	return time.Date(threshold.Year(), threshold.Month(), threshold.Day(), 0, 0, 0, 0, location)
}

func matchStatusIsSettled(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "ft", "aet", "pen", "finished":
		return true
	default:
		return false
	}
}

func scanMatchSummary(scanner interface {
	Scan(...any) error
}) (httpserver.MatchSummary, error) {
	var match httpserver.MatchSummary
	var kickoffAt any
	var finishedAt any
	var statusElapsed sql.NullInt64
	var homeTeamLogo sql.NullString
	var awayTeamLogo sql.NullString
	var homeTeamSlug sql.NullString
	var awayTeamSlug sql.NullString
	var venueName sql.NullString
	var venueCity sql.NullString
	var homeScore sql.NullInt64
	var awayScore sql.NullInt64
	var halfTimeHomeScore sql.NullInt64
	var halfTimeAwayScore sql.NullInt64
	var oddsPayload any
	var homeHistoryCount sql.NullInt64
	var awayHistoryCount sql.NullInt64
	var homeForm sql.NullString
	var awayForm sql.NullString
	var liveStateAt any
	var failureReason sql.NullString
	var predictionID sql.NullInt64
	var predictionSelection sql.NullString
	var predictionConfidence sql.NullFloat64
	var predictionVersion sql.NullInt64
	var predictionPublishedAt any
	var predictionUpdatedAt any
	var predictionLastEvaluatedAt any
	var predictionTrackerState sql.NullString

	if err := scanner.Scan(
		&match.ID,
		&match.LeagueName,
		&match.Country,
		&match.LeagueSlug,
		&match.HomeTeam,
		&match.AwayTeam,
		&homeTeamLogo,
		&awayTeamLogo,
		&homeTeamSlug,
		&awayTeamSlug,
		&kickoffAt,
		&finishedAt,
		&match.Status,
		&match.RawStatus,
		&match.RawStatusLabel,
		&statusElapsed,
		&liveStateAt,
		&venueName,
		&venueCity,
		&homeScore,
		&awayScore,
		&halfTimeHomeScore,
		&halfTimeAwayScore,
		&oddsPayload,
		&homeHistoryCount,
		&awayHistoryCount,
		&homeForm,
		&awayForm,
		&failureReason,
		&predictionID,
		&predictionSelection,
		&predictionConfidence,
		&predictionVersion,
		&predictionPublishedAt,
		&predictionUpdatedAt,
		&predictionLastEvaluatedAt,
		&predictionTrackerState,
	); err != nil {
		return httpserver.MatchSummary{}, err
	}
	parsedKickoffAt, err := parseTimestamp(kickoffAt)
	if err != nil {
		return httpserver.MatchSummary{}, err
	}
	match.KickoffAt = parsedKickoffAt
	if finishedAt != nil {
		parsedFinishedAt, err := parseTimestamp(finishedAt)
		if err != nil {
			return httpserver.MatchSummary{}, err
		}
		match.FinishedAt = &parsedFinishedAt
	}

	match.Status = normalizeLegacyStatus(match.Status)
	match.HomeTeamLogo = nullableString(homeTeamLogo)
	match.AwayTeamLogo = nullableString(awayTeamLogo)
	match.HomeTeamSlug = nullableString(homeTeamSlug)
	match.AwayTeamSlug = nullableString(awayTeamSlug)
	match.StatusElapsed = nullableInt(statusElapsed)
	if liveStateAt != nil {
		parsedLiveStateAt, err := parseTimestamp(liveStateAt)
		if err != nil {
			return httpserver.MatchSummary{}, err
		}
		match.LiveStateAt = &parsedLiveStateAt
	}
	match.VenueName = nullableString(venueName)
	match.VenueCity = nullableString(venueCity)
	match.HomeScore = nullableInt(homeScore)
	match.AwayScore = nullableInt(awayScore)
	match.HalfTimeHomeScore = nullableInt(halfTimeHomeScore)
	match.HalfTimeAwayScore = nullableInt(halfTimeAwayScore)

	odds, err := parseOddsPayload(oddsPayload)
	if err != nil {
		return httpserver.MatchSummary{}, err
	}
	match.Odds = odds
	match.HomeHistoryCount = int(homeHistoryCount.Int64)
	match.AwayHistoryCount = int(awayHistoryCount.Int64)
	match.HomeForm = nullableString(homeForm)
	match.AwayForm = nullableString(awayForm)

	if predictionID.Valid {
		publishedAt := time.Time{}
		if predictionPublishedAt != nil {
			publishedAt, err = parseTimestamp(predictionPublishedAt)
			if err != nil {
				return httpserver.MatchSummary{}, err
			}
		}
		updatedAt := publishedAt
		if predictionUpdatedAt != nil {
			updatedAt, err = parseTimestamp(predictionUpdatedAt)
			if err != nil {
				return httpserver.MatchSummary{}, err
			}
		}
		lastEvaluatedAt := updatedAt
		if predictionLastEvaluatedAt != nil {
			lastEvaluatedAt, err = parseTimestamp(predictionLastEvaluatedAt)
			if err != nil {
				return httpserver.MatchSummary{}, err
			}
		}
		match.Prediction = &httpserver.MatchPredictionSummary{
			ID:               predictionID.Int64,
			MarketKey:        "1x2",
			Selection:        predictionSelection.String,
			Confidence:       predictionConfidence.Float64,
			PublishedVersion: int(predictionVersion.Int64),
			PublishedAt:      publishedAt,
			UpdatedAt:        updatedAt,
			LastEvaluatedAt:  lastEvaluatedAt,
			// Empty when the engine has not judged this pick yet -- a fixture
			// the in-play pass has never covered. Empty is not "unknown":
			// unknown is a verdict the engine reached because the match state
			// cannot decide the pick either way, and it arrives as that word.
			TrackerState: nullableString(predictionTrackerState),
		}
		match.PredictionState = "published"
		match.PredictionStateReason = "published"
		return match, nil
	}
	if failureReason.Valid && failureReason.String != "" {
		match.PredictionState = "failed_generation"
		match.PredictionStateReason = failureReason.String
		return match, nil
	}
	if !hasMinimumHistory(match) {
		match.PredictionState = "insufficient_history"
		match.PredictionStateReason = "insufficient_history"
		return match, nil
	}
	match.PredictionState = "pending"
	match.PredictionStateReason = "pending_generation"
	return match, nil
}

func matchActionable(match httpserver.MatchSummary) bool {
	if match.Prediction != nil {
		return true
	}
	return hasMinimumHistory(match)
}

func hasMinimumHistory(match httpserver.MatchSummary) bool {
	return match.HomeHistoryCount >= minimumHistoryMatchesPerTeam &&
		match.AwayHistoryCount >= minimumHistoryMatchesPerTeam
}

func normalizeQueryDate(query Query, now func() time.Time) (time.Time, error) {
	location, err := loadTimezone(query.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	if strings.TrimSpace(query.Date) != "" {
		parsed, err := time.ParseInLocation("2006-01-02", query.Date, location)
		if err != nil {
			return time.Time{}, err
		}
		return parsed, nil
	}
	base := now().In(location)
	switch strings.TrimSpace(query.Day) {
	case "", "today":
		return truncateDate(base), nil
	case "yesterday":
		return truncateDate(base.AddDate(0, 0, -1)), nil
	case "tomorrow":
		return truncateDate(base.AddDate(0, 0, 1)), nil
	default:
		return time.Time{}, fmt.Errorf("unsupported day filter %q", query.Day)
	}
}

func truncateDate(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, value.Location())
}

func defaultTimezone(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "Africa/Nairobi"
	}
	return trimmed
}

func loadTimezone(value string) (*time.Location, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "Africa/Nairobi" {
		return time.FixedZone("EAT", 3*60*60), nil
	}
	return time.LoadLocation(trimmed)
}

func matchesStatusFilter(status string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	status = normalizeLegacyStatus(status)
	for _, filter := range filters {
		switch strings.TrimSpace(filter) {
		case "live":
			if isLiveLikeStatus(status) {
				return true
			}
		case "finished":
			if status == "finished" {
				return true
			}
		case "upcoming":
			if isUpcomingLikeStatus(status) {
				return true
			}
		default:
			if status == filter {
				return true
			}
		}
	}
	return false
}

func databaseMatchStatuses(filters []string) []string {
	if len(filters) == 0 {
		return nil
	}
	seen := map[string]bool{}
	statuses := make([]string, 0, len(filters))
	add := func(values ...string) {
		for _, value := range values {
			if !seen[value] {
				seen[value] = true
				statuses = append(statuses, value)
			}
		}
	}
	for _, filter := range filters {
		switch strings.TrimSpace(filter) {
		case "live":
			add("live", "halftime", "second_half", "extra_time", "penalties", "break")
		case "upcoming":
			add("scheduled", "upcoming", "delayed")
		case "finished":
			add("finished")
		case "":
			continue
		default:
			add(strings.TrimSpace(filter))
		}
	}
	return statuses
}

func isLiveLikeStatus(status string) bool {
	switch status {
	case "live", "halftime", "second_half", "extra_time", "penalties", "break":
		return true
	default:
		return false
	}
}

func isUpcomingLikeStatus(status string) bool {
	switch status {
	case "scheduled", "upcoming", "delayed":
		return true
	default:
		return false
	}
}

func normalizeLegacyStatus(status string) string {
	switch status {
	case "scheduled":
		return "upcoming"
	default:
		return status
	}
}

func parseOddsPayload(value any) ([]httpserver.OddsPrice, error) {
	body, err := jsonBody(value)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, nil
	}
	var payload struct {
		Prices []httpserver.OddsPrice `json:"prices"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload.Prices, nil
}

func jsonBody(value any) ([]byte, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	default:
		return nil, fmt.Errorf("unsupported odds payload type %T", value)
	}
}

func nullableInt(value sql.NullInt64) *int {
	if !value.Valid {
		return nil
	}
	converted := int(value.Int64)
	return &converted
}

func nullableString(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func parseTimestamp(value any) (time.Time, error) {
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

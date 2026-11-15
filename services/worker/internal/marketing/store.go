package marketing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Post is one row of the marketing_posts ledger.
type Post struct {
	ID        int64
	Key       string
	Channel   string
	Kind      string
	Status    string
	Caption   string
	CardToken string
	Payload   json.RawMessage
	Attempts  int
	CreatedAt time.Time
}

// Ledger channels.
const (
	ChannelTelegram = "telegram"
	ChannelFacebook = "facebook"
	ChannelOwner    = "owner"
)

// Ledger statuses.
const (
	StatusPending = "pending"
	StatusSent    = "sent"
	StatusFailed  = "failed"
	StatusSkipped = "skipped"
)

// MaxAttempts bounds retries of a failed post.
const MaxAttempts = 3

// Store is everything the routine reads and writes.
type Store interface {
	ClaimPost(ctx context.Context, post Post) (Post, error)
	BeginAttempt(ctx context.Context, id int64) error
	MarkSent(ctx context.Context, id int64, externalID string) error
	MarkFailed(ctx context.Context, id int64, cause string) error
	MarkSkipped(ctx context.Context, id int64, reason string) error
	KeyExists(ctx context.Context, key string) (bool, error)
	PostsByKind(ctx context.Context, kind string, since time.Time) ([]Post, error)
	CountKeysByPrefix(ctx context.Context, prefix string, since time.Time) (int, error)
	RetryablePosts(ctx context.Context, since time.Time) ([]Post, error)

	CandidatePicks(ctx context.Context, q PickQuery) ([]Pick, error)
	GradePicks(ctx context.Context, picks []Pick) ([]GradedPick, error)
	RecentGoals(ctx context.Context, q GoalQuery) ([]Goal, error)
	LatestScores(ctx context.Context, fixtureIDs []int64) (map[int64][2]int, error)
	RecentForm(ctx context.Context, teamID int64, before time.Time, limit int) ([]FormResult, error)
	HeadToHead(ctx context.Context, homeID, awayID int64, before time.Time, limit int) ([]FormResult, error)
	MarketRecord(ctx context.Context, q RecordQuery) (MarketRecord, error)
	WeeklyNumbers(ctx context.Context, since time.Time) (Numbers, error)
}

// PickQuery chooses the morning picks.
type PickQuery struct {
	From, To          time.Time
	LeagueIDs         []int64
	MinLeaguePriority int
	Limit             int
}

// GoalQuery chooses which goals are worth posting.
type GoalQuery struct {
	Since             time.Time
	FixtureIDs        []int64
	LeagueIDs         []int64
	MinLeaguePriority int
}

// FormResult is one finished match from a team's point of view.
type FormResult struct {
	HomeTeamID int64
	AwayTeamID int64
	HomeGoals  int
	AwayGoals  int
}

// RecordQuery is a market's graded record over a window.
type RecordQuery struct {
	Market            string
	Since             time.Time
	LeagueIDs         []int64
	MinLeaguePriority int
}

// MarketRecord is how a market's pre-match picks did.
type MarketRecord struct {
	Market  string
	Correct int
	Graded  int
}

// Numbers is the Sunday report.
type Numbers struct {
	Accounts          int
	NewAccounts       int
	ReturningAccounts int
	NewVisitors       int
	ActiveVisitors    int
	VisitorsBySource  []SourceCount
	PostsByChannel    []ChannelCount
}

// SourceCount is new visitors from one first-touch source.
type SourceCount struct {
	Source string
	Count  int
}

// ChannelCount is posts by channel and status.
type ChannelCount struct {
	Channel string
	Status  string
	Count   int
}

// PostgresStore implements Store.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore wraps a pgx database handle.
func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

const postColumns = `id, post_key, channel, kind, status, caption, card_token, payload, attempts, created_at`

func scanPost(row interface{ Scan(...any) error }) (Post, error) {
	var post Post
	var payload []byte
	err := row.Scan(&post.ID, &post.Key, &post.Channel, &post.Kind, &post.Status, &post.Caption, &post.CardToken, &payload, &post.Attempts, &post.CreatedAt)
	post.Payload = payload
	return post, err
}

// ClaimPost inserts the post, or returns the row already there. The content
// of an existing row wins, so a retry sends what the first attempt meant to.
func (s *PostgresStore) ClaimPost(ctx context.Context, post Post) (Post, error) {
	payload := post.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO marketing_posts (post_key, channel, kind, caption, card_token, payload)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (post_key, channel) DO NOTHING`, post.Key, post.Channel, post.Kind, post.Caption, post.CardToken, []byte(payload))
	if err != nil {
		return Post{}, fmt.Errorf("claim marketing post %s/%s: %w", post.Key, post.Channel, err)
	}
	return scanPost(s.db.QueryRowContext(ctx, `SELECT `+postColumns+` FROM marketing_posts WHERE post_key = $1 AND channel = $2`, post.Key, post.Channel))
}

func (s *PostgresStore) BeginAttempt(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE marketing_posts SET status = 'pending', attempts = attempts + 1, updated_at = now() WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) MarkSent(ctx context.Context, id int64, externalID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE marketing_posts SET status = 'sent', external_id = NULLIF($2, ''), last_error = NULL, sent_at = now(), updated_at = now() WHERE id = $1`, id, externalID)
	return err
}

func (s *PostgresStore) MarkFailed(ctx context.Context, id int64, cause string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE marketing_posts SET status = 'failed', last_error = left($2, 2000), updated_at = now() WHERE id = $1`, id, cause)
	return err
}

func (s *PostgresStore) MarkSkipped(ctx context.Context, id int64, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE marketing_posts SET status = 'skipped', last_error = left($2, 2000), updated_at = now() WHERE id = $1`, id, reason)
	return err
}

func (s *PostgresStore) KeyExists(ctx context.Context, key string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM marketing_posts WHERE post_key = $1)`, key).Scan(&exists)
	return exists, err
}

// PostsByKind returns one row per post key (the earliest channel row), so a
// pick posted to three channels is graded once.
func (s *PostgresStore) PostsByKind(ctx context.Context, kind string, since time.Time) ([]Post, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT ON (post_key) `+postColumns+`
FROM marketing_posts
WHERE kind = $1 AND created_at >= $2
ORDER BY post_key, id`, kind, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var posts []Post
	for rows.Next() {
		post, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, post)
	}
	return posts, rows.Err()
}

func (s *PostgresStore) CountKeysByPrefix(ctx context.Context, prefix string, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(DISTINCT post_key) FROM marketing_posts
WHERE starts_with(post_key, $1) AND created_at >= $2 AND status <> 'skipped'`, prefix, since).Scan(&count)
	return count, err
}

// RetryablePosts are failed posts with attempts left. A row left in pending
// after an attempt began is deliberately not retried: the channel may have
// accepted it before the worker died, and a duplicate in a public channel is
// worse than a missing post the owner is told about.
func (s *PostgresStore) RetryablePosts(ctx context.Context, since time.Time) ([]Post, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT `+postColumns+` FROM marketing_posts
WHERE status = 'failed' AND attempts < $1 AND created_at >= $2
ORDER BY id`, MaxAttempts, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var posts []Post
	for rows.Next() {
		post, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		posts = append(posts, post)
	}
	return posts, rows.Err()
}

func int64Array(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func textArray(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return "{" + strings.Join(quoted, ",") + "}"
}

// CandidatePicks applies the same publication filters as the public feed
// (services/api/internal/predictions/source.go): a gated tier, horizon
// eligible, not embargoed, current, generated before kick-off. Two more:
//
//   - best bets are excluded. They are gated to signed-in accounts, and
//     posting them in a public channel would hand out what the account wall
//     exists to protect. The caption points readers at them instead.
//   - one market per fixture, the most confident, so three picks are three
//     matches.
func (s *PostgresStore) CandidatePicks(ctx context.Context, q PickQuery) ([]Pick, error) {
	rows, err := s.db.QueryContext(ctx, `
WITH best_per_fixture AS (
  SELECT DISTINCT ON (ep.fixture_id)
    ep.id::text AS prediction_id, ep.fixture_id, ep.market, ep.prediction, ep.confidence,
    f.kickoff_at, f.league_id, f.home_team_id, f.away_team_id,
    COALESCE(l.name, ''), COALESCE(home.name, ''), COALESCE(away.name, ''),
    COALESCE(home.logo_url, ''), COALESCE(away.logo_url, ''), COALESCE(l.priority_score, 0) AS priority
  FROM engine_predictions ep
  JOIN fixtures f ON f.id = ep.fixture_id
  JOIN leagues l ON l.id = f.league_id
  JOIN teams home ON home.id = f.home_team_id
  JOIN teams away ON away.id = f.away_team_id
  WHERE f.kickoff_at >= $1 AND f.kickoff_at < $2
    AND ep.market = ANY($3::text[])
    AND ep.gate_tier IN ('BASIC', 'FULL')
    AND ep.horizon_eligible = true
    AND ep.embargo_active = false
    AND ep.superseded_at IS NULL
    AND ep.is_best_bet = false
    AND ep.generated_at <= f.kickoff_at
    AND lower(COALESCE(f.status, 'scheduled')) NOT IN ('ft', 'aet', 'pen', 'finished', 'canc', 'canceled', 'cancelled', 'abd', 'abandoned', 'pst', 'postponed', 'susp', 'int', 'awd', 'wo')
    AND (COALESCE(l.priority_score, 0) >= $4 OR f.league_id = ANY($5::bigint[]))
  ORDER BY ep.fixture_id, ep.confidence DESC, ep.public_id
)
SELECT * FROM best_per_fixture
ORDER BY confidence DESC, priority DESC, kickoff_at
LIMIT $6`, q.From, q.To, textArray(SupportedMarkets), q.MinLeaguePriority, int64Array(q.LeagueIDs), q.Limit)
	if err != nil {
		return nil, fmt.Errorf("query candidate picks: %w", err)
	}
	defer rows.Close()
	var picks []Pick
	for rows.Next() {
		var p Pick
		var priority int
		if err := rows.Scan(&p.PredictionID, &p.FixtureID, &p.Market, &p.Selection, &p.Confidence, &p.KickoffAt,
			&p.LeagueID, &p.HomeTeamID, &p.AwayTeamID, &p.League, &p.Home, &p.Away, &p.HomeLogo, &p.AwayLogo, &priority); err != nil {
			return nil, err
		}
		picks = append(picks, p)
	}
	return picks, rows.Err()
}

// GradePicks grades posted picks. The official settlement row is used when it
// settled the selection we posted; otherwise the final score is graded the
// way the engine grades it. Abandoned or cancelled matches are void, and
// anything else is still pending.
func (s *PostgresStore) GradePicks(ctx context.Context, picks []Pick) ([]GradedPick, error) {
	graded := make([]GradedPick, 0, len(picks))
	for _, p := range picks {
		g := GradedPick{Pick: p, Result: ResultPending}
		var status string
		var home, away sql.NullInt32
		err := s.db.QueryRowContext(ctx, `SELECT lower(COALESCE(status, '')), home_goals_ft, away_goals_ft FROM fixtures WHERE id = $1`, p.FixtureID).Scan(&status, &home, &away)
		if errors.Is(err, sql.ErrNoRows) {
			graded = append(graded, g)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read fixture %d: %w", p.FixtureID, err)
		}
		if home.Valid && away.Valid {
			h, a := int(home.Int32), int(away.Int32)
			g.HomeGoals, g.AwayGoals = &h, &a
		}

		var predicted, settlement string
		var correct sql.NullBool
		err = s.db.QueryRowContext(ctx, `
SELECT predicted, settlement_status, is_correct FROM prediction_results
WHERE fixture_id = $1 AND market = $2 AND lifecycle_stage_at_settlement IN ('EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED')
ORDER BY settled_at DESC LIMIT 1`, p.FixtureID, p.Market).Scan(&predicted, &settlement, &correct)
		switch {
		case err == nil && predicted == p.Selection && settlement == "settled" && correct.Valid:
			g.Result = ResultLost
			if correct.Bool {
				g.Result = ResultWon
			}
		case err == nil && predicted == p.Selection && (settlement == "void" || settlement == "unsettled_data_unavailable"):
			g.Result = ResultVoid
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return nil, fmt.Errorf("read settlement for fixture %d: %w", p.FixtureID, err)
		default:
			switch status {
			case "ft", "aet", "pen", "finished":
				if g.HomeGoals != nil && g.AwayGoals != nil {
					if result, ok := Grade(p.Market, p.Selection, *g.HomeGoals, *g.AwayGoals); ok {
						g.Result = result
					}
				}
			case "canc", "canceled", "cancelled", "abd", "abandoned", "awd", "wo", "pst", "postponed":
				g.Result = ResultVoid
			}
		}
		graded = append(graded, g)
	}
	return graded, nil
}

// RecentGoals reads goal-triggered in-play snapshots for watched matches.
// One row per scoreline: the first snapshot that showed it.
func (s *PostgresStore) RecentGoals(ctx context.Context, q GoalQuery) ([]Goal, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT ON (s.fixture_id, s.home_goals, s.away_goals)
  s.fixture_id, s.sequence, COALESCE(s.elapsed_minute, 0), COALESCE(s.elapsed_extra, 0),
  COALESCE(s.home_goals, 0), COALESCE(s.away_goals, 0), s.captured_at,
  COALESCE(l.name, ''), COALESCE(home.name, ''), COALESCE(away.name, ''),
  COALESCE(home.logo_url, ''), COALESCE(away.logo_url, ''),
  s.model_health, s.markets -> 'match_result_in_play',
  COALESCE((
    SELECT ev.player_name FROM fixture_events ev
    WHERE ev.fixture_id = s.fixture_id AND ev.event_type = 'Goal' AND NOT ev.is_retracted
      AND ev.elapsed_minute = s.elapsed_minute
    ORDER BY ev.sequence LIMIT 1
  ), '')
FROM inplay_snapshots s
JOIN fixtures f ON f.id = s.fixture_id
JOIN leagues l ON l.id = f.league_id
JOIN teams home ON home.id = f.home_team_id
JOIN teams away ON away.id = f.away_team_id
WHERE s.captured_at >= $1
  AND s.trigger = 'post_goal'
  AND (s.fixture_id = ANY($2::bigint[]) OR l.id = ANY($3::bigint[]) OR COALESCE(l.priority_score, 0) >= $4)
ORDER BY s.fixture_id, s.home_goals, s.away_goals, s.sequence`, q.Since, int64Array(q.FixtureIDs), int64Array(q.LeagueIDs), q.MinLeaguePriority)
	if err != nil {
		return nil, fmt.Errorf("query recent goals: %w", err)
	}
	defer rows.Close()
	var goals []Goal
	for rows.Next() {
		var g Goal
		var health string
		var probs []byte
		if err := rows.Scan(&g.FixtureID, &g.Sequence, &g.Minute, &g.Extra, &g.HomeGoals, &g.AwayGoals, &g.CapturedAt,
			&g.League, &g.Home, &g.Away, &g.HomeLogo, &g.AwayLogo, &health, &probs, &g.Scorer); err != nil {
			return nil, err
		}
		// Only show numbers the client contract would show: a stale or
		// suspended model gets a score-only card, never old probabilities
		// presented as live.
		if health == "ok" && len(probs) > 0 {
			var p struct{ Home, Draw, Away *int }
			if json.Unmarshal(probs, &p) == nil && p.Home != nil && p.Draw != nil && p.Away != nil {
				g.Probs = &CardProbs{Home: *p.Home, Draw: *p.Draw, Away: *p.Away}
			}
		}
		goals = append(goals, g)
	}
	return goals, rows.Err()
}

// LatestScores is the newest in-play scoreline for each fixture.
func (s *PostgresStore) LatestScores(ctx context.Context, fixtureIDs []int64) (map[int64][2]int, error) {
	scores := map[int64][2]int{}
	if len(fixtureIDs) == 0 {
		return scores, nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT ON (fixture_id) fixture_id, COALESCE(home_goals, 0), COALESCE(away_goals, 0)
FROM inplay_snapshots
WHERE fixture_id = ANY($1::bigint[]) AND captured_at >= now() - interval '6 hours'
ORDER BY fixture_id, sequence DESC`, int64Array(fixtureIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var home, away int
		if err := rows.Scan(&id, &home, &away); err != nil {
			return nil, err
		}
		scores[id] = [2]int{home, away}
	}
	return scores, rows.Err()
}

const finishedStatuses = `('ft', 'aet', 'pen', 'finished')`

// RecentForm is a team's last finished matches, newest first.
func (s *PostgresStore) RecentForm(ctx context.Context, teamID int64, before time.Time, limit int) ([]FormResult, error) {
	return s.formQuery(ctx, `
SELECT home_team_id, away_team_id, home_goals_ft, away_goals_ft FROM fixtures
WHERE (home_team_id = $1 OR away_team_id = $1) AND kickoff_at < $2
  AND home_goals_ft IS NOT NULL AND away_goals_ft IS NOT NULL
  AND lower(status) IN `+finishedStatuses+`
ORDER BY kickoff_at DESC LIMIT $3`, teamID, before, limit)
}

// HeadToHead is the last meetings between two teams, either way round.
func (s *PostgresStore) HeadToHead(ctx context.Context, homeID, awayID int64, before time.Time, limit int) ([]FormResult, error) {
	return s.formQuery(ctx, `
SELECT home_team_id, away_team_id, home_goals_ft, away_goals_ft FROM fixtures
WHERE ((home_team_id = $1 AND away_team_id = $2) OR (home_team_id = $2 AND away_team_id = $1))
  AND kickoff_at < $3
  AND home_goals_ft IS NOT NULL AND away_goals_ft IS NOT NULL
  AND lower(status) IN `+finishedStatuses+`
ORDER BY kickoff_at DESC LIMIT $4`, homeID, awayID, before, limit)
}

func (s *PostgresStore) formQuery(ctx context.Context, query string, args ...any) ([]FormResult, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []FormResult
	for rows.Next() {
		var r FormResult
		if err := rows.Scan(&r.HomeTeamID, &r.AwayTeamID, &r.HomeGoals, &r.AwayGoals); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// MarketRecord counts one graded pre-match pick per fixture (its latest
// settlement), in the leagues the channels cover.
func (s *PostgresStore) MarketRecord(ctx context.Context, q RecordQuery) (MarketRecord, error) {
	record := MarketRecord{Market: q.Market}
	err := s.db.QueryRowContext(ctx, `
WITH latest AS (
  SELECT DISTINCT ON (pr.fixture_id) pr.is_correct
  FROM prediction_results pr
  JOIN leagues l ON l.id = pr.league_id
  WHERE pr.market = $1
    AND pr.settled_at >= $2
    AND pr.settlement_status = 'settled'
    AND pr.is_correct IS NOT NULL
    AND pr.lifecycle_stage_at_settlement IN ('EARLY', 'PRE_MATCH', 'LINEUP_CONFIRMED')
    AND (COALESCE(l.priority_score, 0) >= $3 OR l.id = ANY($4::bigint[]))
  ORDER BY pr.fixture_id, pr.settled_at DESC
)
SELECT COUNT(*) FILTER (WHERE is_correct), COUNT(*) FROM latest`, q.Market, q.Since, q.MinLeaguePriority, int64Array(q.LeagueIDs)).Scan(&record.Correct, &record.Graded)
	return record, err
}

// WeeklyNumbers gathers the decision numbers the database can answer.
func (s *PostgresStore) WeeklyNumbers(ctx context.Context, since time.Time) (Numbers, error) {
	var n Numbers
	err := s.db.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM users WHERE deleted_at IS NULL),
  (SELECT COUNT(*) FROM users WHERE deleted_at IS NULL AND created_at >= $1),
  (SELECT COUNT(DISTINCT u.id) FROM users u JOIN user_sessions us ON us.user_id = u.id
     WHERE u.deleted_at IS NULL AND u.created_at < $1 AND us.last_seen_at >= $1),
  (SELECT COUNT(*) FROM visitor_acquisition WHERE first_seen_at >= $1),
  (SELECT COUNT(*) FROM visitor_acquisition WHERE last_seen_at >= $1)`, since).
		Scan(&n.Accounts, &n.NewAccounts, &n.ReturningAccounts, &n.NewVisitors, &n.ActiveVisitors)
	if err != nil {
		return Numbers{}, fmt.Errorf("weekly numbers: %w", err)
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT COALESCE(NULLIF(first_utm_source, ''), NULLIF(first_referrer_host, ''), 'direct'), COUNT(*)
FROM visitor_acquisition WHERE first_seen_at >= $1
GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT 8`, since)
	if err != nil {
		return Numbers{}, fmt.Errorf("visitors by source: %w", err)
	}
	for rows.Next() {
		var c SourceCount
		if err := rows.Scan(&c.Source, &c.Count); err != nil {
			rows.Close()
			return Numbers{}, err
		}
		n.VisitorsBySource = append(n.VisitorsBySource, c)
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `
SELECT channel, status, COUNT(*) FROM marketing_posts WHERE created_at >= $1
GROUP BY 1, 2 ORDER BY 1, 2`, since)
	if err != nil {
		return Numbers{}, fmt.Errorf("posts by channel: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c ChannelCount
		if err := rows.Scan(&c.Channel, &c.Status, &c.Count); err != nil {
			return Numbers{}, err
		}
		n.PostsByChannel = append(n.PostsByChannel, c)
	}
	return n, rows.Err()
}

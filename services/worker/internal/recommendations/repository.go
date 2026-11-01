package recommendations

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const storedInputsSQL = `
SELECT 'prediction' AS item_type,
       prediction.public_id::text AS item_id,
       fixture.id AS match_id,
       fixture.league_id,
       COALESCE(league.slug, ''),
       COALESCE(league.name, 'Competition'),
       fixture.home_team_id,
       COALESCE(home_team.slug, ''),
       COALESCE(home_team.name, 'Home'),
       fixture.away_team_id,
       COALESCE(away_team.slug, ''),
       COALESCE(away_team.name, 'Away'),
       prediction.market,
       prediction.confidence,
       fixture.kickoff_at,
       prediction.generated_at
FROM engine_predictions AS prediction
JOIN fixtures AS fixture ON fixture.id = prediction.fixture_id
LEFT JOIN leagues AS league ON league.id = fixture.league_id
LEFT JOIN teams AS home_team ON home_team.id = fixture.home_team_id
LEFT JOIN teams AS away_team ON away_team.id = fixture.away_team_id
WHERE prediction.superseded_at IS NULL
  AND prediction.horizon_eligible = true
  AND prediction.embargo_active = false
  AND fixture.kickoff_at >= $1
  AND fixture.kickoff_at < $2
ORDER BY prediction.confidence DESC, fixture.kickoff_at ASC, prediction.public_id ASC
LIMIT $3`

type PostgresRepository struct {
	db *sql.DB
}

func NewPostgresRepository(db *sql.DB) PostgresRepository {
	return PostgresRepository{db: db}
}

func (r PostgresRepository) ListEligibleUsers(ctx context.Context, limit int) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id
FROM users
WHERE account_status = 'active'
  AND personalization_enabled = true
  AND deleted_at IS NULL
ORDER BY id
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []int64
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return nil, err
		}
		users = append(users, userID)
	}
	return users, rows.Err()
}

func (r PostgresRepository) LoadInputs(ctx context.Context, from, until time.Time, limit int) ([]Input, error) {
	rows, err := r.db.QueryContext(ctx, storedInputsSQL, from, until, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inputs := make([]Input, 0, limit)
	for rows.Next() {
		var input Input
		if err := rows.Scan(
			&input.ItemType, &input.ItemID, &input.MatchID, &input.LeagueID, &input.LeagueKey, &input.LeagueName,
			&input.HomeTeamID, &input.HomeTeamKey, &input.HomeTeamName, &input.AwayTeamID, &input.AwayTeamKey, &input.AwayTeamName, &input.Market, &input.Confidence,
			&input.StartsAt, &input.FreshnessAt,
		); err != nil {
			return nil, err
		}
		inputs = append(inputs, input)
	}
	return inputs, rows.Err()
}

func (r PostgresRepository) LoadSignals(ctx context.Context, userID int64, since time.Time) (Signals, error) {
	signals := Signals{
		Favorites: map[string]bool{}, Interests: map[string]float64{}, RecentSearches: map[string]bool{},
		RecentEngagement: map[string]float64{}, Viewed: map[string]bool{}, Dismissed: map[string]bool{},
	}
	favorites, err := r.db.QueryContext(ctx, `SELECT entity_type, entity_id FROM entity_favorites WHERE user_id = $1`, userID)
	if err != nil {
		return Signals{}, err
	}
	for favorites.Next() {
		var entityType, entityID string
		if err := favorites.Scan(&entityType, &entityID); err != nil {
			favorites.Close()
			return Signals{}, err
		}
		signals.Favorites[itemKey(entityType, entityID)] = true
	}
	if err := favorites.Close(); err != nil {
		return Signals{}, err
	}

	interests, err := r.db.QueryContext(ctx, `
SELECT interest_type, interest_key, weight
FROM user_interest_items
WHERE user_id = $1 AND disabled = false`, userID)
	if err != nil {
		return Signals{}, err
	}
	for interests.Next() {
		var interestType, interestID string
		var value float64
		if err := interests.Scan(&interestType, &interestID, &value); err != nil {
			interests.Close()
			return Signals{}, err
		}
		signals.Interests[itemKey(interestType, interestID)] = value
	}
	if err := interests.Close(); err != nil {
		return Signals{}, err
	}

	events, err := r.db.QueryContext(ctx, `
SELECT event_type, entity_type, entity_id
FROM recommendation_events
WHERE user_id = $1 AND occurred_at >= $2
ORDER BY occurred_at DESC
LIMIT 1000`, userID, since)
	if err != nil {
		return Signals{}, err
	}
	for events.Next() {
		var eventType string
		var entityType, entityID sql.NullString
		if err := events.Scan(&eventType, &entityType, &entityID); err != nil {
			events.Close()
			return Signals{}, err
		}
		if !entityType.Valid || !entityID.Valid {
			continue
		}
		key := itemKey(entityType.String, entityID.String)
		switch eventType {
		case "search", "fixture_searched", "team_searched", "league_searched":
			signals.RecentSearches[key] = true
		case "view", "impression", "fixture_viewed", "prediction_viewed", "analysis_opened":
			signals.Viewed[key] = true
		case "save", "click", "open", "prediction_saved", "favorite_added", "recommendation_clicked", "notification_opened":
			signals.RecentEngagement[key] = min(signals.RecentEngagement[key]+0.25, 1)
		}
	}
	if err := events.Close(); err != nil {
		return Signals{}, err
	}

	dismissals, err := r.db.QueryContext(ctx, `
SELECT item_type, item_id
FROM recommendation_dismissals
WHERE user_id = $1 AND (expires_at IS NULL OR expires_at > now())`, userID)
	if err != nil {
		return Signals{}, err
	}
	for dismissals.Next() {
		var itemType, itemID string
		if err := dismissals.Scan(&itemType, &itemID); err != nil {
			dismissals.Close()
			return Signals{}, err
		}
		signals.Dismissed[itemKey(itemType, itemID)] = true
	}
	if err := dismissals.Close(); err != nil {
		return Signals{}, err
	}
	return signals, nil
}

func (r PostgresRepository) LoadWeights(ctx context.Context) (map[string]float64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT weight_key, weight FROM recommendation_weights`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	weights := map[string]float64{}
	for rows.Next() {
		var key string
		var value float64
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		weights[key] = value
	}
	return weights, rows.Err()
}

func (r PostgresRepository) SaveCandidates(ctx context.Context, userID int64, generationKey string, candidates []Candidate) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, candidate := range candidates {
		_, err := tx.ExecContext(ctx, `
INSERT INTO recommendation_candidates (
  user_id, item_type, item_id, score, reason_key, reason_text, interest_key,
  prediction_confidence, starts_at, freshness_at, sponsored, editorial,
  generation_key, expires_at, title, subtitle, href
) VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
ON CONFLICT (user_id, item_type, item_id, generation_key)
DO UPDATE SET score = EXCLUDED.score,
              reason_key = EXCLUDED.reason_key,
              reason_text = EXCLUDED.reason_text,
              interest_key = EXCLUDED.interest_key,
              prediction_confidence = EXCLUDED.prediction_confidence,
              starts_at = EXCLUDED.starts_at,
              freshness_at = EXCLUDED.freshness_at,
              sponsored = EXCLUDED.sponsored,
              editorial = EXCLUDED.editorial,
              expires_at = EXCLUDED.expires_at,
              title = EXCLUDED.title,
              subtitle = EXCLUDED.subtitle,
              href = EXCLUDED.href`,
			userID, candidate.ItemType, candidate.ItemID, candidate.Score, candidate.ReasonKey,
			candidate.ReasonText, candidate.InterestKey, candidate.PredictionConfidence,
			candidate.StartsAt, candidate.FreshnessAt, candidate.Sponsored, candidate.Editorial,
			candidate.GenerationKey, candidate.ExpiresAt, candidate.Title, candidate.Subtitle, candidate.Href,
		)
		if err != nil {
			return fmt.Errorf("upsert recommendation candidate %s:%s: %w", candidate.ItemType, candidate.ItemID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM recommendation_candidates
WHERE user_id = $1
  AND (generation_key <> $2 OR expires_at <= now())`, userID, generationKey); err != nil {
		return fmt.Errorf("prune stale recommendation candidates: %w", err)
	}
	return tx.Commit()
}

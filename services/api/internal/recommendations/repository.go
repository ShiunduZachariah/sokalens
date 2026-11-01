package recommendations

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type Repository interface {
	IngestEvents(context.Context, []Event) error
	LoadWeights(context.Context) (map[string]float64, error)
	LoadCandidates(context.Context, int64, time.Time, int) ([]Candidate, error)
	Dismissed(context.Context, int64, time.Time) (map[string]struct{}, error)
	Dismiss(context.Context, int64, string, string, string, *time.Time, time.Time) error
	CandidateByID(context.Context, int64, int64) (Candidate, error)
}

type Dialect string

const (
	DialectPostgres Dialect = "postgres"
	DialectSQLite   Dialect = "sqlite"
)

type SQLRepository struct {
	db      *sql.DB
	dialect Dialect
}

func NewSQLRepository(db *sql.DB, dialect Dialect) *SQLRepository {
	return &SQLRepository{db: db, dialect: dialect}
}

func (r *SQLRepository) IngestEvents(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := fmt.Sprintf(`INSERT INTO recommendation_events (user_id, guest_session_id, session_public_id, event_type, entity_type, entity_id, recommendation_id, source, metadata, occurred_at) VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s%s, %s)`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5), r.p(6), r.p(7), r.p(8), r.p(9), r.jsonCast(), r.p(10))
	stmt, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, event := range events {
		metadata := event.Metadata
		if len(metadata) == 0 {
			metadata = json.RawMessage(`{}`)
		}
		if _, err := stmt.ExecContext(ctx, nullableInt(event.UserID), nullableInt(event.GuestSessionID), nullableString(event.SessionPublicID), event.EventType, nullableString(event.EntityType), nullableString(event.EntityID), nullableInt(event.RecommendationID), event.Source, string(metadata), event.OccurredAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *SQLRepository) LoadWeights(ctx context.Context) (map[string]float64, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT weight_key, weight FROM recommendation_weights ORDER BY weight_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	weights := make(map[string]float64)
	for rows.Next() {
		var key string
		var weight float64
		if err := rows.Scan(&key, &weight); err != nil {
			return nil, err
		}
		weights[key] = weight
	}
	return weights, rows.Err()
}

func (r *SQLRepository) LoadCandidates(ctx context.Context, userID int64, now time.Time, limit int) ([]Candidate, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	query := fmt.Sprintf(`SELECT id, user_id, item_type, item_id, score, reason_key, reason_text, COALESCE(interest_key, ''), prediction_confidence, starts_at, freshness_at, sponsored, editorial, generation_key, expires_at, created_at, title, subtitle, href FROM recommendation_candidates WHERE user_id = %s AND expires_at > %s ORDER BY score DESC, starts_at, id LIMIT %s`, r.p(1), r.p(2), r.p(3))
	rows, err := r.db.QueryContext(ctx, query, userID, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []Candidate
	for rows.Next() {
		var candidate Candidate
		var confidence sql.NullFloat64
		var starts, freshness, expires, created any
		if err := rows.Scan(&candidate.ID, &candidate.UserID, &candidate.ItemType, &candidate.ItemID, &candidate.BaseScore, &candidate.ReasonKey, &candidate.ReasonText, &candidate.InterestKey, &confidence, &starts, &freshness, &candidate.Sponsored, &candidate.Editorial, &candidate.GenerationKey, &expires, &created, &candidate.Title, &candidate.Subtitle, &candidate.Href); err != nil {
			return nil, err
		}
		if confidence.Valid {
			candidate.PredictionConfidence = &confidence.Float64
		}
		candidate.StartsAt = nullableTime(starts)
		candidate.FreshnessAt, _ = scanTime(freshness)
		candidate.ExpiresAt, _ = scanTime(expires)
		candidate.CreatedAt, _ = scanTime(created)
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func (r *SQLRepository) Dismissed(ctx context.Context, userID int64, now time.Time) (map[string]struct{}, error) {
	query := fmt.Sprintf(`SELECT item_type, item_id FROM recommendation_dismissals WHERE user_id = %s AND (expires_at IS NULL OR expires_at > %s)`, r.p(1), r.p(2))
	rows, err := r.db.QueryContext(ctx, query, userID, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	dismissed := make(map[string]struct{})
	for rows.Next() {
		var itemType, itemID string
		if err := rows.Scan(&itemType, &itemID); err != nil {
			return nil, err
		}
		dismissed[itemKey(itemType, itemID)] = struct{}{}
	}
	return dismissed, rows.Err()
}

func (r *SQLRepository) Dismiss(ctx context.Context, userID int64, itemType, itemID, reason string, expiresAt *time.Time, now time.Time) error {
	query := fmt.Sprintf(`INSERT INTO recommendation_dismissals (user_id, item_type, item_id, reason, expires_at, created_at) VALUES (%s, %s, %s, %s, %s, %s) ON CONFLICT (user_id, item_type, item_id) DO UPDATE SET reason = excluded.reason, expires_at = excluded.expires_at, created_at = excluded.created_at`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5), r.p(6))
	_, err := r.db.ExecContext(ctx, query, userID, itemType, itemID, reason, expiresAt, now)
	return err
}

func (r *SQLRepository) CandidateByID(ctx context.Context, userID, candidateID int64) (Candidate, error) {
	var candidate Candidate
	err := r.db.QueryRowContext(ctx, `SELECT id,user_id,item_type,item_id FROM recommendation_candidates WHERE id=$1 AND user_id=$2`, candidateID, userID).Scan(&candidate.ID, &candidate.UserID, &candidate.ItemType, &candidate.ItemID)
	return candidate, err
}

func (r *SQLRepository) p(position int) string {
	if r.dialect == DialectSQLite {
		return "?"
	}
	return fmt.Sprintf("$%d", position)
}

func (r *SQLRepository) jsonCast() string {
	if r.dialect == DialectPostgres {
		return "::jsonb"
	}
	return ""
}

func nullableInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value any) *time.Time {
	if value == nil {
		return nil
	}
	parsed, err := scanTime(value)
	if err != nil {
		return nil
	}
	return &parsed
}

func scanTime(value any) (time.Time, error) {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC(), nil
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999-07:00", "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, typed); err == nil {
				return parsed.UTC(), nil
			}
		}
	case []byte:
		return scanTime(string(typed))
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %T", value)
}

// MemoryRepository is useful for deterministic ranking tests and for callers
// that want to stage candidates before a worker persists a generation.
type MemoryRepository struct {
	mu         sync.RWMutex
	Events     []Event
	Weights    map[string]float64
	Candidates map[int64][]Candidate
	dismissed  map[int64]map[string]*time.Time
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{Weights: make(map[string]float64), Candidates: make(map[int64][]Candidate), dismissed: make(map[int64]map[string]*time.Time)}
}

func (r *MemoryRepository) IngestEvents(_ context.Context, events []Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Events = append(r.Events, events...)
	return nil
}

func (r *MemoryRepository) LoadWeights(context.Context) (map[string]float64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]float64, len(r.Weights))
	for key, value := range r.Weights {
		result[key] = value
	}
	return result, nil
}

func (r *MemoryRepository) LoadCandidates(_ context.Context, userID int64, now time.Time, limit int) ([]Candidate, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []Candidate
	for _, candidate := range r.Candidates[userID] {
		if candidate.ExpiresAt.IsZero() || candidate.ExpiresAt.After(now) {
			result = append(result, candidate)
		}
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func (r *MemoryRepository) Dismissed(_ context.Context, userID int64, now time.Time) (map[string]struct{}, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]struct{})
	for key, expiry := range r.dismissed[userID] {
		if expiry == nil || expiry.After(now) {
			result[key] = struct{}{}
		}
	}
	return result, nil
}

func (r *MemoryRepository) Dismiss(_ context.Context, userID int64, itemType, itemID, _ string, expiresAt *time.Time, _ time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dismissed[userID] == nil {
		r.dismissed[userID] = make(map[string]*time.Time)
	}
	r.dismissed[userID][itemKey(itemType, itemID)] = expiresAt
	return nil
}

func (r *MemoryRepository) CandidateByID(_ context.Context, userID, candidateID int64) (Candidate, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, candidate := range r.Candidates[userID] {
		if candidate.ID == candidateID {
			return candidate, nil
		}
	}
	return Candidate{}, sql.ErrNoRows
}

var _ Repository = (*SQLRepository)(nil)
var _ Repository = (*MemoryRepository)(nil)

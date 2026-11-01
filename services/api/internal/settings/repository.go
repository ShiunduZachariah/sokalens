package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Dialect string

const (
	DialectPostgres Dialect = "postgres"
	DialectSQLite   Dialect = "sqlite"
)

var ErrNotFound = errors.New("setting not found")

type Setting struct {
	Key       string
	Value     json.RawMessage
	UpdatedBy int64
	UpdatedAt time.Time
}

type Repository struct {
	db      *sql.DB
	dialect Dialect
}

func NewRepository(db *sql.DB, dialect Dialect) Repository {
	return Repository{db: db, dialect: dialect}
}

func (r Repository) Upsert(ctx context.Context, key string, value json.RawMessage, updatedBy int64) error {
	if r.db == nil {
		return errors.New("settings DB is nil")
	}
	if key == "" {
		return errors.New("setting key is required")
	}
	if !json.Valid(value) {
		return errors.New("setting value must be valid JSON")
	}

	query := r.upsertQuery()
	_, err := r.db.ExecContext(ctx, query, key, string(value), updatedBy)
	return err
}

func (r Repository) Get(ctx context.Context, key string) (Setting, error) {
	if r.db == nil {
		return Setting{}, errors.New("settings DB is nil")
	}
	if key == "" {
		return Setting{}, errors.New("setting key is required")
	}

	query := fmt.Sprintf("SELECT key, value, COALESCE(updated_by, 0), CAST(updated_at AS TEXT) FROM app_settings WHERE key = %s", r.placeholder(1))
	var setting Setting
	var rawValue string
	var rawUpdatedAt string
	if err := r.db.QueryRowContext(ctx, query, key).Scan(&setting.Key, &rawValue, &setting.UpdatedBy, &rawUpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Setting{}, ErrNotFound
		}
		return Setting{}, err
	}
	setting.Value = json.RawMessage(rawValue)
	setting.UpdatedAt = parseDBTime(rawUpdatedAt)
	return setting, nil
}

func (r Repository) upsertQuery() string {
	switch r.dialect {
	case DialectSQLite:
		return `
INSERT INTO app_settings (key, value, updated_by, updated_at)
VALUES (?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT (key) DO UPDATE SET
  value = excluded.value,
  updated_by = excluded.updated_by,
  updated_at = CURRENT_TIMESTAMP`
	default:
		return `
INSERT INTO app_settings (key, value, updated_by, updated_at)
VALUES ($1, $2::jsonb, $3, now())
ON CONFLICT (key) DO UPDATE SET
  value = excluded.value,
  updated_by = excluded.updated_by,
  updated_at = now()`
	}
}

func (r Repository) placeholder(position int) string {
	if r.dialect == DialectSQLite {
		return "?"
	}
	return fmt.Sprintf("$%d", position)
}

func parseDBTime(raw string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999-07", "2006-01-02 15:04:05"} {
		parsed, err := time.Parse(layout, raw)
		if err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

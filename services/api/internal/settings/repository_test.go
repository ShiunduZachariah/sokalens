package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRepositoryUpsertsAndReadsSetting(t *testing.T) {
	db := openSettingsDB(t)
	repo := NewRepository(db, DialectSQLite)

	value := json.RawMessage(`{"phase":"foundation"}`)
	if err := repo.Upsert(context.Background(), "platform.phase", value, 42); err != nil {
		t.Fatalf("Upsert returned error: %v", err)
	}

	setting, err := repo.Get(context.Background(), "platform.phase")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}

	if setting.Key != "platform.phase" {
		t.Fatalf("Key = %q, want platform.phase", setting.Key)
	}
	if string(setting.Value) != string(value) {
		t.Fatalf("Value = %s, want %s", setting.Value, value)
	}
	if setting.UpdatedBy != 42 {
		t.Fatalf("UpdatedBy = %d, want 42", setting.UpdatedBy)
	}
}

func TestRepositoryReturnsNotFoundForMissingSetting(t *testing.T) {
	db := openSettingsDB(t)
	repo := NewRepository(db, DialectSQLite)

	_, err := repo.Get(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
}

func TestRepositoryRejectsInvalidJSON(t *testing.T) {
	db := openSettingsDB(t)
	repo := NewRepository(db, DialectSQLite)

	err := repo.Upsert(context.Background(), "bad", json.RawMessage(`{`), 42)
	if err == nil {
		t.Fatal("Upsert returned nil error for invalid JSON")
	}
}

func openSettingsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE app_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_by BIGINT,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
)`); err != nil {
		t.Fatalf("create app_settings: %v", err)
	}
	return db
}

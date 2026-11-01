package emailsettings

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRepositorySetsProviderAndWritesSafeAudit(t *testing.T) {
	db, err := sql.Open("sqlite", "file:email-settings?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE app_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_by INTEGER, updated_at TEXT)`,
		`CREATE TABLE audit_logs (actor_user_id INTEGER, action TEXT, entity_type TEXT, entity_id TEXT, reason TEXT, before_value TEXT, after_value TEXT)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	repo := NewRepository(db, "cloudflare", "sqlite")
	if err := repo.Set(context.Background(), "resend", 7); err != nil {
		t.Fatalf("Set: %v", err)
	}
	status, err := repo.Status(context.Background())
	if err != nil || status.Provider != "resend" {
		t.Fatalf("status = %+v, err=%v", status, err)
	}
	var action, before, after string
	if err := db.QueryRow(`SELECT action,before_value,after_value FROM audit_logs`).Scan(&action, &before, &after); err != nil {
		t.Fatal(err)
	}
	if action != "email.provider_updated" || !strings.Contains(after, "resend") || !strings.Contains(before, "cloudflare") {
		t.Fatalf("audit = %q %q %q", action, before, after)
	}
}

func TestRepositoryRejectsUnknownProvider(t *testing.T) {
	db, _ := sql.Open("sqlite", "file:email-settings-invalid?mode=memory&cache=shared")
	defer db.Close()
	_, _ = db.Exec(`CREATE TABLE app_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_by INTEGER, updated_at TEXT)`)
	repo := NewRepository(db, "cloudflare", "sqlite")
	if !errors.Is(repo.Set(context.Background(), "unknown", 1), ErrInvalidProvider) {
		t.Fatal("unknown provider was accepted")
	}
}

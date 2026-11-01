package emailsettings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

var ErrInvalidProvider = errors.New("invalid email provider")

var providers = map[string]bool{"cloudflare": true, "resend": true, "mailtrap": true, "smtp": true, "log": true}

type Status struct {
	Provider  string   `json:"provider"`
	Providers []string `json:"providers"`
}

type Repository struct {
	db              *sql.DB
	defaultProvider string
	dialect         string
}

func NewRepository(db *sql.DB, defaultProvider string, dialect ...string) Repository {
	dbDialect := "postgres"
	if len(dialect) > 0 && dialect[0] != "" {
		dbDialect = dialect[0]
	}
	return Repository{db: db, defaultProvider: strings.ToLower(strings.TrimSpace(defaultProvider)), dialect: dbDialect}
}

func (r Repository) Status(ctx context.Context) (Status, error) {
	provider, err := r.current(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Status{}, err
	}
	if provider == "" {
		provider = r.defaultProvider
	}
	return Status{Provider: provider, Providers: []string{"cloudflare", "resend", "mailtrap", "smtp", "log"}}, nil
}

func (r Repository) Set(ctx context.Context, provider string, actorID int64) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if !providers[provider] {
		return ErrInvalidProvider
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, err := r.currentTx(ctx, tx)
	if errors.Is(err, sql.ErrNoRows) {
		old = r.defaultProvider
	} else if err != nil {
		return err
	}
	value, _ := json.Marshal(provider)
	query := `INSERT INTO app_settings(key,value,updated_by,updated_at) VALUES($1,$2::jsonb,$3,now()) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_by=excluded.updated_by,updated_at=now()`
	auditQuery := `INSERT INTO audit_logs(actor_user_id,action,entity_type,entity_id,reason,before_value,after_value) VALUES($1,'email.provider_updated','app_setting',$2,'Administrator changed transactional email provider',$3::jsonb,$4::jsonb)`
	if r.dialect == "sqlite" {
		query = `INSERT INTO app_settings(key,value,updated_by,updated_at) VALUES(?,?,?,CURRENT_TIMESTAMP) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_by=excluded.updated_by,updated_at=CURRENT_TIMESTAMP`
		auditQuery = `INSERT INTO audit_logs(actor_user_id,action,entity_type,entity_id,reason,before_value,after_value) VALUES(?,'email.provider_updated','app_setting',?,'Administrator changed transactional email provider',?,?)`
	}
	if _, err := tx.ExecContext(ctx, query, "email.provider", string(value), actorID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, auditQuery, actorID, "email.provider", jsonString(old), string(value)); err != nil {
		return err
	}
	return tx.Commit()
}

func (r Repository) current(ctx context.Context) (string, error) { return r.currentTx(ctx, r.db) }

func (r Repository) currentTx(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (string, error) {
	var raw string
	query := `SELECT value #>> '{}' FROM app_settings WHERE key=$1`
	if r.dialect == "sqlite" {
		query = `SELECT value FROM app_settings WHERE key=?`
	}
	err := q.QueryRowContext(ctx, query, "email.provider").Scan(&raw)
	if err != nil {
		return "", err
	}
	if r.dialect == "sqlite" {
		var decoded string
		if json.Unmarshal([]byte(raw), &decoded) == nil {
			raw = decoded
		}
	}
	return strings.ToLower(strings.TrimSpace(raw)), nil
}

func jsonString(value string) string { encoded, _ := json.Marshal(value); return string(encoded) }

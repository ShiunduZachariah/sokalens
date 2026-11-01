package securityinvariants_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/accounts"
)

const testPassword = "Correct-Pass-8"

func openSecurityDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	mustExec(t, db, securitySchema)
	return db
}

func registerVerified(t *testing.T, db *sql.DB, service *accounts.Service, email string) accounts.Account {
	t.Helper()
	account, err := service.Register(context.Background(), email, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	mustExec(t, db, `UPDATE users SET email_verified_at=? WHERE id=?`, now, account.ID)
	account.EmailVerifiedAt = &now
	return account
}

func mustExec(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), statement, args...); err != nil {
		t.Fatalf("execute schema/test fixture: %v\n%s", err, statement)
	}
}

const securitySchema = `
CREATE TABLE users (
 id INTEGER PRIMARY KEY AUTOINCREMENT, public_id TEXT NOT NULL UNIQUE, email TEXT UNIQUE, email_normalized TEXT UNIQUE,
 password_hash TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'user', email_verified_at DATETIME,
 account_status TEXT NOT NULL DEFAULT 'active', account_status_reason TEXT, country_code TEXT, timezone TEXT NOT NULL DEFAULT 'Africa/Nairobi',
 language TEXT NOT NULL DEFAULT 'en', personalization_enabled BOOLEAN NOT NULL DEFAULT true,
 marketing_consent BOOLEAN NOT NULL DEFAULT false, last_login_at DATETIME, deleted_at DATETIME,
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE user_roles (user_id INTEGER NOT NULL, role_key TEXT NOT NULL, granted_at DATETIME NOT NULL, revoked_at DATETIME, PRIMARY KEY(user_id,role_key));
CREATE TABLE user_identities (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, provider TEXT NOT NULL, provider_subject TEXT NOT NULL, provider_email TEXT, provider_email_verified BOOLEAN NOT NULL DEFAULT false, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, last_used_at DATETIME, UNIQUE(provider,provider_subject), UNIQUE(user_id,provider));
CREATE TABLE role_permissions (role_key TEXT NOT NULL, permission_key TEXT NOT NULL, PRIMARY KEY(role_key,permission_key));
CREATE TABLE user_sessions (
 id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, token_hash TEXT NOT NULL UNIQUE,
 public_id TEXT NOT NULL UNIQUE, session_type TEXT NOT NULL DEFAULT 'user', device_name TEXT NOT NULL DEFAULT '',
 user_agent TEXT NOT NULL DEFAULT '', expires_at DATETIME NOT NULL, absolute_expires_at DATETIME, last_seen_at DATETIME NOT NULL,
 revoked_at DATETIME, revocation_reason TEXT, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE auth_tokens (
 id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, purpose TEXT NOT NULL,
 token_hash TEXT NOT NULL UNIQUE, expires_at DATETIME NOT NULL, used_at DATETIME,
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE user_profiles (
 user_id INTEGER PRIMARY KEY, display_name TEXT NOT NULL DEFAULT '', country_code TEXT,
 odds_format TEXT NOT NULL DEFAULT 'decimal', odds_format_selected_at DATETIME, onboarding_completed_at DATETIME,
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE terms_documents (document_key TEXT NOT NULL, version TEXT NOT NULL, published_at DATETIME NOT NULL, required BOOLEAN NOT NULL, content_url TEXT NOT NULL, PRIMARY KEY(document_key,version));
INSERT INTO terms_documents(document_key,version,published_at,required,content_url) VALUES('terms','2026-08-24',CURRENT_TIMESTAMP,true,'/terms'),('privacy','2026-08-24',CURRENT_TIMESTAMP,true,'/privacy');
CREATE TABLE consent_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, document_key TEXT NOT NULL,
 document_version TEXT NOT NULL, action TEXT NOT NULL, source TEXT NOT NULL,
 metadata TEXT NOT NULL DEFAULT '{}', occurred_at DATETIME NOT NULL
);
CREATE TABLE entity_favorites (
 id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, entity_type TEXT NOT NULL,
 entity_id TEXT NOT NULL, display_name TEXT NOT NULL DEFAULT '', notifications_enabled BOOLEAN NOT NULL DEFAULT true,
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 UNIQUE(user_id,entity_type,entity_id)
);
CREATE TABLE user_interest_items (
 id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, interest_type TEXT NOT NULL,
 interest_key TEXT NOT NULL, weight REAL NOT NULL, source TEXT NOT NULL, disabled BOOLEAN NOT NULL DEFAULT false,
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
 UNIQUE(user_id,interest_type,interest_key)
);
CREATE TABLE user_entitlement_snapshots (
 user_id INTEGER PRIMARY KEY, tier TEXT NOT NULL, entitlements TEXT NOT NULL,
 ads_allowed BOOLEAN NOT NULL, version INTEGER DEFAULT 1, next_change_at DATETIME, updated_at DATETIME
);
CREATE TABLE subscription_plans (plan_key TEXT PRIMARY KEY, tier TEXT NOT NULL);
CREATE TABLE subscriptions (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, plan_key TEXT NOT NULL, status TEXT NOT NULL, current_period_end DATETIME, grace_ends_at DATETIME);
CREATE TABLE plan_entitlements (plan_key TEXT NOT NULL, entitlement_key TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY(plan_key,entitlement_key));
CREATE TABLE user_entitlement_grants (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, entitlement_key TEXT NOT NULL, value TEXT NOT NULL, effective_at DATETIME NOT NULL, expires_at DATETIME, revoked_at DATETIME);
CREATE TABLE user_growth_events (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, visitor_hash TEXT, event_type TEXT NOT NULL, country_code TEXT, platform TEXT NOT NULL DEFAULT 'web', source TEXT NOT NULL DEFAULT '', occurred_at DATETIME NOT NULL, metadata TEXT NOT NULL DEFAULT '{}');
CREATE TABLE visitor_acquisition (visitor_hash TEXT PRIMARY KEY, first_country_code TEXT, first_referrer_host TEXT, first_utm_source TEXT, first_utm_medium TEXT, first_utm_campaign TEXT, first_utm_content TEXT, first_landing_page TEXT, device_type TEXT NOT NULL, platform TEXT NOT NULL, first_seen_at DATETIME NOT NULL, last_seen_at DATETIME NOT NULL);
CREATE TABLE user_acquisition (user_id INTEGER PRIMARY KEY, visitor_hash TEXT, first_country_code TEXT, first_referrer_host TEXT, first_utm_source TEXT, first_utm_medium TEXT, first_utm_campaign TEXT, first_utm_content TEXT, first_landing_page TEXT, signup_country_code TEXT, signup_referrer_host TEXT, signup_utm_source TEXT, signup_utm_medium TEXT, signup_utm_campaign TEXT, signup_page TEXT, device_type TEXT NOT NULL, platform TEXT NOT NULL, created_at DATETIME NOT NULL);
CREATE TABLE notification_outbox (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, channel TEXT NOT NULL, category TEXT NOT NULL, dedupe_key TEXT NOT NULL UNIQUE, payload TEXT NOT NULL, priority INTEGER NOT NULL DEFAULT 100);
`

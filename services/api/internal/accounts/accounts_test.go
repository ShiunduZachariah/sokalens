package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestAccountLifecycleUsesHashedSecretsAndNormalizedIdentity(t *testing.T) {
	db := accountTestDB(t)
	repo := NewSQLRepository(db, DialectSQLite)
	service := NewService(repo)
	service.argon = Argon2Config{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 16}
	// Anchored to the run, not to a calendar date. This was previously pinned
	// to 2026-08-05 09:00 UTC; with the 30-day sessionTTL the session it
	// creates expired at exactly 2026-09-04 09:00 UTC, and the test began
	// failing on every run from that moment. Relative anchoring keeps the
	// clock deterministic within a run without setting a new time bomb.
	fixedNow := time.Now().UTC().Truncate(time.Hour)
	service.now = func() time.Time { return fixedNow }

	account, err := service.Register(context.Background(), "  FAN@Example.COM ", "correct horse battery")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if account.Email != "fan@example.com" || account.PasswordHash != "" {
		t.Fatalf("unexpected public account: %+v", account)
	}
	var normalized, passwordHash string
	if err := db.QueryRow(`SELECT email_normalized, password_hash FROM users WHERE id = ?`, account.ID).Scan(&normalized, &passwordHash); err != nil {
		t.Fatal(err)
	}
	if normalized != "fan@example.com" || !strings.HasPrefix(passwordHash, "$argon2id$") || strings.Contains(passwordHash, "correct horse") {
		t.Fatalf("identity was not normalized/hashed: %q %q", normalized, passwordHash)
	}
	if _, err := service.Register(context.Background(), "fan@example.com", "another secure pass"); !errors.Is(err, ErrEmailExists) {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := service.Authenticate(context.Background(), "FAN@example.com", "wrong password"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("wrong password error = %v", err)
	}
	if _, err := service.Authenticate(context.Background(), "FAN@example.com", "correct horse battery"); err != nil {
		t.Fatalf("pending account authenticate error = %v", err)
	}

	verification, err := service.IssueEmailVerification(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	var storedToken string
	if err := db.QueryRow(`SELECT token_hash FROM auth_tokens WHERE user_id = ? AND purpose = 'email_verification'`, account.ID).Scan(&storedToken); err != nil {
		t.Fatal(err)
	}
	if storedToken == verification.Token || storedToken != hashToken(verification.Token) {
		t.Fatal("verification token was not stored as a SHA-256 digest")
	}
	if err := service.VerifyEmail(context.Background(), verification.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(context.Background(), "FAN@example.com", "correct horse battery"); err != nil {
		t.Fatalf("verified authenticate: %v", err)
	}
	if err := service.VerifyEmail(context.Background(), verification.Token); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("reused verification token error = %v", err)
	}

	credential, err := service.CreateSession(context.Background(), account.ID, "Firefox on Windows", "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	if credential.Token == "" || credential.Session.PublicID == "" {
		t.Fatalf("incomplete session credential: %+v", credential)
	}
	var storedSession string
	if err := db.QueryRow(`SELECT token_hash FROM user_sessions WHERE id = ?`, credential.Session.ID).Scan(&storedSession); err != nil {
		t.Fatal(err)
	}
	if storedSession == credential.Token || storedSession != hashToken(credential.Token) {
		t.Fatal("session token was not stored as a digest")
	}
	if _, _, err := service.ValidateSession(context.Background(), credential.Token); err != nil {
		t.Fatal(err)
	}
	var initialLastSeen time.Time
	if err := db.QueryRow(`SELECT last_seen_at FROM user_sessions WHERE id = ?`, credential.Session.ID).Scan(&initialLastSeen); err != nil {
		t.Fatal(err)
	}
	fixedNow = credential.Session.LastSeenAt.Add(2 * time.Minute)
	validatedAccount, activeSession, err := service.ValidateSession(context.Background(), credential.Token)
	if err != nil || validatedAccount.ID != account.ID || !activeSession.LastSeenAt.Equal(initialLastSeen) {
		t.Fatalf("session validation changed persisted activity: account=%+v session=%+v err=%v", validatedAccount, activeSession, err)
	}
	var storedLastSeen time.Time
	if err := db.QueryRow(`SELECT last_seen_at FROM user_sessions WHERE id = ?`, credential.Session.ID).Scan(&storedLastSeen); err != nil || !storedLastSeen.Equal(initialLastSeen) {
		t.Fatalf("stored sign-in time=%v err=%v, want %v", storedLastSeen, err, initialLastSeen)
	}

	reset, resetAccount, err := service.IssuePasswordReset(context.Background(), account.Email)
	if err != nil {
		t.Fatal(err)
	}
	if resetAccount.ID != account.ID {
		t.Fatalf("reset account=%d want=%d", resetAccount.ID, account.ID)
	}
	if err := service.ResetPassword(context.Background(), reset.Token, "a newly secure pass"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ValidateSession(context.Background(), credential.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("session survived password reset: %v", err)
	}
	if _, err := service.Authenticate(context.Background(), account.Email, "a newly secure pass"); err != nil {
		t.Fatalf("new password does not authenticate: %v", err)
	}
}

func TestPendingRegistrationAtomicallyCreatesSessionIdentityConsentAndAcquisition(t *testing.T) {
	db := accountTestDB(t)
	service := NewService(NewSQLRepository(db, DialectSQLite))
	service.argon = Argon2Config{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 16}
	// Relative, not a calendar date: this test creates a session and the
	// 30-day sessionTTL would otherwise expire it on 2026-09-23.
	now := time.Now().UTC().Truncate(time.Hour)
	service.now = func() time.Time { return now }
	if _, err := db.Exec(`INSERT INTO terms_documents(document_key,version,published_at,required,content_url) VALUES('terms','2026-08-24',?,true,'/terms'),('privacy','2026-08-24',?,true,'/privacy')`, now, now); err != nil {
		t.Fatal(err)
	}
	result, err := service.RegisterPending(context.Background(), Registration{
		Email: "Launch@Example.test", Password: "launch-password", DisplayName: "Fan", CountryCode: "ke",
		PreferredLanguage: "fr-FR", LanguageSource: "selected",
		LegalVersion: "2026-08-24", DeviceName: "mobile web browser", UserAgent: "test-mobile",
		Acquisition: Acquisition{VisitorID: "anonymous-secret", CountryCode: "KE", ReferrerHost: "example.org", UTMSource: "launch", LandingPage: "/predictions/today", SignupPage: "/register", DeviceType: "mobile", Platform: "web"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Account.AccountStatus != "pending_verification" || result.Account.PublicID == "" || result.Credential.Token == "" || result.Verification.Token == "" {
		t.Fatalf("incomplete pending registration: %+v", result)
	}
	parts := strings.Split(result.Account.PublicID, "-")
	if len(parts) != 5 || len(parts[2]) != 4 || parts[2][0] != '7' {
		t.Fatalf("public ID is not UUIDv7: %q", result.Account.PublicID)
	}
	if _, _, err := service.ValidateSession(context.Background(), result.Credential.Token); err != nil {
		t.Fatalf("pending session invalid: %v", err)
	}
	var identityProvider, storedVisitorHash string
	var consentCount, growthCount int
	if err := db.QueryRow(`SELECT provider FROM user_identities WHERE user_id=?`, result.Account.ID).Scan(&identityProvider); err != nil || identityProvider != "PASSWORD" {
		t.Fatalf("identity=%q err=%v", identityProvider, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM consent_events WHERE user_id=?`, result.Account.ID).Scan(&consentCount); err != nil || consentCount != 2 {
		t.Fatalf("consents=%d err=%v", consentCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM consent_events WHERE user_id=? AND document_version='2026-08-24'`, result.Account.ID).Scan(&consentCount); err != nil || consentCount != 2 {
		t.Fatalf("versioned consents=%d err=%v", consentCount, err)
	}
	if err := db.QueryRow(`SELECT visitor_hash FROM user_acquisition WHERE user_id=?`, result.Account.ID).Scan(&storedVisitorHash); err != nil || storedVisitorHash == "anonymous-secret" || storedVisitorHash == "" {
		t.Fatalf("visitor hash=%q err=%v", storedVisitorHash, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_growth_events WHERE user_id=? AND event_type='USER_REGISTERED'`, result.Account.ID).Scan(&growthCount); err != nil || growthCount != 1 {
		t.Fatalf("growth events=%d err=%v", growthCount, err)
	}
	var storedLanguage, languageMetadata string
	if err := db.QueryRow(`SELECT language FROM users WHERE id=?`, result.Account.ID).Scan(&storedLanguage); err != nil || storedLanguage != "fr" {
		t.Fatalf("preferred language=%q err=%v", storedLanguage, err)
	}
	if err := db.QueryRow(`SELECT metadata FROM user_growth_events WHERE user_id=? AND event_type='LANGUAGE_SELECTED'`, result.Account.ID).Scan(&languageMetadata); err != nil || !strings.Contains(languageMetadata, `"preferred_language":"fr"`) {
		t.Fatalf("language selection metadata=%q err=%v", languageMetadata, err)
	}
	if err := service.VerifyEmail(context.Background(), result.Verification.Token); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRow(`SELECT account_status FROM users WHERE id=?`, result.Account.ID).Scan(&status); err != nil || status != "active" {
		t.Fatalf("status=%q err=%v", status, err)
	}
}

func TestAnonymousGrowthEventIsAllowlistedNormalizedAndHashed(t *testing.T) {
	db := accountTestDB(t)
	service := NewService(NewSQLRepository(db, DialectSQLite))
	now := time.Now().UTC().Truncate(time.Hour)
	service.now = func() time.Time { return now }

	err := service.RecordGrowthEvent(context.Background(), GrowthEvent{
		VisitorID:   "anonymous-browser-secret",
		EventType:   " signup_started ",
		CountryCode: "pt",
		Source:      "web_registration",
		Metadata:    json.RawMessage(`{"locale":"pt","step":"register_form"}`),
	})
	if err != nil {
		t.Fatalf("record growth event: %v", err)
	}

	var visitorHash, eventType, countryCode, platform, source, metadata string
	var occurredAt time.Time
	if err := db.QueryRow(`SELECT visitor_hash,event_type,country_code,platform,source,occurred_at,metadata FROM user_growth_events`).Scan(
		&visitorHash, &eventType, &countryCode, &platform, &source, &occurredAt, &metadata,
	); err != nil {
		t.Fatal(err)
	}
	if visitorHash != hashToken("anonymous-browser-secret") || strings.Contains(visitorHash, "anonymous-browser-secret") {
		t.Fatalf("visitor identifier was not stored as a digest: %q", visitorHash)
	}
	if eventType != "SIGNUP_STARTED" || countryCode != "PT" || platform != "web" || source != "web_registration" {
		t.Fatalf("unexpected normalized event: type=%q country=%q platform=%q source=%q", eventType, countryCode, platform, source)
	}
	if !occurredAt.Equal(now) || !strings.Contains(metadata, `"locale":"pt"`) {
		t.Fatalf("unexpected event time/metadata: time=%v metadata=%s", occurredAt, metadata)
	}

	if err := service.RecordGrowthEvent(context.Background(), GrowthEvent{
		VisitorID: "anonymous-browser-secret", EventType: "arbitrary_event", Metadata: json.RawMessage(`{}`),
	}); err == nil {
		t.Fatal("unknown growth event type was accepted")
	}
}

func TestProfileConsentFavoritesAndInterests(t *testing.T) {
	db := accountTestDB(t)
	service := NewService(NewSQLRepository(db, DialectSQLite))
	service.argon = Argon2Config{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 16}
	now := time.Now().UTC().Truncate(time.Hour)
	service.now = func() time.Time { return now }
	account, err := service.Register(context.Background(), "profile@example.com", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}

	profile := Profile{UserID: account.ID, DisplayName: "  Soka Fan  ", Timezone: " Africa/Nairobi ", CountryCode: "ke", OddsFormat: "DECIMAL", OddsFormatSelectedAt: &now, Language: "EN", PersonalizationEnabled: true}
	if err := service.UpdateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	storedProfile, err := service.GetProfile(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedProfile.DisplayName != "Soka Fan" || storedProfile.CountryCode != "KE" || storedProfile.OddsFormat != "decimal" || storedProfile.OddsFormatSelectedAt == nil {
		t.Fatalf("profile was not normalized: %+v", storedProfile)
	}
	profile.Language = "ja"
	if err := service.UpdateProfile(context.Background(), profile); err == nil {
		t.Fatal("unsupported preferred language was accepted")
	}
	profile.Language = "ar-SA"
	if err := service.UpdateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	storedProfile, err = service.GetProfile(context.Background(), account.ID)
	if err != nil || storedProfile.Language != "ar" {
		t.Fatalf("stored preferred language=%q err=%v", storedProfile.Language, err)
	}
	var changeMetadata string
	if err := db.QueryRow(`SELECT metadata FROM user_growth_events WHERE user_id=? AND event_type='LANGUAGE_CHANGED'`, account.ID).Scan(&changeMetadata); err != nil || !strings.Contains(changeMetadata, `"preferred_language":"ar"`) {
		t.Fatalf("language change metadata=%q err=%v", changeMetadata, err)
	}
	profile.Language = "fr"
	now = now.Add(time.Minute)
	if err := service.UpdateProfile(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	var languageChangeCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_growth_events WHERE user_id=? AND event_type='LANGUAGE_CHANGED'`, account.ID).Scan(&languageChangeCount); err != nil || languageChangeCount != 1 {
		t.Fatalf("language change events=%d err=%v", languageChangeCount, err)
	}
	if err := db.QueryRow(`SELECT metadata FROM user_growth_events WHERE user_id=? AND event_type='LANGUAGE_CHANGED'`, account.ID).Scan(&changeMetadata); err != nil || !strings.Contains(changeMetadata, `"previous_language":"ar"`) || !strings.Contains(changeMetadata, `"preferred_language":"fr"`) {
		t.Fatalf("latest language change metadata=%q err=%v", changeMetadata, err)
	}
	profile.OddsFormat = "moneyline-ish"
	if err := service.UpdateProfile(context.Background(), profile); err == nil {
		t.Fatal("invalid odds format was accepted")
	}

	if _, err := db.Exec(`INSERT INTO terms_documents (document_key, version, published_at, required, content_url) VALUES ('terms', 'v1', ?, true, '/terms')`, now); err != nil {
		t.Fatal(err)
	}
	consent := Consent{DocumentKey: " TERMS ", DocumentVersion: "v1", Granted: true, Source: "registration", Metadata: json.RawMessage(`{"locale":"en"}`)}
	if err := service.SetConsent(context.Background(), account.ID, consent); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	consent.Granted = false
	if err := service.SetConsent(context.Background(), account.ID, consent); err != nil {
		t.Fatal(err)
	}
	consents, err := service.ListLatestConsents(context.Background(), account.ID)
	if err != nil || len(consents) != 1 || consents[0].Granted {
		t.Fatalf("latest consent = %+v, %v", consents, err)
	}

	if err := service.AddFavorite(context.Background(), account.ID, " TEAM ", " ARSENAL ", "Arsenal"); err != nil {
		t.Fatal(err)
	}
	if err := service.AddFavorite(context.Background(), account.ID, "team", "arsenal", ""); err != nil {
		t.Fatal(err)
	}
	favorites, err := service.ListFavorites(context.Background(), account.ID)
	if err != nil || len(favorites) != 1 || favorites[0].EntityID != "arsenal" || favorites[0].DisplayName != "Arsenal" {
		t.Fatalf("favorites = %+v, %v", favorites, err)
	}
	if favorites[0].NotificationsEnabled {
		t.Fatal("favorite notifications defaulted on during dormant launch")
	}
	var activationEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM user_growth_events WHERE user_id=? AND event_type='FIRST_FAVORITE_ADDED'`, account.ID).Scan(&activationEvents); err != nil || activationEvents != 1 {
		t.Fatalf("first favorite activation events=%d err=%v", activationEvents, err)
	}
	if err := service.UpdateFavoriteNotifications(context.Background(), account.ID, "team", "arsenal", true); !errors.Is(err, ErrNotificationsDormant) {
		t.Fatalf("enable dormant favorite notifications error = %v", err)
	}

	if err := service.UpsertInterest(context.Background(), account.ID, Interest{Type: " LEAGUE ", Key: " EPL ", Weight: 2.5}); err != nil {
		t.Fatal(err)
	}
	if err := service.UpsertInterest(context.Background(), account.ID, Interest{Type: "league", Key: "epl", Weight: 3}); err != nil {
		t.Fatal(err)
	}
	interests, err := service.ListInterests(context.Background(), account.ID, false)
	if err != nil || len(interests) != 1 || interests[0].Key != "epl" || interests[0].Weight != 3 {
		t.Fatalf("interests = %+v, %v", interests, err)
	}
}

func TestPasswordPolicyAcceptsEightAndBoundsOversizedPasswords(t *testing.T) {
	cfg := Argon2Config{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 16}
	if _, err := HashPassword("seven77", cfg); err == nil {
		t.Fatal("short password was accepted")
	}
	if _, err := HashPassword(strings.Repeat("x", 129), cfg); err == nil {
		t.Fatal("oversized password was accepted")
	}
	for _, password := range []string{"eight888", strings.Repeat("long-pass-", 10)} {
		hash, err := HashPassword(password, cfg)
		if err != nil {
			t.Fatalf("HashPassword(%d chars): %v", len(password), err)
		}
		valid, err := VerifyPassword(hash, password)
		if err != nil || !valid {
			t.Fatalf("VerifyPassword(%d chars) valid=%v err=%v", len(password), valid, err)
		}
	}
}

func TestChangePasswordRequiresCurrentPasswordAndRevokesSessions(t *testing.T) {
	db := accountTestDB(t)
	service := NewService(NewSQLRepository(db, DialectSQLite))
	service.argon = Argon2Config{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 16}
	account, err := service.Register(context.Background(), "change@example.com", "initial-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET email_verified_at=CURRENT_TIMESTAMP WHERE id=?`, account.ID); err != nil {
		t.Fatal(err)
	}
	session, err := service.CreateSession(context.Background(), account.ID, "Browser", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ChangePassword(context.Background(), account.ID, "wrong-passphrase", "changed-passphrase"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("wrong current password error=%v", err)
	}
	if err := service.ChangePassword(context.Background(), account.ID, "initial-passphrase", "changed-passphrase"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ValidateSession(context.Background(), session.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("session survived password change: %v", err)
	}
	if _, err := service.Authenticate(context.Background(), account.Email, "changed-passphrase"); err != nil {
		t.Fatalf("new password does not authenticate: %v", err)
	}
}

func accountTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	schema := `
CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, public_id TEXT NOT NULL UNIQUE, email TEXT UNIQUE, email_normalized TEXT UNIQUE, password_hash TEXT, role TEXT NOT NULL DEFAULT 'user', email_verified_at DATETIME, account_status TEXT NOT NULL DEFAULT 'active', account_status_reason TEXT, country_code TEXT, timezone TEXT NOT NULL DEFAULT 'Africa/Nairobi', language TEXT NOT NULL DEFAULT 'en', personalization_enabled BOOLEAN NOT NULL DEFAULT true, marketing_consent BOOLEAN NOT NULL DEFAULT false, last_login_at DATETIME, deleted_at DATETIME, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE roles (role_key TEXT PRIMARY KEY);
INSERT INTO roles(role_key) VALUES ('basic');
CREATE TABLE user_roles (user_id INTEGER NOT NULL, role_key TEXT NOT NULL, granted_at DATETIME NOT NULL, revoked_at DATETIME, PRIMARY KEY(user_id, role_key));
CREATE TABLE user_identities (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, provider TEXT NOT NULL, provider_subject TEXT NOT NULL, provider_email TEXT, provider_email_verified BOOLEAN NOT NULL DEFAULT false, created_at DATETIME NOT NULL, last_used_at DATETIME, UNIQUE(provider,provider_subject), UNIQUE(user_id,provider));
CREATE TABLE user_sessions (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, token_hash TEXT NOT NULL UNIQUE, public_id TEXT NOT NULL UNIQUE, session_type TEXT NOT NULL DEFAULT 'user', device_name TEXT NOT NULL DEFAULT '', user_agent TEXT NOT NULL DEFAULT '', expires_at DATETIME NOT NULL, absolute_expires_at DATETIME, last_seen_at DATETIME NOT NULL, revoked_at DATETIME, revocation_reason TEXT, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE auth_tokens (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, purpose TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, expires_at DATETIME NOT NULL, used_at DATETIME, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE user_profiles (user_id INTEGER PRIMARY KEY, display_name TEXT NOT NULL DEFAULT '', country_code TEXT, odds_format TEXT NOT NULL DEFAULT 'decimal', odds_format_selected_at DATETIME, onboarding_completed_at DATETIME, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP);
CREATE TABLE terms_documents (document_key TEXT NOT NULL, version TEXT NOT NULL, published_at DATETIME NOT NULL, required BOOLEAN NOT NULL, content_url TEXT NOT NULL, PRIMARY KEY(document_key, version));
CREATE TABLE consent_events (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, document_key TEXT NOT NULL, document_version TEXT NOT NULL, action TEXT NOT NULL, source TEXT NOT NULL, metadata TEXT NOT NULL DEFAULT '{}', occurred_at DATETIME NOT NULL);
CREATE TABLE entity_favorites (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, display_name TEXT NOT NULL DEFAULT '', notifications_enabled BOOLEAN NOT NULL DEFAULT false, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(user_id, entity_type, entity_id));
CREATE TABLE user_interest_items (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, interest_type TEXT NOT NULL, interest_key TEXT NOT NULL, weight REAL NOT NULL, source TEXT NOT NULL, disabled BOOLEAN NOT NULL DEFAULT false, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(user_id, interest_type, interest_key));
CREATE TABLE visitor_acquisition (visitor_hash TEXT PRIMARY KEY, first_country_code TEXT, first_referrer_host TEXT, first_utm_source TEXT, first_utm_medium TEXT, first_utm_campaign TEXT, first_utm_content TEXT, first_landing_page TEXT, device_type TEXT NOT NULL, platform TEXT NOT NULL, first_seen_at DATETIME NOT NULL, last_seen_at DATETIME NOT NULL);
CREATE TABLE user_acquisition (user_id INTEGER PRIMARY KEY, visitor_hash TEXT, first_country_code TEXT, first_referrer_host TEXT, first_utm_source TEXT, first_utm_medium TEXT, first_utm_campaign TEXT, first_utm_content TEXT, first_landing_page TEXT, signup_country_code TEXT, signup_referrer_host TEXT, signup_utm_source TEXT, signup_utm_medium TEXT, signup_utm_campaign TEXT, signup_page TEXT, device_type TEXT NOT NULL, platform TEXT NOT NULL, created_at DATETIME NOT NULL);
CREATE TABLE user_growth_events (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, visitor_hash TEXT, event_type TEXT NOT NULL, country_code TEXT, platform TEXT NOT NULL, source TEXT NOT NULL DEFAULT '', occurred_at DATETIME NOT NULL, metadata TEXT NOT NULL DEFAULT '{}');`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestInjectedClockIsTheClockThatLands pins the whole session flow to one
// clock. Before this, CreateSession stamped created_at with the database's
// now() and last_seen_at with a hardcoded time.Now().UTC(), while
// FindAccountBySession compared expiry against the injected clock. A test
// could inject a fake "now" and still have its rows written by the real one,
// which is how a fake clock ended up judged expired against real timestamps.
func TestInjectedClockIsTheClockThatLands(t *testing.T) {
	db := accountTestDB(t)
	service := NewService(NewSQLRepository(db, DialectSQLite))
	service.argon = Argon2Config{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 16}

	// Deliberately not time.Now(): the point is that this value, and nothing
	// else, reaches the database.
	fixed := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
	service.now = func() time.Time { return fixed }

	account, err := service.Register(context.Background(), "clock@example.com", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}

	var userCreated time.Time
	if err := db.QueryRow(`SELECT created_at FROM users WHERE id = ?`, account.ID).Scan(&userCreated); err != nil {
		t.Fatal(err)
	}
	if !userCreated.UTC().Equal(fixed) {
		t.Fatalf("users.created_at = %s, want the injected clock %s", userCreated.UTC(), fixed)
	}

	if _, err := service.IssueEmailVerification(context.Background(), account.ID); err != nil {
		t.Fatal(err)
	}

	var tokenCreated time.Time
	if err := db.QueryRow(`SELECT created_at FROM auth_tokens WHERE user_id = ?`, account.ID).Scan(&tokenCreated); err != nil {
		t.Fatal(err)
	}
	if !tokenCreated.UTC().Equal(fixed) {
		t.Fatalf("auth_tokens.created_at = %s, want the injected clock %s", tokenCreated.UTC(), fixed)
	}

	credential, err := service.CreateSession(context.Background(), account.ID, "Browser", "test-agent")
	if err != nil {
		t.Fatal(err)
	}

	var sessionCreated, sessionLastSeen time.Time
	if err := db.QueryRow(`SELECT created_at, last_seen_at FROM user_sessions WHERE id = ?`, credential.Session.ID).
		Scan(&sessionCreated, &sessionLastSeen); err != nil {
		t.Fatal(err)
	}
	if !sessionCreated.UTC().Equal(fixed) {
		t.Fatalf("user_sessions.created_at = %s, want the injected clock %s", sessionCreated.UTC(), fixed)
	}
	if !sessionLastSeen.UTC().Equal(fixed) {
		t.Fatalf("user_sessions.last_seen_at = %s, want the injected clock %s", sessionLastSeen.UTC(), fixed)
	}
}

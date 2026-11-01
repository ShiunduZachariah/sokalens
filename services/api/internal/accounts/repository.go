package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

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

func (r *SQLRepository) CreateAccount(ctx context.Context, email, passwordHash string, now time.Time) (Account, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()

	publicID, err := newPublicID()
	if err != nil {
		return Account{}, err
	}
	query := fmt.Sprintf(`INSERT INTO users (public_id, email, email_normalized, password_hash, role, account_status, created_at, updated_at) VALUES (%s, %s, %s, %s, 'user', 'pending_verification', %s, %s) RETURNING id, email, created_at`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5), r.p(6))
	var account Account
	var created any
	if err := tx.QueryRowContext(ctx, query, publicID, email, email, passwordHash, now, now).Scan(&account.ID, &account.Email, &created); err != nil {
		if uniqueViolation(err) {
			return Account{}, ErrEmailExists
		}
		return Account{}, err
	}
	roleQuery := fmt.Sprintf(`INSERT INTO user_roles (user_id, role_key, granted_at) VALUES (%s, 'basic', %s) ON CONFLICT (user_id, role_key) DO NOTHING`, r.p(1), r.p(2))
	if _, err := tx.ExecContext(ctx, roleQuery, account.ID, now); err != nil {
		return Account{}, err
	}
	if err := tx.Commit(); err != nil {
		return Account{}, err
	}
	account.PasswordHash = passwordHash
	account.PublicID = publicID
	account.AccountStatus = "pending_verification"
	account.CreatedAt, _ = scanTime(created)
	return account, nil
}

func (r *SQLRepository) CreatePendingRegistration(ctx context.Context, input Registration, passwordHash, sessionHash, verificationHash string, now, sessionExpiresAt, verificationExpiresAt time.Time) (RegistrationResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return RegistrationResult{}, err
	}
	defer tx.Rollback()
	publicID, err := newPublicID()
	if err != nil {
		return RegistrationResult{}, err
	}
	sessionPublicID, err := newPublicID()
	if err != nil {
		return RegistrationResult{}, err
	}
	insertUser := fmt.Sprintf(`INSERT INTO users (public_id,email,email_normalized,password_hash,role,account_status,country_code,timezone,language,personalization_enabled,marketing_consent,created_at,updated_at) VALUES (%s,%s,%s,%s,'user','pending_verification',%s,'Africa/Nairobi',%s,true,%s,%s,%s) RETURNING id,created_at`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5), r.p(6), r.p(7), r.p(8), r.p(9))
	var account Account
	var created any
	if err := tx.QueryRowContext(ctx, insertUser, publicID, input.Email, input.Email, passwordHash, input.CountryCode, input.PreferredLanguage, input.MarketingConsent, now, now).Scan(&account.ID, &created); err != nil {
		if uniqueViolation(err) {
			return RegistrationResult{}, ErrEmailExists
		}
		return RegistrationResult{}, err
	}
	account.PublicID = publicID
	account.Email = input.Email
	account.AccountStatus = "pending_verification"
	account.CountryCode = input.CountryCode
	account.PasswordHash = passwordHash
	account.CreatedAt, _ = scanTime(created)
	statements := []struct {
		query string
		args  []any
	}{
		{fmt.Sprintf(`INSERT INTO user_identities(user_id,provider,provider_subject,provider_email,provider_email_verified,created_at) VALUES(%s,'PASSWORD',%s,%s,false,%s)`, r.p(1), r.p(2), r.p(3), r.p(4)), []any{account.ID, publicID, input.Email, now}},
		{fmt.Sprintf(`INSERT INTO user_roles(user_id,role_key,granted_at) VALUES(%s,'basic',%s) ON CONFLICT(user_id,role_key) DO NOTHING`, r.p(1), r.p(2)), []any{account.ID, now}},
		{fmt.Sprintf(`INSERT INTO user_profiles(user_id,display_name,country_code,odds_format,created_at,updated_at) VALUES(%s,%s,%s,'decimal',%s,%s)`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5)), []any{account.ID, input.DisplayName, input.CountryCode, now, now}},
		{fmt.Sprintf(`INSERT INTO consent_events(user_id,document_key,document_version,action,source,metadata,occurred_at) VALUES(%s,'terms',%s,'accepted','registration',%s%s,%s)`, r.p(1), r.p(2), r.p(3), r.jsonCast(), r.p(4)), []any{account.ID, input.LegalVersion, `{}`, now}},
		{fmt.Sprintf(`INSERT INTO consent_events(user_id,document_key,document_version,action,source,metadata,occurred_at) VALUES(%s,'privacy',%s,'accepted','registration',%s%s,%s)`, r.p(1), r.p(2), r.p(3), r.jsonCast(), r.p(4)), []any{account.ID, input.LegalVersion, `{}`, now}},
		{fmt.Sprintf(`INSERT INTO user_sessions(user_id,token_hash,public_id,session_type,device_name,user_agent,expires_at,absolute_expires_at,last_seen_at,created_at) VALUES(%s,%s,%s,'user',%s,%s,%s,%s,%s,%s) RETURNING id`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5), r.p(6), r.p(7), r.p(8), r.p(9)), []any{account.ID, sessionHash, sessionPublicID, input.DeviceName, input.UserAgent, sessionExpiresAt, sessionExpiresAt, now, now}},
		{fmt.Sprintf(`INSERT INTO auth_tokens(user_id,purpose,token_hash,expires_at,created_at) VALUES(%s,'email_verification',%s,%s,%s)`, r.p(1), r.p(2), r.p(3), r.p(4)), []any{account.ID, verificationHash, verificationExpiresAt, now}},
	}
	var sessionID int64
	for index, statement := range statements {
		if index == 5 {
			if err := tx.QueryRowContext(ctx, statement.query, statement.args...).Scan(&sessionID); err != nil {
				return RegistrationResult{}, err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return RegistrationResult{}, err
		}
	}
	visitorHash := ""
	if strings.TrimSpace(input.Acquisition.VisitorID) != "" {
		visitorHash = hashToken(input.Acquisition.VisitorID)
		visitorQuery := fmt.Sprintf(`INSERT INTO visitor_acquisition(visitor_hash,first_country_code,first_referrer_host,first_utm_source,first_utm_medium,first_utm_campaign,first_utm_content,first_landing_page,device_type,platform,first_seen_at,last_seen_at) VALUES(%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s) ON CONFLICT(visitor_hash) DO UPDATE SET last_seen_at=excluded.last_seen_at`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5), r.p(6), r.p(7), r.p(8), r.p(9), r.p(10), r.p(11), r.p(12))
		if _, err := tx.ExecContext(ctx, visitorQuery, visitorHash, nullableString(input.Acquisition.CountryCode), nullableString(input.Acquisition.ReferrerHost), nullableString(input.Acquisition.UTMSource), nullableString(input.Acquisition.UTMMedium), nullableString(input.Acquisition.UTMCampaign), nullableString(input.Acquisition.UTMContent), nullableString(input.Acquisition.LandingPage), input.Acquisition.DeviceType, input.Acquisition.Platform, now, now); err != nil {
			return RegistrationResult{}, err
		}
	}
	acquisitionQuery := fmt.Sprintf(`INSERT INTO user_acquisition(user_id,visitor_hash,first_country_code,first_referrer_host,first_utm_source,first_utm_medium,first_utm_campaign,first_utm_content,first_landing_page,signup_country_code,signup_referrer_host,signup_utm_source,signup_utm_medium,signup_utm_campaign,signup_page,device_type,platform,created_at) VALUES(%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s)`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5), r.p(6), r.p(7), r.p(8), r.p(9), r.p(10), r.p(11), r.p(12), r.p(13), r.p(14), r.p(15), r.p(16), r.p(17), r.p(18))
	if _, err := tx.ExecContext(ctx, acquisitionQuery, account.ID, nullableString(visitorHash), nullableString(input.Acquisition.CountryCode), nullableString(input.Acquisition.ReferrerHost), nullableString(input.Acquisition.UTMSource), nullableString(input.Acquisition.UTMMedium), nullableString(input.Acquisition.UTMCampaign), nullableString(input.Acquisition.UTMContent), nullableString(input.Acquisition.LandingPage), input.CountryCode, nullableString(input.Acquisition.ReferrerHost), nullableString(input.Acquisition.UTMSource), nullableString(input.Acquisition.UTMMedium), nullableString(input.Acquisition.UTMCampaign), nullableString(input.Acquisition.SignupPage), input.Acquisition.DeviceType, input.Acquisition.Platform, now); err != nil {
		return RegistrationResult{}, err
	}
	growthQuery := fmt.Sprintf(`INSERT INTO user_growth_events(user_id,visitor_hash,event_type,country_code,platform,source,occurred_at,metadata) VALUES(%s,%s,'USER_REGISTERED',%s,%s,%s,%s,%s%s)`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5), r.p(6), r.p(7), r.jsonCast())
	if _, err := tx.ExecContext(ctx, growthQuery, account.ID, nullableString(visitorHash), input.CountryCode, input.Acquisition.Platform, input.Acquisition.UTMSource, now, `{}`); err != nil {
		return RegistrationResult{}, err
	}
	if input.LanguageSource == "selected" || input.LanguageSource == "guest" {
		metadata, err := json.Marshal(map[string]string{"preferred_language": input.PreferredLanguage, "source": input.LanguageSource})
		if err != nil {
			return RegistrationResult{}, err
		}
		languageQuery := fmt.Sprintf(`INSERT INTO user_growth_events(user_id,visitor_hash,event_type,country_code,platform,source,occurred_at,metadata) VALUES(%s,%s,'LANGUAGE_SELECTED',%s,%s,%s,%s,%s%s)`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5), r.p(6), r.p(7), r.jsonCast())
		if _, err := tx.ExecContext(ctx, languageQuery, account.ID, nullableString(visitorHash), input.CountryCode, input.Acquisition.Platform, input.LanguageSource, now, string(metadata)); err != nil {
			return RegistrationResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return RegistrationResult{}, err
	}
	return RegistrationResult{
		Account:      account,
		Credential:   SessionCredential{Session: Session{ID: sessionID, PublicID: sessionPublicID, UserID: account.ID, DeviceName: input.DeviceName, UserAgent: input.UserAgent, ExpiresAt: sessionExpiresAt, LastSeenAt: now, CreatedAt: now}},
		Verification: IssuedToken{UserID: account.ID, ExpiresAt: verificationExpiresAt},
	}, nil
}

func (r *SQLRepository) RecordGrowthEvent(ctx context.Context, event GrowthEvent) error {
	query := fmt.Sprintf(`INSERT INTO user_growth_events(visitor_hash,event_type,country_code,platform,source,occurred_at,metadata) VALUES(%s,%s,%s,%s,%s,%s,%s%s)`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5), r.p(6), r.p(7), r.jsonCast())
	_, err := r.db.ExecContext(ctx, query, hashToken(event.VisitorID), event.EventType, nullableString(event.CountryCode), event.Platform, event.Source, event.OccurredAt, string(event.Metadata))
	return err
}

func (r *SQLRepository) FindAccountByEmail(ctx context.Context, email string) (Account, error) {
	query := fmt.Sprintf(`SELECT id, public_id, email, password_hash, email_verified_at, account_status, COALESCE(country_code,''), created_at FROM users WHERE email_normalized = %s AND deleted_at IS NULL AND account_status IN ('pending_verification','active')`, r.p(1))
	return r.scanAccount(r.db.QueryRowContext(ctx, query, email))
}

func (r *SQLRepository) FindAccountByID(ctx context.Context, userID int64) (Account, error) {
	query := fmt.Sprintf(`SELECT id, public_id, email, password_hash, email_verified_at, account_status, COALESCE(country_code,''), created_at FROM users WHERE id = %s AND deleted_at IS NULL AND account_status IN ('pending_verification','active')`, r.p(1))
	return r.scanAccount(r.db.QueryRowContext(ctx, query, userID))
}

func (r *SQLRepository) FindAccountBySession(ctx context.Context, tokenHash string, now time.Time) (Account, Session, error) {
	query := fmt.Sprintf(`SELECT u.id, u.public_id, u.email, u.password_hash, u.email_verified_at, u.account_status, COALESCE(u.country_code,''), u.created_at, s.id, s.public_id, s.device_name, s.user_agent, s.expires_at, s.last_seen_at, s.revoked_at, s.created_at FROM user_sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = %s AND s.revoked_at IS NULL AND s.expires_at > %s AND (s.absolute_expires_at IS NULL OR s.absolute_expires_at > %s) AND u.deleted_at IS NULL AND u.account_status IN ('pending_verification','active')`, r.p(1), r.p(2), r.p(3))
	var account Account
	var session Session
	var verified, accountCreated, expires, lastSeen, revoked, sessionCreated any
	err := r.db.QueryRowContext(ctx, query, tokenHash, now, now).Scan(&account.ID, &account.PublicID, &account.Email, &account.PasswordHash, &verified, &account.AccountStatus, &account.CountryCode, &accountCreated, &session.ID, &session.PublicID, &session.DeviceName, &session.UserAgent, &expires, &lastSeen, &revoked, &sessionCreated)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, Session{}, ErrInvalidSession
	}
	if err != nil {
		return Account{}, Session{}, err
	}
	account.EmailVerifiedAt = nullableTime(verified)
	account.CreatedAt, _ = scanTime(accountCreated)
	session.UserID = account.ID
	session.ExpiresAt, _ = scanTime(expires)
	session.LastSeenAt, _ = scanTime(lastSeen)
	session.RevokedAt = nullableTime(revoked)
	session.CreatedAt, _ = scanTime(sessionCreated)
	return account, session, nil
}

func (r *SQLRepository) IssueAccountToken(ctx context.Context, userID int64, purpose TokenPurpose, tokenHash string, expiresAt, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	deleteQuery := fmt.Sprintf(`DELETE FROM auth_tokens WHERE user_id = %s AND purpose = %s AND used_at IS NULL`, r.p(1), r.p(2))
	if _, err := tx.ExecContext(ctx, deleteQuery, userID, purpose); err != nil {
		return err
	}
	insertQuery := fmt.Sprintf(`INSERT INTO auth_tokens (user_id, purpose, token_hash, expires_at, created_at) VALUES (%s, %s, %s, %s, %s)`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5))
	if _, err := tx.ExecContext(ctx, insertQuery, userID, purpose, tokenHash, expiresAt, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLRepository) VerifyEmail(ctx context.Context, tokenHash string, now time.Time) error {
	return r.consumeToken(ctx, tokenHash, PurposeEmailVerification, now, func(tx *sql.Tx, userID int64) error {
		query := fmt.Sprintf(`UPDATE users SET email_verified_at = %s, account_status = 'active', account_status_reason = NULL, updated_at = %s WHERE id = %s`, r.p(1), r.p(2), r.p(3))
		if _, err := tx.ExecContext(ctx, query, now, now, userID); err != nil {
			return err
		}
		identity := fmt.Sprintf(`UPDATE user_identities SET provider_email_verified = true, last_used_at = %s WHERE user_id = %s AND provider = 'PASSWORD'`, r.p(1), r.p(2))
		if _, err := tx.ExecContext(ctx, identity, now, userID); err != nil {
			return err
		}
		growth := fmt.Sprintf(`INSERT INTO user_growth_events(user_id,event_type,platform,occurred_at,metadata) VALUES(%s,'EMAIL_VERIFIED','web',%s,%s%s)`, r.p(1), r.p(2), r.p(3), r.jsonCast())
		_, err := tx.ExecContext(ctx, growth, userID, now, `{}`)
		return err
	})
}

func (r *SQLRepository) RecordLogin(ctx context.Context, userID int64, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := fmt.Sprintf(`UPDATE users SET last_login_at = %s, updated_at = %s WHERE id = %s`, r.p(1), r.p(2), r.p(3))
	if _, err := tx.ExecContext(ctx, query, now, now, userID); err != nil {
		return err
	}
	identity := fmt.Sprintf(`UPDATE user_identities SET last_used_at = %s WHERE user_id = %s AND provider = 'PASSWORD'`, r.p(1), r.p(2))
	if _, err := tx.ExecContext(ctx, identity, now, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLRepository) ResetPassword(ctx context.Context, tokenHash, passwordHash string, now time.Time) error {
	return r.consumeToken(ctx, tokenHash, PurposePasswordReset, now, func(tx *sql.Tx, userID int64) error {
		query := fmt.Sprintf(`UPDATE users SET password_hash = %s, updated_at = %s WHERE id = %s`, r.p(1), r.p(2), r.p(3))
		if _, err := tx.ExecContext(ctx, query, passwordHash, now, userID); err != nil {
			return err
		}
		revoke := fmt.Sprintf(`UPDATE user_sessions SET revoked_at = %s, revocation_reason = 'password_reset' WHERE user_id = %s AND revoked_at IS NULL`, r.p(1), r.p(2))
		_, err := tx.ExecContext(ctx, revoke, now, userID)
		return err
	})
}

func (r *SQLRepository) ChangePassword(ctx context.Context, userID int64, passwordHash string, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := fmt.Sprintf(`UPDATE users SET password_hash = %s, updated_at = %s WHERE id = %s AND deleted_at IS NULL AND account_status IN ('pending_verification','active')`, r.p(1), r.p(2), r.p(3))
	result, err := tx.ExecContext(ctx, query, passwordHash, now, userID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrNotFound
	}
	revoke := fmt.Sprintf(`UPDATE user_sessions SET revoked_at = %s, revocation_reason = 'password_changed' WHERE user_id = %s AND revoked_at IS NULL`, r.p(1), r.p(2))
	if _, err := tx.ExecContext(ctx, revoke, now, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLRepository) consumeToken(ctx context.Context, tokenHash string, purpose TokenPurpose, now time.Time, apply func(*sql.Tx, int64) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := fmt.Sprintf(`UPDATE auth_tokens SET used_at = %s WHERE token_hash = %s AND purpose = %s AND used_at IS NULL AND expires_at > %s RETURNING user_id`, r.p(1), r.p(2), r.p(3), r.p(4))
	var userID int64
	if err := tx.QueryRowContext(ctx, query, now, tokenHash, purpose, now).Scan(&userID); errors.Is(err, sql.ErrNoRows) {
		return ErrInvalidToken
	} else if err != nil {
		return err
	}
	if err := apply(tx, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLRepository) CreateSession(ctx context.Context, userID int64, tokenHash, deviceName, userAgent string, expiresAt, now time.Time) (Session, error) {
	publicID, err := newPublicID()
	if err != nil {
		return Session{}, err
	}
	query := fmt.Sprintf(`INSERT INTO user_sessions (user_id, token_hash, public_id, device_name, user_agent, expires_at, last_seen_at, created_at) VALUES (%s, %s, %s, %s, %s, %s, %s, %s) RETURNING id, created_at`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5), r.p(6), r.p(7), r.p(8))
	var session Session
	var created any
	if err := r.db.QueryRowContext(ctx, query, userID, tokenHash, publicID, deviceName, userAgent, expiresAt, now, now).Scan(&session.ID, &created); err != nil {
		return Session{}, err
	}
	session.PublicID = publicID
	session.UserID = userID
	session.DeviceName = deviceName
	session.UserAgent = userAgent
	session.ExpiresAt = expiresAt
	session.LastSeenAt, _ = scanTime(created)
	session.CreatedAt, _ = scanTime(created)
	return session, nil
}

func (r *SQLRepository) ListSessions(ctx context.Context, userID int64) ([]Session, error) {
	query := fmt.Sprintf(`SELECT id, public_id, device_name, user_agent, expires_at, last_seen_at, revoked_at, created_at FROM user_sessions WHERE user_id = %s ORDER BY created_at DESC, id DESC`, r.p(1))
	rows, err := r.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sessions []Session
	for rows.Next() {
		var session Session
		var expires, lastSeen, revoked, created any
		if err := rows.Scan(&session.ID, &session.PublicID, &session.DeviceName, &session.UserAgent, &expires, &lastSeen, &revoked, &created); err != nil {
			return nil, err
		}
		session.UserID = userID
		session.ExpiresAt, _ = scanTime(expires)
		session.LastSeenAt, _ = scanTime(lastSeen)
		session.RevokedAt = nullableTime(revoked)
		session.CreatedAt, _ = scanTime(created)
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (r *SQLRepository) RevokeSession(ctx context.Context, userID, sessionID int64, now time.Time) error {
	query := fmt.Sprintf(`UPDATE user_sessions SET revoked_at = %s, revocation_reason = 'user_revoked' WHERE id = %s AND user_id = %s AND revoked_at IS NULL`, r.p(1), r.p(2), r.p(3))
	result, err := r.db.ExecContext(ctx, query, now, sessionID, userID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrInvalidSession
	}
	return nil
}

func (r *SQLRepository) RevokeAllSessions(ctx context.Context, userID int64, now time.Time) error {
	query := fmt.Sprintf(`UPDATE user_sessions SET revoked_at = %s, revocation_reason = 'user_revoked_all' WHERE user_id = %s AND revoked_at IS NULL`, r.p(1), r.p(2))
	_, err := r.db.ExecContext(ctx, query, now, userID)
	return err
}

func (r *SQLRepository) UpsertProfile(ctx context.Context, profile Profile) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previousLanguage string
	previousQuery := fmt.Sprintf(`SELECT language FROM users WHERE id = %s`, r.p(1))
	if r.dialect == DialectPostgres {
		// Serialize the read/update/event chain so concurrent preference changes
		// cannot record stale previous-language metadata.
		previousQuery += ` FOR UPDATE`
	}
	if err := tx.QueryRowContext(ctx, previousQuery, profile.UserID).Scan(&previousLanguage); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	userQuery := fmt.Sprintf(`UPDATE users SET timezone = %s, language = %s, personalization_enabled = %s, marketing_consent = %s, updated_at = %s WHERE id = %s`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5), r.p(6))
	if _, err := tx.ExecContext(ctx, userQuery, profile.Timezone, profile.Language, profile.PersonalizationEnabled, profile.MarketingConsent, profile.UpdatedAt, profile.UserID); err != nil {
		return err
	}
	profileQuery := fmt.Sprintf(`INSERT INTO user_profiles (user_id, display_name, country_code, odds_format, odds_format_selected_at, onboarding_completed_at, created_at, updated_at) VALUES (%s, %s, %s, %s, %s, %s, %s, %s) ON CONFLICT (user_id) DO UPDATE SET display_name = excluded.display_name, country_code = excluded.country_code, odds_format = excluded.odds_format, odds_format_selected_at = excluded.odds_format_selected_at, onboarding_completed_at = excluded.onboarding_completed_at, updated_at = excluded.updated_at`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5), r.p(6), r.now(), r.p(7))
	if _, err := tx.ExecContext(ctx, profileQuery, profile.UserID, profile.DisplayName, nullableString(profile.CountryCode), profile.OddsFormat, profile.OddsFormatSelectedAt, profile.OnboardingCompletedAt, profile.UpdatedAt); err != nil {
		return err
	}
	if previousLanguage != profile.Language {
		metadata, err := json.Marshal(map[string]string{"previous_language": previousLanguage, "preferred_language": profile.Language, "source": "profile"})
		if err != nil {
			return err
		}
		// This is a growth signal, not an append-only security audit. Keep one
		// latest language-change event per account so preference toggling cannot
		// grow the event table without bound.
		deleteEventQuery := fmt.Sprintf(`DELETE FROM user_growth_events WHERE user_id = %s AND event_type = 'LANGUAGE_CHANGED'`, r.p(1))
		if _, err := tx.ExecContext(ctx, deleteEventQuery, profile.UserID); err != nil {
			return err
		}
		eventQuery := fmt.Sprintf(`INSERT INTO user_growth_events(user_id,event_type,country_code,platform,source,occurred_at,metadata) VALUES(%s,'LANGUAGE_CHANGED',%s,'web','profile',%s,%s%s)`, r.p(1), r.p(2), r.p(3), r.p(4), r.jsonCast())
		if _, err := tx.ExecContext(ctx, eventQuery, profile.UserID, nullableString(profile.CountryCode), profile.UpdatedAt, string(metadata)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *SQLRepository) GetProfile(ctx context.Context, userID int64) (Profile, error) {
	query := fmt.Sprintf(`SELECT u.id, COALESCE(p.display_name, ''), u.timezone, COALESCE(p.country_code, ''), COALESCE(p.odds_format, 'decimal'), p.odds_format_selected_at, u.language, u.personalization_enabled, u.marketing_consent, p.onboarding_completed_at, COALESCE(p.updated_at, u.updated_at) FROM users u LEFT JOIN user_profiles p ON p.user_id = u.id WHERE u.id = %s AND u.deleted_at IS NULL`, r.p(1))
	var profile Profile
	var oddsSelected, onboarding, updated any
	if err := r.db.QueryRowContext(ctx, query, userID).Scan(&profile.UserID, &profile.DisplayName, &profile.Timezone, &profile.CountryCode, &profile.OddsFormat, &oddsSelected, &profile.Language, &profile.PersonalizationEnabled, &profile.MarketingConsent, &onboarding, &updated); errors.Is(err, sql.ErrNoRows) {
		return Profile{}, ErrNotFound
	} else if err != nil {
		return Profile{}, err
	}
	profile.OddsFormatSelectedAt = nullableTime(oddsSelected)
	profile.OnboardingCompletedAt = nullableTime(onboarding)
	profile.UpdatedAt, _ = scanTime(updated)
	return profile, nil
}

func (r *SQLRepository) RecordConsent(ctx context.Context, userID int64, consent Consent) error {
	metadata := consent.Metadata
	if len(metadata) == 0 {
		metadata = []byte(`{}`)
	}
	query := fmt.Sprintf(`INSERT INTO consent_events (user_id, document_key, document_version, action, source, metadata, occurred_at) VALUES (%s, %s, %s, %s, %s, %s%s, %s)`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5), r.p(6), r.jsonCast(), r.p(7))
	action := "withdrawn"
	if consent.Granted {
		action = "accepted"
	}
	_, err := r.db.ExecContext(ctx, query, userID, consent.DocumentKey, consent.DocumentVersion, action, consent.Source, string(metadata), consent.OccurredAt)
	return err
}

func (r *SQLRepository) ListLatestConsents(ctx context.Context, userID int64) ([]Consent, error) {
	query := fmt.Sprintf(`SELECT document_key, document_version, action, source, metadata, occurred_at FROM (SELECT document_key, document_version, action, source, metadata, occurred_at, ROW_NUMBER() OVER (PARTITION BY document_key ORDER BY occurred_at DESC, id DESC) AS rn FROM consent_events WHERE user_id = %s) latest WHERE rn = 1 ORDER BY document_key`, r.p(1))
	rows, err := r.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var consents []Consent
	for rows.Next() {
		var consent Consent
		var action string
		var metadata []byte
		var occurred any
		if err := rows.Scan(&consent.DocumentKey, &consent.DocumentVersion, &action, &consent.Source, &metadata, &occurred); err != nil {
			return nil, err
		}
		consent.Granted = action == "accepted"
		consent.Metadata = metadata
		consent.OccurredAt, _ = scanTime(occurred)
		consents = append(consents, consent)
	}
	return consents, rows.Err()
}

func (r *SQLRepository) AddFavorite(ctx context.Context, userID int64, favorite Favorite) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := fmt.Sprintf(`INSERT INTO entity_favorites (user_id, entity_type, entity_id, display_name, notifications_enabled, created_at, updated_at) VALUES (%s, %s, %s, %s, %s, %s, %s) ON CONFLICT (user_id, entity_type, entity_id) DO UPDATE SET display_name = CASE WHEN excluded.display_name <> '' THEN excluded.display_name ELSE entity_favorites.display_name END, notifications_enabled = excluded.notifications_enabled, updated_at = excluded.updated_at`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5), r.now(), r.p(6))
	if _, err := tx.ExecContext(ctx, query, userID, favorite.EntityType, favorite.EntityID, favorite.DisplayName, favorite.NotificationsEnabled, favorite.UpdatedAt); err != nil {
		return err
	}
	eventQuery := fmt.Sprintf(`INSERT INTO user_growth_events(user_id,event_type,platform,occurred_at,metadata) SELECT %s,'FIRST_FAVORITE_ADDED','web',%s,%s WHERE NOT EXISTS (SELECT 1 FROM user_growth_events WHERE user_id=%s AND event_type='FIRST_FAVORITE_ADDED') ON CONFLICT DO NOTHING`, r.p(1), r.p(2), r.p(3), r.p(4))
	metadata, _ := json.Marshal(map[string]string{"entity_type": favorite.EntityType})
	if _, err := tx.ExecContext(ctx, eventQuery, userID, favorite.UpdatedAt, metadata, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLRepository) UpdateFavoriteNotifications(ctx context.Context, userID int64, entityType, entityID string, enabled bool, now time.Time) error {
	query := fmt.Sprintf(`UPDATE entity_favorites SET notifications_enabled = %s, updated_at = %s WHERE user_id = %s AND entity_type = %s AND entity_id = %s`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5))
	result, err := r.db.ExecContext(ctx, query, enabled, now, userID, entityType, entityID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SQLRepository) RemoveFavorite(ctx context.Context, userID int64, entityType, entityID string) error {
	query := fmt.Sprintf(`DELETE FROM entity_favorites WHERE user_id = %s AND entity_type = %s AND entity_id = %s`, r.p(1), r.p(2), r.p(3))
	_, err := r.db.ExecContext(ctx, query, userID, entityType, entityID)
	return err
}

func (r *SQLRepository) ListFavorites(ctx context.Context, userID int64) ([]Favorite, error) {
	query := fmt.Sprintf(`SELECT id, entity_type, entity_id, display_name, notifications_enabled, created_at, updated_at FROM entity_favorites WHERE user_id = %s ORDER BY created_at DESC, id DESC`, r.p(1))
	rows, err := r.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var favorites []Favorite
	for rows.Next() {
		var favorite Favorite
		var created, updated any
		if err := rows.Scan(&favorite.ID, &favorite.EntityType, &favorite.EntityID, &favorite.DisplayName, &favorite.NotificationsEnabled, &created, &updated); err != nil {
			return nil, err
		}
		favorite.CreatedAt, _ = scanTime(created)
		favorite.UpdatedAt, _ = scanTime(updated)
		favorites = append(favorites, favorite)
	}
	return favorites, rows.Err()
}

func (r *SQLRepository) UpsertInterest(ctx context.Context, userID int64, interest Interest) error {
	query := fmt.Sprintf(`INSERT INTO user_interest_items (user_id, interest_type, interest_key, weight, source, disabled, created_at, updated_at) VALUES (%s, %s, %s, %s, %s, %s, %s, %s) ON CONFLICT (user_id, interest_type, interest_key) DO UPDATE SET weight = excluded.weight, source = excluded.source, disabled = excluded.disabled, updated_at = excluded.updated_at`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5), r.p(6), r.now(), r.p(7))
	_, err := r.db.ExecContext(ctx, query, userID, interest.Type, interest.Key, interest.Weight, interest.Source, interest.Disabled, interest.UpdatedAt)
	return err
}

func (r *SQLRepository) ListInterests(ctx context.Context, userID int64, includeDisabled bool) ([]Interest, error) {
	query := fmt.Sprintf(`SELECT interest_type, interest_key, weight, source, disabled, updated_at FROM user_interest_items WHERE user_id = %s`, r.p(1))
	if !includeDisabled {
		query += " AND disabled = false"
	}
	query += " ORDER BY interest_type, weight DESC, interest_key"
	rows, err := r.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var interests []Interest
	for rows.Next() {
		var interest Interest
		var updated any
		if err := rows.Scan(&interest.Type, &interest.Key, &interest.Weight, &interest.Source, &interest.Disabled, &updated); err != nil {
			return nil, err
		}
		interest.UpdatedAt, _ = scanTime(updated)
		interests = append(interests, interest)
	}
	return interests, rows.Err()
}

func (r *SQLRepository) ReplaceInterests(ctx context.Context, userID int64, interests []Interest) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM user_interest_items WHERE user_id = %s AND source = 'explicit'`, r.p(1)), userID); err != nil {
		return err
	}
	for _, interest := range interests {
		query := fmt.Sprintf(`INSERT INTO user_interest_items (user_id, interest_type, interest_key, weight, source, disabled, created_at, updated_at) VALUES (%s, %s, %s, %s, %s, %s, %s, %s) ON CONFLICT (user_id, interest_type, interest_key) DO UPDATE SET weight = excluded.weight, source = excluded.source, disabled = excluded.disabled, updated_at = excluded.updated_at`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5), r.p(6), r.now(), r.p(7))
		if _, err := tx.ExecContext(ctx, query, userID, interest.Type, interest.Key, interest.Weight, interest.Source, interest.Disabled, interest.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *SQLRepository) ResetPersonalization(ctx context.Context, userID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`DELETE FROM recommendation_candidates WHERE user_id = $1`,
		`DELETE FROM recommendation_dismissals WHERE user_id = $1`,
		`DELETE FROM recommendation_events WHERE user_id = $1`,
		`DELETE FROM user_interest_items WHERE user_id = $1`,
	} {
		if _, err := tx.ExecContext(ctx, statement, userID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE users SET personalization_enabled = false, updated_at = now() WHERE id = $1`, userID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLRepository) RequestDeletion(ctx context.Context, userID int64, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_deletion_requests (user_id, eligible_after) VALUES ($1, $2) ON CONFLICT (user_id) WHERE status IN ('pending','processing') DO NOTHING`, userID, now.Add(30*24*time.Hour)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET account_status = 'deletion_pending', updated_at = $2 WHERE id = $1`, userID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE user_sessions SET revoked_at = $2, revocation_reason = 'account_deletion_requested' WHERE user_id = $1 AND revoked_at IS NULL`, userID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE notification_outbox SET status = 'suppressed', last_error = 'account deletion requested' WHERE user_id = $1 AND status IN ('pending','retry')`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *SQLRepository) scanAccount(row *sql.Row) (Account, error) {
	var account Account
	var verified, created any
	err := row.Scan(&account.ID, &account.PublicID, &account.Email, &account.PasswordHash, &verified, &account.AccountStatus, &account.CountryCode, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	account.EmailVerifiedAt = nullableTime(verified)
	account.CreatedAt, _ = scanTime(created)
	return account, nil
}

func (r *SQLRepository) p(position int) string {
	if r.dialect == DialectSQLite {
		return "?"
	}
	return fmt.Sprintf("$%d", position)
}

func (r *SQLRepository) now() string {
	if r.dialect == DialectSQLite {
		return "CURRENT_TIMESTAMP"
	}
	return "now()"
}

func (r *SQLRepository) jsonCast() string {
	if r.dialect == DialectPostgres {
		return "::jsonb"
	}
	return ""
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newPublicID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	// UUIDv7 keeps public identifiers opaque while making their index insertion
	// order follow creation time. The remaining bits stay cryptographically
	// random; database primary/foreign keys remain unchanged.
	millis := uint64(time.Now().UTC().UnixMilli())
	var timestamp [8]byte
	binary.BigEndian.PutUint64(timestamp[:], millis)
	copy(value[0:6], timestamp[2:8])
	value[6] = (value[6] & 0x0f) | 0x70
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func uniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	return (errors.As(err, &pgErr) && pgErr.Code == "23505") || strings.Contains(strings.ToLower(err.Error()), "unique constraint")
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

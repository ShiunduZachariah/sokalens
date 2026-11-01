package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type Dialect string

const (
	DialectPostgres Dialect = "postgres"
	DialectSQLite   Dialect = "sqlite"
)

var (
	ErrInvalidCredentials = errors.New("invalid admin credentials")
	ErrInvalidSession     = errors.New("invalid admin session")
)

type LoginResult struct {
	Token     string
	ExpiresAt time.Time
	User      User
}

type SQLAuthStore struct {
	db      *sql.DB
	dialect Dialect
}

func NewSQLAuthStore(db *sql.DB, dialect Dialect) SQLAuthStore {
	return SQLAuthStore{db: db, dialect: dialect}
}

type AuthService struct {
	store SQLAuthStore
	ttl   time.Duration
	now   func() time.Time
}

// Authorize resolves administrative permissions from the database-backed RBAC
// model. Consumer plan/tier state is deliberately not consulted here.
func (s AuthService) Authorize(ctx context.Context, userID int64, permission Permission) (bool, error) {
	return s.store.authorize(ctx, userID, permission, time.Now().UTC())
}

func NewAuthService(store SQLAuthStore, ttl time.Duration) AuthService {
	return AuthService{store: store, ttl: ttl, now: func() time.Time { return time.Now().UTC() }}
}

func (s AuthService) Login(ctx context.Context, email, password string) (LoginResult, error) {
	if s.ttl <= 0 {
		return LoginResult{}, errors.New("admin session TTL must be positive")
	}

	user, err := s.store.findActiveAdminByEmail(ctx, email)
	if err != nil {
		return LoginResult{}, err
	}
	if user.ID == 0 {
		return LoginResult{}, ErrInvalidCredentials
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return LoginResult{}, ErrInvalidCredentials
	}

	token, err := newSessionToken()
	if err != nil {
		return LoginResult{}, err
	}
	expiresAt := s.now().Add(s.ttl)
	if err := s.store.insertSession(ctx, user.ID, hashSessionToken(token), expiresAt); err != nil {
		return LoginResult{}, err
	}
	user.PasswordHash = ""
	return LoginResult{Token: token, ExpiresAt: expiresAt, User: user}, nil
}

func (s AuthService) ValidateSession(ctx context.Context, token string) (User, error) {
	if strings.TrimSpace(token) == "" {
		return User{}, ErrInvalidSession
	}
	user, err := s.store.findUserBySessionHash(ctx, hashSessionToken(token), s.now())
	if err != nil {
		return User{}, err
	}
	if user.ID == 0 {
		return User{}, ErrInvalidSession
	}
	user.PasswordHash = ""
	return user, nil
}

func (s AuthService) Logout(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return ErrInvalidSession
	}
	return s.store.revokeSession(ctx, hashSessionToken(token), s.now())
}

func (s SQLAuthStore) findActiveAdminByEmail(ctx context.Context, email string) (User, error) {
	if s.db == nil {
		return User{}, errors.New("admin auth DB is nil")
	}
	query := fmt.Sprintf(`
SELECT id, email, password_hash, role, CAST(created_at AS TEXT)
FROM users
WHERE lower(email) = lower(%s)
  AND deleted_at IS NULL
  AND account_status = 'active'
  AND role IN ('super_admin', 'admin', 'viewer', 'support_admin', 'content_admin', 'billing_admin', 'analyst')`, s.placeholder(1))
	var user User
	var rawCreatedAt string
	if err := s.db.QueryRowContext(ctx, query, strings.TrimSpace(email)).Scan(&user.ID, &user.Email, &user.PasswordHash, &user.Role, &rawCreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, nil
		}
		return User{}, err
	}
	user.CreatedAt = parseSQLTime(rawCreatedAt)
	return user, nil
}

func (s SQLAuthStore) insertSession(ctx context.Context, userID int64, tokenHash string, expiresAt time.Time) error {
	query := fmt.Sprintf(`
INSERT INTO user_sessions (user_id, token_hash, expires_at, created_at)
VALUES (%s, %s, %s, %s)`, s.placeholder(1), s.placeholder(2), s.placeholder(3), s.currentTimestamp())
	_, err := s.db.ExecContext(ctx, query, userID, tokenHash, expiresAt)
	return err
}

func (s SQLAuthStore) findUserBySessionHash(ctx context.Context, tokenHash string, now time.Time) (User, error) {
	query := fmt.Sprintf(`
SELECT u.id, u.email, u.password_hash, u.role, CAST(u.created_at AS TEXT)
FROM user_sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = %s
  AND s.revoked_at IS NULL
  AND s.expires_at > %s
	AND u.deleted_at IS NULL
	AND u.account_status = 'active'
	AND u.role IN ('super_admin', 'admin', 'viewer', 'support_admin', 'content_admin', 'billing_admin', 'analyst')`, s.placeholder(1), s.placeholder(2))
	var user User
	var rawCreatedAt string
	if err := s.db.QueryRowContext(ctx, query, tokenHash, now).Scan(&user.ID, &user.Email, &user.PasswordHash, &user.Role, &rawCreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, nil
		}
		return User{}, err
	}
	user.CreatedAt = parseSQLTime(rawCreatedAt)
	return user, nil
}

func (s SQLAuthStore) revokeSession(ctx context.Context, tokenHash string, now time.Time) error {
	query := fmt.Sprintf("UPDATE user_sessions SET revoked_at = %s WHERE token_hash = %s", s.placeholder(1), s.placeholder(2))
	_, err := s.db.ExecContext(ctx, query, now, tokenHash)
	return err
}

func (s SQLAuthStore) authorize(ctx context.Context, userID int64, permission Permission, now time.Time) (bool, error) {
	if s.db == nil || userID < 1 || strings.TrimSpace(string(permission)) == "" {
		return false, nil
	}
	query := fmt.Sprintf(`
SELECT EXISTS (
  SELECT 1
  FROM principal_role_assignments pra
  JOIN role_permissions rp ON rp.role_key = pra.role_key
  WHERE pra.principal_type = 'HUMAN_ADMIN'
    AND pra.principal_id = CAST(%s AS TEXT)
    AND pra.revoked_at IS NULL
    AND pra.valid_from <= %s
    AND (pra.valid_until IS NULL OR pra.valid_until > %s)
    AND pra.scope_type = 'GLOBAL'
    AND rp.permission_key = %s
)`, s.placeholder(1), s.placeholder(2), s.placeholder(3), s.placeholder(4))
	var allowed bool
	if err := s.db.QueryRowContext(ctx, query, strconv.FormatInt(userID, 10), now, now, string(permission)).Scan(&allowed); err != nil {
		return false, err
	}
	return allowed, nil
}

func (s SQLAuthStore) placeholder(position int) string {
	if s.dialect == DialectSQLite {
		return "?"
	}
	return fmt.Sprintf("$%d", position)
}

func (s SQLAuthStore) currentTimestamp() string {
	if s.dialect == DialectSQLite {
		return "CURRENT_TIMESTAMP"
	}
	return "now()"
}

func newSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func hashSessionToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func parseSQLTime(raw string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999-07", "2006-01-02 15:04:05"} {
		parsed, err := time.Parse(layout, raw)
		if err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

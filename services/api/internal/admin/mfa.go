package admin

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // TOTP interoperability requires HMAC-SHA1 by RFC 6238.
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrMFARequired        = errors.New("admin MFA is required")
	ErrInvalidMFACode     = errors.New("invalid admin MFA code")
	ErrAdminAlreadyExists = errors.New("admin account already exists")
)

type MFAAuthService struct {
	base          AuthService
	encryptionKey [32]byte
}

func NewMFAAuthService(store SQLAuthStore, ttl time.Duration, encryptionSecret string) MFAAuthService {
	return MFAAuthService{
		base:          NewAuthService(store, ttl),
		encryptionKey: sha256.Sum256([]byte(encryptionSecret)),
	}
}

// Login deliberately refuses password-only authentication. It exists so the
// service can share the session-validation interface with legacy admin routes.
func (s MFAAuthService) Login(context.Context, string, string) (LoginResult, error) {
	return LoginResult{}, ErrMFARequired
}

func (s MFAAuthService) LoginWithMFA(ctx context.Context, email, password, code string) (LoginResult, error) {
	if s.base.ttl <= 0 {
		return LoginResult{}, errors.New("admin session TTL must be positive")
	}
	user, err := s.base.store.findActiveAdminByEmail(ctx, email)
	if err != nil {
		return LoginResult{}, err
	}
	if user.ID == 0 || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return LoginResult{}, ErrInvalidCredentials
	}
	ciphertext, enabled, err := s.base.store.findEnabledTOTP(ctx, user.ID)
	if err != nil {
		return LoginResult{}, err
	}
	if !enabled {
		return LoginResult{}, ErrMFARequired
	}
	secret, err := decryptMFASecret(s.encryptionKey, ciphertext)
	if err != nil {
		return LoginResult{}, err
	}
	if !validateTOTP(secret, code, s.base.now()) {
		return LoginResult{}, ErrInvalidMFACode
	}
	token, err := newSessionToken()
	if err != nil {
		return LoginResult{}, err
	}
	expiresAt := s.base.now().Add(s.base.ttl)
	if err := s.base.store.insertSession(ctx, user.ID, hashSessionToken(token), expiresAt); err != nil {
		return LoginResult{}, err
	}
	user.PasswordHash = ""
	return LoginResult{Token: token, ExpiresAt: expiresAt, User: user}, nil
}

func (s MFAAuthService) ValidateSession(ctx context.Context, token string) (User, error) {
	return s.base.ValidateSession(ctx, token)
}

func (s MFAAuthService) Logout(ctx context.Context, token string) error {
	return s.base.Logout(ctx, token)
}

func (s MFAAuthService) Authorize(ctx context.Context, userID int64, permission Permission) (bool, error) {
	return s.base.Authorize(ctx, userID, permission)
}

func (s SQLAuthStore) findEnabledTOTP(ctx context.Context, userID int64) ([]byte, bool, error) {
	query := fmt.Sprintf(`
SELECT secret_ciphertext
FROM user_mfa_methods
WHERE user_id = %s AND method = 'totp' AND enabled_at IS NOT NULL`, s.placeholder(1))
	var ciphertext []byte
	if err := s.db.QueryRowContext(ctx, query, userID).Scan(&ciphertext); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return ciphertext, true, nil
}

func GenerateTOTPSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

func encryptMFASecret(key [32]byte, secret string) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(secret), nil), nil
}

func decryptMFASecret(key [32]byte, ciphertext []byte) (string, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return "", errors.New("invalid MFA secret ciphertext")
	}
	plain, err := gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func validateTOTP(secret, rawCode string, now time.Time) bool {
	code := strings.TrimSpace(rawCode)
	if len(code) != 6 {
		return false
	}
	if _, err := strconv.Atoi(code); err != nil {
		return false
	}
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return false
	}
	counter := now.UTC().Unix() / 30
	for delta := int64(-1); delta <= 1; delta++ {
		if hmac.Equal([]byte(totpCode(decoded, counter+delta)), []byte(code)) {
			return true
		}
	}
	return false
}

func totpCode(secret []byte, counter int64) string {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(counter))
	mac := hmac.New(sha1.New, secret)
	_, _ = mac.Write(buf)
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 0x0f
	value := (uint32(digest[offset])&0x7f)<<24 |
		uint32(digest[offset+1])<<16 |
		uint32(digest[offset+2])<<8 |
		uint32(digest[offset+3])
	return fmt.Sprintf("%06d", value%1_000_000)
}

func ProvisionTOTPAdmin(ctx context.Context, store SQLAuthStore, encryptionSecret, email, password string, role Role) (string, error) {
	normalized, err := normalizeEmail(email)
	if err != nil {
		return "", err
	}
	if len(password) < 14 {
		return "", errors.New("admin password must be at least 14 characters")
	}
	switch role {
	case RoleSuperAdmin, RoleSupportAdmin, RoleContentAdmin, RoleBillingAdmin, RoleAnalyst:
	default:
		return "", errors.New("invalid admin role")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return "", err
	}
	secret, err := GenerateTOTPSecret()
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte(encryptionSecret))
	ciphertext, err := encryptMFASecret(key, secret)
	if err != nil {
		return "", err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var userID int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO users (email, email_normalized, password_hash, role, account_status, email_verified_at, updated_at)
VALUES ($1, $1, $2, $3, 'active', now(), now())
ON CONFLICT (email) DO NOTHING
RETURNING id`, normalized, string(hash), string(role)).Scan(&userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrAdminAlreadyExists
		}
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO user_mfa_methods (user_id, method, secret_ciphertext, enabled_at)
VALUES ($1, 'totp', $2, now())`, userID, ciphertext); err != nil {
		return "", err
	}
	roleKey := administrativeRoleKey(role)
	if roleKey == "" {
		return "", errors.New("admin role has no RBAC mapping")
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO principal_role_assignments
  (principal_type, principal_id, role_key, scope_type, scope_id, created_by)
VALUES ('HUMAN_ADMIN', $1::text, $2, 'GLOBAL', '', $1)
ON CONFLICT DO NOTHING`, userID, roleKey); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO audit_logs (actor_user_id, action, entity_type, entity_id, reason, after_value)
VALUES ($1, 'admin.provisioned', 'user', $2, 'secure CLI provisioning', jsonb_build_object('role', $3::text, 'rbac_role', $4::text))`, userID, fmt.Sprint(userID), string(role), roleKey); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return secret, nil
}

func administrativeRoleKey(role Role) string {
	switch role {
	case RoleSuperAdmin:
		return "platform_owner"
	case RoleAdmin:
		return "platform_admin"
	case RoleSupportAdmin:
		return "support_agent"
	case RoleContentAdmin:
		return "content_editor"
	case RoleBillingAdmin:
		return "billing_admin"
	case RoleAnalyst, RoleViewer:
		return "auditor"
	default:
		return ""
	}
}

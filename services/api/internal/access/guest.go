package access

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidGuestToken = errors.New("invalid or expired guest token")
	ErrViewLimitReached  = errors.New("guest analysis view limit reached")
)

type guestClaims struct {
	Secret    string `json:"s"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

type GuestSigner struct {
	key []byte
}

func NewGuestSigner(key []byte) (*GuestSigner, error) {
	if len(key) < 32 {
		return nil, errors.New("guest signing key must contain at least 32 bytes")
	}
	return &GuestSigner{key: append([]byte(nil), key...)}, nil
}

func (s *GuestSigner) issue(now time.Time, ttl time.Duration) (string, guestClaims, error) {
	secret := make([]byte, 24)
	if _, err := rand.Read(secret); err != nil {
		return "", guestClaims{}, err
	}
	claims := guestClaims{Secret: hex.EncodeToString(secret), IssuedAt: now.Unix(), ExpiresAt: now.Add(ttl).Unix()}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", guestClaims{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + s.signature(encoded), claims, nil
}

func (s *GuestSigner) parse(token string, now time.Time) (guestClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return guestClaims{}, ErrInvalidGuestToken
	}
	expected, err := hex.DecodeString(s.signature(parts[0]))
	if err != nil {
		return guestClaims{}, ErrInvalidGuestToken
	}
	actual, err := hex.DecodeString(parts[1])
	if err != nil || !hmac.Equal(actual, expected) {
		return guestClaims{}, ErrInvalidGuestToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return guestClaims{}, ErrInvalidGuestToken
	}
	var claims guestClaims
	if json.Unmarshal(payload, &claims) != nil || claims.Secret == "" || claims.ExpiresAt <= now.Unix() || claims.IssuedAt > now.Add(time.Minute).Unix() {
		return guestClaims{}, ErrInvalidGuestToken
	}
	return claims, nil
}

func (s *GuestSigner) signature(payload string) string {
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

type GuestSession struct {
	ID        int64
	ExpiresAt time.Time
}

type ViewResult struct {
	Allowed   bool
	Counted   bool
	Count     int
	Remaining int
}

type GuestService struct {
	repo       *SQLGuestRepository
	signer     *GuestSigner
	now        func() time.Time
	sessionTTL time.Duration
	window     time.Duration
	limit      int
}

func NewGuestService(repo *SQLGuestRepository, signer *GuestSigner, limit int) *GuestService {
	if limit < 1 {
		limit = 3
	}
	return &GuestService{repo: repo, signer: signer, now: func() time.Time { return time.Now().UTC() }, sessionTTL: 30 * 24 * time.Hour, window: 24 * time.Hour, limit: limit}
}

func (s *GuestService) ConfigureWindow(window, sessionTTL time.Duration) {
	if window > 0 {
		s.window = window
	}
	if sessionTTL > 0 {
		s.sessionTTL = sessionTTL
	}
}

func (s *GuestService) CreateSession(ctx context.Context, visitorID string) (string, GuestSession, error) {
	visitorID = strings.TrimSpace(visitorID)
	if visitorID == "" {
		return "", GuestSession{}, errors.New("visitor id is required")
	}
	now := s.now()
	token, claims, err := s.signer.issue(now, s.sessionTTL)
	if err != nil {
		return "", GuestSession{}, err
	}
	session, err := s.repo.CreateOrRefreshSession(ctx, digest(visitorID), digest(claims.Secret), now, now.Add(s.sessionTTL))
	return token, session, err
}

func (s *GuestService) Resolve(ctx context.Context, token string) (GuestSession, error) {
	claims, err := s.signer.parse(strings.TrimSpace(token), s.now())
	if err != nil {
		return GuestSession{}, err
	}
	return s.repo.ResolveSession(ctx, digest(claims.Secret), s.now())
}

func (s *GuestService) ObserveAnalysis(ctx context.Context, token string, matchID int64, pageType string) (ViewResult, error) {
	if matchID < 1 {
		return ViewResult{}, errors.New("match id is required")
	}
	pageType = strings.ToLower(strings.TrimSpace(pageType))
	if pageType == "" {
		pageType = "match_analysis"
	}
	if pageType != "match_analysis" && pageType != "match_details" {
		return ViewResult{}, errors.New("unsupported guest analysis page type")
	}
	session, err := s.Resolve(ctx, token)
	if err != nil {
		return ViewResult{}, err
	}
	result, err := s.repo.RecordAnalysisView(ctx, session.ID, matchID, pageType, s.now(), s.window, s.limit)
	if err != nil {
		return ViewResult{}, err
	}
	if !result.Allowed {
		return result, ErrViewLimitReached
	}
	return result, nil
}

// Status returns the current rolling-window allowance without consuming a
// view. It is used by the access bootstrap endpoint and never calls a provider.
func (s *GuestService) Status(ctx context.Context, token string) (GuestSession, ViewResult, error) {
	session, err := s.Resolve(ctx, token)
	if err != nil {
		return GuestSession{}, ViewResult{}, err
	}
	count, err := s.repo.AnalysisViewCount(ctx, session.ID, s.now().Add(-s.window))
	if err != nil {
		return GuestSession{}, ViewResult{}, err
	}
	return session, resultFor(count < s.limit, false, count, s.limit), nil
}

type SQLGuestRepository struct {
	db      *sql.DB
	dialect Dialect
}

func NewSQLGuestRepository(db *sql.DB, dialect Dialect) *SQLGuestRepository {
	return &SQLGuestRepository{db: db, dialect: dialect}
}

func (r *SQLGuestRepository) CreateOrRefreshSession(ctx context.Context, visitorHash, sessionHash string, now, expiresAt time.Time) (GuestSession, error) {
	query := fmt.Sprintf(`INSERT INTO guest_sessions (visitor_hash, session_hash, first_seen_at, last_seen_at, expires_at) VALUES (%s, %s, %s, %s, %s) ON CONFLICT (visitor_hash) DO UPDATE SET session_hash = excluded.session_hash, last_seen_at = excluded.last_seen_at, expires_at = excluded.expires_at RETURNING id, expires_at`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5))
	var session GuestSession
	var expires any
	if err := r.db.QueryRowContext(ctx, query, visitorHash, sessionHash, now, now, expiresAt).Scan(&session.ID, &expires); err != nil {
		return GuestSession{}, err
	}
	session.ExpiresAt, _ = guestScanTime(expires)
	return session, nil
}

func (r *SQLGuestRepository) ResolveSession(ctx context.Context, sessionHash string, now time.Time) (GuestSession, error) {
	query := fmt.Sprintf(`UPDATE guest_sessions SET last_seen_at = %s WHERE session_hash = %s AND expires_at > %s RETURNING id, expires_at`, r.p(1), r.p(2), r.p(3))
	var session GuestSession
	var expires any
	if err := r.db.QueryRowContext(ctx, query, now, sessionHash, now).Scan(&session.ID, &expires); errors.Is(err, sql.ErrNoRows) {
		return GuestSession{}, ErrInvalidGuestToken
	} else if err != nil {
		return GuestSession{}, err
	}
	session.ExpiresAt, _ = guestScanTime(expires)
	return session, nil
}

func (r *SQLGuestRepository) AnalysisViewCount(ctx context.Context, guestSessionID int64, windowStart time.Time) (int, error) {
	query := fmt.Sprintf(`SELECT COUNT(*) FROM guest_analysis_views WHERE guest_session_id = %s AND last_viewed_at >= %s`, r.p(1), r.p(2))
	var count int
	err := r.db.QueryRowContext(ctx, query, guestSessionID, windowStart).Scan(&count)
	return count, err
}

func (r *SQLGuestRepository) RecordAnalysisView(ctx context.Context, guestSessionID, matchID int64, pageType string, now time.Time, window time.Duration, limit int) (ViewResult, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return ViewResult{}, err
	}
	defer tx.Rollback()
	lockQuery := fmt.Sprintf(`SELECT id FROM guest_sessions WHERE id = %s AND expires_at > %s`, r.p(1), r.p(2))
	if r.dialect == DialectPostgres {
		lockQuery += " FOR UPDATE"
	}
	var lockedID int64
	if err := tx.QueryRowContext(ctx, lockQuery, guestSessionID, now).Scan(&lockedID); errors.Is(err, sql.ErrNoRows) {
		return ViewResult{}, ErrInvalidGuestToken
	} else if err != nil {
		return ViewResult{}, err
	}

	windowStart := now.Add(-window)
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM guest_analysis_views WHERE guest_session_id = %s AND last_viewed_at >= %s`, r.p(1), r.p(2))
	var count int
	if err := tx.QueryRowContext(ctx, countQuery, guestSessionID, windowStart).Scan(&count); err != nil {
		return ViewResult{}, err
	}
	existingQuery := fmt.Sprintf(`SELECT last_viewed_at FROM guest_analysis_views WHERE guest_session_id = %s AND match_id = %s AND page_type = %s`, r.p(1), r.p(2), r.p(3))
	var lastViewed any
	err = tx.QueryRowContext(ctx, existingQuery, guestSessionID, matchID, pageType).Scan(&lastViewed)
	if err == nil {
		last, parseErr := guestScanTime(lastViewed)
		if parseErr != nil {
			return ViewResult{}, parseErr
		}
		if !last.Before(windowStart) {
			update := fmt.Sprintf(`UPDATE guest_analysis_views SET last_viewed_at = %s, view_count = view_count + 1 WHERE guest_session_id = %s AND match_id = %s AND page_type = %s`, r.p(1), r.p(2), r.p(3), r.p(4))
			if _, err := tx.ExecContext(ctx, update, now, guestSessionID, matchID, pageType); err != nil {
				return ViewResult{}, err
			}
			if err := tx.Commit(); err != nil {
				return ViewResult{}, err
			}
			return resultFor(true, false, count, limit), nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ViewResult{}, err
	}
	if count >= limit {
		return resultFor(false, false, count, limit), nil
	}
	upsert := fmt.Sprintf(`INSERT INTO guest_analysis_views (guest_session_id, match_id, page_type, first_viewed_at, last_viewed_at, view_count) VALUES (%s, %s, %s, %s, %s, 1) ON CONFLICT (guest_session_id, match_id, page_type) DO UPDATE SET first_viewed_at = excluded.first_viewed_at, last_viewed_at = excluded.last_viewed_at, view_count = 1`, r.p(1), r.p(2), r.p(3), r.p(4), r.p(5))
	if _, err := tx.ExecContext(ctx, upsert, guestSessionID, matchID, pageType, now, now); err != nil {
		return ViewResult{}, err
	}
	count++
	if err := tx.Commit(); err != nil {
		return ViewResult{}, err
	}
	return resultFor(true, true, count, limit), nil
}

func resultFor(allowed, counted bool, count, limit int) ViewResult {
	remaining := limit - count
	if remaining < 0 {
		remaining = 0
	}
	return ViewResult{Allowed: allowed, Counted: counted, Count: count, Remaining: remaining}
}

func (r *SQLGuestRepository) p(position int) string {
	if r.dialect == DialectSQLite {
		return "?"
	}
	return fmt.Sprintf("$%d", position)
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func guestScanTime(value any) (time.Time, error) {
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
		return guestScanTime(string(typed))
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %T", value)
}

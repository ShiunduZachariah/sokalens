package usernotifications

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

var (
	ErrNotFound        = errors.New("notification not found")
	ErrDeliveryDormant = errors.New("notification delivery is dormant during initial launch")
)

type Item struct {
	ID        int64      `json:"id"`
	Category  string     `json:"category"`
	Title     string     `json:"title"`
	Message   string     `json:"message"`
	Href      string     `json:"href,omitempty"`
	Read      bool       `json:"read"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type Preferences struct {
	InAppEnabled     bool `json:"in_app_enabled"`
	FavoriteAlerts   bool `json:"favorite_alerts"`
	KickoffReminders bool `json:"kickoff_reminders"`
	DigestEnabled    bool `json:"digest_enabled"`
}

type Service struct {
	db            *sql.DB
	payloadCipher cipher.AEAD
}

func NewService(db *sql.DB, secret ...string) Service {
	service := Service{db: db}
	if len(secret) > 0 && strings.TrimSpace(secret[0]) != "" {
		key := sha256.Sum256([]byte(secret[0]))
		block, err := aes.NewCipher(key[:])
		if err == nil {
			service.payloadCipher, _ = cipher.NewGCM(block)
		}
	}
	return service
}

func (s Service) List(ctx context.Context, userID int64, limit int) ([]Item, Preferences, error) {
	if limit < 1 || limit > 100 {
		limit = 30
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, category, title, body, COALESCE(action_url,''), read_at, created_at, expires_at FROM notifications WHERE user_id=$1 AND (expires_at IS NULL OR expires_at > now()) ORDER BY created_at DESC, id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, Preferences{}, err
	}
	defer rows.Close()
	items := []Item{}
	for rows.Next() {
		var item Item
		var readAt, expiry sql.NullTime
		if err := rows.Scan(&item.ID, &item.Category, &item.Title, &item.Message, &item.Href, &readAt, &item.CreatedAt, &expiry); err != nil {
			return nil, Preferences{}, err
		}
		item.Read = readAt.Valid
		if expiry.Valid {
			value := expiry.Time.UTC()
			item.ExpiresAt = &value
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, Preferences{}, err
	}
	prefs, err := s.GetPreferences(ctx, userID)
	return items, prefs, err
}

func (s Service) GetPreferences(ctx context.Context, userID int64) (Preferences, error) {
	prefs := Preferences{}
	rows, err := s.db.QueryContext(ctx, `SELECT channel, category, enabled, digest_mode FROM notification_channel_preferences WHERE user_id=$1`, userID)
	if err != nil {
		return Preferences{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var channel, category, digest string
		var enabled bool
		if err := rows.Scan(&channel, &category, &enabled, &digest); err != nil {
			return Preferences{}, err
		}
		switch {
		case channel == "in_app" && category == "all":
			prefs.InAppEnabled = enabled
		case category == "favorites":
			prefs.FavoriteAlerts = enabled
		case category == "kickoff":
			prefs.KickoffReminders = enabled
		case category == "digest":
			prefs.DigestEnabled = enabled && digest != "off"
		}
	}
	return prefs, rows.Err()
}

func (s Service) SavePreferences(ctx context.Context, userID int64, prefs Preferences) error {
	if prefs.InAppEnabled || prefs.FavoriteAlerts || prefs.KickoffReminders || prefs.DigestEnabled {
		return ErrDeliveryDormant
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	values := []struct {
		channel, category string
		enabled           bool
		digest            string
	}{
		{"in_app", "all", prefs.InAppEnabled, "immediate"},
		{"in_app", "favorites", prefs.FavoriteAlerts, "immediate"},
		{"in_app", "kickoff", prefs.KickoffReminders, "immediate"},
		{"email", "digest", prefs.DigestEnabled, map[bool]string{true: "weekly", false: "off"}[prefs.DigestEnabled]},
	}
	for _, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO notification_channel_preferences (user_id, channel, category, enabled, digest_mode) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (user_id,channel,category) DO UPDATE SET enabled=excluded.enabled,digest_mode=excluded.digest_mode,updated_at=now()`, userID, value.channel, value.category, value.enabled, value.digest); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s Service) MarkRead(ctx context.Context, userID, notificationID int64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE notifications SET read_at=COALESCE(read_at,now()) WHERE id=$1 AND user_id=$2`, notificationID, userID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s Service) QueueEmail(ctx context.Context, userID int64, category, dedupeKey, to, subject, body string) error {
	return s.QueueEmailHTML(ctx, userID, category, dedupeKey, to, subject, body, "", "")
}

func (s Service) QueueEmailHTML(ctx context.Context, userID int64, category, dedupeKey, to, subject, textBody, htmlBody, requestID string) error {
	if category != "email_verification" && category != "password_reset" {
		return ErrDeliveryDormant
	}
	payload, err := json.Marshal(map[string]string{"to": strings.TrimSpace(to), "subject": strings.TrimSpace(subject), "text": textBody, "html": htmlBody, "request_id": requestID})
	if err != nil {
		return err
	}
	if strings.TrimSpace(to) == "" || strings.ContainsAny(to+subject, "\r\n") {
		return errors.New("invalid email notification")
	}
	payload, err = s.encryptSensitivePayload(payload)
	if err != nil {
		return err
	}
	payloadPlaceholder := "$4::jsonb"
	if s.db != nil && strings.Contains(strings.ToLower(fmt.Sprintf("%T", s.db.Driver())), "sqlite") {
		payloadPlaceholder = "$4"
	}
	query := fmt.Sprintf(`INSERT INTO notification_outbox (user_id,channel,category,dedupe_key,payload,priority) VALUES ($1,'email',$2,$3,%s,20) ON CONFLICT (dedupe_key) DO NOTHING`, payloadPlaceholder)
	_, err = s.db.ExecContext(ctx, query, userID, category, fmt.Sprintf("%s:%s", category, dedupeKey), string(payload))
	return err
}

func (s Service) encryptSensitivePayload(payload []byte) ([]byte, error) {
	if s.payloadCipher == nil {
		return nil, errors.New("notification payload encryption is not configured")
	}
	nonce := make([]byte, s.payloadCipher.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	ciphertext := s.payloadCipher.Seal(nil, nonce, payload, []byte("sokalens-notification-v1"))
	return json.Marshal(map[string]any{
		"encrypted":  true,
		"version":    1,
		"nonce":      base64.RawStdEncoding.EncodeToString(nonce),
		"ciphertext": base64.RawStdEncoding.EncodeToString(ciphertext),
	})
}

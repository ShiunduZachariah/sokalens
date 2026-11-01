package notifications

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
)

const claimBatchSQL = `
WITH candidates AS (
  SELECT id
  FROM notification_outbox
  WHERE channel = $1
    AND attempts < max_attempts
    AND (
      (status IN ('pending', 'retry') AND available_at <= $2)
      OR (status = 'sending' AND lease_expires_at <= $2)
    )
  ORDER BY priority ASC, available_at ASC, id ASC
  FOR UPDATE SKIP LOCKED
  LIMIT $3
)
UPDATE notification_outbox AS outbox
SET status = 'sending',
    attempts = outbox.attempts + 1,
    lease_token = gen_random_uuid(),
    lease_expires_at = $4,
    last_error = NULL
FROM candidates
WHERE outbox.id = candidates.id
RETURNING outbox.id,
          outbox.user_id,
          outbox.notification_id,
          outbox.channel,
          outbox.category,
          outbox.dedupe_key,
          outbox.payload,
          outbox.priority,
          outbox.attempts,
          outbox.max_attempts,
          outbox.lease_token::text`

const rescheduleSQL = `
UPDATE notification_outbox
SET status = 'pending',
    available_at = $3,
    attempts = GREATEST(attempts - 1, 0),
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error = NULLIF($4, '')
WHERE id = $1
  AND status = 'sending'
  AND lease_token = $2::uuid`

type Repository struct {
	db            *sql.DB
	payloadCipher cipher.AEAD
}

func NewRepository(db *sql.DB) Repository {
	return Repository{db: db}
}

func (r Repository) WithPayloadSecret(secret string) Repository {
	if secret == "" {
		return r
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return r
	}
	r.payloadCipher, _ = cipher.NewGCM(block)
	return r
}

func (r Repository) ClaimBatch(ctx context.Context, opts ClaimOptions) ([]Message, error) {
	if r.db == nil {
		return nil, errors.New("notification outbox DB is nil")
	}
	if opts.Channel == "" {
		return nil, errors.New("notification channel is required")
	}
	if opts.BatchSize <= 0 {
		return nil, errors.New("notification batch size must be positive")
	}
	if opts.LeaseDuration <= 0 {
		return nil, errors.New("notification lease duration must be positive")
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}

	rows, err := r.db.QueryContext(
		ctx,
		claimBatchSQL,
		opts.Channel,
		opts.Now,
		opts.BatchSize,
		opts.Now.Add(opts.LeaseDuration),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := make([]Message, 0, opts.BatchSize)
	for rows.Next() {
		var message Message
		var notificationID sql.NullInt64
		if err := rows.Scan(
			&message.ID,
			&message.UserID,
			&notificationID,
			&message.Channel,
			&message.Category,
			&message.DedupeKey,
			&message.Payload,
			&message.Priority,
			&message.Attempts,
			&message.MaxAttempts,
			&message.LeaseToken,
		); err != nil {
			return nil, err
		}
		if notificationID.Valid {
			id := notificationID.Int64
			message.NotificationID = &id
		}
		decrypted, err := r.decryptPayload(message.Payload)
		if err != nil {
			return nil, fmt.Errorf("decrypt notification payload %d: %w", message.ID, err)
		}
		message.Payload = decrypted
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

func (r Repository) decryptPayload(payload []byte) ([]byte, error) {
	var envelope struct {
		Encrypted  bool   `json:"encrypted"`
		Version    int    `json:"version"`
		Nonce      string `json:"nonce"`
		Ciphertext string `json:"ciphertext"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || !envelope.Encrypted {
		return payload, nil
	}
	if envelope.Version != 1 || r.payloadCipher == nil {
		return nil, errors.New("notification payload encryption key is unavailable")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return nil, errors.New("invalid notification payload nonce")
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return nil, errors.New("invalid notification payload ciphertext")
	}
	plaintext, err := r.payloadCipher.Open(nil, nonce, ciphertext, []byte("sokalens-notification-v1"))
	if err != nil {
		return nil, errors.New("notification payload authentication failed")
	}
	return plaintext, nil
}

func (r Repository) MarkSent(ctx context.Context, message Message, delivery Delivery, now time.Time) error {
	return r.transition(ctx, message, transition{
		status:            "sent",
		providerMessageID: delivery.ProviderMessageID,
		provider:          delivery.Provider,
		responseCode:      delivery.ResponseCode,
		attemptStatus:     "sent",
		now:               now,
	})
}

func (r Repository) MarkRetry(ctx context.Context, message Message, retry Retry, now time.Time) (string, error) {
	status := "retry"
	if message.Attempts >= message.MaxAttempts {
		status = "dead_letter"
	}
	err := r.transition(ctx, message, transition{
		status:        status,
		reason:        retry.Reason,
		errorCategory: retry.ErrorCategory,
		responseCode:  retry.ResponseCode,
		attemptStatus: status,
		availableAt:   retry.AvailableAt,
		now:           now,
	})
	return status, err
}

// Reschedule releases a claimed delivery without counting the claim as a
// delivery attempt. This is used for policy deferrals such as user quiet hours,
// where no transport call has occurred and consuming the retry budget would be
// incorrect.
func (r Repository) Reschedule(ctx context.Context, message Message, availableAt time.Time, reason string, now time.Time) error {
	if r.db == nil {
		return errors.New("notification outbox DB is nil")
	}
	if message.LeaseToken == "" {
		return errors.New("notification lease token is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if availableAt.IsZero() || availableAt.Before(now) {
		availableAt = now
	}
	result, err := r.db.ExecContext(ctx, rescheduleSQL, message.ID, message.LeaseToken, availableAt, reason)
	if err != nil {
		return err
	}
	return requireFencedUpdate(result)
}

func (r Repository) Suppress(ctx context.Context, message Message, reason string, now time.Time) error {
	if r.db == nil {
		return errors.New("notification outbox DB is nil")
	}
	if message.LeaseToken == "" {
		return errors.New("notification lease token is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	result, err := r.db.ExecContext(ctx, `
UPDATE notification_outbox
SET status = 'suppressed',
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error = $3
WHERE id = $1
  AND status = 'sending'
  AND lease_token = $2::uuid`, message.ID, message.LeaseToken, reason)
	if err != nil {
		return err
	}
	return requireFencedUpdate(result)
}

type transition struct {
	status            string
	reason            string
	errorCategory     string
	providerMessageID string
	provider          string
	responseCode      string
	attemptStatus     string
	availableAt       time.Time
	now               time.Time
}

func (r Repository) transition(ctx context.Context, message Message, next transition) error {
	if r.db == nil {
		return errors.New("notification outbox DB is nil")
	}
	if message.LeaseToken == "" {
		return errors.New("notification lease token is required")
	}
	if next.now.IsZero() {
		next.now = time.Now().UTC()
	}
	if next.availableAt.IsZero() {
		next.availableAt = next.now
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var attempt int
	err = tx.QueryRowContext(ctx, `
UPDATE notification_outbox
SET status = $3,
    available_at = CASE WHEN $3 = 'retry' THEN $4 ELSE available_at END,
    lease_token = NULL,
    lease_expires_at = NULL,
    last_error = NULLIF($5, ''),
    sent_at = CASE WHEN $3 = 'sent' THEN $6 ELSE sent_at END
WHERE id = $1
  AND status = 'sending'
  AND lease_token = $2::uuid
RETURNING attempts`,
		message.ID,
		message.LeaseToken,
		next.status,
		next.availableAt,
		next.reason,
		next.now,
	).Scan(&attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO notification_delivery_attempts (
  outbox_id,
  attempt_no,
  provider_message_id,
  provider,
  recipient_domain,
  status,
  response_code,
  error_category,
  error_message,
  failure_code,
  attempted_at
) VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''), NULLIF($5, ''), $6, NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, ''), $10, NULLIF($11, ''))
ON CONFLICT (outbox_id, attempt_no) DO NOTHING`,
		message.ID,
		attempt,
		next.providerMessageID,
		next.provider,
		recipientDomain(message.Payload),
		next.attemptStatus,
		next.responseCode,
		next.errorCategory,
		next.reason,
		next.now,
		next.errorCategory,
	)
	if err != nil {
		return fmt.Errorf("record notification delivery attempt: %w", err)
	}
	return tx.Commit()
}

func recipientDomain(payload []byte) string {
	var email EmailPayload
	if json.Unmarshal(payload, &email) != nil {
		return ""
	}
	address, err := mail.ParseAddress(strings.TrimSpace(email.To))
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.ToLower(address.Address), "@")
	if len(parts) != 2 || parts[1] == "" {
		return ""
	}
	return parts[1]
}

func requireFencedUpdate(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrLeaseLost
	}
	return nil
}

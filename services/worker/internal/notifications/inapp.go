package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const materializeInAppSQL = `
WITH claimed AS (
  SELECT notification_id
  FROM notification_outbox
  WHERE id = $1
    AND status = 'sending'
    AND lease_token = $2::uuid
), created AS (
  INSERT INTO notifications (user_id, category, title, body, action_url, entity_type, entity_id)
  SELECT $3, $4, $5, $6, NULLIF($7, ''), NULLIF($8, ''), NULLIF($9, '')
  FROM claimed
  WHERE claimed.notification_id IS NULL
  RETURNING id
), linked AS (
  UPDATE notification_outbox AS outbox
  SET notification_id = COALESCE(claimed.notification_id, created.id)
  FROM claimed
  LEFT JOIN created ON true
  WHERE outbox.id = $1
    AND outbox.status = 'sending'
    AND outbox.lease_token = $2::uuid
  RETURNING outbox.notification_id
)
SELECT notification_id FROM linked`

// InAppTransport materializes a prepared outbox intent into the user inbox.
// Delivery remains asynchronous and idempotent because the outbox dedupe key is
// unique and each row is leased before this transport runs.
type InAppTransport struct{ DB *sql.DB }

func (t InAppTransport) Send(ctx context.Context, request DeliveryRequest) (Delivery, error) {
	if t.DB == nil {
		return Delivery{}, errors.New("in-app transport DB is nil")
	}
	var payload struct {
		Title      string `json:"title"`
		Body       string `json:"body"`
		Message    string `json:"message"`
		ActionURL  string `json:"action_url"`
		EntityType string `json:"entity_type"`
		EntityID   string `json:"entity_id"`
	}
	if err := json.Unmarshal(request.Message.Payload, &payload); err != nil {
		return Delivery{}, err
	}
	if payload.Body == "" {
		payload.Body = payload.Message
	}
	if strings.TrimSpace(payload.Title) == "" || strings.TrimSpace(payload.Body) == "" {
		return Delivery{}, errors.New("in-app notification title and body are required")
	}
	if request.Message.LeaseToken == "" {
		return Delivery{}, errors.New("in-app notification lease token is required")
	}
	var id int64
	err := t.DB.QueryRowContext(ctx, materializeInAppSQL,
		request.Message.ID, request.Message.LeaseToken, request.Message.UserID, request.Message.Category,
		payload.Title, payload.Body, payload.ActionURL, payload.EntityType, payload.EntityID,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Delivery{}, ErrLeaseLost
	}
	if err != nil {
		return Delivery{}, err
	}
	return Delivery{ProviderMessageID: fmt.Sprintf("in-app:%d", id), ResponseCode: "stored"}, nil
}

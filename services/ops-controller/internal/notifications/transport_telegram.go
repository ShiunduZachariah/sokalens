package notifications

import (
	"context"
	"errors"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
)

// TelegramTransport delivers through the ops bridge's outbound sender.
//
// It is a thin wrapper on purpose. Chunking, HTML escaping, the token bucket
// and the 429 retry_after handling all belong to telegram.RateLimitedSender
// and are not reimplemented here; duplicating any of them would give the
// service two rate limiters that disagree.
//
// This transport is the *primary*, not the special one. Its only privilege is
// being tried first.
type TelegramTransport struct {
	sender telegram.Sender
	chatID int64
}

var _ Transport = (*TelegramTransport)(nil)

// NewTelegramTransport builds the primary transport. chatID is the ops chat
// from OPS_TELEGRAM_CHAT_ID.
func NewTelegramTransport(sender telegram.Sender, chatID int64) (*TelegramTransport, error) {
	if sender == nil {
		return nil, errors.New("notifications: telegram transport requires a sender")
	}
	if chatID == 0 {
		return nil, errors.New("notifications: telegram transport requires a chat id")
	}
	return &TelegramTransport{sender: sender, chatID: chatID}, nil
}

// Name is the ops_notification_outbox.transport value.
func (t *TelegramTransport) Name() string { return TransportTelegram }

// Deliver renders and sends. An error here is what triggers the email
// fallback, so it must never be swallowed: the sender already distinguishes a
// retryable 429 from a fatal 400/403, and both surface as an error.
func (t *TelegramTransport) Deliver(ctx context.Context, n Notification) error {
	return t.sender.Send(ctx, t.chatID, RenderTelegram(n))
}

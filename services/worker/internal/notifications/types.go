package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var ErrLeaseLost = errors.New("notification outbox lease lost")

type Message struct {
	ID             int64
	UserID         int64
	NotificationID *int64
	Channel        string
	Category       string
	DedupeKey      string
	Payload        json.RawMessage
	Priority       int
	Attempts       int
	MaxAttempts    int
	LeaseToken     string
}

type ClaimOptions struct {
	Channel       string
	BatchSize     int
	Now           time.Time
	LeaseDuration time.Duration
}

type DeliveryRequest struct {
	Message        Message
	IdempotencyKey string
}

type Delivery struct {
	Provider          string
	ProviderMessageID string
	ResponseCode      string
}

type Retry struct {
	Reason        string
	ErrorCategory string
	ResponseCode  string
	AvailableAt   time.Time
}

type Store interface {
	ClaimBatch(context.Context, ClaimOptions) ([]Message, error)
	MarkSent(context.Context, Message, Delivery, time.Time) error
	MarkRetry(context.Context, Message, Retry, time.Time) (string, error)
	Reschedule(context.Context, Message, time.Time, string, time.Time) error
	Suppress(context.Context, Message, string, time.Time) error
}

type Transport interface {
	Send(context.Context, DeliveryRequest) (Delivery, error)
}

type GuardDecision struct {
	Allowed    bool
	Reason     string
	DeferUntil time.Time
}

type AccountGuard interface {
	CheckAccount(context.Context, Message) (GuardDecision, error)
}

type PreferenceGuard interface {
	CheckPreference(context.Context, Message) (GuardDecision, error)
}

type EntitlementGuard interface {
	CheckEntitlement(context.Context, Message) (GuardDecision, error)
}

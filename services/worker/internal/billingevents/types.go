package billingevents

import (
	"context"
	"encoding/json"
	"time"
)

type InboxEvent struct {
	ID                int64
	Provider          string
	Environment       string
	ProviderEventID   string
	EventType         string
	OccurredAt        time.Time
	SignatureVerified bool
	Payload           json.RawMessage
	Attempts          int
}

type SubscriptionUpdate struct {
	UserID                 int64               `json:"user_id"`
	PlanKey                string              `json:"plan_key"`
	ProviderSubscriptionID string              `json:"provider_subscription_id"`
	Status                 string              `json:"status"`
	CurrentPeriodStart     *time.Time          `json:"current_period_start"`
	CurrentPeriodEnd       *time.Time          `json:"current_period_end"`
	CancelAtPeriodEnd      bool                `json:"cancel_at_period_end"`
	CancelledAt            *time.Time          `json:"cancelled_at"`
	GraceEndsAt            *time.Time          `json:"grace_ends_at"`
	Revision               string              `json:"revision"`
	EffectiveAt            time.Time           `json:"effective_at"`
	Notification           *NotificationIntent `json:"notification,omitempty"`
	Dispute                *DisputeUpdate      `json:"dispute,omitempty"`
}

type DisputeUpdate struct {
	ProviderDisputeID string     `json:"provider_dispute_id"`
	Status            string     `json:"status"`
	Reason            string     `json:"reason"`
	AmountMinor       *int64     `json:"amount_minor"`
	Currency          string     `json:"currency"`
	OpenedAt          *time.Time `json:"opened_at"`
	ResolvedAt        *time.Time `json:"resolved_at"`
}

type Subscription struct {
	ID                int64
	UserID            int64
	PlanKey           string
	Status            string
	ProviderRevision  string
	ProviderEventTime time.Time
	CurrentPeriodEnd  *time.Time
}

type NotificationIntent struct {
	Channel   string          `json:"channel"`
	Category  string          `json:"category"`
	DedupeKey string          `json:"dedupe_key"`
	Payload   json.RawMessage `json:"payload"`
	Priority  int             `json:"priority"`
}

type Decoder interface {
	Decode(InboxEvent) (SubscriptionUpdate, error)
	CompareRevision(current, incoming string) int
}

type TransactionStore interface {
	ClaimNext(context.Context) (InboxEvent, bool, error)
	LoadSubscriptionForUpdate(context.Context, InboxEvent, string) (Subscription, bool, error)
	UpsertSubscription(context.Context, InboxEvent, SubscriptionUpdate) (Subscription, error)
	AppendSubscriptionEvent(context.Context, InboxEvent, Subscription, SubscriptionUpdate, string) error
	SyncEntitlements(context.Context, Subscription, SubscriptionUpdate, bool, time.Time) error
	RecordDispute(context.Context, InboxEvent, Subscription, DisputeUpdate) error
	EnqueueNotification(context.Context, int64, NotificationIntent) error
	MarkProcessed(context.Context, int64, time.Time) error
	MarkIgnored(context.Context, int64, string, time.Time) error
	MarkFailed(context.Context, int64, string, time.Time) error
}

type Store interface {
	WithinTransaction(context.Context, func(TransactionStore) error) error
}

type ProcessResult struct {
	EventID        int64
	Processed      bool
	Applied        bool
	Ignored        bool
	Failed         bool
	SubscriptionID int64
	Reason         string
}

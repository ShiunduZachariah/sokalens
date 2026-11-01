package reconciliation

import (
	"context"
	"errors"
	"time"
)

type Lifecycle string

const (
	PendingPayment Lifecycle = "pending_payment"
	Active         Lifecycle = "active"
	Grace          Lifecycle = "grace"
	Renewal        Lifecycle = "renewal"
	Refund         Lifecycle = "refund"
	Chargeback     Lifecycle = "chargeback"
	Cancellation   Lifecycle = "cancellation"
	Retry          Lifecycle = "retry"
	Expiry         Lifecycle = "expiry"
)

type Grant struct {
	ID                     int64
	RevokedAt              *time.Time
	ReconciliationRevision string
}

type InternalSnapshot struct {
	SubscriptionID         int64
	ProviderSubscriptionID string
	Status                 string
	CurrentPeriodStart     *time.Time
	CurrentPeriodEnd       *time.Time
	GraceEndsAt            *time.Time
	CancelAtPeriodEnd      bool
	ProviderRevision       string
	ProviderObservedAt     time.Time
	Grants                 []Grant
	NewGrantsFrozen        bool
}

type ProviderSnapshot struct {
	ProviderSubscriptionID string
	Lifecycle              Lifecycle
	Revision               string
	ObservedAt             time.Time
	CurrentPeriodStart     *time.Time
	CurrentPeriodEnd       *time.Time
	GraceEndsAt            *time.Time
}

// ProviderResult represents data fetched before reconciliation. FailureCode is
// typed and contains no provider response body or secret.
type ProviderResult struct {
	ProviderSubscriptionID string
	Snapshot               *ProviderSnapshot
	FailureCode            string
}

type Request struct {
	IdempotencyKey   string
	Provider         string
	Environment      string
	StartedAt        time.Time
	CompletedAt      time.Time
	Now              time.Time
	Internal         []InternalSnapshot
	ProviderResults  []ProviderResult
	ScopeFailureCode string
}

type ItemResult string

const (
	Matched  ItemResult = "matched"
	Mismatch ItemResult = "mismatch"
	Failed   ItemResult = "failed"
)

type Action string

const (
	FreezeNewGrants     Action = "freeze_new_grants"
	UnfreezeNewGrants   Action = "unfreeze_new_grants"
	VerifyGrant         Action = "verify_grant"
	CorrectSubscription Action = "correct_subscription"
	RevokeGrant         Action = "revoke_grant"
)

type Correction struct {
	IdempotencyKey    string
	Action            Action
	SubscriptionID    int64
	GrantID           int64
	TargetStatus      string
	EffectiveAt       time.Time
	ProviderRevision  string
	PeriodStart       *time.Time
	PeriodEnd         *time.Time
	GraceEndsAt       *time.Time
	CancelAtPeriodEnd bool
}

type Item struct {
	SubscriptionID         int64
	ProviderSubscriptionID string
	Classification         string
	Result                 ItemResult
	InternalRevision       string
	ProviderRevision       string
	ProviderObservedAt     *time.Time
	FailureCode            string
	Corrections            []Correction
}

type Run struct {
	IdempotencyKey  string
	Provider        string
	Environment     string
	StartedAt       time.Time
	CompletedAt     time.Time
	Result          string
	Items           []Item
	MatchedCount    int
	MismatchCount   int
	FailedCount     int
	CorrectionCount int
	Applied         bool
}

type Repository interface {
	// Apply atomically records the append-only run/items/corrections and applies
	// only the listed scoped corrections. Replays return the original run.
	Apply(context.Context, Run) (Run, error)
}

var (
	ErrInvalidRequest        = errors.New("reconciliation: invalid request")
	ErrRepositoryUnavailable = errors.New("reconciliation: repository unavailable")
)

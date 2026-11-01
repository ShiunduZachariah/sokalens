package ads

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrNoProviders     = errors.New("no eligible ad providers")
	ErrInvalidToken    = errors.New("invalid ad attempt token")
	ErrDuplicateReport = errors.New("ad attempt outcome already reported")
	ErrStateNotFound   = errors.New("ad session state not found")
)

type Provider struct {
	Code             string          `json:"code"`
	Name             string          `json:"name"`
	AdapterKey       string          `json:"adapter_key"`
	Enabled          bool            `json:"enabled"`
	GlobalPriority   int             `json:"global_priority"`
	TrafficWeight    float64         `json:"traffic_weight"`
	HealthStatus     string          `json:"health_status"`
	LastHealthCheck  *time.Time      `json:"last_health_check,omitempty"`
	FailureThreshold int             `json:"failure_threshold"`
	CooldownSeconds  int             `json:"cooldown_seconds"`
	RequestTimeoutMS int             `json:"request_timeout_ms"`
	PublicConfig     json.RawMessage `json:"public_config"`
}

type ProviderAttempt struct {
	Provider Provider `json:"provider"`
	Token    string   `json:"token"`
}

type ProviderRoute struct {
	SessionID         string            `json:"session_id"`
	PlacementKey      string            `json:"placement_key"`
	PreferredProvider string            `json:"preferred_provider,omitempty"`
	ActiveProvider    string            `json:"active_provider,omitempty"`
	Attempts          []ProviderAttempt `json:"attempts"`
	Hidden            bool              `json:"hidden"`
	Reason            string            `json:"reason,omitempty"`
}

type ResolveRequest struct {
	SessionID    string
	PlacementKey string
	CountryCode  string
}

type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailure Outcome = "failure"
)

type FailureReason string

const (
	FailureNoFill              FailureReason = "NO_FILL"
	FailureScriptError         FailureReason = "SCRIPT_ERROR"
	FailureTimeout             FailureReason = "TIMEOUT"
	FailureNetworkError        FailureReason = "NETWORK_ERROR"
	FailureBlocked             FailureReason = "BLOCKED"
	FailureInvalidZone         FailureReason = "INVALID_ZONE"
	FailureProviderUnavailable FailureReason = "PROVIDER_UNAVAILABLE"
)

func (reason FailureReason) Valid() bool {
	switch reason {
	case FailureNoFill, FailureScriptError, FailureTimeout, FailureNetworkError,
		FailureBlocked, FailureInvalidZone, FailureProviderUnavailable:
		return true
	default:
		return false
	}
}

// TripsCircuit deliberately excludes outcomes that only describe one visitor
// or one inventory decision. Invalid-zone reports are also kept session-local:
// browser reports are untrusted, so configuration health needs a server-side
// check rather than allowing one client to disable a provider globally.
func (reason FailureReason) TripsCircuit() bool {
	switch reason {
	case FailureScriptError, FailureTimeout, FailureNetworkError, FailureProviderUnavailable:
		return true
	default:
		return false
	}
}

type OutcomeReport struct {
	Token         string        `json:"attempt_token"`
	Outcome       Outcome       `json:"outcome"`
	FailureReason FailureReason `json:"failure_reason,omitempty"`
	DurationMS    int           `json:"duration_ms"`
}

type FailedProvider struct {
	Provider   string    `json:"provider"`
	RetryAfter time.Time `json:"retry_after"`
}

type SessionState struct {
	PreferredProvider string           `json:"preferred_provider"`
	ActiveProvider    string           `json:"active_provider"`
	FailedProviders   []FailedProvider `json:"failed_providers"`
	AssignedAt        time.Time        `json:"assigned_at"`
}

type BreakerState struct {
	Provider  string    `json:"provider"`
	OpenUntil time.Time `json:"open_until,omitempty"`
	HalfOpen  bool      `json:"half_open,omitempty"`
}

// StateStore owns only ephemeral routing coordination. PostgreSQL remains the
// source of truth for provider configuration and the append-only outcome log.
type StateStore interface {
	LoadSession(context.Context, string) (SessionState, error)
	CreateSession(context.Context, string, SessionState, time.Duration) (SessionState, error)
	RecordFailure(context.Context, string, string, time.Time, time.Duration) (SessionState, error)
	RecordSuccess(context.Context, string, string, time.Duration) (SessionState, error)
	ClaimReport(context.Context, string, time.Duration) (bool, error)
	ClaimBreakerContribution(context.Context, string, string, time.Duration) (bool, error)
	BreakerStates(context.Context, []string, time.Time) (map[string]BreakerState, error)
	RecordBreakerFailure(context.Context, string, int, time.Duration, time.Time) error
	RecordBreakerSuccess(context.Context, string) error
}

type ProviderRepository interface {
	ListProviders(context.Context) ([]Provider, error)
	RecordOutcome(context.Context, string, string, string, string, Outcome, FailureReason, int, time.Time) error
}

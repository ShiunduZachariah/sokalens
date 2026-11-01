package aicontract

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

type Priority string

const (
	PriorityCritical Priority = "critical"
	PriorityNormal   Priority = "normal"
)

type BudgetPolicy struct {
	MaxTokensPerIncident     int64 `json:"max_tokens_per_incident"`
	MaxCostMicrosPerIncident int64 `json:"max_cost_micros_per_incident"`
	MaxAttemptsPerIncident   int   `json:"max_attempts_per_incident"`
	MaxParallelPerIncident   int   `json:"max_parallel_per_incident"`
	GlobalParallelLimit      int   `json:"global_parallel_limit"`
	ReservedCriticalSlots    int   `json:"reserved_critical_slots"`
}

func (p BudgetPolicy) Validate() error {
	if p.MaxTokensPerIncident <= 0 || p.MaxTokensPerIncident > 10_000_000 {
		return errors.New("max tokens per incident must be between 1 and 10000000")
	}
	if p.MaxCostMicrosPerIncident <= 0 || p.MaxCostMicrosPerIncident > 1_000_000_000 {
		return errors.New("max cost micros per incident must be between 1 and 1000000000")
	}
	if p.MaxAttemptsPerIncident <= 0 || p.MaxAttemptsPerIncident > 100 {
		return errors.New("max attempts per incident must be between 1 and 100")
	}
	if p.MaxParallelPerIncident <= 0 || p.MaxParallelPerIncident > 16 {
		return errors.New("max parallel per incident must be between 1 and 16")
	}
	if p.GlobalParallelLimit <= 0 || p.GlobalParallelLimit > 256 {
		return errors.New("global parallel limit must be between 1 and 256")
	}
	if p.MaxParallelPerIncident > p.GlobalParallelLimit {
		return errors.New("per-incident parallel limit cannot exceed global limit")
	}
	if p.ReservedCriticalSlots < 0 || p.ReservedCriticalSlots >= p.GlobalParallelLimit {
		return errors.New("reserved critical slots must be non-negative and below global limit")
	}
	return nil
}

var (
	ErrBudgetExhausted = errors.New("AI incident budget exhausted")
	ErrConcurrencyFull = errors.New("AI concurrency limit reached")
	ErrUsageAccounting = errors.New("AI provider usage accounting failed")
)

type Usage struct {
	Attempts   int   `json:"attempts"`
	Tokens     int64 `json:"tokens"`
	CostMicros int64 `json:"cost_micros"`
	Active     int   `json:"active"`
}

type Manager struct {
	mu           sync.Mutex
	policy       BudgetPolicy
	usage        map[string]Usage
	globalActive int
	normalActive int
}

// ProviderUsage is the actual aggregate usage for every outbound attempt made
// by one logical provider call. Complete must be true even when every numeric
// value is zero; it distinguishes known-zero usage from missing telemetry.
type ProviderUsage struct {
	Attempts     int
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	CostMicros   int64
	Complete     bool
}

// MeteredProvider exposes retry bounds before dispatch and actual cumulative
// usage afterwards. Providers which retry internally must implement it.
type MeteredProvider interface {
	Provider
	MaxOutboundAttempts() int
	RecommendMetered(context.Context, Evidence) (Recommendation, ProviderUsage, error)
}

type Reservation struct {
	manager   *Manager
	incident  string
	priority  Priority
	reserved  Usage
	once      sync.Once
	resultErr error
}

// Budget is the durable, controller-safe budget boundary. The in-memory Manager
// and PostgreSQL-backed implementations both satisfy it, so callers can select
// restart-durable accounting without depending on a concrete type.
type Budget interface {
	Reserve(incidentID string, priority Priority, attempts int, tokens, costMicros int64) (ReservationHandle, error)
	Usage(incidentID string) Usage
}

// ReservationHandle is an outstanding budget reservation that can be settled
// with actual usage or released while retaining the conservative worst-case
// charge. Implementations must be safe to call at most once per reservation.
type ReservationHandle interface {
	Settle(actual ProviderUsage) error
	Release()
}

func NewBudgetManager(policy BudgetPolicy) (*Manager, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &Manager{policy: policy, usage: make(map[string]Usage)}, nil
}

// Acquire conservatively reserves the estimated tokens and cost at dispatch.
// Reservations remain charged after release so retries cannot evade a budget.
func (m *Manager) Acquire(incidentID string, priority Priority, estimatedTokens, estimatedCostMicros int64) (func(), error) {
	reservation, err := m.Reserve(incidentID, priority, 1, estimatedTokens, estimatedCostMicros)
	if err != nil {
		return nil, err
	}
	return reservation.Release, nil
}

// Reserve atomically charges the maximum permitted outbound attempts and
// token/cost envelope before dispatch. One reservation consumes one active
// concurrency slot regardless of how many retries happen inside the provider.
func (m *Manager) Reserve(incidentID string, priority Priority, attempts int, tokens, costMicros int64) (ReservationHandle, error) {
	if m == nil {
		return nil, errors.New("budget manager is required")
	}
	if strings.TrimSpace(incidentID) == "" {
		return nil, errors.New("incident id is required")
	}
	if priority != PriorityCritical && priority != PriorityNormal {
		return nil, fmt.Errorf("invalid priority %q", priority)
	}
	if attempts <= 0 || tokens <= 0 || costMicros < 0 {
		return nil, errors.New("reserved attempts and tokens must be positive and cost must be non-negative")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	usage := m.usage[incidentID]
	if attempts > m.policy.MaxAttemptsPerIncident-usage.Attempts ||
		tokens > m.policy.MaxTokensPerIncident-usage.Tokens ||
		costMicros > m.policy.MaxCostMicrosPerIncident-usage.CostMicros {
		return nil, ErrBudgetExhausted
	}
	if usage.Active >= m.policy.MaxParallelPerIncident || m.globalActive >= m.policy.GlobalParallelLimit {
		return nil, ErrConcurrencyFull
	}
	if priority == PriorityNormal && m.normalActive >= m.policy.GlobalParallelLimit-m.policy.ReservedCriticalSlots {
		return nil, ErrConcurrencyFull
	}

	usage.Attempts += attempts
	usage.Tokens += tokens
	usage.CostMicros += costMicros
	usage.Active++
	m.usage[incidentID] = usage
	m.globalActive++
	if priority == PriorityNormal {
		m.normalActive++
	}

	return &Reservation{manager: m, incident: incidentID, priority: priority, reserved: Usage{Attempts: attempts, Tokens: tokens, CostMicros: costMicros}}, nil
}

// Settle replaces the conservative reservation with complete actual usage. If
// telemetry is missing, invalid, or outside the reservation, the full
// reservation remains charged and the call fails closed.
func (r *Reservation) Settle(actual ProviderUsage) error {
	if r == nil || r.manager == nil {
		return ErrUsageAccounting
	}
	r.once.Do(func() {
		wellFormed := actual.Complete && actual.Attempts >= 0 &&
			actual.InputTokens >= 0 && actual.OutputTokens >= 0 && actual.TotalTokens >= 0 &&
			actual.InputTokens <= actual.TotalTokens && actual.OutputTokens == actual.TotalTokens-actual.InputTokens &&
			actual.CostMicros >= 0 &&
			(actual.Attempts > 0 || (actual.TotalTokens == 0 && actual.CostMicros == 0))
		withinReservation := wellFormed && actual.Attempts <= r.reserved.Attempts &&
			actual.TotalTokens <= r.reserved.Tokens && actual.CostMicros <= r.reserved.CostMicros
		r.manager.mu.Lock()
		defer r.manager.mu.Unlock()
		current := r.manager.usage[r.incident]
		if withinReservation {
			current.Attempts -= r.reserved.Attempts - actual.Attempts
			current.Tokens -= r.reserved.Tokens - actual.TotalTokens
			current.CostMicros -= r.reserved.CostMicros - actual.CostMicros
		} else {
			r.resultErr = ErrUsageAccounting
			if wellFormed {
				current.Attempts = saturatingAddInt(current.Attempts, positiveDifferenceInt(actual.Attempts, r.reserved.Attempts))
				current.Tokens = saturatingAddInt64(current.Tokens, positiveDifferenceInt64(actual.TotalTokens, r.reserved.Tokens))
				current.CostMicros = saturatingAddInt64(current.CostMicros, positiveDifferenceInt64(actual.CostMicros, r.reserved.CostMicros))
			}
		}
		r.releaseLocked(&current)
	})
	return r.resultErr
}

func positiveDifferenceInt(actual, reserved int) int {
	if actual <= reserved {
		return 0
	}
	return actual - reserved
}

func positiveDifferenceInt64(actual, reserved int64) int64 {
	if actual <= reserved {
		return 0
	}
	return actual - reserved
}

func saturatingAddInt(current, delta int) int {
	maxInt := int(^uint(0) >> 1)
	if delta > maxInt-current {
		return maxInt
	}
	return current + delta
}

func saturatingAddInt64(current, delta int64) int64 {
	const maxInt64 = int64(^uint64(0) >> 1)
	if delta > maxInt64-current {
		return maxInt64
	}
	return current + delta
}

// Release retains the worst-case charge. It is safe and idempotent and is the
// required fallback if a caller cannot produce trustworthy actual usage.
func (r *Reservation) Release() {
	if r == nil || r.manager == nil {
		return
	}
	r.once.Do(func() {
		r.resultErr = ErrUsageAccounting
		r.manager.mu.Lock()
		defer r.manager.mu.Unlock()
		current := r.manager.usage[r.incident]
		r.releaseLocked(&current)
	})
}

func (r *Reservation) releaseLocked(current *Usage) {
	if current.Active > 0 {
		current.Active--
		r.manager.usage[r.incident] = *current
		r.manager.globalActive--
		if r.priority == PriorityNormal {
			r.manager.normalActive--
		}
	}
}

func (m *Manager) Usage(incidentID string) Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.usage[incidentID]
}

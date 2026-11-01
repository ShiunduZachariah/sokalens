// Package canary defines deterministic, side-effect-free release canary policy.
// It deliberately contains no deployment, traffic, incident, or rollback executor.
package canary

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const PolicyVersion = "ops-canary-policy/v1"

const (
	minimumWindow  = time.Minute
	maximumWindow  = 24 * time.Hour
	maximumSamples = 10_000_000
)

var (
	ErrInvalidPolicy      = errors.New("invalid canary policy")
	ErrInvalidObservation = errors.New("invalid canary observation")
)

// Outcome is a recommendation only. AutomaticRollback means a future trusted
// caller must select its typed rollback path; this package cannot execute it.
type Outcome string

const (
	Proceed           Outcome = "proceed"
	Hold              Outcome = "hold"
	AutomaticRollback Outcome = "rollback"
)

type Signal string

const (
	ErrorRate                  Signal = "error_rate"
	LatencyP95Milliseconds     Signal = "latency_p95_milliseconds"
	AuthViolationRate          Signal = "auth_violation_rate"
	WebhookFailureRate         Signal = "webhook_failure_rate"
	ReconciliationMismatchRate Signal = "reconciliation_mismatch_rate"
	DatabaseFailureRate        Signal = "database_failure_rate"
	CPUUtilizationRatio        Signal = "cpu_utilization_ratio"
	MemoryUtilizationRatio     Signal = "memory_utilization_ratio"
	BusinessInvariantRate      Signal = "business_invariant_rate"

	UnauthorizedAdminAccess      Signal = "unauthorized_admin_access_count"
	ExpiredTokenAcceptance       Signal = "expired_token_acceptance_count"
	DuplicatePaymentProcessing   Signal = "duplicate_payment_processing_count"
	UnverifiedEntitlementGrant   Signal = "unverified_entitlement_grant_count"
	UnsupportedActiveEntitlement Signal = "unsupported_active_entitlement_count"
)

var requiredSignals = []Signal{
	ErrorRate, LatencyP95Milliseconds, AuthViolationRate, WebhookFailureRate,
	ReconciliationMismatchRate, DatabaseFailureRate, CPUUtilizationRatio,
	MemoryUtilizationRatio, BusinessInvariantRate, UnauthorizedAdminAccess,
	ExpiredTokenAcceptance, DuplicatePaymentProcessing, UnverifiedEntitlementGrant,
	UnsupportedActiveEntitlement,
}

var hardRollbackSignals = map[Signal]struct{}{
	UnauthorizedAdminAccess: {}, ExpiredTokenAcceptance: {},
	DuplicatePaymentProcessing: {}, UnverifiedEntitlementGrant: {},
	UnsupportedActiveEntitlement: {},
}

type Stage struct {
	TrafficPercent    int
	ObservationWindow time.Duration
	MaximumSignalAge  time.Duration
}

// Threshold applies both an absolute ceiling and a prior-baseline-relative
// ceiling. Candidate must be <= MaxValue and <= baseline*(1+MaxRelativeIncrease).
type Threshold struct {
	Signal              Signal
	Critical            bool
	MinimumSamples      int
	MaxValue            float64
	MaxRelativeIncrease float64
}

type Policy struct {
	Version    string
	Stages     []Stage
	Thresholds []Threshold
}

func (p Policy) Validate() error {
	if p.Version != PolicyVersion {
		return invalidPolicy("version must be %q", PolicyVersion)
	}
	if len(p.Stages) != 3 {
		return invalidPolicy("exactly three stages are required")
	}
	for index, percent := range []int{5, 25, 100} {
		stage := p.Stages[index]
		if stage.TrafficPercent != percent {
			return invalidPolicy("stage %d must use %d percent traffic", index, percent)
		}
		if stage.ObservationWindow < minimumWindow || stage.ObservationWindow > maximumWindow {
			return invalidPolicy("stage %d observation window is out of bounds", index)
		}
		if stage.MaximumSignalAge <= 0 || stage.MaximumSignalAge > stage.ObservationWindow {
			return invalidPolicy("stage %d maximum signal age is out of bounds", index)
		}
	}
	if len(p.Thresholds) != len(requiredSignals) {
		return invalidPolicy("exactly %d signal thresholds are required", len(requiredSignals))
	}
	seen := make(map[Signal]struct{}, len(p.Thresholds))
	for _, threshold := range p.Thresholds {
		if _, duplicate := seen[threshold.Signal]; duplicate {
			return invalidPolicy("duplicate signal %q", threshold.Signal)
		}
		seen[threshold.Signal] = struct{}{}
		if !knownSignal(threshold.Signal) {
			return invalidPolicy("unknown signal %q", threshold.Signal)
		}
		if threshold.MinimumSamples <= 0 || threshold.MinimumSamples > maximumSamples {
			return invalidPolicy("signal %q minimum samples are out of bounds", threshold.Signal)
		}
		if !finiteNonNegative(threshold.MaxValue) || !finiteNonNegative(threshold.MaxRelativeIncrease) {
			return invalidPolicy("signal %q thresholds must be finite and non-negative", threshold.Signal)
		}
		if _, hard := hardRollbackSignals[threshold.Signal]; hard {
			if !threshold.Critical || threshold.MaxValue != 0 || threshold.MaxRelativeIncrease != 0 {
				return invalidPolicy("hard invariant %q must be critical with zero thresholds", threshold.Signal)
			}
		}
	}
	for _, signal := range requiredSignals {
		if _, exists := seen[signal]; !exists {
			return invalidPolicy("required signal %q is missing", signal)
		}
	}
	return nil
}

func knownSignal(candidate Signal) bool {
	for _, signal := range requiredSignals {
		if candidate == signal {
			return true
		}
	}
	return false
}

func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func invalidPolicy(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidPolicy, fmt.Sprintf(format, args...))
}

func boundedReason(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 256 {
		return value[:256]
	}
	return value
}

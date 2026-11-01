package canary

import (
	"errors"
	"math"
	"slices"
	"testing"
	"time"
)

func TestPolicyRequiresExactVersionStagesAndCompleteSignalCatalog(t *testing.T) {
	valid := validPolicy()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}

	tests := map[string]func(*Policy){
		"version":                       func(p *Policy) { p.Version = "ops-canary-policy/v2" },
		"stage count":                   func(p *Policy) { p.Stages = p.Stages[:2] },
		"stage order":                   func(p *Policy) { p.Stages[0].TrafficPercent = 25 },
		"short window":                  func(p *Policy) { p.Stages[0].ObservationWindow = time.Second },
		"long window":                   func(p *Policy) { p.Stages[0].ObservationWindow = 25 * time.Hour },
		"zero freshness":                func(p *Policy) { p.Stages[0].MaximumSignalAge = 0 },
		"freshness beyond window":       func(p *Policy) { p.Stages[0].MaximumSignalAge = 11 * time.Minute },
		"missing signal":                func(p *Policy) { p.Thresholds = p.Thresholds[:len(p.Thresholds)-1] },
		"duplicate signal":              func(p *Policy) { p.Thresholds[1].Signal = p.Thresholds[0].Signal },
		"unknown signal":                func(p *Policy) { p.Thresholds[0].Signal = "unknown" },
		"zero samples":                  func(p *Policy) { p.Thresholds[0].MinimumSamples = 0 },
		"excess samples":                func(p *Policy) { p.Thresholds[0].MinimumSamples = maximumSamples + 1 },
		"negative absolute":             func(p *Policy) { p.Thresholds[0].MaxValue = -1 },
		"nan relative":                  func(p *Policy) { p.Thresholds[0].MaxRelativeIncrease = math.NaN() },
		"infinite absolute":             func(p *Policy) { p.Thresholds[0].MaxValue = math.Inf(1) },
		"hard invariant not critical":   func(p *Policy) { threshold(p, UnauthorizedAdminAccess).Critical = false },
		"hard invariant nonzero max":    func(p *Policy) { threshold(p, ExpiredTokenAcceptance).MaxValue = 1 },
		"hard invariant relative slack": func(p *Policy) { threshold(p, DuplicatePaymentProcessing).MaxRelativeIncrease = .01 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			policy := validPolicy()
			mutate(&policy)
			if err := policy.Validate(); !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("error=%v, want invalid policy", err)
			}
		})
	}
}

func TestHardInvariantCatalogIsClosedAndMandatory(t *testing.T) {
	policy := validPolicy()
	for signal := range hardRollbackSignals {
		configured := threshold(&policy, signal)
		if !configured.Critical || configured.MaxValue != 0 || configured.MaxRelativeIncrease != 0 {
			t.Fatalf("hard invariant %q is not fail-closed: %+v", signal, configured)
		}
	}
	if len(hardRollbackSignals) != 5 || len(requiredSignals) != 14 {
		t.Fatalf("unexpected catalogs: hard=%d required=%d", len(hardRollbackSignals), len(requiredSignals))
	}
}

func TestRequiredSignalsReturnsNoMutablePublicCatalog(t *testing.T) {
	want := append([]Signal(nil), requiredSignals...)
	policy := validPolicy()
	policy.Thresholds[0].Signal = "changed"
	if !slices.Equal(requiredSignals, want) {
		t.Fatal("policy mutation changed package signal catalog")
	}
}

func validPolicy() Policy {
	thresholds := make([]Threshold, 0, len(requiredSignals))
	for _, signal := range requiredSignals {
		item := Threshold{Signal: signal, Critical: true, MinimumSamples: 100, MaxValue: 1, MaxRelativeIncrease: .2}
		if _, hard := hardRollbackSignals[signal]; hard {
			item.MaxValue = 0
			item.MaxRelativeIncrease = 0
		}
		thresholds = append(thresholds, item)
	}
	// A resource warning demonstrates hold without weakening the critical set.
	for index := range thresholds {
		if thresholds[index].Signal == CPUUtilizationRatio {
			thresholds[index].Critical = false
		}
	}
	return Policy{
		Version: PolicyVersion,
		Stages: []Stage{
			{TrafficPercent: 5, ObservationWindow: 10 * time.Minute, MaximumSignalAge: 5 * time.Minute},
			{TrafficPercent: 25, ObservationWindow: 20 * time.Minute, MaximumSignalAge: 5 * time.Minute},
			{TrafficPercent: 100, ObservationWindow: 30 * time.Minute, MaximumSignalAge: 5 * time.Minute},
		},
		Thresholds: thresholds,
	}
}

func threshold(policy *Policy, signal Signal) *Threshold {
	for index := range policy.Thresholds {
		if policy.Thresholds[index].Signal == signal {
			return &policy.Thresholds[index]
		}
	}
	panic("test signal not configured: " + signal)
}

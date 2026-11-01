package canary

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEvaluatePassingEvidenceProceedsAtEveryExactStage(t *testing.T) {
	policy := validPolicy()
	for _, stage := range policy.Stages {
		t.Run(string(rune(stage.TrafficPercent)), func(t *testing.T) {
			observation := validObservation(policy, stage.TrafficPercent)
			decision, err := Evaluate(policy, observation)
			if err != nil || decision.Outcome != Proceed || len(decision.Findings) != 0 {
				t.Fatalf("decision=%+v error=%v", decision, err)
			}
		})
	}
}

func TestIncompleteWindowHoldsWithoutPrematureRollback(t *testing.T) {
	policy := validPolicy()
	observation := validObservation(policy, 5)
	observation.EvaluatedAt = observation.WindowEnd.Add(-time.Nanosecond)
	observation.Signals = nil
	decision, err := Evaluate(policy, observation)
	if err != nil || decision.Outcome != Hold || len(decision.Findings) != 1 {
		t.Fatalf("decision=%+v error=%v", decision, err)
	}
}

func TestCriticalEvidenceFailuresAutomaticallyRollback(t *testing.T) {
	tests := map[string]func(*Observation){
		"missing": func(o *Observation) { delete(o.Signals, ErrorRate) },
		"nan candidate": func(o *Observation) {
			m := o.Signals[ErrorRate]
			m.CandidateValue = math.NaN()
			o.Signals[ErrorRate] = m
		},
		"infinite baseline": func(o *Observation) {
			m := o.Signals[ErrorRate]
			m.BaselineValue = math.Inf(1)
			o.Signals[ErrorRate] = m
		},
		"zero candidate samples": func(o *Observation) { m := o.Signals[ErrorRate]; m.CandidateSamples = 0; o.Signals[ErrorRate] = m },
		"zero baseline samples":  func(o *Observation) { m := o.Signals[ErrorRate]; m.BaselineSamples = 0; o.Signals[ErrorRate] = m },
		"under sample":           func(o *Observation) { m := o.Signals[ErrorRate]; m.CandidateSamples = 99; o.Signals[ErrorRate] = m },
		"stale": func(o *Observation) {
			m := o.Signals[ErrorRate]
			m.ObservedAt = o.WindowStart
			o.Signals[ErrorRate] = m
		},
		"outside window": func(o *Observation) {
			m := o.Signals[ErrorRate]
			m.ObservedAt = o.WindowEnd.Add(time.Second)
			o.Signals[ErrorRate] = m
		},
		"absolute": func(o *Observation) { m := o.Signals[ErrorRate]; m.CandidateValue = 1.01; o.Signals[ErrorRate] = m },
		"relative": func(o *Observation) {
			m := o.Signals[ErrorRate]
			m.CandidateValue = .13
			m.BaselineValue = .1
			o.Signals[ErrorRate] = m
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			policy := validPolicy()
			observation := validObservation(policy, 5)
			mutate(&observation)
			decision, err := Evaluate(policy, observation)
			if err != nil || decision.Outcome != AutomaticRollback || len(decision.Findings) != 1 || !decision.Findings[0].Critical {
				t.Fatalf("decision=%+v error=%v", decision, err)
			}
		})
	}
}

func TestEveryHardBusinessInvariantAutomaticallyRollsBackOnOneViolation(t *testing.T) {
	policy := validPolicy()
	for signal := range hardRollbackSignals {
		t.Run(string(signal), func(t *testing.T) {
			observation := validObservation(policy, 25)
			measurement := observation.Signals[signal]
			measurement.CandidateValue = 1
			observation.Signals[signal] = measurement
			decision, err := Evaluate(policy, observation)
			if err != nil || decision.Outcome != AutomaticRollback || decision.Findings[0].Signal != signal {
				t.Fatalf("decision=%+v error=%v", decision, err)
			}
		})
	}
}

func TestNonCriticalFailureHoldsAndCriticalFailureDominates(t *testing.T) {
	policy := validPolicy()
	observation := validObservation(policy, 100)
	delete(observation.Signals, CPUUtilizationRatio)
	decision, err := Evaluate(policy, observation)
	if err != nil || decision.Outcome != Hold || len(decision.Findings) != 1 || decision.Findings[0].Critical {
		t.Fatalf("hold decision=%+v error=%v", decision, err)
	}
	delete(observation.Signals, ErrorRate)
	decision, err = Evaluate(policy, observation)
	if err != nil || decision.Outcome != AutomaticRollback || len(decision.Findings) != 2 {
		t.Fatalf("dominant decision=%+v error=%v", decision, err)
	}
}

func TestThresholdBoundariesAreInclusiveAndZeroBaselineIsSafe(t *testing.T) {
	policy := validPolicy()
	threshold(&policy, ErrorRate).MaxValue = .12
	observation := validObservation(policy, 5)
	measurement := observation.Signals[ErrorRate]
	measurement.BaselineValue = .1
	measurement.CandidateValue = .12
	observation.Signals[ErrorRate] = measurement
	decision, err := Evaluate(policy, observation)
	if err != nil || decision.Outcome != Proceed {
		t.Fatalf("inclusive boundary decision=%+v error=%v", decision, err)
	}

	measurement = observation.Signals[UnauthorizedAdminAccess]
	measurement.BaselineValue = 0
	measurement.CandidateValue = 0
	observation.Signals[UnauthorizedAdminAccess] = measurement
	decision, err = Evaluate(policy, observation)
	if err != nil || decision.Outcome != Proceed {
		t.Fatalf("zero baseline decision=%+v error=%v", decision, err)
	}
}

func TestInvalidPolicyOrObservationFailsClosed(t *testing.T) {
	policy := validPolicy()
	observation := validObservation(policy, 5)
	policy.Version = "invalid"
	decision, err := Evaluate(policy, observation)
	if !errors.Is(err, ErrInvalidPolicy) || decision.Outcome != AutomaticRollback {
		t.Fatalf("policy decision=%+v error=%v", decision, err)
	}

	policy = validPolicy()
	for name, mutate := range map[string]func(*Observation){
		"unknown stage":           func(o *Observation) { o.StagePercent = 10 },
		"missing timestamp":       func(o *Observation) { o.WindowStart = time.Time{} },
		"wrong duration":          func(o *Observation) { o.WindowStart = o.WindowStart.Add(time.Second) },
		"evaluation before start": func(o *Observation) { o.EvaluatedAt = o.WindowStart.Add(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := validObservation(policy, 5)
			mutate(&candidate)
			decision, err := Evaluate(policy, candidate)
			if !errors.Is(err, ErrInvalidObservation) || decision.Outcome != AutomaticRollback {
				t.Fatalf("decision=%+v error=%v", decision, err)
			}
		})
	}
}

func TestEvaluationIsDeterministicAndDoesNotMutateInputs(t *testing.T) {
	policy := validPolicy()
	observation := validObservation(policy, 25)
	delete(observation.Signals, CPUUtilizationRatio)
	delete(observation.Signals, ErrorRate)
	beforePolicy := clonePolicy(policy)
	beforeSignals := cloneSignals(observation.Signals)
	first, firstErr := Evaluate(policy, observation)
	second, secondErr := Evaluate(policy, observation)
	if firstErr != nil || secondErr != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("non-deterministic decisions: first=%+v second=%+v", first, second)
	}
	if !reflect.DeepEqual(policy, beforePolicy) || !reflect.DeepEqual(observation.Signals, beforeSignals) {
		t.Fatal("evaluation mutated policy or observations")
	}
	if first.Findings[0].Signal != ErrorRate || first.Findings[1].Signal != CPUUtilizationRatio {
		t.Fatalf("findings did not preserve policy order: %+v", first.Findings)
	}
}

func FuzzEvaluateNeverProceedsWithHardInvariantViolation(f *testing.F) {
	f.Add(uint8(0), 1.0)
	f.Add(uint8(4), math.MaxFloat64)
	f.Fuzz(func(t *testing.T, index uint8, value float64) {
		if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
			value = 1
		}
		policy := validPolicy()
		observation := validObservation(policy, 5)
		signals := []Signal{UnauthorizedAdminAccess, ExpiredTokenAcceptance, DuplicatePaymentProcessing, UnverifiedEntitlementGrant, UnsupportedActiveEntitlement}
		signal := signals[int(index)%len(signals)]
		measurement := observation.Signals[signal]
		measurement.CandidateValue = value
		observation.Signals[signal] = measurement
		decision, _ := Evaluate(policy, observation)
		if decision.Outcome == Proceed || decision.Outcome == Hold {
			t.Fatalf("hard invariant %s value %v produced %s", signal, value, decision.Outcome)
		}
	})
}

func FuzzEvaluateMalformedCriticalEvidenceNeverProceeds(f *testing.F) {
	f.Add(0, 100, 0.1)
	f.Add(100, 0, math.NaN())
	f.Fuzz(func(t *testing.T, candidateSamples, baselineSamples int, candidateValue float64) {
		if candidateSamples > 0 && baselineSamples > 0 && !math.IsNaN(candidateValue) && !math.IsInf(candidateValue, 0) && candidateValue >= 0 {
			t.Skip()
		}
		policy := validPolicy()
		observation := validObservation(policy, 5)
		measurement := observation.Signals[ErrorRate]
		measurement.CandidateSamples = candidateSamples
		measurement.BaselineSamples = baselineSamples
		measurement.CandidateValue = candidateValue
		observation.Signals[ErrorRate] = measurement
		decision, _ := Evaluate(policy, observation)
		if decision.Outcome != AutomaticRollback {
			t.Fatalf("malformed critical evidence produced %s", decision.Outcome)
		}
	})
}

func validObservation(policy Policy, percent int) Observation {
	var stage Stage
	for _, candidate := range policy.Stages {
		if candidate.TrafficPercent == percent {
			stage = candidate
		}
	}
	end := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	observation := Observation{
		StagePercent: percent, WindowStart: end.Add(-stage.ObservationWindow),
		WindowEnd: end, EvaluatedAt: end, Signals: make(map[Signal]Measurement, len(requiredSignals)),
	}
	for _, item := range policy.Thresholds {
		value := .1
		if _, hard := hardRollbackSignals[item.Signal]; hard {
			value = 0
		}
		observation.Signals[item.Signal] = Measurement{
			CandidateValue: value, BaselineValue: value, CandidateSamples: item.MinimumSamples,
			BaselineSamples: item.MinimumSamples, ObservedAt: end,
		}
	}
	return observation
}

func clonePolicy(policy Policy) Policy {
	policy.Stages = append([]Stage(nil), policy.Stages...)
	policy.Thresholds = append([]Threshold(nil), policy.Thresholds...)
	return policy
}

func cloneSignals(signals map[Signal]Measurement) map[Signal]Measurement {
	copy := make(map[Signal]Measurement, len(signals))
	for key, value := range signals {
		copy[key] = value
	}
	return copy
}

func TestReasonsAreBounded(t *testing.T) {
	if got := boundedReason(strings.Repeat("x", 300)); len(got) != 256 {
		t.Fatalf("bounded reason length=%d", len(got))
	}
}

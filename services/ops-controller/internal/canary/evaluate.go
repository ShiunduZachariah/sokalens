package canary

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Measurement contains candidate and prior-version evidence for one signal.
// Values are rates, milliseconds, ratios, or counts according to Signal.
type Measurement struct {
	CandidateValue   float64
	BaselineValue    float64
	CandidateSamples int
	BaselineSamples  int
	ObservedAt       time.Time
}

type Observation struct {
	StagePercent int
	WindowStart  time.Time
	WindowEnd    time.Time
	EvaluatedAt  time.Time
	Signals      map[Signal]Measurement
}

type Finding struct {
	Signal   Signal
	Critical bool
	Reason   string
}

type Decision struct {
	Outcome  Outcome
	Stage    int
	Findings []Finding
}

// Evaluate is pure and deterministic. Findings follow policy threshold order,
// and rollback dominates hold when failures of both severities are present.
func Evaluate(policy Policy, observation Observation) (Decision, error) {
	if err := policy.Validate(); err != nil {
		return Decision{Outcome: AutomaticRollback, Stage: observation.StagePercent}, err
	}
	stage, err := selectStage(policy.Stages, observation.StagePercent)
	if err != nil {
		return Decision{Outcome: AutomaticRollback, Stage: observation.StagePercent}, err
	}
	if err := validateWindow(stage, observation); err != nil {
		return Decision{Outcome: AutomaticRollback, Stage: observation.StagePercent}, err
	}
	if observation.EvaluatedAt.Before(observation.WindowEnd) {
		return Decision{Outcome: Hold, Stage: observation.StagePercent, Findings: []Finding{{Reason: "observation window is incomplete"}}}, nil
	}

	decision := Decision{Outcome: Proceed, Stage: observation.StagePercent}
	for _, threshold := range policy.Thresholds {
		measurement, present := observation.Signals[threshold.Signal]
		reason := measurementFailure(stage, observation, threshold, measurement, present)
		if reason == "" {
			continue
		}
		decision.Findings = append(decision.Findings, Finding{
			Signal: threshold.Signal, Critical: threshold.Critical, Reason: boundedReason(reason),
		})
		if threshold.Critical {
			decision.Outcome = AutomaticRollback
		} else if decision.Outcome != AutomaticRollback {
			decision.Outcome = Hold
		}
	}
	return decision, nil
}

func selectStage(stages []Stage, percent int) (Stage, error) {
	for _, stage := range stages {
		if stage.TrafficPercent == percent {
			return stage, nil
		}
	}
	return Stage{}, fmt.Errorf("%w: unknown stage %d", ErrInvalidObservation, percent)
}

func validateWindow(stage Stage, observation Observation) error {
	if observation.WindowStart.IsZero() || observation.WindowEnd.IsZero() || observation.EvaluatedAt.IsZero() {
		return errors.Join(ErrInvalidObservation, errors.New("window timestamps are required"))
	}
	if !observation.WindowEnd.After(observation.WindowStart) || observation.WindowEnd.Sub(observation.WindowStart) != stage.ObservationWindow {
		return errors.Join(ErrInvalidObservation, errors.New("window must exactly match the configured stage duration"))
	}
	if observation.EvaluatedAt.Before(observation.WindowStart) {
		return errors.Join(ErrInvalidObservation, errors.New("evaluation precedes the observation window"))
	}
	return nil
}

func measurementFailure(stage Stage, observation Observation, threshold Threshold, measurement Measurement, present bool) string {
	if !present {
		return "required signal is missing"
	}
	if math.IsNaN(measurement.CandidateValue) || math.IsInf(measurement.CandidateValue, 0) || measurement.CandidateValue < 0 ||
		math.IsNaN(measurement.BaselineValue) || math.IsInf(measurement.BaselineValue, 0) || measurement.BaselineValue < 0 {
		return "signal contains a non-finite or negative value"
	}
	if measurement.CandidateSamples <= 0 || measurement.BaselineSamples <= 0 {
		return "signal has zero required samples"
	}
	if measurement.CandidateSamples < threshold.MinimumSamples || measurement.BaselineSamples < threshold.MinimumSamples {
		return "signal is below its minimum sample requirement"
	}
	if measurement.ObservedAt.IsZero() || measurement.ObservedAt.Before(observation.WindowStart) || measurement.ObservedAt.After(observation.WindowEnd) ||
		observation.EvaluatedAt.Sub(measurement.ObservedAt) > stage.MaximumSignalAge {
		return "signal is missing current-window fresh evidence"
	}
	if measurement.CandidateValue > threshold.MaxValue {
		return "signal exceeds its absolute threshold"
	}
	baselineCeiling := measurement.BaselineValue * (1 + threshold.MaxRelativeIncrease)
	if math.IsInf(baselineCeiling, 0) || math.IsNaN(baselineCeiling) || measurement.CandidateValue > baselineCeiling {
		return "signal exceeds its prior-baseline-relative threshold"
	}
	return ""
}

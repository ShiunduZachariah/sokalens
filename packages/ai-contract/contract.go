package aicontract

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const SchemaVersion = "1"

type EvidenceReference struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Evidence is deliberately bounded and contains references instead of raw log
// or trace bodies. Redaction must happen before a provider is called.
type Evidence struct {
	SchemaVersion string              `json:"schema_version"`
	IncidentID    string              `json:"incident_id"`
	Redacted      bool                `json:"redacted"`
	Summary       string              `json:"summary"`
	References    []EvidenceReference `json:"references,omitempty"`
}

func (e Evidence) Validate() error {
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported evidence schema %q", e.SchemaVersion)
	}
	if strings.TrimSpace(e.IncidentID) == "" {
		return errors.New("incident_id is required")
	}
	if !e.Redacted {
		return errors.New("evidence must be redacted before AI dispatch")
	}
	if len(e.Summary) == 0 || len(e.Summary) > 8_000 {
		return errors.New("summary must contain 1..8000 bytes")
	}
	if len(e.References) > 100 {
		return errors.New("at most 100 evidence references are allowed")
	}
	for _, reference := range e.References {
		if strings.TrimSpace(reference.Kind) == "" || strings.TrimSpace(reference.ID) == "" {
			return errors.New("evidence reference kind and id are required")
		}
	}
	return nil
}

type Operation string

const (
	OperationObserveMore       Operation = "observe_more"
	OperationRecommendRollback Operation = "recommend_rollback"
	OperationRecommendRestart  Operation = "recommend_restart"
	OperationEscalateHuman     Operation = "escalate_human"
)

type Action struct {
	Operation Operation `json:"operation"`
	Target    string    `json:"target"`
	Rationale string    `json:"rationale"`
}

type Recommendation struct {
	SchemaVersion  string   `json:"schema_version"`
	IncidentID     string   `json:"incident_id"`
	Classification string   `json:"classification"`
	Confidence     float64  `json:"confidence"`
	Actions        []Action `json:"actions"`
}

func (r Recommendation) Validate() error {
	if r.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported recommendation schema %q", r.SchemaVersion)
	}
	if strings.TrimSpace(r.IncidentID) == "" || strings.TrimSpace(r.Classification) == "" {
		return errors.New("incident_id and classification are required")
	}
	if r.Confidence < 0 || r.Confidence > 1 {
		return errors.New("confidence must be between 0 and 1")
	}
	if len(r.Actions) == 0 || len(r.Actions) > 10 {
		return errors.New("actions must contain 1..10 typed recommendations")
	}
	for _, action := range r.Actions {
		switch action.Operation {
		case OperationObserveMore, OperationRecommendRollback, OperationRecommendRestart, OperationEscalateHuman:
		default:
			return fmt.Errorf("unsupported operation %q", action.Operation)
		}
		if strings.TrimSpace(action.Target) == "" || strings.TrimSpace(action.Rationale) == "" {
			return errors.New("action target and rationale are required")
		}
	}
	return nil
}

// Provider can recommend typed operations only. It has no execution method and
// therefore cannot directly mutate infrastructure or run arbitrary commands.
type Provider interface {
	Recommend(context.Context, Evidence) (Recommendation, error)
}

// ValidatingProvider enforces both sides of the provider boundary, including
// for future remote implementations.
type ValidatingProvider struct {
	Next Provider
}

func (p ValidatingProvider) Recommend(ctx context.Context, evidence Evidence) (Recommendation, error) {
	if err := evidence.Validate(); err != nil {
		return Recommendation{}, err
	}
	if p.Next == nil {
		return Recommendation{}, errors.New("AI provider is not configured")
	}
	recommendation, err := p.Next.Recommend(ctx, evidence)
	if err != nil {
		return Recommendation{}, err
	}
	if recommendation.IncidentID != evidence.IncidentID {
		return Recommendation{}, errors.New("recommendation incident_id does not match evidence")
	}
	if err := recommendation.Validate(); err != nil {
		return Recommendation{}, err
	}
	return recommendation, nil
}

// MockProvider provides deterministic, zero-network behavior for groundwork
// and contract tests until a paid provider is explicitly configured.
type MockProvider struct {
	Recommendation Recommendation
	Err            error
}

func (p MockProvider) Recommend(_ context.Context, _ Evidence) (Recommendation, error) {
	return p.Recommendation, p.Err
}

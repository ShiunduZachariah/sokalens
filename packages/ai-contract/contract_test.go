package aicontract

import (
	"context"
	"testing"
)

func evidence() Evidence {
	return Evidence{SchemaVersion: SchemaVersion, IncidentID: "inc-1", Redacted: true, Summary: "API latency rose after release rel-1"}
}

func recommendation() Recommendation {
	return Recommendation{
		SchemaVersion:  SchemaVersion,
		IncidentID:     "inc-1",
		Classification: "release_regression",
		Confidence:     0.9,
		Actions:        []Action{{Operation: OperationRecommendRollback, Target: "api", Rationale: "correlated with release rel-1"}},
	}
}

func TestValidatingProviderAcceptsRedactedEvidenceAndTypedOutput(t *testing.T) {
	provider := ValidatingProvider{Next: MockProvider{Recommendation: recommendation()}}
	got, err := provider.Recommend(context.Background(), evidence())
	if err != nil {
		t.Fatal(err)
	}
	if got.Actions[0].Operation != OperationRecommendRollback {
		t.Fatalf("unexpected operation %q", got.Actions[0].Operation)
	}
}

func TestValidatingProviderRejectsUnredactedEvidenceBeforeDispatch(t *testing.T) {
	input := evidence()
	input.Redacted = false
	provider := ValidatingProvider{Next: MockProvider{Recommendation: recommendation()}}
	if _, err := provider.Recommend(context.Background(), input); err == nil {
		t.Fatal("unredacted evidence reached the provider boundary")
	}
}

func TestValidatingProviderRejectsFreeFormOperations(t *testing.T) {
	output := recommendation()
	output.Actions[0].Operation = "shell"
	provider := ValidatingProvider{Next: MockProvider{Recommendation: output}}
	if _, err := provider.Recommend(context.Background(), evidence()); err == nil {
		t.Fatal("unallowlisted operation was accepted")
	}
}

func TestValidatingProviderRejectsCrossIncidentOutput(t *testing.T) {
	output := recommendation()
	output.IncidentID = "inc-other"
	provider := ValidatingProvider{Next: MockProvider{Recommendation: output}}
	if _, err := provider.Recommend(context.Background(), evidence()); err == nil {
		t.Fatal("cross-incident recommendation was accepted")
	}
}

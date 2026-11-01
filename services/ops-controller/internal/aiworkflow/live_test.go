package aiworkflow

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	aicontract "github.com/ShiunduZachariah/sports-prediction-platform/packages/ai-contract"
)

// TestDeepSeekLiveMultiSpecialist is an explicit, paid, manual smoke test. It
// dispatches the four fixed advisory specialists and one synthesis request.
// Normal CI never calls an external AI provider.
func TestDeepSeekLiveMultiSpecialist(t *testing.T) {
	if os.Getenv("DEEPSEEK_LIVE_MULTIAGENT") != "1" {
		t.Skip("set DEEPSEEK_LIVE_MULTIAGENT=1 for the paid multi-specialist smoke test")
	}

	providerConfig, err := aicontract.DeepSeekConfigFromEnv()
	if err != nil {
		t.Fatal("DeepSeek provider configuration is invalid")
	}
	provider, err := aicontract.NewDeepSeekProvider(providerConfig)
	if err != nil {
		t.Fatal("DeepSeek provider construction failed")
	}
	budget, err := aicontract.NewBudgetManager(aicontract.BudgetPolicy{
		MaxTokensPerIncident:     int64(10_000 * provider.MaxOutboundAttempts()),
		MaxCostMicrosPerIncident: 10_000_000,
		MaxAttemptsPerIncident:   5 * provider.MaxOutboundAttempts(),
		MaxParallelPerIncident:   4,
		GlobalParallelLimit:      4,
		ReservedCriticalSlots:    0,
	})
	if err != nil {
		t.Fatal("DeepSeek smoke budget configuration failed")
	}
	workflow, err := New(provider, budget, nil, Config{
		MaxParallel:            4,
		MinimumQuorum:          3,
		IncidentTimeout:        90 * time.Second,
		CallTimeout:            30 * time.Second,
		EstimatedTokensPerCall: 2_000,
		EstimatedCostPerCall:   5_000,
		Priority:               aicontract.PriorityNormal,
	})
	if err != nil {
		t.Fatal("DeepSeek smoke workflow configuration failed")
	}

	evidence := aicontract.Evidence{
		SchemaVersion: aicontract.SchemaVersion,
		IncidentID:    "smoke-incident-001",
		Redacted:      true,
		Summary:       "A read-only football prediction API has elevated latency while error rate remains normal. Produce advisory analysis only; no mutation is authorized.",
	}
	result, err := workflow.Run(context.Background(), Request{
		IdempotencyKey: "manual-deepseek-multi-specialist-v1",
		Evidence:       evidence,
	})
	if err != nil {
		if synthesisEvidence, buildErr := buildSynthesisEvidence(evidence, result.Specialists); buildErr == nil {
			_, detailErr := provider.RecommendWithMetadata(context.Background(), synthesisEvidence)
			var providerErr *aicontract.DeepSeekError
			if errors.As(detailErr, &providerErr) {
				t.Fatalf("DeepSeek multi-specialist smoke failed: kind=%s stage=%s", providerErr.Kind, providerErr.Stage)
			}
		}
		t.Fatalf("DeepSeek multi-specialist smoke failed: %v", err)
	}
	if len(result.Specialists) != len(Roles()) {
		t.Fatalf("got %d specialists, want %d", len(result.Specialists), len(Roles()))
	}
	successes := 0
	for _, specialist := range result.Specialists {
		if specialist.Recommendation != nil && specialist.Error == "" {
			successes++
		}
	}
	if successes < 3 {
		t.Fatalf("got %d successful specialists, want at least 3", successes)
	}
	if err := result.Recommendation.Validate(); err != nil {
		t.Fatal("DeepSeek synthesis returned an invalid recommendation")
	}
	t.Logf("DeepSeek multi-specialist smoke passed: specialists=%d successful=%d synthesis_actions=%d", len(result.Specialists), successes, len(result.Recommendation.Actions))
}

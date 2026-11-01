package aiworkflow

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	aicontract "github.com/ShiunduZachariah/sports-prediction-platform/packages/ai-contract"
)

type providerFunc func(context.Context, aicontract.Evidence) (aicontract.Recommendation, error)

func (f providerFunc) Recommend(ctx context.Context, evidence aicontract.Evidence) (aicontract.Recommendation, error) {
	return f(ctx, evidence)
}

type meteredProvider struct {
	maxAttempts int
	call        func(context.Context, aicontract.Evidence) (aicontract.Recommendation, aicontract.ProviderUsage, error)
}

func (p meteredProvider) Recommend(ctx context.Context, evidence aicontract.Evidence) (aicontract.Recommendation, error) {
	recommendation, _, err := p.call(ctx, evidence)
	return recommendation, err
}
func (p meteredProvider) RecommendMetered(ctx context.Context, evidence aicontract.Evidence) (aicontract.Recommendation, aicontract.ProviderUsage, error) {
	return p.call(ctx, evidence)
}
func (p meteredProvider) MaxOutboundAttempts() int { return p.maxAttempts }

func validEvidence() aicontract.Evidence {
	return aicontract.Evidence{
		SchemaVersion: aicontract.SchemaVersion,
		IncidentID:    "incident-42",
		Redacted:      true,
		Summary:       "API latency rose after release sha256:abc; token=[REDACTED]",
		References: []aicontract.EvidenceReference{
			{Kind: "metric", ID: "metric-1"},
			{Kind: "release", ID: "release-abc"},
		},
	}
}

func recommendation(incidentID, classification string, actions ...aicontract.Action) aicontract.Recommendation {
	if len(actions) == 0 {
		actions = []aicontract.Action{{Operation: aicontract.OperationObserveMore, Target: "api", Rationale: "collect bounded evidence"}}
	}
	return aicontract.Recommendation{
		SchemaVersion:  aicontract.SchemaVersion,
		IncidentID:     incidentID,
		Classification: classification,
		Confidence:     0.8,
		Actions:        actions,
	}
}

func testConfig() Config {
	return Config{
		MaxParallel:            2,
		MinimumQuorum:          3,
		IncidentTimeout:        time.Second,
		CallTimeout:            250 * time.Millisecond,
		EstimatedTokensPerCall: 100,
		EstimatedCostPerCall:   20,
		Priority:               aicontract.PriorityNormal,
	}
}

func testBudget(t *testing.T, attempts int, maxParallel int) *aicontract.Manager {
	t.Helper()
	manager, err := aicontract.NewBudgetManager(aicontract.BudgetPolicy{
		MaxTokensPerIncident:     100_000,
		MaxCostMicrosPerIncident: 100_000,
		MaxAttemptsPerIncident:   attempts,
		MaxParallelPerIncident:   maxParallel,
		GlobalParallelLimit:      maxParallel,
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func roleFrom(evidence aicontract.Evidence) string {
	line, _, _ := strings.Cut(evidence.Summary, "\n")
	return strings.TrimPrefix(line, "specialist_role=")
}

func TestWorkflowUsesFixedRolesWithBoundedParallelismAndDeterministicOrder(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	provider := providerFunc(func(ctx context.Context, evidence aicontract.Evidence) (aicontract.Recommendation, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			seen := maximum.Load()
			if current <= seen || maximum.CompareAndSwap(seen, current) {
				break
			}
		}
		role := roleFrom(evidence)
		if role == "synthesis" {
			return recommendation(evidence.IncidentID, "synthesized",
				aicontract.Action{Operation: aicontract.OperationEscalateHuman, Target: " owner ", Rationale: "z rationale"},
				aicontract.Action{Operation: aicontract.OperationObserveMore, Target: "api", Rationale: "inspect metrics"},
				aicontract.Action{Operation: aicontract.OperationEscalateHuman, Target: "OWNER", Rationale: "a rationale"},
			), nil
		}
		// Reverse completion order without changing returned ordering.
		delay := map[string]time.Duration{"diagnosis": 40, "security": 30, "reliability": 20, "test-plan": 10}[role]
		select {
		case <-time.After(delay * time.Millisecond):
		case <-ctx.Done():
			return aicontract.Recommendation{}, ctx.Err()
		}
		return recommendation(evidence.IncidentID, role), nil
	})

	workflow, err := New(provider, testBudget(t, 5, 2), nil, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	result, err := workflow.Run(context.Background(), Request{IdempotencyKey: "run-1", Evidence: validEvidence()})
	if err != nil {
		t.Fatal(err)
	}
	if maximum.Load() > 2 {
		t.Fatalf("maximum concurrency = %d, want <= 2", maximum.Load())
	}
	wantRoles := Roles()
	for index, got := range result.Specialists {
		if got.Role != wantRoles[index] {
			t.Fatalf("role[%d] = %q, want %q", index, got.Role, wantRoles[index])
		}
	}
	if len(result.Recommendation.Actions) != 2 {
		t.Fatalf("deduplicated actions = %d, want 2", len(result.Recommendation.Actions))
	}
	if result.Recommendation.Actions[0].Operation != aicontract.OperationEscalateHuman ||
		result.Recommendation.Actions[0].Rationale != "a rationale" ||
		result.Recommendation.Actions[1].Operation != aicontract.OperationObserveMore {
		t.Fatalf("actions not canonical or deterministically deduplicated: %+v", result.Recommendation.Actions)
	}
}

func TestWorkflowCallTimeoutPreservesFailureAndAllowsQuorum(t *testing.T) {
	provider := providerFunc(func(ctx context.Context, evidence aicontract.Evidence) (aicontract.Recommendation, error) {
		role := roleFrom(evidence)
		if role == "security" {
			<-ctx.Done()
			return aicontract.Recommendation{}, ctx.Err()
		}
		return recommendation(evidence.IncidentID, role), nil
	})
	config := testConfig()
	config.CallTimeout = 15 * time.Millisecond
	workflow, err := New(provider, testBudget(t, 5, 2), nil, config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := workflow.Run(context.Background(), Request{IdempotencyKey: "timeout", Evidence: validEvidence()})
	if err != nil {
		t.Fatal(err)
	}
	security := result.Specialists[1]
	if security.Recommendation != nil || !strings.Contains(security.Error, context.DeadlineExceeded.Error()) {
		t.Fatalf("security result = %+v, want preserved timeout", security)
	}
	if result.Specialists[0].Recommendation == nil || result.Specialists[2].Recommendation == nil || result.Specialists[3].Recommendation == nil {
		t.Fatal("successful specialist evidence was not preserved")
	}
}

func TestWorkflowDeadlineReturnsWhenProviderTemporarilyIgnoresCancellation(t *testing.T) {
	blocked := make(chan struct{})
	provider := providerFunc(func(_ context.Context, evidence aicontract.Evidence) (aicontract.Recommendation, error) {
		<-blocked
		return recommendation(evidence.IncidentID, roleFrom(evidence)), nil
	})
	config := testConfig()
	config.MaxParallel = 1
	config.MinimumQuorum = 1
	config.IncidentTimeout = 25 * time.Millisecond
	config.CallTimeout = 25 * time.Millisecond
	budget := testBudget(t, 5, 1)
	workflow, err := New(provider, budget, nil, config)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = workflow.Run(context.Background(), Request{IdempotencyKey: "hard-timeout", Evidence: validEvidence()})
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrQuorumNotMet) {
		t.Fatalf("error = %v, want deadline and quorum errors", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("workflow exceeded bounded incident timeout: %s", elapsed)
	}
	if got := budget.Usage(validEvidence().IncidentID).Active; got != 1 {
		t.Fatalf("active reservation = %d, want provider retained until it returns", got)
	}
	close(blocked)
	deadline := time.Now().Add(time.Second)
	for budget.Usage(validEvidence().IncidentID).Active != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := budget.Usage(validEvidence().IncidentID).Active; got != 0 {
		t.Fatalf("active reservation after provider return = %d, want 0", got)
	}
}

func TestWorkflowBudgetExhaustionFailsClosedAtQuorum(t *testing.T) {
	provider := providerFunc(func(_ context.Context, evidence aicontract.Evidence) (aicontract.Recommendation, error) {
		return recommendation(evidence.IncidentID, roleFrom(evidence)), nil
	})
	workflow, err := New(provider, testBudget(t, 2, 2), nil, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	result, err := workflow.Run(context.Background(), Request{IdempotencyKey: "budget", Evidence: validEvidence()})
	if !errors.Is(err, ErrQuorumNotMet) {
		t.Fatalf("error = %v, want quorum error", err)
	}
	failures := 0
	for _, specialist := range result.Specialists {
		if strings.Contains(specialist.Error, aicontract.ErrBudgetExhausted.Error()) {
			failures++
		}
	}
	if failures != 2 {
		t.Fatalf("budget failures = %d, want 2; results=%+v", failures, result.Specialists)
	}
}

func TestWorkflowReservesWorstCaseRetriesAndReconcilesActualUsage(t *testing.T) {
	provider := meteredProvider{maxAttempts: 3, call: func(_ context.Context, evidence aicontract.Evidence) (aicontract.Recommendation, aicontract.ProviderUsage, error) {
		return recommendation(evidence.IncidentID, roleFrom(evidence)), aicontract.ProviderUsage{
			Attempts: 2, InputTokens: 20, OutputTokens: 10, TotalTokens: 30, CostMicros: 6, Complete: true,
		}, nil
	}}
	manager, err := aicontract.NewBudgetManager(aicontract.BudgetPolicy{
		MaxTokensPerIncident: 500, MaxCostMicrosPerIncident: 100, MaxAttemptsPerIncident: 10,
		MaxParallelPerIncident: 1, GlobalParallelLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	config := testConfig()
	config.MinimumQuorum = 1
	config.MaxParallel = 1
	workflow, err := New(provider, manager, nil, config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = workflow.Run(context.Background(), Request{IdempotencyKey: "metered", Evidence: validEvidence()})
	if !errors.Is(err, aicontract.ErrBudgetExhausted) { // four actual two-attempt calls fit; a worst-case three-attempt synthesis does not
		t.Fatalf("error = %v, want worst-case synthesis reservation failure", err)
	}
	if got := manager.Usage(validEvidence().IncidentID); got.Attempts > 10 || got.Tokens > 500 || got.CostMicros > 100 || got.Active != 0 {
		t.Fatalf("usage escaped policy: %+v", got)
	}
}

func TestWorkflowUnknownMeteredUsageFailsClosedAndRetainsReservation(t *testing.T) {
	provider := meteredProvider{maxAttempts: 2, call: func(_ context.Context, evidence aicontract.Evidence) (aicontract.Recommendation, aicontract.ProviderUsage, error) {
		return recommendation(evidence.IncidentID, roleFrom(evidence)), aicontract.ProviderUsage{}, nil
	}}
	manager := testBudget(t, 10, 2)
	config := testConfig()
	config.MinimumQuorum = 1
	workflow, err := New(provider, manager, nil, config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = workflow.Run(context.Background(), Request{IdempotencyKey: "unknown-usage", Evidence: validEvidence()})
	if !errors.Is(err, ErrQuorumNotMet) {
		t.Fatalf("error = %v, want quorum failure", err)
	}
	got := manager.Usage(validEvidence().IncidentID)
	if got.Attempts != 8 || got.Tokens != 800 || got.CostMicros != 160 || got.Active != 0 {
		t.Fatalf("missing usage did not retain four worst-case reservations: %+v", got)
	}
}

func TestWorkflowRejectsWorstCaseReservationBeforeProviderDispatch(t *testing.T) {
	var calls atomic.Int32
	provider := meteredProvider{maxAttempts: 3, call: func(_ context.Context, evidence aicontract.Evidence) (aicontract.Recommendation, aicontract.ProviderUsage, error) {
		calls.Add(1)
		return recommendation(evidence.IncidentID, roleFrom(evidence)), aicontract.ProviderUsage{Complete: true}, nil
	}}
	manager, err := aicontract.NewBudgetManager(aicontract.BudgetPolicy{
		MaxTokensPerIncident: 250, MaxCostMicrosPerIncident: 1_000, MaxAttemptsPerIncident: 10,
		MaxParallelPerIncident: 1, GlobalParallelLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	config := testConfig()
	config.MaxParallel = 1
	config.MinimumQuorum = 1
	workflow, err := New(provider, manager, nil, config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = workflow.Run(context.Background(), Request{IdempotencyKey: "reserve-first", Evidence: validEvidence()})
	if !errors.Is(err, ErrQuorumNotMet) || calls.Load() != 0 {
		t.Fatalf("error=%v calls=%d, want pre-dispatch budget rejection", err, calls.Load())
	}
}

func TestWorkflowRejectsUnboundedMeteredProvider(t *testing.T) {
	provider := meteredProvider{maxAttempts: 101, call: func(context.Context, aicontract.Evidence) (aicontract.Recommendation, aicontract.ProviderUsage, error) {
		return aicontract.Recommendation{}, aicontract.ProviderUsage{}, nil
	}}
	_, err := New(provider, testBudget(t, 5, 2), nil, testConfig())
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want invalid config", err)
	}
}

func TestWorkflowRejectsMalformedProviderResponse(t *testing.T) {
	provider := providerFunc(func(_ context.Context, evidence aicontract.Evidence) (aicontract.Recommendation, error) {
		return aicontract.Recommendation{SchemaVersion: "bad", IncidentID: evidence.IncidentID}, nil
	})
	workflow, err := New(provider, testBudget(t, 5, 2), nil, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	_, err = workflow.Run(context.Background(), Request{IdempotencyKey: "malformed", Evidence: validEvidence()})
	if !errors.Is(err, ErrQuorumNotMet) {
		t.Fatalf("error = %v, want quorum error", err)
	}
}

func TestWorkflowRejectsMalformedSynthesisWithoutLeakingProviderDetails(t *testing.T) {
	provider := providerFunc(func(_ context.Context, evidence aicontract.Evidence) (aicontract.Recommendation, error) {
		if roleFrom(evidence) == "synthesis" {
			return recommendation(evidence.IncidentID, "malformed", aicontract.Action{
				Operation: "authorization=super-secret-value", Target: "api", Rationale: "invalid operation",
			}), nil
		}
		return recommendation(evidence.IncidentID, roleFrom(evidence)), nil
	})
	workflow, err := New(provider, testBudget(t, 5, 2), nil, testConfig())
	if err != nil {
		t.Fatal(err)
	}
	result, err := workflow.Run(context.Background(), Request{IdempotencyKey: "bad-synthesis", Evidence: validEvidence()})
	if err == nil || strings.Contains(err.Error(), "super-secret-value") || strings.Contains(err.Error(), "authorization=") {
		t.Fatalf("unsafe synthesis error = %v", err)
	}
	if len(result.Specialists) != len(specialistRoles) {
		t.Fatalf("specialist evidence lost on synthesis failure: %+v", result.Specialists)
	}
}

func TestWorkflowCancelsQueuedAndActiveCalls(t *testing.T) {
	var cancelled atomic.Int32
	provider := providerFunc(func(ctx context.Context, _ aicontract.Evidence) (aicontract.Recommendation, error) {
		<-ctx.Done()
		cancelled.Add(1)
		return aicontract.Recommendation{}, ctx.Err()
	})
	config := testConfig()
	config.MaxParallel = 1
	config.MinimumQuorum = 1
	config.CallTimeout = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	workflow, err := New(provider, testBudget(t, 5, 1), nil, config)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, runErr := workflow.Run(ctx, Request{IdempotencyKey: "cancel", Evidence: validEvidence()})
		done <- runErr
	}()
	time.Sleep(15 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrQuorumNotMet) {
			t.Fatalf("error = %v, want quorum error", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("workflow did not propagate cancellation")
	}
	deadline := time.Now().Add(time.Second)
	for cancelled.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if cancelled.Load() != 1 {
		t.Fatalf("active provider cancellations = %d, want 1", cancelled.Load())
	}
}

func TestWorkflowCopiesRedactedEvidenceForEveryProviderCall(t *testing.T) {
	var mu sync.Mutex
	seen := make(map[string][]aicontract.EvidenceReference)
	provider := providerFunc(func(_ context.Context, evidence aicontract.Evidence) (aicontract.Recommendation, error) {
		role := roleFrom(evidence)
		mu.Lock()
		seen[role] = append([]aicontract.EvidenceReference(nil), evidence.References...)
		mu.Unlock()
		if role != "synthesis" {
			evidence.References[0].ID = "provider-mutated"
		}
		if !evidence.Redacted || strings.Contains(evidence.Summary, "secret-value") {
			t.Error("unredacted evidence reached provider")
		}
		return recommendation(evidence.IncidentID, role), nil
	})
	evidence := validEvidence()
	workflow, err := New(provider, testBudget(t, 5, 4), nil, Config{
		MaxParallel: 4, MinimumQuorum: 4, IncidentTimeout: time.Second, CallTimeout: time.Second,
		EstimatedTokensPerCall: 100, EstimatedCostPerCall: 1, Priority: aicontract.PriorityNormal,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := workflow.Run(context.Background(), Request{IdempotencyKey: "copies", Evidence: evidence})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.References[0].ID != "metric-1" {
		t.Fatal("provider mutation escaped into caller evidence")
	}
	for _, specialist := range result.Specialists {
		if specialist.Evidence.References[0].ID != "metric-1" {
			t.Fatalf("provider mutation escaped into %s evidence", specialist.Role)
		}
	}
	for _, role := range Roles() {
		if got := seen[string(role)][0].ID; got != "metric-1" {
			t.Fatalf("%s received leaked mutation %q", role, got)
		}
	}
}

func TestWorkflowSynthesisInputIsDeterministicAndPreservesDissent(t *testing.T) {
	var mu sync.Mutex
	var syntheses []string
	provider := providerFunc(func(ctx context.Context, evidence aicontract.Evidence) (aicontract.Recommendation, error) {
		role := roleFrom(evidence)
		if role == "synthesis" {
			mu.Lock()
			syntheses = append(syntheses, evidence.Summary)
			mu.Unlock()
			return recommendation(evidence.IncidentID, "combined"), nil
		}
		if role == "diagnosis" || role == "test-plan" {
			time.Sleep(12 * time.Millisecond)
		}
		classification := "rollback"
		if role == "security" {
			classification = "observe"
		}
		return recommendation(evidence.IncidentID, classification), nil
	})
	for i := 0; i < 2; i++ {
		workflow, err := New(provider, testBudget(t, 5, 4), nil, Config{
			MaxParallel: 4, MinimumQuorum: 4, IncidentTimeout: time.Second, CallTimeout: time.Second,
			EstimatedTokensPerCall: 100, EstimatedCostPerCall: 1, Priority: aicontract.PriorityNormal,
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := workflow.Run(context.Background(), Request{IdempotencyKey: "deterministic", Evidence: validEvidence()})
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, len(result.Specialists))
		for index, specialist := range result.Specialists {
			got[index] = specialist.Recommendation.Classification
		}
		if !reflect.DeepEqual(got, []string{"rollback", "observe", "rollback", "rollback"}) {
			t.Fatalf("dissent lost: %v", got)
		}
	}
	if len(syntheses) != 2 || syntheses[0] != syntheses[1] {
		t.Fatalf("synthesis inputs differ:\n%q\n%q", syntheses[0], syntheses[1])
	}
	positions := make([]int, 0, len(specialistRoles))
	for _, role := range specialistRoles {
		positions = append(positions, strings.Index(syntheses[0], `"role":"`+string(role)+`"`))
	}
	if !sort.IntsAreSorted(positions) || positions[0] < 0 {
		t.Fatalf("roles are not in canonical synthesis order: %v", positions)
	}
}

type memoryReplay struct {
	mu     sync.Mutex
	result Result
	found  bool
	stores int
}

func (m *memoryReplay) Claim(_ context.Context, _, _ string) (ReplayClaim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.found {
		return ReplayClaim{Replayed: true, Result: cloneResult(m.result)}, nil
	}
	return ReplayClaim{Owned: true}, nil
}

func (m *memoryReplay) Complete(_ context.Context, result Result) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.result = cloneResult(result)
	m.found = true
	m.stores++
	return nil
}

func TestWorkflowReplaysCompletedResultWithoutProviderCall(t *testing.T) {
	var calls atomic.Int32
	provider := providerFunc(func(_ context.Context, evidence aicontract.Evidence) (aicontract.Recommendation, error) {
		calls.Add(1)
		return recommendation(evidence.IncidentID, roleFrom(evidence)), nil
	})
	replay := &memoryReplay{}
	workflow, err := New(provider, testBudget(t, 5, 4), replay, Config{
		MaxParallel: 4, MinimumQuorum: 4, IncidentTimeout: time.Second, CallTimeout: time.Second,
		EstimatedTokensPerCall: 100, EstimatedCostPerCall: 1, Priority: aicontract.PriorityNormal,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{IdempotencyKey: "replay", Evidence: validEvidence()}
	first, err := workflow.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := workflow.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 5 || replay.stores != 1 || first.Replayed || !second.Replayed {
		t.Fatalf("calls=%d stores=%d first.replayed=%t second.replayed=%t", calls.Load(), replay.stores, first.Replayed, second.Replayed)
	}
	second.Specialists[0].Evidence.References[0].ID = "caller-mutated"
	third, err := workflow.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if third.Specialists[0].Evidence.References[0].ID != "metric-1" {
		t.Fatal("replay store result was not defensively copied")
	}
}

func TestWorkflowRejectsCorruptReplayWithoutProviderDispatch(t *testing.T) {
	var calls atomic.Int32
	provider := providerFunc(func(_ context.Context, evidence aicontract.Evidence) (aicontract.Recommendation, error) {
		calls.Add(1)
		return recommendation(evidence.IncidentID, roleFrom(evidence)), nil
	})
	replay := &memoryReplay{found: true, result: Result{
		IncidentID:     validEvidence().IncidentID,
		IdempotencyKey: "corrupt",
		Recommendation: recommendation("different-incident", "poisoned"),
	}}
	workflow, err := New(provider, testBudget(t, 5, 4), replay, Config{
		MaxParallel: 4, MinimumQuorum: 4, IncidentTimeout: time.Second, CallTimeout: time.Second,
		EstimatedTokensPerCall: 100, EstimatedCostPerCall: 1, Priority: aicontract.PriorityNormal,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = workflow.Run(context.Background(), Request{IdempotencyKey: "corrupt", Evidence: validEvidence()})
	if !errors.Is(err, ErrInvalidReplay) {
		t.Fatalf("error = %v, want invalid replay", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0 for corrupt replay", calls.Load())
	}
}

func TestNewRejectsUnsafeOrUnboundedConfiguration(t *testing.T) {
	provider := providerFunc(func(context.Context, aicontract.Evidence) (aicontract.Recommendation, error) {
		return aicontract.Recommendation{}, nil
	})
	config := testConfig()
	config.MaxParallel = len(specialistRoles) + 1
	_, err := New(provider, testBudget(t, 5, 4), nil, config)
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want invalid config", err)
	}
	roles := Roles()
	roles[0] = "attacker-selected-role"
	if Roles()[0] != RoleDiagnosis {
		t.Fatal("caller mutated fixed role allowlist")
	}
}

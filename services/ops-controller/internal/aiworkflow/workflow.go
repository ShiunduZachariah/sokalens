// Package aiworkflow coordinates bounded, advisory-only AI analysis.
//
// The workflow deliberately exposes no shell, SQL, deployment, or other
// mutation capability. Providers can return only the typed recommendations
// defined by packages/ai-contract; a separate policy boundary must decide what
// to do with those recommendations.
package aiworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	aicontract "github.com/ShiunduZachariah/sports-prediction-platform/packages/ai-contract"
)

type Role string

const (
	RoleDiagnosis   Role = "diagnosis"
	RoleSecurity    Role = "security"
	RoleReliability Role = "reliability"
	RoleTestPlan    Role = "test-plan"
)

var specialistRoles = [...]Role{
	RoleDiagnosis,
	RoleSecurity,
	RoleReliability,
	RoleTestPlan,
}

var (
	ErrInvalidConfig          = errors.New("invalid AI workflow configuration")
	ErrInvalidRequest         = errors.New("invalid AI workflow request")
	ErrInvalidReplay          = errors.New("invalid AI workflow replay")
	ErrQuorumNotMet           = errors.New("AI specialist quorum not met")
	ErrSynthesisInputTooLarge = errors.New("AI synthesis input exceeds evidence bound")
	ErrReplayClaimed          = errors.New("AI workflow replay already claimed by another controller")
)

// Roles returns a copy of the fixed specialist allowlist in canonical order.
func Roles() []Role {
	roles := make([]Role, len(specialistRoles))
	copy(roles, specialistRoles[:])
	return roles
}

type Config struct {
	MaxParallel            int
	MinimumQuorum          int
	IncidentTimeout        time.Duration
	CallTimeout            time.Duration
	EstimatedTokensPerCall int64
	EstimatedCostPerCall   int64
	Priority               aicontract.Priority
}

func (c Config) validate() error {
	if c.MaxParallel < 1 || c.MaxParallel > len(specialistRoles) {
		return fmt.Errorf("%w: max parallel must be within 1..%d", ErrInvalidConfig, len(specialistRoles))
	}
	if c.MinimumQuorum < 1 || c.MinimumQuorum > len(specialistRoles) {
		return fmt.Errorf("%w: minimum quorum must be within 1..%d", ErrInvalidConfig, len(specialistRoles))
	}
	if c.IncidentTimeout <= 0 || c.CallTimeout <= 0 || c.CallTimeout > c.IncidentTimeout {
		return fmt.Errorf("%w: call timeout must be positive and no greater than incident timeout", ErrInvalidConfig)
	}
	if c.EstimatedTokensPerCall <= 0 || c.EstimatedCostPerCall < 0 {
		return fmt.Errorf("%w: estimated tokens must be positive and estimated cost non-negative", ErrInvalidConfig)
	}
	if c.Priority != aicontract.PriorityCritical && c.Priority != aicontract.PriorityNormal {
		return fmt.Errorf("%w: unsupported priority %q", ErrInvalidConfig, c.Priority)
	}
	return nil
}

type Request struct {
	IdempotencyKey string
	Evidence       aicontract.Evidence
}

type SpecialistResult struct {
	Role           Role
	Evidence       aicontract.Evidence
	Recommendation *aicontract.Recommendation
	Error          string
}

type Result struct {
	IncidentID     string
	IdempotencyKey string
	Specialists    []SpecialistResult
	Recommendation aicontract.Recommendation
	Replayed       bool
}

// ReplayClaim is the outcome of an atomic workflow replay claim. Exactly one of
// Owned and Replayed is true for a successful claim.
type ReplayClaim struct {
	Owned    bool
	Replayed bool
	Result   Result
}

// ReplayStore is the persistence seam for workflow idempotency. Production
// adapters must make Claim atomic across controllers using a unique
// (incident_id, idempotency_key) constraint so exactly one controller runs a
// given work item and the others receive the immutable stored result. No
// provider receives this interface.
type ReplayStore interface {
	Claim(context.Context, string, string) (ReplayClaim, error)
	Complete(context.Context, Result) error
}

type Workflow struct {
	provider aicontract.Provider
	metered  aicontract.MeteredProvider
	budget   aicontract.Budget
	replay   ReplayStore
	config   Config
	slots    chan struct{}
}

func New(provider aicontract.Provider, budget aicontract.Budget, replay ReplayStore, config Config) (*Workflow, error) {
	if provider == nil {
		return nil, fmt.Errorf("%w: provider is required", ErrInvalidConfig)
	}
	if budget == nil {
		return nil, fmt.Errorf("%w: budget manager is required", ErrInvalidConfig)
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	workflow := &Workflow{
		provider: provider,
		budget:   budget,
		replay:   replay,
		config:   config,
		slots:    make(chan struct{}, config.MaxParallel),
	}
	if metered, ok := provider.(aicontract.MeteredProvider); ok {
		if attempts := metered.MaxOutboundAttempts(); attempts < 1 || attempts > 100 {
			return nil, fmt.Errorf("%w: provider outbound attempts must be within 1..100", ErrInvalidConfig)
		}
		workflow.metered = metered
	}
	return workflow, nil
}

func (w *Workflow) Run(ctx context.Context, request Request) (Result, error) {
	if ctx == nil {
		return Result{}, fmt.Errorf("%w: context is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		return Result{}, fmt.Errorf("%w: idempotency key is required", ErrInvalidRequest)
	}
	if err := request.Evidence.Validate(); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if w.replay != nil {
		claim, err := w.replay.Claim(ctx, request.Evidence.IncidentID, request.IdempotencyKey)
		if err != nil {
			return Result{}, fmt.Errorf("claim workflow replay: %w", err)
		}
		if claim.Replayed {
			if err := w.validateReplay(claim.Result, request); err != nil {
				return Result{}, err
			}
			replayed := cloneResult(claim.Result)
			replayed.Replayed = true
			return replayed, nil
		}
		if !claim.Owned {
			return Result{}, fmt.Errorf("%w: incident %q idempotency %q", ErrReplayClaimed, request.Evidence.IncidentID, request.IdempotencyKey)
		}
	}

	incidentCtx, cancel := context.WithTimeout(ctx, w.config.IncidentTimeout)
	defer cancel()

	type completed struct {
		index  int
		result SpecialistResult
	}
	results := make(chan completed, len(specialistRoles))
	for index, role := range specialistRoles {
		go func(index int, role Role) {
			result := w.runSpecialist(incidentCtx, request.Evidence, role)
			results <- completed{index: index, result: result}
		}(index, role)
	}

	ordered := make([]SpecialistResult, len(specialistRoles))
	successes := 0
	for range specialistRoles {
		item := <-results
		ordered[item.index] = item.result
		if item.result.Recommendation != nil {
			successes++
		}
	}
	partial := Result{
		IncidentID:     request.Evidence.IncidentID,
		IdempotencyKey: request.IdempotencyKey,
		Specialists:    ordered,
	}
	if successes < w.config.MinimumQuorum {
		quorumErr := fmt.Errorf("%w: got %d successful specialists, require %d", ErrQuorumNotMet, successes, w.config.MinimumQuorum)
		if incidentCtx.Err() != nil {
			return partial, errors.Join(quorumErr, incidentCtx.Err())
		}
		return partial, quorumErr
	}

	synthesisEvidence, err := buildSynthesisEvidence(request.Evidence, ordered)
	if err != nil {
		return partial, err
	}
	recommendation, err := w.callProvider(incidentCtx, request.Evidence.IncidentID, synthesisEvidence)
	if err != nil {
		return partial, fmt.Errorf("synthesize recommendations: %w", safeCallError(err))
	}
	partial.Recommendation = canonicalize(recommendation)
	if err := partial.Recommendation.Validate(); err != nil {
		return partial, fmt.Errorf("validate canonical synthesis: %w", err)
	}

	if w.replay != nil {
		if err := w.replay.Complete(incidentCtx, cloneResult(partial)); err != nil {
			return partial, fmt.Errorf("complete workflow replay: %w", err)
		}
	}
	return partial, nil
}

func (w *Workflow) validateReplay(stored Result, request Request) error {
	if stored.IncidentID != request.Evidence.IncidentID || stored.IdempotencyKey != request.IdempotencyKey {
		return fmt.Errorf("%w: identity mismatch", ErrInvalidReplay)
	}
	if stored.Recommendation.IncidentID != request.Evidence.IncidentID {
		return fmt.Errorf("%w: synthesis incident mismatch", ErrInvalidReplay)
	}
	if err := stored.Recommendation.Validate(); err != nil {
		return fmt.Errorf("%w: invalid synthesis", ErrInvalidReplay)
	}
	if len(stored.Specialists) != len(specialistRoles) {
		return fmt.Errorf("%w: specialist count mismatch", ErrInvalidReplay)
	}
	successes := 0
	for index, specialist := range stored.Specialists {
		if specialist.Role != specialistRoles[index] || specialist.Evidence.IncidentID != request.Evidence.IncidentID {
			return fmt.Errorf("%w: specialist identity mismatch", ErrInvalidReplay)
		}
		if err := specialist.Evidence.Validate(); err != nil {
			return fmt.Errorf("%w: invalid specialist evidence", ErrInvalidReplay)
		}
		if specialist.Recommendation == nil {
			continue
		}
		if specialist.Recommendation.IncidentID != request.Evidence.IncidentID {
			return fmt.Errorf("%w: specialist recommendation incident mismatch", ErrInvalidReplay)
		}
		if err := specialist.Recommendation.Validate(); err != nil {
			return fmt.Errorf("%w: invalid specialist recommendation", ErrInvalidReplay)
		}
		successes++
	}
	if successes < w.config.MinimumQuorum {
		return fmt.Errorf("%w: stored result is below quorum", ErrInvalidReplay)
	}
	return nil
}

func (w *Workflow) runSpecialist(ctx context.Context, evidence aicontract.Evidence, role Role) SpecialistResult {
	copy := cloneEvidence(evidence)
	copy.Summary = fmt.Sprintf("specialist_role=%s\n%s", role, copy.Summary)
	result := SpecialistResult{Role: role, Evidence: copy}
	recommendation, err := w.callProvider(ctx, evidence.IncidentID, copy)
	if err != nil {
		result.Error = safeCallError(err).Error()
		return result
	}
	result.Recommendation = &recommendation
	return result
}

func safeCallError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, aicontract.ErrBudgetExhausted):
		return aicontract.ErrBudgetExhausted
	case errors.Is(err, aicontract.ErrConcurrencyFull):
		return aicontract.ErrConcurrencyFull
	default:
		return errors.New("AI provider response rejected or unavailable")
	}
}

func (w *Workflow) callProvider(ctx context.Context, incidentID string, evidence aicontract.Evidence) (aicontract.Recommendation, error) {
	if err := ctx.Err(); err != nil {
		return aicontract.Recommendation{}, err
	}
	select {
	case w.slots <- struct{}{}:
	case <-ctx.Done():
		return aicontract.Recommendation{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-w.slots
		return aicontract.Recommendation{}, err
	}

	maxAttempts := 1
	if w.metered != nil {
		maxAttempts = w.metered.MaxOutboundAttempts()
	}
	reservedTokens, ok := multiplyBounded(w.config.EstimatedTokensPerCall, maxAttempts)
	if !ok {
		<-w.slots
		return aicontract.Recommendation{}, aicontract.ErrBudgetExhausted
	}
	reservedCost, ok := multiplyBounded(w.config.EstimatedCostPerCall, maxAttempts)
	if !ok {
		<-w.slots
		return aicontract.Recommendation{}, aicontract.ErrBudgetExhausted
	}
	reservation, err := w.budget.Reserve(incidentID, w.config.Priority, maxAttempts, reservedTokens, reservedCost)
	if err != nil {
		<-w.slots
		return aicontract.Recommendation{}, err
	}

	callCtx, cancel := context.WithTimeout(ctx, w.config.CallTimeout)
	type providerResult struct {
		recommendation aicontract.Recommendation
		usage          aicontract.ProviderUsage
		err            error
	}
	done := make(chan providerResult, 1)
	go func() {
		providerEvidence := cloneEvidence(evidence)
		var recommendation aicontract.Recommendation
		var usage aicontract.ProviderUsage
		var callErr error
		if w.metered != nil {
			recommendation, usage, callErr = w.metered.RecommendMetered(callCtx, providerEvidence)
		} else {
			recommendation, callErr = w.provider.Recommend(callCtx, providerEvidence)
			usage = aicontract.ProviderUsage{Attempts: 1, TotalTokens: reservedTokens, InputTokens: reservedTokens, CostMicros: reservedCost, Complete: true}
		}
		if callErr == nil {
			if usage.Attempts < 1 {
				usage.Complete = false
				callErr = aicontract.ErrUsageAccounting
			}
			if recommendation.IncidentID != providerEvidence.IncidentID {
				callErr = errors.New("recommendation incident_id does not match evidence")
			} else if validationErr := recommendation.Validate(); validationErr != nil {
				callErr = validationErr
			}
		}
		if settlementErr := reservation.Settle(usage); settlementErr != nil {
			callErr = errors.Join(callErr, settlementErr)
		}
		<-w.slots
		done <- providerResult{recommendation: recommendation, usage: usage, err: callErr}
	}()

	select {
	case result := <-done:
		contextErr := callCtx.Err()
		cancel()
		if contextErr != nil {
			return aicontract.Recommendation{}, contextErr
		}
		return result.recommendation, result.err
	case <-callCtx.Done():
		// The provider owns the active reservation and slot until it actually
		// returns. An implementation that ignores cancellation therefore fails
		// closed instead of escaping the global concurrency accounting.
		cancel()
		return aicontract.Recommendation{}, callCtx.Err()
	}
}

func multiplyBounded(value int64, multiplier int) (int64, bool) {
	if value < 0 || multiplier <= 0 || (value > 0 && int64(multiplier) > (1<<63-1)/value) {
		return 0, false
	}
	return value * int64(multiplier), true
}

type synthesisInput struct {
	Role           Role                      `json:"role"`
	Recommendation aicontract.Recommendation `json:"recommendation"`
}

func buildSynthesisEvidence(original aicontract.Evidence, specialists []SpecialistResult) (aicontract.Evidence, error) {
	inputs := make([]synthesisInput, 0, len(specialists))
	for _, specialist := range specialists {
		if specialist.Recommendation != nil {
			inputs = append(inputs, synthesisInput{Role: specialist.Role, Recommendation: *specialist.Recommendation})
		}
	}
	encoded, err := json.Marshal(inputs)
	if err != nil {
		return aicontract.Evidence{}, fmt.Errorf("encode synthesis input: %w", err)
	}
	summary := "specialist_role=synthesis\n" + string(encoded)
	if len(summary) > 8_000 {
		return aicontract.Evidence{}, ErrSynthesisInputTooLarge
	}
	evidence := cloneEvidence(original)
	evidence.Summary = summary
	return evidence, nil
}

func canonicalize(recommendation aicontract.Recommendation) aicontract.Recommendation {
	type keyedAction struct {
		key    string
		action aicontract.Action
	}
	byKey := make(map[string]aicontract.Action, len(recommendation.Actions))
	for _, action := range recommendation.Actions {
		action.Target = strings.TrimSpace(action.Target)
		action.Rationale = strings.TrimSpace(action.Rationale)
		key := string(action.Operation) + "\x00" + strings.ToLower(action.Target)
		current, exists := byKey[key]
		if !exists || action.Rationale < current.Rationale {
			byKey[key] = action
		}
	}
	actions := make([]keyedAction, 0, len(byKey))
	for key, action := range byKey {
		actions = append(actions, keyedAction{key: key, action: action})
	}
	sort.Slice(actions, func(i, j int) bool {
		if actions[i].key != actions[j].key {
			return actions[i].key < actions[j].key
		}
		return actions[i].action.Rationale < actions[j].action.Rationale
	})
	recommendation.Actions = make([]aicontract.Action, len(actions))
	for index, item := range actions {
		recommendation.Actions[index] = item.action
	}
	return recommendation
}

func cloneEvidence(evidence aicontract.Evidence) aicontract.Evidence {
	copy := evidence
	copy.References = append([]aicontract.EvidenceReference(nil), evidence.References...)
	return copy
}

func cloneResult(result Result) Result {
	copy := result
	copy.Specialists = make([]SpecialistResult, len(result.Specialists))
	for index, specialist := range result.Specialists {
		copy.Specialists[index] = specialist
		copy.Specialists[index].Evidence = cloneEvidence(specialist.Evidence)
		if specialist.Recommendation != nil {
			recommendation := *specialist.Recommendation
			recommendation.Actions = append([]aicontract.Action(nil), recommendation.Actions...)
			copy.Specialists[index].Recommendation = &recommendation
		}
	}
	copy.Recommendation.Actions = append([]aicontract.Action(nil), result.Recommendation.Actions...)
	return copy
}

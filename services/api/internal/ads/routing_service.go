package ads

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

const (
	defaultAdSessionTTL = 24 * time.Hour
	defaultAttemptTTL   = 5 * time.Minute
)

type Service struct {
	repository ProviderRepository
	state      StateStore
	signerKey  []byte
	now        func() time.Time
	sessionTTL time.Duration
	attemptTTL time.Duration
}

func NewService(db *sql.DB, state StateStore, signerKey []byte, clock func() time.Time) *Service {
	return NewServiceWithRepository(NewSQLRepository(db), state, signerKey, clock)
}

func NewServiceWithRepository(dbRepository ProviderRepository, state StateStore, signerKey []byte, clock func() time.Time) *Service {
	if clock == nil {
		clock = time.Now
	}
	return &Service{
		repository: dbRepository,
		state:      state,
		signerKey:  append([]byte(nil), signerKey...),
		now:        clock,
		sessionTTL: defaultAdSessionTTL,
		attemptTTL: defaultAttemptTTL,
	}
}

// ListProviders returns the complete PostgreSQL configuration, including
// disabled rows, for the authenticated admin surface. Public Resolve responses
// still receive only enabled and currently eligible providers.
func (service *Service) ListProviders(ctx context.Context) ([]Provider, error) {
	if service == nil || service.repository == nil {
		return nil, errors.New("ads routing unavailable")
	}
	return service.repository.ListProviders(ctx)
}

// Resolve pins a new session using traffic weights, then builds a chain from
// the sticky active provider, the original preference, and global priority.
// Redis failure removes cross-request stickiness but not deterministic initial
// assignment, so an ad-state outage cannot take down the content page.
func (service *Service) Resolve(ctx context.Context, request ResolveRequest) (ProviderRoute, error) {
	if service == nil || service.repository == nil {
		return ProviderRoute{}, errors.New("ads routing unavailable")
	}
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.PlacementKey = strings.TrimSpace(request.PlacementKey)
	if request.SessionID == "" || request.PlacementKey == "" {
		return ProviderRoute{}, errors.New("session and placement are required")
	}

	now := service.now().UTC()
	providers, err := service.repository.ListProviders(ctx)
	if err != nil {
		return ProviderRoute{}, err
	}
	providers = normalizedProviders(providers)
	breakers := service.breakerStates(ctx, providers, now)

	state, found := service.loadSession(ctx, request.SessionID)
	if !found {
		preferred := weightedProvider(providers, request.SessionID, breakers, now)
		if preferred == "" {
			return hiddenRoute(request.SessionID, request.PlacementKey), nil
		}
		state = SessionState{
			PreferredProvider: preferred,
			ActiveProvider:    preferred,
			FailedProviders:   []FailedProvider{},
			AssignedAt:        now,
		}
		if service.state != nil {
			if created, createErr := service.state.CreateSession(ctx, request.SessionID, state, service.sessionTTL); createErr == nil {
				state = created
			}
		}
	}
	return service.buildRoute(request.SessionID, request.PlacementKey, providers, state, breakers, now)
}

// Report validates the signed attempt, records and deduplicates the durable
// outcome in PostgreSQL, updates Redis session stickiness, and returns the route that a
// subsequent slot should use. State-store failures degrade to a deterministic
// local route; database failures remain visible because outcome telemetry is
// part of the routing contract, not a best-effort log.
func (service *Service) Report(ctx context.Context, sessionID string, report OutcomeReport) (ProviderRoute, error) {
	if service == nil || service.repository == nil {
		return ProviderRoute{}, errors.New("ads routing unavailable")
	}
	now := service.now().UTC()
	claims, err := service.verifyAttempt(report.Token, now)
	if err != nil || !hmac.Equal([]byte(claims.SessionID), []byte(strings.TrimSpace(sessionID))) {
		return ProviderRoute{}, ErrInvalidToken
	}
	if report.DurationMS < 0 || report.DurationMS > 60_000 {
		return ProviderRoute{}, errors.New("invalid ad duration")
	}
	if report.Outcome != OutcomeSuccess && report.Outcome != OutcomeFailure {
		return ProviderRoute{}, errors.New("invalid ad outcome")
	}
	if report.Outcome == OutcomeFailure && !report.FailureReason.Valid() {
		return ProviderRoute{}, errors.New("invalid failure reason")
	}
	if report.Outcome == OutcomeSuccess && report.FailureReason != "" {
		return ProviderRoute{}, errors.New("successful outcome cannot have a failure reason")
	}

	allProviders, err := service.repository.ListProviders(ctx)
	if err != nil {
		return ProviderRoute{}, err
	}
	provider, exists := findProvider(allProviders, claims.Provider)
	if !exists {
		return ProviderRoute{}, ErrInvalidToken
	}
	providers := normalizedProviders(allProviders)

	// The append-only PostgreSQL event is the durable idempotency boundary.
	// Persist it before touching Redis so a transient database failure can be
	// retried with the same attempt token without losing the audit record.
	if err := service.repository.RecordOutcome(
		ctx,
		sessionID,
		claims.Nonce,
		claims.PlacementKey,
		claims.Provider,
		report.Outcome,
		report.FailureReason,
		report.DurationMS,
		now,
	); err != nil {
		return ProviderRoute{}, err
	}

	stateAvailable := service.state != nil
	if stateAvailable {
		_, claimErr := service.state.ClaimReport(ctx, claims.Nonce, claims.ExpiresAt.Sub(now))
		if claimErr != nil {
			stateAvailable = false
		}
	}

	state := SessionState{
		PreferredProvider: claims.PreferredProvider,
		ActiveProvider:    claims.ActiveProvider,
		AssignedAt:        claims.AssignedAt,
		FailedProviders:   []FailedProvider{},
	}
	if loaded, found := service.loadSession(ctx, sessionID); found {
		state = loaded
	}

	if report.Outcome == OutcomeSuccess {
		state.ActiveProvider = claims.Provider
		state.FailedProviders = removeFailedProvider(state.FailedProviders, claims.Provider)
		if stateAvailable {
			if updated, updateErr := service.state.RecordSuccess(ctx, sessionID, claims.Provider, service.sessionTTL); updateErr == nil {
				state = updated
			}
			if claims.HalfOpen {
				_ = service.state.RecordBreakerSuccess(ctx, claims.Provider)
			}
		}
	} else {
		retryAfter := now.Add(time.Duration(provider.CooldownSeconds) * time.Second)
		state.FailedProviders = upsertFailedProvider(state.FailedProviders, claims.Provider, retryAfter)
		if stateAvailable {
			if updated, updateErr := service.state.RecordFailure(ctx, sessionID, claims.Provider, retryAfter, service.sessionTTL); updateErr == nil {
				state = updated
			}
			if report.FailureReason.TripsCircuit() {
				contributes, contributionErr := service.state.ClaimBreakerContribution(
					ctx,
					sessionID,
					claims.Provider,
					time.Duration(provider.CooldownSeconds)*time.Second,
				)
				if claims.HalfOpen || (contributionErr == nil && contributes) {
					_ = service.state.RecordBreakerFailure(
						ctx,
						claims.Provider,
						provider.FailureThreshold,
						time.Duration(provider.CooldownSeconds)*time.Second,
						now,
					)
				}
			}
		}
	}

	breakers := service.breakerStates(ctx, providers, now)
	return service.buildRoute(sessionID, claims.PlacementKey, providers, state, breakers, now)
}

func (service *Service) loadSession(ctx context.Context, sessionID string) (SessionState, bool) {
	if service.state == nil {
		return SessionState{}, false
	}
	state, err := service.state.LoadSession(ctx, sessionID)
	return state, err == nil
}

func (service *Service) breakerStates(ctx context.Context, providers []Provider, now time.Time) map[string]BreakerState {
	states := map[string]BreakerState{}
	if service.state == nil {
		return states
	}
	codes := make([]string, 0, len(providers))
	for _, provider := range providers {
		codes = append(codes, provider.Code)
	}
	if loaded, err := service.state.BreakerStates(ctx, codes, now); err == nil {
		return loaded
	}
	return states
}

func (service *Service) buildRoute(
	sessionID string,
	placementKey string,
	providers []Provider,
	state SessionState,
	breakers map[string]BreakerState,
	now time.Time,
) (ProviderRoute, error) {
	route := ProviderRoute{
		SessionID:         sessionID,
		PlacementKey:      placementKey,
		PreferredProvider: state.PreferredProvider,
		ActiveProvider:    state.ActiveProvider,
		Attempts:          []ProviderAttempt{},
	}
	for _, provider := range orderedProviders(providers, state, breakers, now) {
		token, err := service.signAttempt(attemptClaims{
			SessionID:         sessionID,
			PlacementKey:      placementKey,
			Provider:          provider.Code,
			PreferredProvider: state.PreferredProvider,
			ActiveProvider:    state.ActiveProvider,
			AssignedAt:        state.AssignedAt,
			HalfOpen:          breakers[provider.Code].HalfOpen,
			Nonce:             newNonce(),
			ExpiresAt:         now.Add(service.attemptTTL),
		})
		if err != nil {
			return ProviderRoute{}, err
		}
		route.Attempts = append(route.Attempts, ProviderAttempt{Provider: provider, Token: token})
	}
	if len(route.Attempts) == 0 {
		route.Hidden = true
		route.Reason = "no_eligible_provider"
	}
	return route, nil
}

func hiddenRoute(sessionID, placementKey string) ProviderRoute {
	return ProviderRoute{
		SessionID:    sessionID,
		PlacementKey: placementKey,
		Attempts:     []ProviderAttempt{},
		Hidden:       true,
		Reason:       "no_eligible_provider",
	}
}

func normalizedProviders(providers []Provider) []Provider {
	result := make([]Provider, 0, len(providers))
	for _, provider := range providers {
		provider.Code = strings.ToLower(strings.TrimSpace(provider.Code))
		provider.HealthStatus = strings.ToUpper(strings.TrimSpace(provider.HealthStatus))
		if provider.Code == "" || !provider.Enabled || provider.HealthStatus == "DISABLED" {
			continue
		}
		if provider.FailureThreshold <= 0 {
			provider.FailureThreshold = 20
		}
		if provider.CooldownSeconds <= 0 {
			provider.CooldownSeconds = 300
		}
		result = append(result, provider)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].GlobalPriority == result[j].GlobalPriority {
			return result[i].Code < result[j].Code
		}
		return result[i].GlobalPriority < result[j].GlobalPriority
	})
	return result
}

func globallyEligible(provider Provider, breakers map[string]BreakerState, now time.Time) bool {
	if !provider.Enabled || provider.HealthStatus != "HEALTHY" {
		return false
	}
	state, found := breakers[provider.Code]
	return !found || state.OpenUntil.IsZero() || !now.Before(state.OpenUntil)
}

func weightedProvider(providers []Provider, sessionID string, breakers map[string]BreakerState, now time.Time) string {
	eligible := make([]Provider, 0, len(providers))
	total := 0.0
	for _, provider := range providers {
		if provider.TrafficWeight > 0 && globallyEligible(provider, breakers, now) {
			eligible = append(eligible, provider)
			total += provider.TrafficWeight
		}
	}
	if total <= 0 {
		return ""
	}
	digest := sha256.Sum256([]byte(sessionID))
	fraction := float64(binary.BigEndian.Uint64(digest[:8])) / float64(^uint64(0))
	pick := fraction * total
	for _, provider := range eligible {
		if pick < provider.TrafficWeight {
			return provider.Code
		}
		pick -= provider.TrafficWeight
	}
	return eligible[len(eligible)-1].Code
}

func orderedProviders(providers []Provider, state SessionState, breakers map[string]BreakerState, now time.Time) []Provider {
	byCode := map[string]Provider{}
	for _, provider := range providers {
		byCode[provider.Code] = provider
	}
	ordered := []Provider{}
	seen := map[string]bool{}
	add := func(code string) {
		provider, found := byCode[code]
		if !found || seen[code] || !globallyEligible(provider, breakers, now) || now.Before(failedUntil(state, code)) {
			return
		}
		seen[code] = true
		ordered = append(ordered, provider)
	}
	add(state.ActiveProvider)
	add(state.PreferredProvider)
	for _, provider := range providers {
		add(provider.Code)
	}
	return ordered
}

func failedUntil(state SessionState, code string) time.Time {
	for _, failed := range state.FailedProviders {
		if failed.Provider == code {
			return failed.RetryAfter
		}
	}
	return time.Time{}
}

func upsertFailedProvider(failures []FailedProvider, code string, retryAfter time.Time) []FailedProvider {
	result := make([]FailedProvider, 0, len(failures)+1)
	for _, failure := range failures {
		if failure.Provider != code {
			result = append(result, failure)
		}
	}
	return append(result, FailedProvider{Provider: code, RetryAfter: retryAfter})
}

func removeFailedProvider(failures []FailedProvider, code string) []FailedProvider {
	result := make([]FailedProvider, 0, len(failures))
	for _, failure := range failures {
		if failure.Provider != code {
			result = append(result, failure)
		}
	}
	return result
}

func findProvider(providers []Provider, code string) (Provider, bool) {
	for _, provider := range providers {
		if strings.EqualFold(strings.TrimSpace(provider.Code), code) {
			provider.Code = strings.ToLower(strings.TrimSpace(provider.Code))
			return provider, true
		}
	}
	return Provider{}, false
}

type attemptClaims struct {
	SessionID         string    `json:"sid"`
	PlacementKey      string    `json:"plc"`
	Provider          string    `json:"prv"`
	PreferredProvider string    `json:"pref"`
	ActiveProvider    string    `json:"active"`
	AssignedAt        time.Time `json:"assigned"`
	HalfOpen          bool      `json:"half_open,omitempty"`
	Nonce             string    `json:"nonce"`
	ExpiresAt         time.Time `json:"expires"`
}

func (service *Service) signAttempt(claims attemptClaims) (string, error) {
	if len(service.signerKey) < 32 {
		return "", errors.New("ad attempt signer key must contain at least 32 bytes")
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, service.signerKey)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (service *Service) verifyAttempt(token string, now time.Time) (attemptClaims, error) {
	var claims attemptClaims
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(service.signerKey) < 32 {
		return claims, ErrInvalidToken
	}
	payload, payloadErr := base64.RawURLEncoding.DecodeString(parts[0])
	signature, signatureErr := base64.RawURLEncoding.DecodeString(parts[1])
	if payloadErr != nil || signatureErr != nil {
		return claims, ErrInvalidToken
	}
	mac := hmac.New(sha256.New, service.signerKey)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) || json.Unmarshal(payload, &claims) != nil {
		return attemptClaims{}, ErrInvalidToken
	}
	if claims.SessionID == "" || claims.PlacementKey == "" || claims.Provider == "" || claims.Nonce == "" || !now.Before(claims.ExpiresAt) {
		return attemptClaims{}, ErrInvalidToken
	}
	return claims, nil
}

func newNonce() string {
	random := make([]byte, 18)
	if _, err := rand.Read(random); err != nil {
		digest := sha256.Sum256([]byte(time.Now().String()))
		random = digest[:18]
	}
	return base64.RawURLEncoding.EncodeToString(random)
}

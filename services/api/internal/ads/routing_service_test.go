package ads

import (
	"context"
	"errors"
	"testing"
	"time"
)

type recordedOutcome struct {
	provider string
	outcome  Outcome
	reason   FailureReason
}

type fakeProviderRepository struct {
	providers []Provider
	outcomes  []recordedOutcome
	attempts  map[string]bool
}

func (repository *fakeProviderRepository) ListProviders(context.Context) ([]Provider, error) {
	return append([]Provider(nil), repository.providers...), nil
}

func (repository *fakeProviderRepository) RecordOutcome(_ context.Context, _, attempt, _, provider string, outcome Outcome, reason FailureReason, _ int, _ time.Time) error {
	if repository.attempts == nil {
		repository.attempts = map[string]bool{}
	}
	if repository.attempts[attempt] {
		return ErrDuplicateReport
	}
	repository.attempts[attempt] = true
	repository.outcomes = append(repository.outcomes, recordedOutcome{provider: provider, outcome: outcome, reason: reason})
	return nil
}

type fakeStateStore struct {
	sessions        map[string]SessionState
	claimed         map[string]bool
	breakers        map[string]BreakerState
	breakerFailures map[string]int
	contributions   map[string]bool
}

func newFakeStateStore() *fakeStateStore {
	return &fakeStateStore{
		sessions:        map[string]SessionState{},
		claimed:         map[string]bool{},
		breakers:        map[string]BreakerState{},
		breakerFailures: map[string]int{},
		contributions:   map[string]bool{},
	}
}

func (store *fakeStateStore) LoadSession(_ context.Context, id string) (SessionState, error) {
	state, found := store.sessions[id]
	if !found {
		return SessionState{}, ErrStateNotFound
	}
	return state, nil
}

func (store *fakeStateStore) CreateSession(_ context.Context, id string, initial SessionState, _ time.Duration) (SessionState, error) {
	if existing, found := store.sessions[id]; found {
		return existing, nil
	}
	store.sessions[id] = initial
	return initial, nil
}

func (store *fakeStateStore) RecordFailure(_ context.Context, id, provider string, retryAfter time.Time, _ time.Duration) (SessionState, error) {
	state, found := store.sessions[id]
	if !found {
		return SessionState{}, ErrStateNotFound
	}
	state.FailedProviders = upsertFailedProvider(state.FailedProviders, provider, retryAfter)
	store.sessions[id] = state
	return state, nil
}

func (store *fakeStateStore) RecordSuccess(_ context.Context, id, provider string, _ time.Duration) (SessionState, error) {
	state, found := store.sessions[id]
	if !found {
		return SessionState{}, ErrStateNotFound
	}
	state.ActiveProvider = provider
	state.FailedProviders = removeFailedProvider(state.FailedProviders, provider)
	store.sessions[id] = state
	return state, nil
}

func (store *fakeStateStore) ClaimReport(_ context.Context, nonce string, _ time.Duration) (bool, error) {
	if store.claimed[nonce] {
		return false, nil
	}
	store.claimed[nonce] = true
	return true, nil
}

func (store *fakeStateStore) ClaimBreakerContribution(_ context.Context, sessionID, provider string, _ time.Duration) (bool, error) {
	key := sessionID + ":" + provider
	if store.contributions[key] {
		return false, nil
	}
	store.contributions[key] = true
	return true, nil
}

func (store *fakeStateStore) BreakerStates(context.Context, []string, time.Time) (map[string]BreakerState, error) {
	copyOfStates := map[string]BreakerState{}
	for code, state := range store.breakers {
		copyOfStates[code] = state
	}
	return copyOfStates, nil
}

func (store *fakeStateStore) RecordBreakerFailure(_ context.Context, provider string, threshold int, cooldown time.Duration, now time.Time) error {
	store.breakerFailures[provider]++
	if store.breakerFailures[provider] >= threshold {
		store.breakers[provider] = BreakerState{Provider: provider, OpenUntil: now.Add(cooldown)}
	}
	return nil
}

func (store *fakeStateStore) RecordBreakerSuccess(_ context.Context, provider string) error {
	if _, open := store.breakers[provider]; open {
		delete(store.breakers, provider)
		delete(store.breakerFailures, provider)
	}
	return nil
}

func TestOneSessionContributesOnlyOnceToGlobalCircuit(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	repository := &fakeProviderRepository{providers: testProviders()}
	state := newFakeStateStore()
	service := NewServiceWithRepository(repository, state, []byte("0123456789abcdef0123456789abcdef"), func() time.Time { return now })

	first, err := service.Resolve(context.Background(), ResolveRequest{SessionID: "session-1", PlacementKey: "home_top"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Resolve(context.Background(), ResolveRequest{SessionID: "session-1", PlacementKey: "home_inline"})
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []ProviderAttempt{first.Attempts[0], second.Attempts[0]} {
		if _, err := service.Report(context.Background(), "session-1", OutcomeReport{
			Token: attempt.Token, Outcome: OutcomeFailure, FailureReason: FailureTimeout, DurationMS: 900,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if got := state.breakerFailures["adsterra"]; got != 1 {
		t.Fatalf("global failure contributions = %d, want 1 unique session", got)
	}
}

func testProviders() []Provider {
	return []Provider{
		{Code: "adsterra", Name: "Adsterra", AdapterKey: "adsterra", Enabled: true, GlobalPriority: 10, TrafficWeight: 100, HealthStatus: "HEALTHY", FailureThreshold: 2, CooldownSeconds: 300, RequestTimeoutMS: 900},
		{Code: "monetag", Name: "Monetag", AdapterKey: "monetag", Enabled: true, GlobalPriority: 20, TrafficWeight: 0, HealthStatus: "HEALTHY", FailureThreshold: 2, CooldownSeconds: 300, RequestTimeoutMS: 1_000},
		{Code: "reserve", Name: "Reserve", AdapterKey: "monetag", Enabled: true, GlobalPriority: 30, TrafficWeight: 0, HealthStatus: "HEALTHY", FailureThreshold: 2, CooldownSeconds: 300, RequestTimeoutMS: 1_100},
	}
}

func TestOrderedProvidersStartsWithActiveThenRecoveredPreference(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 20, 0, 0, time.UTC)
	state := SessionState{
		PreferredProvider: "adsterra",
		ActiveProvider:    "monetag",
		FailedProviders:   []FailedProvider{{Provider: "adsterra", RetryAfter: now.Add(-time.Second)}},
	}
	ordered := orderedProviders(testProviders(), state, nil, now)
	assertProviderCodes(t, ordered, "monetag", "adsterra", "reserve")
}

func TestOrderedProvidersSkipsSessionCooldownAndGlobalBreaker(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	state := SessionState{
		PreferredProvider: "adsterra",
		ActiveProvider:    "adsterra",
		FailedProviders:   []FailedProvider{{Provider: "adsterra", RetryAfter: now.Add(time.Minute)}},
	}
	breakers := map[string]BreakerState{"monetag": {Provider: "monetag", OpenUntil: now.Add(time.Minute)}}
	ordered := orderedProviders(testProviders(), state, breakers, now)
	assertProviderCodes(t, ordered, "reserve")
}

func TestZeroWeightProviderIsFallbackButNeverInitialAssignment(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	providers := testProviders()
	for _, session := range []string{"one", "two", "three", "four"} {
		if got := weightedProvider(providers, session, nil, now); got != "adsterra" {
			t.Fatalf("weightedProvider(%q) = %q, want adsterra", session, got)
		}
	}
	ordered := orderedProviders(providers, SessionState{PreferredProvider: "adsterra", ActiveProvider: "adsterra"}, nil, now)
	assertProviderCodes(t, ordered, "adsterra", "monetag", "reserve")
}

func TestFailureFallsThroughAndSuccessMakesFallbackSticky(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	repository := &fakeProviderRepository{providers: testProviders()}
	state := newFakeStateStore()
	service := NewServiceWithRepository(repository, state, []byte("0123456789abcdef0123456789abcdef"), clock)

	route, err := service.Resolve(context.Background(), ResolveRequest{SessionID: "session-1", PlacementKey: "home_top"})
	if err != nil {
		t.Fatal(err)
	}
	assertAttemptCodes(t, route.Attempts, "adsterra", "monetag", "reserve")

	route, err = service.Report(context.Background(), "session-1", OutcomeReport{
		Token:         route.Attempts[0].Token,
		Outcome:       OutcomeFailure,
		FailureReason: FailureNoFill,
		DurationMS:    850,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertAttemptCodes(t, route.Attempts, "monetag", "reserve")
	if state.breakerFailures["adsterra"] != 0 {
		t.Fatal("NO_FILL must not affect the global circuit")
	}

	route, err = service.Report(context.Background(), "session-1", OutcomeReport{
		Token:      route.Attempts[0].Token,
		Outcome:    OutcomeSuccess,
		DurationMS: 400,
	})
	if err != nil {
		t.Fatal(err)
	}
	if route.ActiveProvider != "monetag" {
		t.Fatalf("active provider = %q, want monetag", route.ActiveProvider)
	}
	assertAttemptCodes(t, route.Attempts, "monetag", "reserve")

	now = now.Add(301 * time.Second)
	route, err = service.Resolve(context.Background(), ResolveRequest{SessionID: "session-1", PlacementKey: "home_inline"})
	if err != nil {
		t.Fatal(err)
	}
	assertAttemptCodes(t, route.Attempts, "monetag", "adsterra", "reserve")
}

func TestTransportFailuresTripCircuitButLocalFailuresDoNot(t *testing.T) {
	for reason, want := range map[FailureReason]bool{
		FailureNoFill:              false,
		FailureBlocked:             false,
		FailureInvalidZone:         false,
		FailureScriptError:         true,
		FailureTimeout:             true,
		FailureNetworkError:        true,
		FailureProviderUnavailable: true,
	} {
		if got := reason.TripsCircuit(); got != want {
			t.Errorf("%s TripsCircuit = %t, want %t", reason, got, want)
		}
	}
}

func TestAttemptTokenIsBoundToSessionAndSingleUse(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	repository := &fakeProviderRepository{providers: testProviders()}
	state := newFakeStateStore()
	service := NewServiceWithRepository(repository, state, []byte("0123456789abcdef0123456789abcdef"), func() time.Time { return now })
	route, err := service.Resolve(context.Background(), ResolveRequest{SessionID: "session-1", PlacementKey: "home_top"})
	if err != nil {
		t.Fatal(err)
	}
	report := OutcomeReport{Token: route.Attempts[0].Token, Outcome: OutcomeFailure, FailureReason: FailureBlocked, DurationMS: 1}
	if _, err := service.Report(context.Background(), "another-session", report); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong-session error = %v, want ErrInvalidToken", err)
	}
	if _, err := service.Report(context.Background(), "session-1", report); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Report(context.Background(), "session-1", report); !errors.Is(err, ErrDuplicateReport) {
		t.Fatalf("duplicate error = %v, want ErrDuplicateReport", err)
	}
}

func TestResolveHidesSlotWhenEveryProviderIsIneligible(t *testing.T) {
	repository := &fakeProviderRepository{providers: []Provider{
		{Code: "off", Enabled: false, TrafficWeight: 100, HealthStatus: "DISABLED"},
		{Code: "down", Enabled: true, TrafficWeight: 100, HealthStatus: "UNHEALTHY"},
	}}
	service := NewServiceWithRepository(repository, nil, []byte("0123456789abcdef0123456789abcdef"), time.Now)
	route, err := service.Resolve(context.Background(), ResolveRequest{SessionID: "session", PlacementKey: "inline"})
	if err != nil {
		t.Fatal(err)
	}
	if !route.Hidden || len(route.Attempts) != 0 {
		t.Fatalf("route = %+v, want hidden with no attempts", route)
	}
}

func assertProviderCodes(t *testing.T, providers []Provider, want ...string) {
	t.Helper()
	if len(providers) != len(want) {
		t.Fatalf("provider count = %d, want %d (%+v)", len(providers), len(want), providers)
	}
	for index := range want {
		if providers[index].Code != want[index] {
			t.Fatalf("providers[%d] = %q, want %q", index, providers[index].Code, want[index])
		}
	}
}

func assertAttemptCodes(t *testing.T, attempts []ProviderAttempt, want ...string) {
	t.Helper()
	providers := make([]Provider, 0, len(attempts))
	for _, attempt := range attempts {
		providers = append(providers, attempt.Provider)
	}
	assertProviderCodes(t, providers, want...)
}

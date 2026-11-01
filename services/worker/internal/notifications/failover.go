package notifications

import (
	"sync"
	"time"
)

// ReadinessChecker lets a transport declare that it is configured well enough
// to be worth trying. A provider missing its API key should be skipped by the
// resolver, not discovered at the moment a password reset needs to go out.
type ReadinessChecker interface {
	Ready() error
}

// ProviderReady reports whether a transport is usable now. A transport that
// does not implement ReadinessChecker is assumed ready; the interface is
// opt-in so adding a provider never requires touching the resolver.
func ProviderReady(transport Transport) error {
	if checker, ok := transport.(ReadinessChecker); ok {
		return checker.Ready()
	}
	return nil
}

const (
	defaultBreakerThreshold = 3
	defaultBreakerCooldown  = 2 * time.Minute
)

// ProviderBreaker keeps a provider that is currently down from being tried
// first for every message in a batch.
//
// Without it, a Cloudflare outage costs one failed request per message before
// each one falls through to the fallback -- so the outage is paid for twice,
// once in latency and once in a rate-limit counter that is already angry. The
// breaker is per provider name and process-local by design: it is a latency
// optimisation, not a source of truth, and a stale opinion in one worker
// replica must never be able to stop another replica from sending.
type ProviderBreaker struct {
	FailureThreshold int
	OpenFor          time.Duration
	Now              func() time.Time

	mu    sync.Mutex
	state map[string]*breakerState
}

type breakerState struct {
	failures int
	openTill time.Time
}

func (b *ProviderBreaker) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *ProviderBreaker) threshold() int {
	if b.FailureThreshold > 0 {
		return b.FailureThreshold
	}
	return defaultBreakerThreshold
}

func (b *ProviderBreaker) cooldown() time.Duration {
	if b.OpenFor > 0 {
		return b.OpenFor
	}
	return defaultBreakerCooldown
}

// Allow reports whether the provider may be tried. A nil breaker allows
// everything, so the breaker stays optional at every call site.
func (b *ProviderBreaker) Allow(provider string) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.state[provider]
	if state == nil || state.openTill.IsZero() {
		return true
	}
	if b.now().Before(state.openTill) {
		return false
	}
	// Cooldown elapsed: let exactly one message through to find out whether
	// the provider recovered. Success closes it; failure re-opens it.
	state.openTill = time.Time{}
	state.failures = b.threshold() - 1
	return true
}

// Success closes the breaker for a provider that just delivered.
func (b *ProviderBreaker) Success(provider string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.state, provider)
}

// Failure records a provider-side fault and opens the breaker once the
// provider has failed consecutively past the threshold.
func (b *ProviderBreaker) Failure(provider string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == nil {
		b.state = map[string]*breakerState{}
	}
	state := b.state[provider]
	if state == nil {
		state = &breakerState{}
		b.state[provider] = state
	}
	state.failures++
	if state.failures >= b.threshold() {
		state.openTill = b.now().Add(b.cooldown())
	}
}

// Open reports whether the breaker is currently holding a provider out. It
// exists for status surfaces and tests, not for the send path.
func (b *ProviderBreaker) Open(provider string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.state[provider]
	return state != nil && !state.openTill.IsZero() && b.now().Before(state.openTill)
}

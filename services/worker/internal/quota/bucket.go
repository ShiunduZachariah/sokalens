// Package quota implements the two independent API-Football quota controls
// (per-minute rate governor and daily budget governor) plus a fixture-aware
// daily quota planner that converts the known football schedule into a
// priority-tier plan and a safe discretionary budget.
//
// Provider response headers are authoritative: the governors re-seed their
// state from x-ratelimit-* headers on every response instead of trusting local
// counters alone.
package quota

import (
	"context"
	"math"
	"math/rand"
	"sync"
	"time"
)

// TokenBucket is a leaky/token bucket for the provider's per-minute limit.
// It is safe for concurrent use.
type TokenBucket struct {
	mu       sync.Mutex
	capacity float64 // max tokens
	rate     float64 // tokens per second
	tokens   float64
	last     time.Time
	now      func() time.Time
	rand     *rand.Rand
}

// NewTokenBucket creates a bucket with limitPerMinute tokens that refills at
// limitPerMinute/60 tokens per second. now may be nil (defaults to time.Now).
func NewTokenBucket(limitPerMinute int, now func() time.Time) *TokenBucket {
	if limitPerMinute <= 0 {
		limitPerMinute = 60
	}
	if now == nil {
		now = time.Now
	}
	return &TokenBucket{
		capacity: float64(limitPerMinute),
		rate:     float64(limitPerMinute) / 60.0,
		tokens:   float64(limitPerMinute),
		last:     now(),
		now:      now,
		rand:     rand.New(rand.NewSource(now().UnixNano())),
	}
}

// refill adds tokens proportional to elapsed time, capped at capacity.
func (b *TokenBucket) refill(now time.Time) {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens = math.Min(b.capacity, b.tokens+elapsed*b.rate)
		b.last = now
	}
}

// TryTake attempts to consume one token without blocking.
func (b *TokenBucket) TryTake() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.refill(now)
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// Wait blocks until a token is available or ctx is done. It adds bounded
// jitter so multiple workers do not synchronize on the same refill tick.
func (b *TokenBucket) Wait(ctx context.Context) error {
	for {
		if b.TryTake() {
			return nil
		}
		b.mu.Lock()
		now := b.now()
		b.refill(now)
		needed := 1 - b.tokens
		wait := time.Duration((needed / b.rate) * float64(time.Second))
		if wait <= 0 {
			wait = time.Second
		}
		// jitter: up to 10% of the wait, minimum 100ms.
		jitter := time.Duration(float64(wait) * (0.05 + 0.05*b.rand.Float64()))
		wait += jitter
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// Observe re-seeds the bucket from authoritative provider headers. When the
// provider reports a fresh minute limit/remaining, the bucket is rebuilt to
// that exact state.
func (b *TokenBucket) Observe(limit, remaining int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if limit > 0 {
		b.capacity = float64(limit)
		b.rate = float64(limit) / 60.0
	}
	if remaining >= 0 {
		b.tokens = math.Min(b.capacity, float64(remaining))
		b.last = now
	}
}

// Remaining reports the tokens currently available, refilled to now.
//
// Used to tell a single rejected request apart from a genuinely spent minute:
// a provider that rejects one call while this still reads in the hundreds has
// not run the allowance out, and pausing every caller for a minute over it
// costs far more than the one request that was refused.
func (b *TokenBucket) Remaining() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill(b.now())
	if b.tokens < 0 {
		return 0
	}
	return int(b.tokens)
}

// WaitEstimate returns how long a caller should wait before a token becomes
// available, or zero if one is available now.
func (b *TokenBucket) WaitEstimate() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.refill(now)
	if b.tokens >= 1 {
		return 0
	}
	needed := 1 - b.tokens
	wait := time.Duration((needed / b.rate) * float64(time.Second))
	if wait <= 0 {
		wait = time.Second
	}
	return wait
}

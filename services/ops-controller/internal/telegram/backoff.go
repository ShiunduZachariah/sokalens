package telegram

import (
	"math/rand/v2"
	"sync"
	"time"
)

// jitterSpread is the fraction of the current base delay that jitter may add.
// A full-jitter scheme (uniform over [0, base]) would return delays below the
// floor, which the poller loop treats as a hot-retry hazard; adding upward
// instead keeps every delay inside [base, 1.5*base] and never below the floor
// (spec §11.3: back off 1s to 5m, never faster than 1s).
const jitterSpread = 0.5

// Backoff is the poller's retry schedule: exponential from floor to ceiling
// with bounded upward jitter so that several instances recovering from the
// same outage do not re-poll in lockstep.
//
// The zero value is not usable; construct with NewBackoff. Safe for concurrent
// use, although the poller drives it from a single goroutine.
type Backoff struct {
	floor   time.Duration
	ceiling time.Duration
	// jitter returns a value in [0,1]. Injectable so tests are deterministic.
	jitter func() float64

	mu sync.Mutex
	// base is the un-jittered delay the next Next call starts from. Zero means
	// "not started yet", which is what Reset restores.
	base time.Duration
}

// NewBackoff builds the schedule the poller loop uses:
// NewBackoff(1*time.Second, 5*time.Minute).
func NewBackoff(floor, ceiling time.Duration) *Backoff {
	return NewBackoffWithJitter(floor, ceiling, rand.Float64)
}

// newBackoff is the unexported spelling the implementation plan uses for the
// same constructor.
func newBackoff(floor, ceiling time.Duration) *Backoff { return NewBackoff(floor, ceiling) }

// NewBackoffWithJitter is NewBackoff with an injectable jitter source. jitter
// must return a value in [0,1]; values outside that range are clamped so a
// misbehaving source cannot produce a delay outside [floor, ceiling].
func NewBackoffWithJitter(floor, ceiling time.Duration, jitter func() float64) *Backoff {
	if floor <= 0 {
		floor = time.Second
	}
	if ceiling < floor {
		ceiling = floor
	}
	if jitter == nil {
		jitter = rand.Float64
	}
	return &Backoff{floor: floor, ceiling: ceiling, jitter: jitter}
}

// Next returns the next delay and advances the schedule. The result is always
// within [floor, ceiling]: the first call returns at least floor, and once the
// schedule has saturated every call returns exactly ceiling.
func (b *Backoff) Next() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.base <= 0 {
		b.base = b.floor
	}
	base := b.base
	if base > b.ceiling {
		base = b.ceiling
	}

	// Advance without overflowing: double only while doubling stays below the
	// ceiling, then pin to the ceiling.
	if b.base > b.ceiling/2 {
		b.base = b.ceiling
	} else {
		b.base *= 2
	}

	upper := base + time.Duration(float64(base)*jitterSpread)
	if upper > b.ceiling {
		upper = b.ceiling
	}
	fraction := b.jitter()
	switch {
	case fraction < 0:
		fraction = 0
	case fraction > 1:
		fraction = 1
	}
	return base + time.Duration(fraction*float64(upper-base))
}

// Reset returns the schedule to the floor. The poller calls this after any
// successful poll so a single transient failure does not leave the loop in a
// slow state.
func (b *Backoff) Reset() {
	b.mu.Lock()
	b.base = 0
	b.mu.Unlock()
}

package quota

import (
	"sync"
	"time"
)

const providerResetHeaderGrace = 15 * time.Minute

// unconfirmedProbeAllowance is how many requests may bypass the pacing
// reserve while this budget has never been confirmed by a provider header.
//
// It exists because without it the governor can deadlock, and did: on
// 2026-09-21 a worker restart seeded `remaining` at 6,656 from our own
// request log while the provider's own accounting said 23,491. The pacing
// reserve that afternoon was about 22,000, so every request was refused --
// and `Observe` only corrects `remaining` from a response header, which
// requires a request the governor would not permit. All provider traffic
// stopped, including live match polling, and could not resume until the
// reserve decayed hours later.
//
// A budget that has never been confirmed must be allowed to confirm itself.
// Five requests is enough for one to return a header and far too few to
// matter against a daily limit in the tens of thousands.
const unconfirmedProbeAllowance = 5

// probeInterval is how long a refusing budget may go without testing whether
// it is right to refuse.
//
// The allowance above was meant to prevent the deadlock and did not, because
// it stops at the first confirmation. On 2026-09-22 the provider returned the
// previous day's remaining shortly after the 00:00 UTC reset -- its own header
// lag -- we accepted 33 of 75,000, set confirmed, and thereby switched off the
// one mechanism that could have corrected it. Every provider call was refused
// for the next fifteen hours while the provider's dashboard showed the day's
// quota untouched at 0% used. Live scores froze; 882 matches sat in 'upcoming'
// with kickoff long past. It was noticed by a person comparing the site to
// FlashScore, which is not a monitoring strategy.
//
// The lesson is not that the staleness heuristic needs to be smarter. It is
// that a belief which forbids the only action that could correct it must never
// be held indefinitely, however it was arrived at. So a refusing budget always
// lets one request through every few minutes to find out.
//
// Five minutes bounds the damage of any wrong belief to five minutes. The cost
// is at most 288 requests a day against a limit of 75,000, and only while we
// are refusing -- a budget that is spending normally never probes at all. If
// the refusal was right, the provider answers 429 and nothing is lost.
const probeInterval = 5 * time.Minute

// dailyProbeCeiling bounds probing over a whole day, so a persistent genuine
// exhaustion cannot turn into unbounded failing traffic.
const dailyProbeCeiling = 500

// DailyBudget is the daily provider budget governor. The daily limit is a hard
// cap on total consumption for the provider day; the reserve is maximum
// protected headroom. It decays with the UTC day so unused quota can be spent
// on prediction-critical history instead of expiring at midnight. Provider headers are
// authoritative: Observe re-seeds remaining from x-ratelimit-requests-remaining.
type DailyBudget struct {
	mu        sync.Mutex
	limit     int
	remaining int
	reserve   int
	consumed  int
	day       time.Time // UTC day boundary this budget applies to
	now       func() time.Time

	// planLimit is the size of the package we actually pay for, and it is the
	// one number here that no provider response may change.
	//
	// Everything else in this struct is a belief that headers correct:
	// `limit`, `remaining` and `consumed` are all re-seeded by Observe. That
	// is right for pacing and wrong for a ceiling, because it means the
	// ceiling is whatever the provider last claimed. While the account was on
	// the 75,000 package and the configuration said 7,500, a single header
	// would have raised the cap back to 75,000 and the platform would have
	// spent it -- which is exactly the bill this is here to prevent.
	//
	// So: pace against the headers, refuse against this.
	planLimit int

	// issued counts requests this process has actually sent today, counted by
	// us and reconstructed from nothing. `consumed` cannot serve here because
	// Observe rewrites it from a header, so a stale or wrong header moves the
	// very counter the ceiling is measured against.
	issued int

	// confirmed records whether a provider header has ever re-seeded this
	// budget. Until it has, `remaining` is an estimate reconstructed from our
	// own request log, and that log can be wrong.
	confirmed bool
	// probes counts requests let through purely so the budget can learn the
	// truth. Bounded, and reset with the provider day.
	probes int

	// probesToday and lastProbeAt drive recovery probing. Unlike `probes`,
	// these are never switched off by a confirmation: a confirmed balance can
	// still be wrong, and the whole point is that we keep finding out.
	probesToday int
	lastProbeAt time.Time
	// refusingSince is when this budget last started saying no. Cleared on any
	// normal spend, so it measures an unbroken run of refusals rather than
	// refusals in general.
	refusingSince time.Time
}

// NewDailyBudget creates a budget. limit is the daily cap, reserve is the
// protected headroom, initialRemaining seeds the counter (headers win later).
func NewDailyBudget(limit, reserve, initialRemaining int, now func() time.Time) *DailyBudget {
	if limit <= 0 {
		limit = 7500
	}
	if reserve < 0 || reserve >= limit {
		reserve = limit / 5
	}
	// A negative remaining means our own request log says today has already
	// spent more than the package holds. Reading that as "a fresh day" is the
	// exact inversion to avoid, and it is reachable: shrinking the configured
	// package from 75,000 to 7,500 makes every day that spent more than 7,500
	// arrive here negative, and the ceiling would hand each of them a full new
	// allowance. Over-spent means spent.
	if initialRemaining < 0 {
		initialRemaining = 0
	}
	if now == nil {
		now = time.Now
	}
	return &DailyBudget{
		limit:     limit,
		remaining: initialRemaining,
		reserve:   reserve,
		consumed:  max(0, limit-initialRemaining),
		planLimit: limit,
		// Seeded, not zeroed. A count that restarts with the process could be
		// spent once per restart, and the 2026-09-21 incident began with a
		// restart.
		issued: max(0, limit-initialRemaining),
		day:    utcDay(now()),
		now:    now,
	}
}

// CanSpend reports whether spending cost is allowed without breaking the
// reserve. It is advisory; the caller still records Spend.
func (b *DailyBudget) CanSpend(cost int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollDay()
	// The ceiling, checked before anything that could bypass it.
	//
	// Order is the whole point. Below this line are three deliberate escape
	// hatches -- the unconfirmed-budget probe allowance, the recovery probe,
	// and a reserve that decays toward midnight -- each of which exists to
	// stop a wrong belief from stalling ingestion. Every one of them lets a
	// request through that pacing refused, so every one of them has to sit
	// underneath the number we are contractually not allowed to exceed.
	// Checked last instead, an escape hatch would be a way to overspend the
	// package, which is the opposite of what it is for.
	if b.planLimit > 0 && b.issued+cost > b.planLimit {
		return false
	}
	if b.remaining-cost >= b.effectiveReserve() {
		// Spending normally, so nothing is stuck and no probing is owed.
		b.refusingSince = time.Time{}
		return true
	}
	// The pacing reserve refused this. If nothing has ever confirmed the
	// balance we are pacing against, refusing is a guess -- and a guess that
	// prevents the one action that would replace it with a fact. Let a few
	// through, bounded by the hard reserve so a genuinely exhausted budget
	// still cannot overspend.
	//
	// This mutates on a read, which is ugly in a method named CanSpend. The
	// alternative is a probe that is never counted and therefore unbounded,
	// and an unbounded escape hatch is not an escape hatch.
	if !b.confirmed && b.probes < unconfirmedProbeAllowance && b.remaining-cost >= b.reserve {
		b.probes++
		return true
	}
	// Confirmed or not, a budget that is refusing must keep testing whether it
	// is right to refuse. `confirmed` means a header once said so; it does not
	// mean the header was true then, and it certainly does not mean it is true
	// now. Treating it as permanent is what made a single stale reading
	// survive a quota reset and stall ingestion for a day.
	//
	// Deliberately not gated on b.remaining: the case being recovered from is
	// precisely the one where remaining is wrong. A probe that respects the
	// number it is trying to verify verifies nothing.
	now := b.now().UTC()
	if b.refusingSince.IsZero() {
		// First refusal. Refusing is normal and usually correct -- pacing says
		// wait, and waiting is the right answer. Only a refusal that persists
		// looks like a stuck belief rather than a working governor.
		b.refusingSince = now
		return false
	}
	since := b.lastProbeAt
	if since.IsZero() {
		since = b.refusingSince
	}
	if b.probesToday < dailyProbeCeiling && now.Sub(since) >= probeInterval {
		b.lastProbeAt = now
		b.probesToday++
		return true
	}
	return false
}

// ProbeState reports recovery probing, for logging and alerting.
//
// A budget that is probing is one that believes it cannot spend. That is
// normal near a genuine daily exhaustion and is a fault at any other time, and
// the difference is worth surfacing rather than inferring from silence.
func (b *DailyBudget) ProbeState() (probesToday int, lastProbeAt time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.probesToday, b.lastProbeAt
}

// Spend records consumption of cost requests.
func (b *DailyBudget) Spend(cost int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollDay()
	if cost > b.remaining {
		cost = b.remaining
	}
	b.consumed += cost
	b.remaining -= cost
}

// RecordIssued counts one request that was actually sent, for the daily
// ceiling.
//
// Separate from Spend because the two answer different questions and only one
// of them is ours to answer. Spend maintains `remaining`, our running estimate
// of the provider's balance, and Observe overwrites it whenever the provider
// says something different -- correctly, since the provider owns that number.
// This counter is ours: how many requests this process has sent since 00:00
// UTC. Nothing the provider returns can move it, which is precisely why the
// ceiling is measured against it.
//
// Called on every response, including error responses: a request that failed
// still cost a request against the package.
func (b *DailyBudget) RecordIssued(cost int) {
	if cost <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollDay()
	b.issued += cost
}

// Observe re-seeds the budget from authoritative provider daily headers and
// records the day boundary. remaining -1 means "no header this response".
func (b *DailyBudget) Observe(limit, remaining int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollDay()
	if limit > 0 {
		b.limit = limit
		// A header may lower the ceiling -- if the provider says the package
		// is smaller than configured, it is smaller than configured and the
		// provider is right. It may never raise it. See planLimit.
		if b.planLimit > 0 && limit < b.planLimit {
			b.planLimit = limit
		}
	}
	if remaining >= 0 {
		// API-Sports can briefly return the previous UTC day's remaining value
		// immediately after 00:00 UTC. Trusting that stale low value strands the
		// new day's entire budget. During the reset grace window, retain the
		// locally reconstructed new-day balance until a plausible fresh header
		// arrives. Minute throttling still independently caps probe traffic.
		elapsed := b.now().UTC().Sub(b.day)
		resetEdgeStale := elapsed >= 0 && elapsed < providerResetHeaderGrace &&
			b.consumed <= 10 && b.remaining > b.limit/2 && remaining < b.limit/10
		if resetEdgeStale {
			return
		}
		delta := b.remaining - remaining
		if delta > 0 {
			b.consumed += delta
		} else if delta < 0 {
			b.consumed = max(0, b.consumed+delta)
		}
		b.remaining = remaining
		// The provider has spoken. Everything after this is measured rather
		// than reconstructed, so the probe allowance is no longer needed.
		b.confirmed = true
	}
}

// Snapshot returns the current budget state without mutating it.
func (b *DailyBudget) Snapshot() DailySnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.rollDay()
	return DailySnapshot{
		Limit:     b.limit,
		Remaining: b.remaining,
		Reserve:   b.effectiveReserve(),
		Consumed:  b.consumed,
		PlanLimit: b.planLimit,
		Issued:    b.issued,
		Day:       b.day,
	}
}

// effectiveReserve paces spend through the provider day instead of allowing a
// morning backfill burst to consume the whole allowance. Ten percent of the
// daily limit is available as an advance buffer for imminent fixtures; the
// remainder is released linearly and is almost fully available near midnight.
// The recurring-call reserve remains a lower bound while it is relevant.
func (b *DailyBudget) effectiveReserve() int {
	now := b.now().UTC()
	minutesLeft := int(now.Truncate(24*time.Hour).Add(24*time.Hour).Sub(now).Minutes() + 0.999)
	floor := max(1, b.limit/500)
	if b.limit >= 500 {
		floor = max(15, floor)
	}
	if b.limit < 1000 {
		reserve := minutesLeft * 3
		if reserve > b.reserve {
			reserve = b.reserve
		}
		return max(floor, reserve)
	}
	linearReserve := (b.limit*minutesLeft + (24*60 - 1)) / (24 * 60)
	burstAllowance := b.limit / 10
	linearReserve -= burstAllowance
	if linearReserve < floor {
		linearReserve = floor
	}
	recurringReserve := minutesLeft * 3
	if recurringReserve > b.reserve {
		recurringReserve = b.reserve
	}
	if recurringReserve < floor {
		recurringReserve = floor
	}
	return max(linearReserve, recurringReserve)
}

// DailySnapshot is an immutable view of the daily budget.
type DailySnapshot struct {
	Limit     int
	Remaining int
	Reserve   int
	Consumed  int
	// PlanLimit is the hard daily ceiling: the package size, never raised by a
	// provider header.
	PlanLimit int
	// Issued is how many requests this process has sent since 00:00 UTC, by
	// our own count. The ceiling is enforced against this.
	Issued int
	Day    time.Time
}

func (b *DailyBudget) rollDay() {
	now := b.now()
	day := utcDay(now)
	if !day.Equal(b.day) {
		// New provider day: reset consumed to zero and remaining to limit.
		b.day = day
		b.consumed = 0
		b.remaining = b.limit
		// The ceiling is per provider day, so the count it is measured
		// against resets with the day -- and only with the day. Notably not on
		// process start: NewDailyBudget seeds `issued` from the spend already
		// reconstructed from our request log, because a restart that reset
		// this to zero would let a single day spend the package twice, and
		// restarts are the one event this platform has had plenty of.
		b.issued = 0
		// A new day's balance is again an assumption until a header confirms
		// it, so the probe allowance comes back with it.
		b.confirmed = false
		b.probes = 0
		// Probing starts fresh with the provider day too, so a day that ended
		// legitimately exhausted -- having used its whole ceiling on probes --
		// does not begin the next one unable to check anything.
		b.probesToday = 0
		b.lastProbeAt = time.Time{}
		b.refusingSince = time.Time{}
	}
}

func utcDay(now time.Time) time.Time {
	return now.UTC().Truncate(24 * time.Hour)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

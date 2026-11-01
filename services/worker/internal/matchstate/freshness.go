package matchstate

import "time"

// Freshness thresholds.
//
// These are the §6.4 staleness ladder. The numbers are deliberately generous
// relative to the poll interval: live polling runs on a 30s-to-few-minute
// cadence depending on quota pressure, so a match must miss several polls
// before anything is said about it. A guard that fires on one missed poll is a
// guard that cries wolf during every quota squeeze, and a guard nobody trusts
// gets switched off.
const (
	// StaleAfter: the clock stops being presented as current. The match is
	// still shown as in play, because it almost certainly is -- one or two
	// missed polls is normal -- but the minute is no longer asserted.
	StaleAfter = 90 * time.Second

	// ReconcileAfter: long enough that a missed poll is no longer the likely
	// explanation. The match is queued for a targeted refresh.
	ReconcileAfter = 5 * time.Minute

	// HideAfter: the match stops appearing as in play. Whatever is happening,
	// this row is not describing it.
	HideAfter = 15 * time.Minute
)

// Duration bounds from kickoff.
//
// This is the part that actually prevents "second half, three hours". Even
// with no freshness signal at all -- a row written before `live_state_at`
// existed, say -- a match cannot be in play four hours after kickoff, and the
// reaper does not need a provider to tell it so.
const (
	// PlausibleDuration covers 90 minutes plus a generous allowance for
	// half-time, stoppage, VAR checks and a delayed kickoff.
	PlausibleDuration = 150 * time.Minute

	// HardBound covers extra time and penalties on top of that. Past this,
	// nothing legitimate is still being played.
	HardBound = 210 * time.Minute
)

// Freshness is what the read side needs to know about a live claim.
type Freshness int

const (
	// FreshnessCurrent: recently confirmed. Show the minute.
	FreshnessCurrent Freshness = iota
	// FreshnessStale: still in play, but the minute is not asserted.
	FreshnessStale
	// FreshnessReconciling: overdue enough to be queued for a refresh.
	FreshnessReconciling
	// FreshnessLost: no longer presented as in play.
	FreshnessLost
)

func (f Freshness) String() string {
	switch f {
	case FreshnessCurrent:
		return "current"
	case FreshnessStale:
		return "stale"
	case FreshnessReconciling:
		return "reconciling"
	case FreshnessLost:
		return "lost"
	}
	return "unknown"
}

// Observation is one match as stored: what it claims, when that claim was last
// confirmed, and when it kicked off.
type Observation struct {
	State State
	// LiveStateAt is when a provider last confirmed the live state. Zero means
	// never recorded -- true for every row written before the column existed,
	// and the case that must not be read as "confirmed at the epoch".
	LiveStateAt time.Time
	KickoffAt   time.Time
}

// Assess reports how much of a match's live claim still stands.
//
// A state that makes no claim about the present moment cannot go stale, so
// quiescent states are always current. Everything else is judged on
// the more damning of two signals: how long since a provider confirmed it, and
// how long since kickoff. The second is what catches a row whose freshness was
// never recorded at all.
func Assess(observation Observation, now time.Time) Freshness {
	state := observation.State
	if state.Quiescent() {
		// A pending match whose kickoff has long passed is the exception, and
		// it is the larger half of the problem.
		//
		// Quiescent groups settled, pending and unknown as "cannot go stale".
		// That holds for settled (it is over) and for unknown (it asserts
		// nothing), but not for pending: a match that says it has not kicked
		// off, three and a half hours after it did, is making a claim about the
		// present that the clock contradicts. Live polling only reports matches
		// in play, so one it never saw at all never leaves this state, and
		// nothing else revisits it.
		//
		// Production held 3,207 such rows on 2026-09-22, the oldest kicking off
		// in December 2025, none with a score, 680 of them in the indexable
		// sitemap advertised as fixtures still to come, and all of them reading
		// as "starting soon" on the in-play board long after full time.
		//
		// Only the hard bound applies here, never the reconcile threshold: a
		// pending row has no live stamp to be stale against, so the clock since
		// kickoff is the only evidence there is. Settled and unknown keep their
		// exemption.
		if state.Pending() && PastHardBound(observation, now) {
			return FreshnessLost
		}
		return FreshnessCurrent
	}

	byDuration := FreshnessCurrent
	if !observation.KickoffAt.IsZero() {
		since := now.Sub(observation.KickoffAt)
		switch {
		case since >= HardBound:
			byDuration = FreshnessLost
		case since >= PlausibleDuration:
			byDuration = FreshnessReconciling
		}
	}

	byFreshness := FreshnessCurrent
	if observation.LiveStateAt.IsZero() {
		// Never confirmed. Not evidence of staleness on its own -- the column
		// is new -- so this leans on the duration bound alone rather than
		// declaring every backfilled row lost.
		return byDuration
	}
	age := now.Sub(observation.LiveStateAt)
	switch {
	case age >= HideAfter:
		byFreshness = FreshnessLost
	case age >= ReconcileAfter:
		byFreshness = FreshnessReconciling
	case age >= StaleAfter:
		byFreshness = FreshnessStale
	}

	if byDuration > byFreshness {
		return byDuration
	}
	return byFreshness
}

// ClockIsCurrent reports whether the stored minute may be presented as the
// current match minute.
//
// This is the single question the API answers for the web app. The web app
// renders `status_elapsed` directly and has no way to judge its age, which is
// why an hours-old "2H" reads as authoritative today.
func ClockIsCurrent(observation Observation, now time.Time) bool {
	return Assess(observation, now) == FreshnessCurrent
}

// ShouldReconcile reports whether a match is overdue enough to be worth a
// provider request.
func ShouldReconcile(observation Observation, now time.Time) bool {
	freshness := Assess(observation, now)
	return freshness == FreshnessReconciling || freshness == FreshnessLost
}

// PastHardBound reports whether a match has exceeded every plausible duration.
// A match here stops claiming to be in play whether or not reconciliation
// succeeded.
func PastHardBound(observation Observation, now time.Time) bool {
	if observation.KickoffAt.IsZero() {
		return false
	}
	return now.Sub(observation.KickoffAt) >= HardBound
}

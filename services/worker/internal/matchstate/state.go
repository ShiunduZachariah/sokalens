// Package matchstate bounds what a live match is allowed to claim.
//
// The defect it exists for is the one users see: a match that reads "second
// half" three hours after kickoff. The cause is not a clock that runs too fast
// -- nothing in this system ticks a match minute forward. It is the opposite.
// A fixture stops appearing in the provider's live feed, no later observation
// contradicts what was last written, and `matches.status` keeps saying
// `second_half` forever. The web app renders `status_elapsed` faithfully; it
// simply has no way to know the number is hours old, because until now nothing
// recorded when that number was last confirmed.
//
// So the fix is in three parts, and this package holds the vocabulary for all
// three:
//
//  1. Freshness. Every live observation stamps `matches.live_state_at`. A
//     status with no recent confirmation is stale, and stale is a thing the
//     API can say out loud instead of presenting an hours-old minute as
//     current.
//  2. Legal transitions. A finished match must not silently become live again.
//     The upsert already guards that one direction; this makes the whole
//     matrix explicit and rejects the rest rather than writing them.
//  3. A hard bound. Ninety minutes of football cannot last four hours. Past a
//     plausible duration a non-terminal match is reconciled, and if that does
//     not resolve it, it stops claiming to be in play.
package matchstate

import "strings"

// State is the canonical match status vocabulary already stored in
// `matches.status` and already rendered by `apps/web/app/lib/matchStatus.ts`.
// It is mirrored rather than redefined: inventing a second vocabulary here
// would need a mapping layer, and a mapping layer is somewhere for the two to
// disagree.
type State string

const (
	StateScheduled   State = "scheduled"
	StateUpcoming    State = "upcoming"
	StateDelayed     State = "delayed"
	StateLive        State = "live"
	StateHalftime    State = "halftime"
	StateSecondHalf  State = "second_half"
	StateExtraTime   State = "extra_time"
	StatePenalties   State = "penalties"
	StateBreak       State = "break"
	StateInterrupted State = "interrupted"
	StateSuspended   State = "suspended"
	StateFinished    State = "finished"
	StatePostponed   State = "postponed"
	StateCanceled    State = "canceled"
	StateAbandoned   State = "abandoned"
	StateAwarded     State = "awarded"
	StateWalkover    State = "walkover"

	// StateUnknown is where the reaper puts a match that is past every
	// plausible duration and that reconciliation could not resolve.
	//
	// It is deliberately not "finished". Calling an unresolved match finished
	// invents a result, and a result is the one thing this product must never
	// invent -- it would settle predictions against a scoreline no provider
	// ever confirmed. Unknown is a safe state; wrong is not.
	StateUnknown State = "unknown"
)

// Normalize maps a stored value onto the vocabulary. Historical rows and the
// football-data importer use a few provider spellings directly, so this
// tolerates them rather than treating them as unrecognised.
func Normalize(value string) State {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "scheduled":
		return StateScheduled
	case "upcoming", "ns", "tbd", "":
		return StateUpcoming
	case "delayed":
		return StateDelayed
	case "live", "1h":
		return StateLive
	case "halftime", "ht":
		return StateHalftime
	case "second_half", "2h":
		return StateSecondHalf
	case "extra_time", "et":
		return StateExtraTime
	case "penalties", "p", "pen_live":
		return StatePenalties
	case "break", "bt":
		return StateBreak
	case "interrupted", "int":
		return StateInterrupted
	case "suspended", "susp":
		return StateSuspended
	case "finished", "ft", "aet", "pen":
		return StateFinished
	case "postponed", "pst":
		return StatePostponed
	case "canceled", "cancelled", "canc":
		return StateCanceled
	case "abandoned", "abd":
		return StateAbandoned
	case "awarded", "awd":
		return StateAwarded
	case "walkover", "wo":
		return StateWalkover
	case "unknown":
		return StateUnknown
	}
	return StateUnknown
}

// inPlay is the set that claims a match is happening right now. These are the
// only states that can go stale, because they are the only ones making a claim
// about the present moment. A finished match is just as true an hour later.
var inPlay = map[State]bool{
	StateLive:       true,
	StateHalftime:   true,
	StateSecondHalf: true,
	StateExtraTime:  true,
	StatePenalties:  true,
	StateBreak:      true,
}

// settled is the set a provider has given an outcome for. These will not
// change again through normal play, and re-opening one is always wrong.
//
// `unknown` is deliberately absent, and its absence is the point. Unknown is
// not an outcome -- it is the admission that we do not have one. Holding it in
// this set made it terminal in the literal sense: `Allowed` rejected every
// transition out of it, so a match the reaper had given up on could never
// accept the `FT` the provider published an hour later. Production reached 922
// matches sitting at unknown, every one of them already carrying a score.
//
// A state nobody can leave is a dead end, and a dead end is not a safe state,
// whatever the comment on StateUnknown says. Unknown is safe only while it
// stays a waiting room.
var settled = map[State]bool{
	StateFinished:  true,
	StatePostponed: true,
	StateCanceled:  true,
	StateAbandoned: true,
	StateAwarded:   true,
	StateWalkover:  true,
}

// pending is the set before a ball is kicked.
var pending = map[State]bool{
	StateScheduled: true,
	StateUpcoming:  true,
	StateDelayed:   true,
}

func (s State) InPlay() bool  { return inPlay[s] }
func (s State) Settled() bool { return settled[s] }
func (s State) Pending() bool { return pending[s] }

// Quiescent reports whether a state makes no claim about the present moment.
//
// Settled and pending states qualify for opposite reasons -- one is over, the
// other has not begun -- and unknown qualifies because it asserts nothing at
// all. None of them can go stale, so none of them is worth reaping. This is
// the distinction `Terminal` used to blur: freshness wanted "claims nothing"
// and the transition matrix wanted "has an outcome", and unknown answers those
// two questions differently.
func (s State) Quiescent() bool { return settled[s] || pending[s] || s == StateUnknown }

// Interrupted and suspended are neither: play has started and may resume, so
// they are bounded by the reaper like an in-play state but do not advertise a
// running clock.
func (s State) Halted() bool {
	return s == StateInterrupted || s == StateSuspended
}

// TransitionRejection explains why a write was refused. It is a value rather
// than an error because the caller logs it and keeps the old state; nothing
// upstream can act on it.
type TransitionRejection struct {
	From   State
	To     State
	Reason string
}

// Allowed reports whether a provider observation may move a match from one
// state to another.
//
// The rule that matters is that settled states are settled. A finished match
// reappearing as `2H` is either a provider glitch or a fixture-identity
// collision, and writing it produces exactly the display corruption this
// package exists to prevent -- a settled match that starts claiming to be in
// play. The upsert in `catalog_repository.go` already guards finished-to-live;
// this makes the whole matrix explicit so the other settled states are guarded
// too, and so the rule is testable in one place instead of inferred from SQL.
//
// Unknown is not settled, so every transition out of it is permitted. That is
// the whole mechanism by which a reaped match recovers: the provider publishes
// `FT`, the fixtures ingest normalises it to `finished`, and this lets the
// write land. Including an unknown match that turns out to still be playing --
// the reaper will mark it again if it goes stale a second time, and a fresh
// observation should always beat an old guess.
func Allowed(from, to State) (bool, TransitionRejection) {
	if from == to {
		return true, TransitionRejection{}
	}
	if to == StateUnknown {
		// Only the reaper produces unknown, and only from a state with no
		// outcome yet. It is never a provider observation.
		if from.Settled() {
			return false, TransitionRejection{From: from, To: to, Reason: "settled state cannot become unknown"}
		}
		return true, TransitionRejection{}
	}
	if from.Settled() {
		// A postponed fixture that is later played arrives as a new kickoff
		// time, which is a different row under the (league, teams, kickoff)
		// key -- so re-opening this row is never the right answer.
		return false, TransitionRejection{From: from, To: to, Reason: "match already has a settled outcome"}
	}
	if to.Pending() && !from.Pending() {
		// Once a match has started it cannot go back to being scheduled.
		// Postponed and abandoned are the legitimate ways to say "this is not
		// happening", and both are settled.
		//
		// Unknown gets no exemption here. A genuinely rescheduled fixture comes
		// back as a new row under the (league, teams, kickoff) key, so letting
		// an unknown row return to `scheduled` would not recover that fixture --
		// it would only make a match that was played read as upcoming, and put
		// it back in front of readers as something to predict.
		return false, TransitionRejection{From: from, To: to, Reason: "a started match cannot return to a pending state"}
	}
	return true, TransitionRejection{}
}

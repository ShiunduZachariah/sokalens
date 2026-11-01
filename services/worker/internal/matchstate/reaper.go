package matchstate

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"
)

// ReaperJobKey is the scheduled sweep that bounds non-terminal matches.
const (
	ReaperJobKey = "matchstate.reap-stuck-matches"
	Queue        = "maintenance"
)

// StuckMatch is one row the sweep found still claiming to be in play.
type StuckMatch struct {
	MatchID     int64
	State       State
	KickoffAt   time.Time
	LiveStateAt time.Time
}

// Store is the reaper's view of the database.
//
// Deliberately narrow. The reaper reads a bounded set of overdue rows and
// writes one column on the ones past every plausible duration; giving it a
// wider interface would make it easy for a later change to have it start
// editing scores, which is the one thing it must never do.
type Store interface {
	// StuckMatches returns non-terminal matches whose live claim is overdue,
	// most overdue first, capped at limit.
	StuckMatches(ctx context.Context, now time.Time, limit int) ([]StuckMatch, error)
	// MarkUnknown moves a match out of its live state. It must refuse to
	// change a match whose state has moved on since it was read.
	MarkUnknown(ctx context.Context, matchID int64, from State, now time.Time) (bool, error)
}

// Reconciler asks for a fresh provider observation of a set of matches.
//
// It is expressed in whole days rather than fixture ids on purpose. The
// provider adapter has no `ids=` branch yet (in-play architecture 4.1 flags
// this and the repository confirms it), and adding one is a separate piece of
// work. A date-scoped refresh reuses a capability that already exists, and it
// costs one request per distinct date rather than one per stuck fixture --
// which is cheaper than the per-fixture call the architecture assumed, because
// stuck matches cluster on the same day.
type Reconciler interface {
	ReconcileDate(ctx context.Context, day time.Time) error
}

// ResultResolver asks for the final result of specific matches, by id.
//
// Distinct from Reconciler, which refreshes a whole date. A match the reaper
// has just given up on is three days old on average, and a date refresh only
// reaches the provider's -3..+7 day window, so the matches most in need of a
// result are the ones a date sweep can no longer see. Asking by fixture id has
// no such horizon.
type ResultResolver interface {
	ResolveMatches(ctx context.Context, matchIDs []int64) error
}

// Reaper is the two-minute sweep.
//
// It is the part of this package that actually prevents "second half, three
// hours". Everything else describes the problem; this ends it.
type Reaper struct {
	Store      Store
	Reconciler Reconciler
	// Resolver turns "unknown" back into a real status.
	//
	// unknown is not a football state -- a match is upcoming, in play,
	// finished, postponed or abandoned. It exists here only to stop a match
	// claiming to be in play forever, and it must be a moment, not a
	// destination: production accumulated 922 of them, every single one
	// already carrying a score, averaging three days old.
	//
	// So the sweep that marks a match unknown immediately asks for its result.
	// Optional: without it the behaviour is exactly as before.
	Resolver ResultResolver
	Logger   *slog.Logger
	Now      func() time.Time
	// Limit caps one sweep. A backlog is worked through over successive runs
	// rather than in one long transaction: the sweep runs every two minutes
	// and there is no deadline by which a stuck row must be resolved.
	Limit int
}

// Result is what one sweep did, for logging and tests.
type Result struct {
	Examined      int
	Reconciled    int
	MarkedUnknown int
	Skipped       int
}

func (r Reaper) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r Reaper) logger() *slog.Logger {
	if r.Logger != nil {
		return r.Logger
	}
	return slog.Default()
}

// Sweep bounds every overdue match it can see.
//
// Two things happen, and the order matters. A match past the hard bound stops
// claiming to be in play immediately, whether or not a provider ever answers
// -- that is the user-visible fix, and it must not depend on a request
// succeeding. A match that is merely overdue is queued for a refresh and left
// alone, because the likeliest explanation is still that it is being played
// and we missed a poll.
func (r Reaper) Sweep(ctx context.Context) (Result, error) {
	if r.Store == nil {
		return Result{}, errors.New("matchstate store is required")
	}
	limit := r.Limit
	if limit <= 0 {
		limit = 200
	}
	now := r.now()
	stuck, err := r.Store.StuckMatches(ctx, now, limit)
	if err != nil {
		return Result{}, err
	}

	result := Result{Examined: len(stuck)}
	days := map[time.Time]bool{}
	markedUnknown := make([]int64, 0, 8)
	for _, match := range stuck {
		observation := Observation{State: match.State, KickoffAt: match.KickoffAt, LiveStateAt: match.LiveStateAt}
		if !ShouldReconcile(observation, now) {
			// The query is a filter, not the decision. Assess is the single
			// place the thresholds live, so a row that no longer qualifies by
			// the time it is read is left alone.
			result.Skipped++
			continue
		}

		if PastHardBound(observation, now) {
			allowed, rejection := Allowed(match.State, StateUnknown)
			if !allowed {
				r.logger().Warn("reaper refused an illegal transition",
					"match_id", match.MatchID, "from", string(rejection.From),
					"to", string(rejection.To), "reason", rejection.Reason)
				result.Skipped++
				continue
			}
			changed, err := r.Store.MarkUnknown(ctx, match.MatchID, match.State, now)
			if err != nil {
				return result, err
			}
			if changed {
				result.MarkedUnknown++
				markedUnknown = append(markedUnknown, match.MatchID)
				r.logger().Warn("match passed every plausible duration and stopped claiming to be in play",
					"match_id", match.MatchID,
					"from", string(match.State),
					"minutes_since_kickoff", int(now.Sub(match.KickoffAt).Minutes()),
					"live_state_age_seconds", liveStateAge(observation, now),
				)
			} else {
				// Something else moved it while this sweep was running. That
				// is the good outcome, not a conflict to retry.
				result.Skipped++
			}
			continue
		}

		// Overdue but still plausible: ask for a fresh look.
		days[match.KickoffAt.UTC().Truncate(24*time.Hour)] = true
	}

	// Resolve what was just given up on, before anything else. A match that
	// has stopped claiming to be in play has almost always finished, and the
	// provider knows the score; leaving it to a later sweep is what turned a
	// transient marker into a population.
	if r.Resolver != nil && len(markedUnknown) > 0 {
		if err := r.Resolver.ResolveMatches(ctx, markedUnknown); err != nil {
			// Same reasoning as the date reconciliation below: the state
			// change has already landed and the next sweep is two minutes
			// away, so a failed enqueue is not worth abandoning the sweep.
			r.logger().Error("reaper could not queue a result resolution",
				"matches", len(markedUnknown), "error", err)
		} else {
			r.logger().Info("queued result resolution for matches that stopped claiming to be in play",
				"matches", len(markedUnknown))
		}
	}

	if r.Reconciler != nil && len(days) > 0 {
		ordered := make([]time.Time, 0, len(days))
		for day := range days {
			ordered = append(ordered, day)
		}
		sort.Slice(ordered, func(a, b int) bool { return ordered[a].Before(ordered[b]) })
		for _, day := range ordered {
			if err := r.Reconciler.ReconcileDate(ctx, day); err != nil {
				// A failed enqueue is not worth abandoning the sweep: the
				// hard-bound work above has already landed, and the next
				// sweep is two minutes away.
				r.logger().Error("reaper could not queue a reconciliation",
					"date", day.Format(time.DateOnly), "error", err)
				continue
			}
			result.Reconciled++
		}
	}

	if result.MarkedUnknown > 0 || result.Reconciled > 0 {
		r.logger().Info("match state reaper swept",
			"examined", result.Examined,
			"reconciliation_days", result.Reconciled,
			"marked_unknown", result.MarkedUnknown,
			"skipped", result.Skipped,
		)
	}
	return result, nil
}

func liveStateAge(observation Observation, now time.Time) int {
	if observation.LiveStateAt.IsZero() {
		return -1
	}
	return int(now.Sub(observation.LiveStateAt).Seconds())
}

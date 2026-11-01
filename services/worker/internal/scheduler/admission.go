package scheduler

import (
	"context"
	"errors"
)

// Admission control for the worker's handler budget.
//
// The worker is the backbone: every provider call, every prediction run, every
// ingest passes through it. So the interesting failure is not a crash, which
// is loud and restarts. It is the worker staying up, reporting healthy, and
// quietly not doing one particular kind of work.
//
// That happened on 2026-09-21. A restart left 4,076 team-history backfill jobs
// queued. The backfill loops claimed thirteen of the shared in-flight slots
// within seconds of boot, live match polling got none, and in-play data
// stopped for forty minutes. Every queue was healthy. Every worker was busy.
// The heartbeat was green throughout.
//
// The cause was that a single shared budget is first-come-first-served, and a
// queue with a standing backlog always comes first. Fairness is not something
// you can add by tuning concurrency numbers, because the numbers are per queue
// and the contention is global.
//
// So a queue may hold a reservation: slots it can always take back, whatever
// else is running. Beyond that it competes for a shared pool as before. The
// reservation is the part that makes starvation impossible rather than merely
// unlikely, and the shared pool is the part that keeps the machine busy when
// the reserved queues are idle -- which is most of the time.

// admission hands out the right to run one handler.
type admission struct {
	// shared is the common pool every queue may draw from.
	shared chan struct{}
	// reserved holds one channel per queue that asked for a guarantee.
	reserved map[string]chan struct{}
}

// newAdmission partitions the budget.
//
// Reservations come off the top; whatever is left is shared. A configuration
// that reserves everything would leave no shared pool and turn every
// unreserved queue into a queue that never runs, so the split is bounded: at
// least one slot always stays shared, and reservations are scaled back to fit
// rather than silently over-subscribing.
func newAdmission(maxConcurrent int, workers []QueueWorker) *admission {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}

	wanted := 0
	for _, worker := range workers {
		if worker.Reserved > 0 {
			wanted += reservationFor(worker)
		}
	}

	// Never let reservations consume the whole budget. An unreserved queue
	// with no shared slot is a queue that never runs, which is the same
	// starvation this exists to prevent, arrived at from the other side.
	ceiling := maxConcurrent - 1
	if ceiling < 0 {
		ceiling = 0
	}
	scale := 1.0
	if wanted > ceiling && wanted > 0 {
		scale = float64(ceiling) / float64(wanted)
	}

	a := &admission{
		reserved: make(map[string]chan struct{}, len(workers)),
	}
	granted := 0
	for _, worker := range workers {
		want := reservationFor(worker)
		if want <= 0 {
			continue
		}
		give := int(float64(want) * scale)
		// Scaling must not round a reservation away to nothing. A queue that
		// asked for a guarantee and received zero is worse than one that never
		// asked, because the configuration says it is protected and it is not.
		if give < 1 && ceiling-granted > 0 {
			give = 1
		}
		if give > ceiling-granted {
			give = ceiling - granted
		}
		if give <= 0 {
			continue
		}
		a.reserved[worker.Queue] = make(chan struct{}, give)
		granted += give
	}

	a.shared = make(chan struct{}, maxConcurrent-granted)
	return a
}

// reservationFor bounds what a queue may ask for.
//
// A reservation larger than the queue's own concurrency is meaningless -- the
// queue cannot run more handlers than it has loops -- and would quietly shrink
// the shared pool for slots nobody can use.
func reservationFor(worker QueueWorker) int {
	if worker.Reserved <= 0 {
		return 0
	}
	concurrency := worker.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if worker.Reserved > concurrency {
		return concurrency
	}
	return worker.Reserved
}

// acquire takes a slot for one handler and returns its release.
//
// The reserved pool is tried first and without blocking. That order is the
// whole mechanism: a queue holding a reservation skips the queue of everyone
// waiting on the shared pool, so a backlog elsewhere cannot delay it.
func (a *admission) acquire(ctx context.Context, queue string) (func(), error) {
	if a == nil {
		return nil, errors.New("admission control is not configured")
	}
	if pool, ok := a.reserved[queue]; ok {
		select {
		case pool <- struct{}{}:
			return func() { <-pool }, nil
		default:
			// Reservation is fully used by this queue's own handlers. Fall
			// through and compete for the shared pool like everyone else.
		}
	}
	select {
	case a.shared <- struct{}{}:
		return func() { <-a.shared }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// capacity reports the total handlers that may run at once. Used by tests to
// assert the partition never over-subscribes the pool it was given.
func (a *admission) capacity() int {
	total := cap(a.shared)
	for _, pool := range a.reserved {
		total += cap(pool)
	}
	return total
}

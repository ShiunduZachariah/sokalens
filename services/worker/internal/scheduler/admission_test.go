package scheduler

import (
	"context"
	"testing"
	"time"
)

// The 2026-09-21 starvation, as a test.
//
// A restart left 4,076 team-history backfill jobs queued. The backfill loops
// claimed thirteen of the shared in-flight slots within seconds of boot, live
// match polling got none, and in-play data stopped for forty minutes. Every
// queue was healthy, every worker was busy, the heartbeat was green.
//
// These tests are about the property that made that possible -- a single
// first-come-first-served budget -- rather than about the particular queues.

func TestABacklogCannotStarveAReservedQueue(t *testing.T) {
	// The outage, reproduced. A queue with unlimited work saturates everything
	// it can take; the reserved queue must still get in, immediately.
	a := newAdmission(14, []QueueWorker{
		{Queue: "data-backfill", Concurrency: 10},
		{Queue: "provider-fixtures", Concurrency: 10, Reserved: 4},
	})

	// Backfill grabs everything it possibly can. The short deadline is how
	// the loop learns the pool is full: acquire blocks by design, which is the
	// behaviour that makes a full pool back-pressure rather than over-admit.
	saturate, stopSaturating := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer stopSaturating()
	var held []func()
	for range 100 {
		release, err := a.acquire(saturate, "data-backfill")
		if err != nil {
			break
		}
		held = append(held, release)
	}
	defer func() {
		for _, release := range held {
			release()
		}
	}()

	if len(held) == 0 {
		t.Fatal("backfill could not acquire anything; the pool is misconfigured")
	}

	// The reserved queue must get its slots regardless, and without waiting.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	for i := range 4 {
		release, err := a.acquire(ctx, "provider-fixtures")
		if err != nil {
			t.Fatalf("reserved slot %d denied while backfill held %d slots -- this is the outage", i, len(held))
		}
		defer release()
	}
}

func TestAnUnreservedQueueStillRunsWhenReservedQueuesAreIdle(t *testing.T) {
	// The reservation must not be a permanent hole in the budget. Reserved
	// slots sit idle most of the time -- live polling is one job every thirty
	// seconds -- and a machine that cannot use them is a slower machine for no
	// benefit.
	a := newAdmission(10, []QueueWorker{
		{Queue: "provider-fixtures", Concurrency: 4, Reserved: 4},
		{Queue: "data-backfill", Concurrency: 10},
	})
	ctx := context.Background()
	got := 0
	var held []func()
	for range 6 {
		release, err := a.acquire(ctx, "data-backfill")
		if err != nil {
			break
		}
		held = append(held, release)
		got++
	}
	for _, release := range held {
		release()
	}
	if got < 6 {
		t.Fatalf("an unreserved queue got only %d of the 6 shared slots while reserved queues were idle", got)
	}
}

func TestTotalCapacityNeverExceedsTheBudget(t *testing.T) {
	// The budget exists because handlers hold database connections; the pool
	// is 24 and over-subscribing it means a claimed job blocking on a
	// connection it needs, which is a slower version of the same outage.
	cases := []struct {
		name    string
		max     int
		workers []QueueWorker
	}{
		{"no reservations", 10, []QueueWorker{{Queue: "a", Concurrency: 5}}},
		{"one reservation", 10, []QueueWorker{{Queue: "a", Concurrency: 5, Reserved: 3}}},
		{"reservations exactly fill", 10, []QueueWorker{
			{Queue: "a", Concurrency: 5, Reserved: 5},
			{Queue: "b", Concurrency: 5, Reserved: 5},
		}},
		{"reservations over-subscribe", 6, []QueueWorker{
			{Queue: "a", Concurrency: 10, Reserved: 10},
			{Queue: "b", Concurrency: 10, Reserved: 10},
			{Queue: "c", Concurrency: 10, Reserved: 10},
		}},
		{"single slot", 1, []QueueWorker{{Queue: "a", Concurrency: 4, Reserved: 4}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newAdmission(tc.max, tc.workers)
			if got := a.capacity(); got > tc.max {
				t.Fatalf("capacity = %d, over-subscribes the budget of %d", got, tc.max)
			}
			if a.capacity() < 1 {
				t.Fatal("capacity is zero; nothing would ever run")
			}
		})
	}
}

func TestSomeSharedCapacityAlwaysSurvives(t *testing.T) {
	// Reserving the entire budget turns every unreserved queue into a queue
	// that never runs -- the same starvation, reached from the other side.
	a := newAdmission(4, []QueueWorker{
		{Queue: "a", Concurrency: 10, Reserved: 10},
		{Queue: "b", Concurrency: 10, Reserved: 10},
	})
	if cap(a.shared) < 1 {
		t.Fatal("reservations consumed the whole budget; an unreserved queue could never run")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	release, err := a.acquire(ctx, "never-heard-of-it")
	if err != nil {
		t.Fatal("an unreserved queue was denied entirely")
	}
	release()
}

func TestAGrantedReservationIsNeverRoundedAwayToNothing(t *testing.T) {
	// Scaling down an over-subscribed configuration must not silently leave a
	// queue with zero. A queue whose config says it is protected and is not is
	// worse than one that never asked, because nobody goes looking.
	a := newAdmission(5, []QueueWorker{
		{Queue: "a", Concurrency: 10, Reserved: 10},
		{Queue: "b", Concurrency: 10, Reserved: 10},
		{Queue: "c", Concurrency: 10, Reserved: 10},
	})
	for _, queue := range []string{"a", "b", "c"} {
		pool, ok := a.reserved[queue]
		if !ok || cap(pool) < 1 {
			t.Fatalf("queue %q asked for a reservation and got none", queue)
		}
	}
}

func TestAReservationLargerThanTheQueueIsClamped(t *testing.T) {
	// A queue cannot run more handlers than it has loops, so reserving more
	// would shrink the shared pool for slots nobody can occupy.
	got := reservationFor(QueueWorker{Queue: "a", Concurrency: 2, Reserved: 9})
	if got != 2 {
		t.Fatalf("reservation = %d for a queue with concurrency 2, want 2", got)
	}
	if got := reservationFor(QueueWorker{Queue: "a", Concurrency: 0, Reserved: 5}); got != 1 {
		t.Fatalf("reservation = %d for a queue with unset concurrency, want 1", got)
	}
	if got := reservationFor(QueueWorker{Queue: "a", Concurrency: 5}); got != 0 {
		t.Fatalf("reservation = %d for a queue that asked for none, want 0", got)
	}
	if got := reservationFor(QueueWorker{Queue: "a", Concurrency: 5, Reserved: -3}); got != 0 {
		t.Fatalf("reservation = %d for a negative request, want 0", got)
	}
}

func TestAnExhaustedReservationFallsBackToSharedRatherThanBlocking(t *testing.T) {
	// A reservation is a floor, not a ceiling. Once a queue has used its
	// guaranteed slots it should still compete for whatever else is free.
	a := newAdmission(6, []QueueWorker{{Queue: "a", Concurrency: 6, Reserved: 2}})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	var held []func()
	for i := range 6 {
		release, err := a.acquire(ctx, "a")
		if err != nil {
			t.Fatalf("acquire %d failed; a reserved queue could not reach the shared pool", i)
		}
		held = append(held, release)
	}
	for _, release := range held {
		release()
	}
}

func TestAcquireRespectsCancellationRatherThanHangingForever(t *testing.T) {
	// A worker shutting down must not be held open by a full pool. This is the
	// difference between a rolling deploy and one that needs a kill -9.
	a := newAdmission(1, []QueueWorker{{Queue: "a", Concurrency: 1}})
	release, err := a.acquire(context.Background(), "a")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.acquire(ctx, "a"); err == nil {
		t.Fatal("acquire on a cancelled context returned a slot")
	}
}

func TestReleasingReturnsTheSlot(t *testing.T) {
	// The obvious one, and the one whose absence would leak the pool to zero
	// over hours and present as the worker silently stopping.
	a := newAdmission(1, []QueueWorker{{Queue: "a", Concurrency: 1}})
	for i := range 20 {
		release, err := a.acquire(context.Background(), "a")
		if err != nil {
			t.Fatalf("acquire %d failed; slots are leaking", i)
		}
		release()
	}
}

func TestANilAdmissionIsAnErrorNotAPanic(t *testing.T) {
	// A misconfigured runtime should report, not crash the worker process and
	// take every queue down with it.
	var a *admission
	if _, err := a.acquire(context.Background(), "a"); err == nil {
		t.Fatal("nil admission handed out a slot")
	}
}

func TestZeroAndNegativeBudgetsStillRunSomething(t *testing.T) {
	// Config arrives from the environment. A zero must degrade to "one at a
	// time", never to "nothing ever runs" -- a worker that starts, reports
	// healthy and processes no jobs is the worst of both outcomes.
	for _, max := range []int{0, -5} {
		a := newAdmission(max, []QueueWorker{{Queue: "a", Concurrency: 4}})
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		release, err := a.acquire(ctx, "a")
		cancel()
		if err != nil {
			t.Fatalf("max=%d: nothing could run at all", max)
		}
		release()
	}
}

func TestNoWorkersConfiguredStillYieldsAUsablePool(t *testing.T) {
	a := newAdmission(4, nil)
	if a.capacity() != 4 {
		t.Fatalf("capacity = %d with no workers, want the full budget", a.capacity())
	}
}

func TestConcurrentAcquireNeverExceedsCapacity(t *testing.T) {
	// The property under real contention rather than in sequence. If this can
	// over-admit, the connection pool is the thing that discovers it.
	const budget = 8
	a := newAdmission(budget, []QueueWorker{
		{Queue: "hot", Concurrency: 8, Reserved: 3},
		{Queue: "cold", Concurrency: 8},
	})

	live := make(chan struct{}, budget+16)
	done := make(chan bool, 64)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for i := range 64 {
		queue := "cold"
		if i%2 == 0 {
			queue = "hot"
		}
		go func(queue string) {
			release, err := a.acquire(ctx, queue)
			if err != nil {
				done <- false
				return
			}
			select {
			case live <- struct{}{}:
			default:
				done <- false
				return
			}
			time.Sleep(time.Millisecond)
			<-live
			release()
			done <- true
		}(queue)
	}
	for range 64 {
		<-done
	}
	if len(live) != 0 {
		t.Fatalf("%d slots still held after every goroutine finished", len(live))
	}
}

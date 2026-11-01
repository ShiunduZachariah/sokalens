package matchstate

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeStore struct {
	rows      []StuckMatch
	marked    []int64
	markedAs  map[int64]State
	conflict  map[int64]bool
	readErr   error
	writeErr  error
	readCalls int
}

func (f *fakeStore) StuckMatches(context.Context, time.Time, int) ([]StuckMatch, error) {
	f.readCalls++
	return f.rows, f.readErr
}

func (f *fakeStore) MarkUnknown(_ context.Context, matchID int64, from State, _ time.Time) (bool, error) {
	if f.writeErr != nil {
		return false, f.writeErr
	}
	if f.conflict[matchID] {
		return false, nil
	}
	f.marked = append(f.marked, matchID)
	if f.markedAs == nil {
		f.markedAs = map[int64]State{}
	}
	f.markedAs[matchID] = from
	return true, nil
}

type fakeReconciler struct {
	days []time.Time
	err  error
}

func (f *fakeReconciler) ReconcileDate(_ context.Context, day time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.days = append(f.days, day)
	return nil
}

var kickoff = time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)

func TestSweepEndsTheThreeHourSecondHalf(t *testing.T) {
	// The reported defect. Four hours after kickoff, still claiming 2H.
	store := &fakeStore{rows: []StuckMatch{
		{MatchID: 77, State: StateSecondHalf, KickoffAt: kickoff, LiveStateAt: kickoff.Add(50 * time.Minute)},
	}}
	reconciler := &fakeReconciler{}
	result, err := Reaper{Store: store, Reconciler: reconciler, Now: func() time.Time { return kickoff.Add(4 * time.Hour) }}.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep returned error: %v", err)
	}
	if result.MarkedUnknown != 1 || len(store.marked) != 1 || store.marked[0] != 77 {
		t.Fatalf("result = %+v, marked = %v", result, store.marked)
	}
	// Past the hard bound the fix must not depend on a provider answering.
	if len(reconciler.days) != 0 {
		t.Fatalf("a match past the hard bound should be bounded directly, not queued: %v", reconciler.days)
	}
}

func TestSweepQueuesAReconciliationBeforeTheHardBound(t *testing.T) {
	// Overdue, but still plausibly being played: ask, do not conclude.
	now := kickoff.Add(80 * time.Minute)
	store := &fakeStore{rows: []StuckMatch{
		{MatchID: 12, State: StateSecondHalf, KickoffAt: kickoff, LiveStateAt: now.Add(-8 * time.Minute)},
	}}
	reconciler := &fakeReconciler{}
	result, err := Reaper{Store: store, Reconciler: reconciler, Now: func() time.Time { return now }}.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep returned error: %v", err)
	}
	if len(store.marked) != 0 {
		t.Fatalf("a match inside the hard bound must not be concluded: %v", store.marked)
	}
	if result.Reconciled != 1 || len(reconciler.days) != 1 || !reconciler.days[0].Equal(kickoff.Truncate(24*time.Hour)) {
		t.Fatalf("result = %+v, days = %v", result, reconciler.days)
	}
}

func TestReconciliationIsOnePerDayNotOnePerFixture(t *testing.T) {
	// Stuck matches cluster on the same day -- a provider outage during a
	// Saturday afternoon strands the whole slate. One request per date is the
	// difference between a handful of requests and hundreds.
	// A late Saturday kickoff and an early Sunday one, both overdue on
	// freshness and both still inside the hard bound.
	now := time.Date(2026, 9, 7, 0, 30, 0, 0, time.UTC)
	saturday := time.Date(2026, 9, 6, 23, 0, 0, 0, time.UTC)
	sunday := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	rows := []StuckMatch{}
	for id := int64(1); id <= 40; id++ {
		rows = append(rows, StuckMatch{MatchID: id, State: StateSecondHalf, KickoffAt: saturday, LiveStateAt: now.Add(-8 * time.Minute)})
	}
	rows = append(rows, StuckMatch{MatchID: 99, State: StateLive, KickoffAt: sunday, LiveStateAt: now.Add(-9 * time.Minute)})
	store := &fakeStore{rows: rows}
	reconciler := &fakeReconciler{}
	if _, err := (Reaper{Store: store, Reconciler: reconciler, Now: func() time.Time { return now }}).Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep returned error: %v", err)
	}
	if len(store.marked) != 0 {
		t.Fatalf("matches inside the hard bound must not be concluded: %v", store.marked)
	}
	if len(reconciler.days) != 2 {
		t.Fatalf("41 stuck matches across 2 days produced %d requests: %v", len(reconciler.days), reconciler.days)
	}
	// Oldest first, so the longest-stranded day is refreshed first.
	if !reconciler.days[0].Before(reconciler.days[1]) {
		t.Fatalf("days not ordered: %v", reconciler.days)
	}
}

func TestSweepLeavesHealthyMatchesAlone(t *testing.T) {
	now := kickoff.Add(70 * time.Minute)
	store := &fakeStore{rows: []StuckMatch{
		{MatchID: 5, State: StateSecondHalf, KickoffAt: kickoff, LiveStateAt: now.Add(-20 * time.Second)},
	}}
	reconciler := &fakeReconciler{}
	result, err := Reaper{Store: store, Reconciler: reconciler, Now: func() time.Time { return now }}.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep returned error: %v", err)
	}
	if result.Skipped != 1 || len(store.marked) != 0 || len(reconciler.days) != 0 {
		t.Fatalf("a freshly polled match was acted on: %+v", result)
	}
}

func TestSweepNeverInventsAResult(t *testing.T) {
	// A match the reaper cannot resolve becomes unknown, never finished.
	// Calling it finished would settle predictions against a scoreline no
	// provider ever confirmed.
	store := &fakeStore{rows: []StuckMatch{
		{MatchID: 8, State: StateSecondHalf, KickoffAt: kickoff, LiveStateAt: kickoff.Add(50 * time.Minute)},
	}}
	if _, err := (Reaper{Store: store, Now: func() time.Time { return kickoff.Add(5 * time.Hour) }}).Sweep(context.Background()); err != nil {
		t.Fatalf("Sweep returned error: %v", err)
	}
	if store.markedAs[8] != StateSecondHalf {
		t.Fatalf("MarkUnknown was not given the state it read: %v", store.markedAs)
	}
	// The store contract carries `from` precisely so the write can refuse if
	// the row moved on; the reaper must always supply it.
}

func TestSweepTreatsAConcurrentUpdateAsSuccess(t *testing.T) {
	// A real provider observation landing mid-sweep is the outcome we wanted.
	store := &fakeStore{
		rows:     []StuckMatch{{MatchID: 3, State: StateSecondHalf, KickoffAt: kickoff, LiveStateAt: kickoff.Add(50 * time.Minute)}},
		conflict: map[int64]bool{3: true},
	}
	result, err := Reaper{Store: store, Now: func() time.Time { return kickoff.Add(4 * time.Hour) }}.Sweep(context.Background())
	if err != nil {
		t.Fatalf("a lost race must not be an error: %v", err)
	}
	if result.MarkedUnknown != 0 || result.Skipped != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestSweepStillBoundsMatchesWhenReconciliationIsBroken(t *testing.T) {
	// The user-visible fix must not depend on the provider queue working.
	now := kickoff.Add(4 * time.Hour)
	store := &fakeStore{rows: []StuckMatch{
		{MatchID: 1, State: StateSecondHalf, KickoffAt: kickoff, LiveStateAt: kickoff.Add(50 * time.Minute)},
		{MatchID: 2, State: StateLive, KickoffAt: now.Add(-80 * time.Minute), LiveStateAt: now.Add(-8 * time.Minute)},
	}}
	reconciler := &fakeReconciler{err: errors.New("queue unavailable")}
	result, err := Reaper{Store: store, Reconciler: reconciler, Now: func() time.Time { return now }}.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep returned error: %v", err)
	}
	if result.MarkedUnknown != 1 || result.Reconciled != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestSweepRequiresAStore(t *testing.T) {
	if _, err := (Reaper{}).Sweep(context.Background()); err == nil {
		t.Fatal("expected an error with no store")
	}
}

func TestSweepBoundsARowThatNeverHadAFreshnessStamp(t *testing.T) {
	// Every match stuck before this migration shipped looks like this.
	store := &fakeStore{rows: []StuckMatch{
		{MatchID: 42, State: StateSecondHalf, KickoffAt: kickoff},
	}}
	result, err := Reaper{Store: store, Now: func() time.Time { return kickoff.Add(6 * time.Hour) }}.Sweep(context.Background())
	if err != nil {
		t.Fatalf("Sweep returned error: %v", err)
	}
	if result.MarkedUnknown != 1 {
		t.Fatalf("a pre-migration stuck row was not bounded: %+v", result)
	}
}

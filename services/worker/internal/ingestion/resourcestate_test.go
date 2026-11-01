package ingestion

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

var testTier = TTLTier{Min: 60 * time.Second, Max: 4 * time.Minute}

func at(seconds int) time.Time {
	return time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC).Add(time.Duration(seconds) * time.Second)
}

func TestUnchangedResponsesClimbTowardsTheCeilingAndStop(t *testing.T) {
	// The worked example from 4.2: a 0-0 match with nothing happening backs off
	// to 3-4 minutes. Walk the whole ladder rather than one step, because the
	// bug this guards against is a climb that either never reaches the ceiling
	// or sails past it.
	state := ResourceState{Provider: "api-football", ResourceKey: "fixture_stats:1"}
	state = Advance(state, Observation{ResponseHash: "a", FetchedAt: at(0)}, testTier)
	if state.CurrentInterval != testTier.Min {
		t.Fatalf("first fetch interval = %v, want the floor %v", state.CurrentInterval, testTier.Min)
	}

	want := []time.Duration{
		90 * time.Second,  // 60 * 1.5
		135 * time.Second, // 90 * 1.5
		202500 * time.Millisecond,
		4 * time.Minute, // clamped: 303.75s would exceed the ceiling
		4 * time.Minute, // and stays there
	}
	for i, expected := range want {
		state = Advance(state, Observation{ResponseHash: "a", FetchedAt: at(i + 1)}, testTier)
		if state.CurrentInterval != expected {
			t.Fatalf("after %d unchanged fetches interval = %v, want %v", i+1, state.CurrentInterval, expected)
		}
		if state.UnchangedStreak != i+1 {
			t.Fatalf("after %d unchanged fetches streak = %d, want %d", i+1, state.UnchangedStreak, i+1)
		}
	}
	if state.CurrentInterval > testTier.Max {
		t.Fatalf("interval %v exceeded the ceiling %v", state.CurrentInterval, testTier.Max)
	}
}

func TestAChangedResponseDropsStraightBackToTheFloor(t *testing.T) {
	state := ResourceState{Provider: "api-football", ResourceKey: "fixture_stats:1"}
	for i := 0; i < 6; i++ {
		state = Advance(state, Observation{ResponseHash: "a", FetchedAt: at(i)}, testTier)
	}
	if state.CurrentInterval != testTier.Max {
		t.Fatalf("setup did not reach the ceiling: %v", state.CurrentInterval)
	}

	state = Advance(state, Observation{ResponseHash: "b", FetchedAt: at(10)}, testTier)
	if state.CurrentInterval != testTier.Min {
		t.Fatalf("changed response interval = %v, want the floor %v", state.CurrentInterval, testTier.Min)
	}
	if state.UnchangedStreak != 0 {
		t.Fatalf("changed response streak = %d, want 0", state.UnchangedStreak)
	}
}

func TestAGoalResetsToTheFloorEvenWhenTheHashIsUnchanged(t *testing.T) {
	// 4.2: "Goal or red card -> immediate reset to minimum, regardless of
	// streak." The hash being identical is exactly the case that would
	// otherwise back off -- a statistics payload can lag the score by a poll,
	// so the significant flag has to win over the hash comparison, not merely
	// tie with it.
	state := ResourceState{Provider: "api-football", ResourceKey: "fixture_stats:1"}
	for i := 0; i < 6; i++ {
		state = Advance(state, Observation{ResponseHash: "a", FetchedAt: at(i)}, testTier)
	}
	if state.CurrentInterval != testTier.Max {
		t.Fatalf("setup did not reach the ceiling: %v", state.CurrentInterval)
	}

	state = Advance(state, Observation{ResponseHash: "a", Significant: true, FetchedAt: at(20)}, testTier)
	if state.CurrentInterval != testTier.Min {
		t.Fatalf("interval after a goal = %v, want the floor %v", state.CurrentInterval, testTier.Min)
	}
	if state.UnchangedStreak != 0 {
		t.Fatalf("streak after a goal = %d, want 0", state.UnchangedStreak)
	}
	if want := at(20).Add(testTier.Min); !state.NextEligibleAt.Equal(want) {
		t.Fatalf("next eligible = %v, want %v", state.NextEligibleAt, want)
	}
}

func TestAnUnhashableResponseIsTreatedAsChangedNotUnchanged(t *testing.T) {
	// An empty hash means we could not tell what came back. Extending the
	// interval on that basis would let a resource we cannot read back off
	// forever -- silence read as stability.
	state := ResourceState{Provider: "api-football", ResourceKey: "fixture_stats:1"}
	for i := 0; i < 4; i++ {
		state = Advance(state, Observation{ResponseHash: "a", FetchedAt: at(i)}, testTier)
	}
	climbed := state.CurrentInterval
	if climbed <= testTier.Min {
		t.Fatalf("setup did not climb: %v", climbed)
	}

	state = Advance(state, Observation{ResponseHash: "", FetchedAt: at(10)}, testTier)
	if state.CurrentInterval != testTier.Min {
		t.Fatalf("unhashable response interval = %v, want the floor %v", state.CurrentInterval, testTier.Min)
	}
}

func TestTwoConsecutiveEmptyHashesDoNotCountAsUnchanged(t *testing.T) {
	// The subtle version of the above: prev hash "" and new hash "" are equal
	// as strings. If the comparison were a bare equality the second unreadable
	// response would start a streak, and a permanently broken resource would
	// climb to the ceiling and stay there looking healthy.
	state := ResourceState{Provider: "api-football", ResourceKey: "fixture_stats:1"}
	state = Advance(state, Observation{ResponseHash: "", FetchedAt: at(0)}, testTier)
	state = Advance(state, Observation{ResponseHash: "", FetchedAt: at(1)}, testTier)
	if state.UnchangedStreak != 0 {
		t.Fatalf("streak after two unreadable responses = %d, want 0", state.UnchangedStreak)
	}
	if state.CurrentInterval != testTier.Min {
		t.Fatalf("interval after two unreadable responses = %v, want the floor", state.CurrentInterval)
	}
}

func TestTerminalMarksImmutableAndOutranksEverythingElse(t *testing.T) {
	state := ResourceState{Provider: "api-football", ResourceKey: "fixture_stats:1"}
	state = Advance(state, Observation{
		ResponseHash: "a",
		Significant:  true, // would otherwise force the floor
		Terminal:     true,
		FetchedAt:    at(0),
	}, testTier)

	if !state.Immutable {
		t.Fatal("terminal observation did not mark the resource immutable")
	}
	if state.DueAt(at(1_000_000)) {
		t.Fatal("an immutable resource reported itself due")
	}
}

func TestDueAtTreatsNeverFetchedAsDueAndRespectsTheBoundary(t *testing.T) {
	fresh := ResourceState{Provider: "api-football", ResourceKey: "fixture_stats:1"}
	if !fresh.DueAt(at(0)) {
		t.Fatal("a resource that was never fetched reported itself not due")
	}

	state := Advance(fresh, Observation{ResponseHash: "a", FetchedAt: at(0)}, testTier)
	if state.DueAt(at(59)) {
		t.Fatal("resource was due one second early")
	}
	// Exactly at the boundary counts as due: a strict comparison here would
	// push every fetch one scheduler tick later than intended, and the drift
	// compounds across a match.
	if !state.DueAt(at(60)) {
		t.Fatal("resource was not due exactly at its next-eligible instant")
	}
}

func TestNormalizeRepairsRatherThanRejects(t *testing.T) {
	// A misconfigured tier must not fail open into unlimited fetching.
	got := TTLTier{Min: 0, Max: 0}.Normalize()
	if got.Min <= 0 || got.Max < got.Min {
		t.Fatalf("normalized zero tier = %+v, want a usable band", got)
	}
	inverted := TTLTier{Min: 2 * time.Minute, Max: time.Minute}.Normalize()
	if inverted.Max < inverted.Min {
		t.Fatalf("inverted tier = %+v, want max >= min", inverted)
	}
}

func TestClimbAlwaysBuysSomething(t *testing.T) {
	// With a sub-second floor, rounding by 1.5 can land back on the same value
	// and stall the ladder: the streak would grow while the interval never did.
	tiny := TTLTier{Min: time.Millisecond, Max: time.Minute}
	state := ResourceState{}
	state = Advance(state, Observation{ResponseHash: "a", FetchedAt: at(0)}, tiny)
	prev := state.CurrentInterval
	for i := 0; i < 3; i++ {
		state = Advance(state, Observation{ResponseHash: "a", FetchedAt: at(i + 1)}, tiny)
		if state.CurrentInterval <= prev {
			t.Fatalf("climb %d did not increase the interval: %v -> %v", i, prev, state.CurrentInterval)
		}
		prev = state.CurrentInterval
	}
}

func TestResourceKeyNamespacesByKind(t *testing.T) {
	if got, want := ResourceKey("fixture_stats", 1234567), "fixture_stats:1234567"; got != want {
		t.Fatalf("ResourceKey = %q, want %q", got, want)
	}
	// Two kinds sharing an id must not collide.
	if ResourceKey("fixture_stats", 7) == ResourceKey("fixture_odds", 7) {
		t.Fatal("different kinds produced the same key")
	}
}

func TestSaveNeverWritesANonPositiveInterval(t *testing.T) {
	// The column is CHECK (current_interval_s > 0). A sub-second interval
	// truncates to zero, which would turn a scheduling detail into a failed job.
	db := &fakeResourceStateDB{}
	repo := ResourceStateRepository{db: db}

	err := repo.Save(context.Background(), ResourceState{
		Provider:        "api-football",
		ResourceKey:     "fixture_stats:1",
		CurrentInterval: 500 * time.Millisecond,
		LastFetchedAt:   at(0),
		NextEligibleAt:  at(1),
	})
	if err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	call := db.call(t, 0)
	if got := call.args[5]; got != 1 {
		t.Fatalf("interval written = %v, want 1 second rather than a rejected zero", got)
	}
}

func TestSaveKeepsImmutabilityOneWay(t *testing.T) {
	// Asserted on the statement rather than a live database, because the
	// property lives in the ON CONFLICT clause: a later non-terminal write must
	// not un-settle a fixture and restart the spending.
	db := &fakeResourceStateDB{}
	repo := ResourceStateRepository{db: db}

	if err := repo.Save(context.Background(), ResourceState{
		Provider:        "api-football",
		ResourceKey:     "fixture_stats:1",
		CurrentInterval: time.Minute,
		LastFetchedAt:   at(0),
		NextEligibleAt:  at(60),
	}); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	query := db.call(t, 0).query
	if !strings.Contains(query, "provider_resource_state.immutable OR excluded.immutable") {
		t.Fatalf("upsert does not preserve immutability one-way:\n%s", query)
	}
}

func TestSaveRequiresProviderAndKey(t *testing.T) {
	repo := ResourceStateRepository{db: &fakeResourceStateDB{}}

	if err := repo.Save(context.Background(), ResourceState{ResourceKey: "k", CurrentInterval: time.Minute}); err == nil {
		t.Error("Save accepted a state with no provider")
	}
	if err := repo.Save(context.Background(), ResourceState{Provider: "p", CurrentInterval: time.Minute}); err == nil {
		t.Error("Save accepted a state with no resource key")
	}
}

// fakeResourceStateDB records every statement. Self-contained rather than
// reusing the request-log fake, so this package's other tests stay free to
// change their own fake without silently changing what these assert.
type fakeResourceStateCall struct {
	query string
	args  []any
}

type fakeResourceStateDB struct {
	calls []fakeResourceStateCall
	row   rowScanner
	err   error
}

func (f *fakeResourceStateDB) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	f.calls = append(f.calls, fakeResourceStateCall{query: query, args: args})
	return fakeResourceStateResult(1), f.err
}

func (f *fakeResourceStateDB) QueryRowContext(_ context.Context, _ string, _ ...any) rowScanner {
	return f.row
}

func (f *fakeResourceStateDB) call(t *testing.T, n int) fakeResourceStateCall {
	t.Helper()
	if len(f.calls) <= n {
		t.Fatalf("wanted statement %d, only %d were issued", n, len(f.calls))
	}
	return f.calls[n]
}

type fakeResourceStateResult int64

func (f fakeResourceStateResult) LastInsertId() (int64, error) { return int64(f), nil }
func (f fakeResourceStateResult) RowsAffected() (int64, error) { return int64(f), nil }

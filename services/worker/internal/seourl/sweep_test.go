package seourl

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeSource struct {
	entities []Entity
	err      error
	calls    int
}

func (f *fakeSource) Entities(_ context.Context, _ bool, offset, limit int) ([]Entity, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if offset >= len(f.entities) {
		return nil, nil
	}
	end := min(offset+limit, len(f.entities))
	return f.entities[offset:end], nil
}

type fakeStore struct {
	rows        []Row
	retireCalls int
	retireCut   time.Time
	upsertErr   error
	retireErr   error
}

func (f *fakeStore) Upsert(_ context.Context, rows []Row) (int, error) {
	if f.upsertErr != nil {
		return 0, f.upsertErr
	}
	f.rows = append(f.rows, rows...)
	return len(rows), nil
}

func (f *fakeStore) RetireUnseen(_ context.Context, _ bool, before time.Time) (int, error) {
	f.retireCalls++
	f.retireCut = before
	if f.retireErr != nil {
		return 0, f.retireErr
	}
	return 3, nil
}

func sweepAt(src Source, store Store, batch, maxBatches int) Sweep {
	return Sweep{
		Store: store, Source: src,
		Now:   func() time.Time { return now },
		Batch: batch, MaxBatches: maxBatches,
	}
}

func manyFixtures(n int) []Entity {
	out := make([]Entity, 0, n)
	for i := range n {
		out = append(out, fixture(func(e *Entity) {
			e.ID = int64(i)
			e.Path = "/predictions/league/fixture-" + string(rune('a'+i%26)) + "-" + time.Duration(i).String()
		}))
	}
	return out
}

func TestSweepDecidesEveryEntityItReads(t *testing.T) {
	src := &fakeSource{entities: []Entity{
		fixture(nil),
		fixture(func(e *Entity) { e.Path = "/predictions/l/old-2"; e.KickoffAt = now.AddDate(0, 0, -30) }),
		{Path: "/football/teams/arsenal", Type: TypeTeam, HasContent: true},
	}}
	store := &fakeStore{}
	got, err := sweepAt(src, store, 10, 5).Run(context.Background(), true)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Examined != 3 || got.Written != 3 {
		t.Fatalf("examined=%d written=%d, want 3 and 3", got.Examined, got.Written)
	}
	// And the decisions actually differ, or the sweep is just copying rows.
	var indexable, excluded int
	for _, r := range store.rows {
		if r.Indexable {
			indexable++
		} else {
			excluded++
		}
	}
	if indexable != 2 || excluded != 1 {
		t.Fatalf("indexable=%d excluded=%d, want 2 and 1", indexable, excluded)
	}
}

func TestSweepSkipsMalformedPathsWithoutFailingTheRun(t *testing.T) {
	// One bad row must not stop a catalogue-wide pass. The path is the primary
	// key, so storing it would collide or render invalid XML -- but aborting
	// would leave every later entity unrefreshed, which is worse.
	src := &fakeSource{entities: []Entity{
		fixture(func(e *Entity) { e.Path = "" }),
		fixture(func(e *Entity) { e.Path = "https://sokalens.com/absolute" }),
		fixture(func(e *Entity) { e.Path = "/en/locale-duplicate" }),
		fixture(func(e *Entity) { e.Path = "/predictions/league/good-one" }),
	}}
	store := &fakeStore{}
	got, err := sweepAt(src, store, 10, 5).Run(context.Background(), true)
	if err != nil {
		t.Fatalf("Run failed on a malformed path: %v", err)
	}
	if got.Examined != 4 {
		t.Fatalf("examined = %d, want all 4 seen", got.Examined)
	}
	if got.Written != 1 {
		t.Fatalf("written = %d, want only the one valid path", got.Written)
	}
	if store.rows[0].Path != "/predictions/league/good-one" {
		t.Fatalf("stored the wrong row: %q", store.rows[0].Path)
	}
}

func TestSweepPagesThroughEverything(t *testing.T) {
	src := &fakeSource{entities: manyFixtures(25)}
	store := &fakeStore{}
	got, err := sweepAt(src, store, 10, 10).Run(context.Background(), true)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Examined != 25 {
		t.Fatalf("examined = %d, want 25", got.Examined)
	}
	if got.Truncated {
		t.Fatal("a 25-row catalogue was reported truncated")
	}
}

func TestATruncatedSweepDoesNotRetireAnything(t *testing.T) {
	// The most dangerous failure in this file. Rows a truncated pass never
	// reached look exactly like rows whose entity has disappeared. Retiring on
	// that evidence would de-index whatever happened to sort last, and
	// recovery is a re-crawl at roughly 275 URLs a day.
	src := &fakeSource{entities: manyFixtures(100)}
	store := &fakeStore{}
	got, err := sweepAt(src, store, 10, 3).Run(context.Background(), true)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !got.Truncated {
		t.Fatal("precondition: this run should have hit its batch ceiling")
	}
	if store.retireCalls != 0 {
		t.Fatal("a truncated sweep retired rows it had not looked at")
	}
	if got.Retired != 0 {
		t.Fatalf("reported %d retired on a truncated pass", got.Retired)
	}
}

func TestACompleteSweepRetiresOnlyLongUnseenRows(t *testing.T) {
	src := &fakeSource{entities: manyFixtures(5)}
	store := &fakeStore{}
	got, err := sweepAt(src, store, 10, 5).Run(context.Background(), true)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if store.retireCalls != 1 {
		t.Fatalf("retire called %d times, want once", store.retireCalls)
	}
	if got.Retired != 3 {
		t.Fatalf("retired = %d, want the store's 3", got.Retired)
	}
	// The window is generous on purpose: a row goes unconfirmed because its
	// entity vanished, or because one pass failed, or a deploy restarted the
	// worker mid-sweep. Only a sustained absence should count.
	wantCut := now.Add(-RetireAfter)
	if !store.retireCut.Equal(wantCut) {
		t.Fatalf("retire cutoff = %s, want %s", store.retireCut, wantCut)
	}
	if RetireAfter < 24*time.Hour {
		t.Fatalf("RetireAfter is %s; a single failed sweep would start de-indexing the catalogue", RetireAfter)
	}
}

func TestSweepStopsOnASourceFailureRatherThanRetiring(t *testing.T) {
	// If the source is broken we know nothing about what still exists, so
	// retiring would be acting on an absence of evidence.
	src := &fakeSource{err: errors.New("database is down")}
	store := &fakeStore{}
	_, err := sweepAt(src, store, 10, 5).Run(context.Background(), true)
	if err == nil {
		t.Fatal("a source failure was swallowed")
	}
	if store.retireCalls != 0 {
		t.Fatal("retired rows after failing to read the source")
	}
}

func TestSweepStopsOnAStoreFailure(t *testing.T) {
	src := &fakeSource{entities: manyFixtures(5)}
	store := &fakeStore{upsertErr: errors.New("disk full")}
	_, err := sweepAt(src, store, 10, 5).Run(context.Background(), true)
	if err == nil {
		t.Fatal("an upsert failure was swallowed")
	}
	if store.retireCalls != 0 {
		t.Fatal("retired rows after failing to write")
	}
}

func TestAnEmptyCatalogueRetiresNothingAndDoesNotError(t *testing.T) {
	// A brand-new database, or a source that legitimately has nothing in this
	// band. It must not look like "everything disappeared".
	src := &fakeSource{entities: nil}
	store := &fakeStore{}
	got, err := sweepAt(src, store, 10, 5).Run(context.Background(), false)
	if err != nil {
		t.Fatalf("Run on an empty catalogue: %v", err)
	}
	if got.Examined != 0 || got.Written != 0 {
		t.Fatalf("examined=%d written=%d on an empty catalogue", got.Examined, got.Written)
	}
	// Retirement still runs -- an empty band with stale rows is exactly when
	// they should go -- but the store decides, on last_seen_at, not the sweep.
	if store.retireCalls != 1 {
		t.Fatalf("retire called %d times, want once", store.retireCalls)
	}
}

func TestSweepRefusesToRunWithoutItsCollaborators(t *testing.T) {
	if _, err := (Sweep{}).Run(context.Background(), true); err == nil {
		t.Fatal("a sweep with no store or source reported success")
	}
}

func TestTheStableBandRunsOnItsOwnClock(t *testing.T) {
	// Re-deriving 13,763 team rows every couple of minutes to discover nothing
	// changed is how a sweep becomes the thing somebody disables.
	if !DueForStableSweep(time.Time{}, now) {
		t.Fatal("a stable band that has never run should be due")
	}
	if DueForStableSweep(now.Add(-time.Hour), now) {
		t.Fatal("the stable band ran an hour ago and was reported due again")
	}
	if !DueForStableSweep(now.Add(-25*time.Hour), now) {
		t.Fatal("the stable band has not run for 25 hours and was not due")
	}
	if StableSweepInterval < time.Hour {
		t.Fatalf("StableSweepInterval is %s; stable entities do not change that fast", StableSweepInterval)
	}
}

func TestBatchCeilingsHaveSaneDefaults(t *testing.T) {
	// Zero must mean "use the default", not "one batch of nothing" or an
	// unbounded table scan holding a transaction open.
	src := &fakeSource{entities: manyFixtures(3)}
	store := &fakeStore{}
	got, err := sweepAt(src, store, 0, 0).Run(context.Background(), true)
	if err != nil {
		t.Fatalf("Run with zero ceilings: %v", err)
	}
	if got.Examined != 3 {
		t.Fatalf("examined = %d with default ceilings, want 3", got.Examined)
	}
}

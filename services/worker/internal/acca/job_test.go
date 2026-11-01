package acca

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

func TestGenerationFollowUpIsBoundedAndIdempotentPerSourceJob(t *testing.T) {
	now := time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC)
	first := GenerationFollowUp(jobs.Job{ID: 42}, now)
	retry := GenerationFollowUp(jobs.Job{ID: 42}, now.Add(time.Minute))
	next := GenerationFollowUp(jobs.Job{ID: 43}, now)
	if first.JobKey != GenerateInventoryJobKey || first.Queue != "acca" || first.Priority != 40 || string(first.Payload) != `{"target":3000,"period":"seven_days","intent":"core"}` {
		t.Fatalf("unexpected ACCA follow-up: %+v", first)
	}
	if !first.DeduplicateCompleted || first.IdempotencyKey == "" || first.IdempotencyKey != retry.IdempotencyKey || first.IdempotencyKey == next.IdempotencyKey {
		t.Fatalf("follow-up idempotency keys = %q, %q, %q", first.IdempotencyKey, retry.IdempotencyKey, next.IdempotencyKey)
	}
	if !first.RunAfter.Equal(now) {
		t.Fatalf("follow-up run-after = %s, want %s", first.RunAfter, now)
	}
}

type candidateSourceFunc func(context.Context, time.Time) ([]Candidate, error)

func (f candidateSourceFunc) EligibleCandidates(ctx context.Context, now time.Time) ([]Candidate, error) {
	return f(ctx, now)
}

// This repository models publication and eligibility independently, allowing a
// prediction to be superseded while the job still holds its earlier snapshot.
type inventoryState struct {
	active        []Slip
	superseded    map[string]bool
	published     int
	invalidations int
	beforePublish func()
	finalError    error
	prunes        int
	prunedFor     time.Duration
	pruneErr      error
}

func (r *inventoryState) Expire(context.Context) (sql.Result, error) { return nil, nil }

func (r *inventoryState) Prune(_ context.Context, retainFor time.Duration, _ int) (int64, error) {
	r.prunes++
	r.prunedFor = retainFor
	return 0, r.pruneErr
}

func (r *inventoryState) InvalidateStale(context.Context) (sql.Result, error) {
	r.invalidations++
	if r.invalidations > 1 && r.finalError != nil {
		return nil, r.finalError
	}
	kept := r.active[:0]
	for _, slip := range r.active {
		stale := false
		for _, leg := range slip.Legs {
			stale = stale || r.superseded[leg.PredictionID]
		}
		if !stale {
			kept = append(kept, slip)
		}
	}
	r.active = kept
	return nil, nil
}

func (r *inventoryState) UpsertBatch(_ context.Context, _, _ string, slips []Slip) error {
	if r.beforePublish != nil {
		r.beforePublish()
	}
	r.published += len(slips)
	r.active = append(r.active, slips...)
	return nil
}

func TestGenerationRemovesCandidatesSupersededDuringPublication(t *testing.T) {
	for _, tab := range []string{"1x2", "all"} {
		for _, timing := range []string{"after_snapshot", "during_publication"} {
			t.Run(tab+"/"+timing, func(t *testing.T) {
				now := time.Date(2026, 9, 9, 21, 15, 0, 0, time.UTC)
				state := &inventoryState{
					superseded: map[string]bool{},
					active:     []Slip{{Legs: []Leg{{PredictionID: "unchanged"}}}},
				}
				supersede := func() { state.superseded["snapshot-a"] = true }
				source := candidateSourceFunc(func(context.Context, time.Time) ([]Candidate, error) {
					candidates := []Candidate{
						{Source: "engine", PredictionID: "snapshot-a", MatchID: 1, Market: "1x2", Selection: "home", Probability: .6, Kickoff: now.Add(time.Hour)},
						{Source: "engine", PredictionID: "snapshot-b", MatchID: 2, Market: "1x2", Selection: "home", Probability: .6, Kickoff: now.Add(time.Hour)},
					}
					if timing == "after_snapshot" {
						supersede()
					}
					return candidates, nil
				})
				if timing == "during_publication" {
					state.beforePublish = supersede
				}
				job := Job{Source: source, Repository: state, Target: 2, Legs: 2, Tab: tab, Now: func() time.Time { return now }}
				if err := job.Run(context.Background(), jobs.Job{}); err != nil {
					t.Fatal(err)
				}
				if state.published == 0 {
					t.Fatal("test did not publish the stale candidate snapshot")
				}
				if len(state.active) != 1 || state.active[0].Legs[0].PredictionID != "unchanged" {
					t.Fatalf("active inventory retained superseded candidates or removed a valid slip: %+v", state.active)
				}
			})
		}
	}
}

func TestGenerationRechecksEmptyInventoryAndPropagatesFailure(t *testing.T) {
	for _, tab := range []string{"1x2", "all"} {
		for _, fail := range []bool{false, true} {
			t.Run(tab+"/"+map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
				state := &inventoryState{}
				if fail {
					state.finalError = errors.New("eligibility recheck unavailable")
				}
				job := Job{Source: candidateSourceFunc(func(context.Context, time.Time) ([]Candidate, error) { return nil, nil }), Repository: state, Tab: tab}
				err := job.Run(context.Background(), jobs.Job{})
				if !errors.Is(err, state.finalError) {
					t.Fatalf("Run() error = %v, want %v", err, state.finalError)
				}
				if state.invalidations != 2 {
					t.Fatalf("empty generation performed %d eligibility checks, want before and after", state.invalidations)
				}
			})
		}
	}
}

// Pruning is housekeeping over rows that are already dead, so it must never be
// able to fail a run whose inventory was generated correctly. The failure it
// replaced was the opposite shape: unbounded growth quietly made the deploy
// readiness gate slower until it exceeded its budget and rolled back releases.
func TestRunPrunesNonActiveSlips(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	newJob := func(state *inventoryState, retain time.Duration) Job {
		source := candidateSourceFunc(func(context.Context, time.Time) ([]Candidate, error) {
			return []Candidate{
				{Source: "engine", PredictionID: "a", MatchID: 1, Market: "1x2", Selection: "home", Probability: .6, Kickoff: now.Add(time.Hour)},
				{Source: "engine", PredictionID: "b", MatchID: 2, Market: "1x2", Selection: "home", Probability: .6, Kickoff: now.Add(time.Hour)},
			}, nil
		})
		return Job{
			Source: source, Repository: state, Target: 2, Legs: 2, Tab: "all",
			Now: func() time.Time { return now }, RetainNonActiveFor: retain,
		}
	}

	t.Run("applies the default retention window", func(t *testing.T) {
		state := &inventoryState{superseded: map[string]bool{}}
		if err := newJob(state, 0).Run(context.Background(), jobs.Job{}); err != nil {
			t.Fatal(err)
		}
		if state.prunes != 1 {
			t.Fatalf("expected exactly one prune per run, got %d", state.prunes)
		}
		if state.prunedFor != DefaultRetainNonActiveFor {
			t.Fatalf("expected the default retention window, got %s", state.prunedFor)
		}
	})

	t.Run("honours an explicit retention window", func(t *testing.T) {
		state := &inventoryState{superseded: map[string]bool{}}
		if err := newJob(state, 24*time.Hour).Run(context.Background(), jobs.Job{}); err != nil {
			t.Fatal(err)
		}
		if state.prunedFor != 24*time.Hour {
			t.Fatalf("expected the configured retention window, got %s", state.prunedFor)
		}
	})

	t.Run("a prune failure does not fail the run", func(t *testing.T) {
		state := &inventoryState{superseded: map[string]bool{}, pruneErr: errors.New("deadlock detected")}
		if err := newJob(state, 0).Run(context.Background(), jobs.Job{}); err != nil {
			t.Fatalf("prune failure must not fail a run that generated inventory: %v", err)
		}
		if state.published == 0 {
			t.Fatal("inventory should still have been published")
		}
	})
}

func TestRetentionWindowStaysShortEnoughToKeepTheTableResident(t *testing.T) {
	// This is the regression guard for the /v1/accas outage of 2026-09-22, and
	// it is a bound rather than an equality because the exact figure is a
	// judgement call while the ceiling is not.
	//
	// The window was 72h, chosen to bound storage, and it settled the table at
	// ~200,571 slips and 1,707,869 legs against an active pool of 3,000. The
	// endpoint builds country and league facets by walking the active pool's
	// legs, and with the live rows scattered through that much dead inventory
	// the query took 3,456ms cold against a 2750ms request timeout -- while
	// taking 62-86ms warm. Production served 111 failures to 108 successes.
	//
	// The generator wrote 51k-81k slips a day while this was measured, so the
	// window translates more or less directly into table size. Anything much
	// above half a day puts it back into the range where a cold read loses.
	const ceiling = 12 * time.Hour
	if DefaultRetainNonActiveFor > ceiling {
		t.Fatalf("retention window %s exceeds %s; at the observed generation rate "+
			"that is enough dead inventory to make the ACCA facet query miss cache "+
			"and time out", DefaultRetainNonActiveFor, ceiling)
	}
	// A floor too: pruning faster than the generator's own cycle would delete
	// slips it is still settling, and leaves nothing to diagnose a bad run with.
	if DefaultRetainNonActiveFor < time.Hour {
		t.Fatalf("retention window %s is shorter than an hour; a bad generation run "+
			"would be unexplainable by the time anyone looked", DefaultRetainNonActiveFor)
	}
}

func TestOddsTargetsSpanTheReachableRange(t *testing.T) {
	// The inventory hugged the standards floor: every 5-leg slip in a 3,000
	// slip inventory landed between 2.76 and 3.55 against a floor of 2.76, and
	// 19 leg counts produced 201 stocked cells of a 1,596-cell grid. The
	// composer ranks by confidence, so with no odds target it returns the most
	// confident slip that clears the floor -- every time.
	for _, legs := range []int{2, 5, 10, 20} {
		windows := oddsTargetsFor(legs)
		if len(windows) < 2 {
			t.Fatalf("legs=%d produced %d odds windows; one window is the clustering this fixes",
				legs, len(windows))
		}
		floor := MinimumSystemCombinedFairOdds(legs)
		for i, w := range windows {
			if w[0] < floor {
				t.Fatalf("legs=%d window %d starts at %v, below the standards floor %v",
					legs, i, w[0], floor)
			}
			if w[1] > maxInventoryFairOdds {
				t.Fatalf("legs=%d window %d ends at %v, above the schema ceiling %v",
					legs, i, w[1], maxInventoryFairOdds)
			}
			if w[1] <= w[0] {
				t.Fatalf("legs=%d window %d is empty: %v", legs, i, w)
			}
		}
		// Windows must actually separate, or several of them land in one odds
		// band and nothing has been spread.
		if windows[1][0] <= windows[0][0] {
			t.Fatalf("legs=%d windows do not advance: %v", legs, windows)
		}
	}
}

func TestCoreIntentsStockMoreThanTheFloorOfEachLegCount(t *testing.T) {
	intents := CoreIntents(3000, 0, "seven_days", "core", nil)
	if len(intents) == 0 {
		t.Fatal("no intents produced")
	}

	// Every intent must carry an odds target now. Without one the composer
	// falls back to "cheapest that clears the floor", which is what produced a
	// 3,000-slip inventory covering 12.6% of the grid.
	cells := map[string]bool{}
	for _, in := range intents {
		if in.MinFairOdds <= 0 || in.MaxFairOdds <= in.MinFairOdds {
			t.Fatalf("intent %+v has no usable odds window", in)
		}
		if floor := MinimumSystemCombinedFairOdds(in.Legs); in.MinFairOdds < floor {
			t.Fatalf("intent for %d legs targets %v, below its floor %v", in.Legs, in.MinFairOdds, floor)
		}
		cells[fmt.Sprintf("%s|%d|%.2f", in.Tab, in.Legs, in.MinFairOdds)] = true
	}

	// Several distinct odds windows per leg count, not one.
	windowsForFive := map[float64]bool{}
	for _, in := range intents {
		if in.Legs == 5 {
			windowsForFive[in.MinFairOdds] = true
		}
	}
	if len(windowsForFive) < 2 {
		t.Fatalf("5-leg intents target %d distinct odds windows; want several so the "+
			"inventory stops clustering at the floor", len(windowsForFive))
	}
}

func TestCoreIntentsRespectTheInventoryCeiling(t *testing.T) {
	// The readiness gate fails above MaxActiveInventory, so the allocation must
	// never ask for more than it however many cells the grid has.
	intents := CoreIntents(MaxActiveInventory*10, 0, "seven_days", "core", nil)
	total := 0
	for _, in := range intents {
		total += in.Target
	}
	if total > MaxActiveInventory {
		t.Fatalf("allocated %d slips, above the %d ceiling the readiness gate enforces",
			total, MaxActiveInventory)
	}
}

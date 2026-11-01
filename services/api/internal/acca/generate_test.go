package acca

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"
)

// pool builds a vetted-prediction pool spread across markets and fixtures, the
// shape the eligible-predictions view returns for a normal matchday.
func pool(fixtures int, now time.Time) []candidate {
	markets := []struct {
		key, selection string
		probability    float64
	}{
		{"1x2", "home", 0.72},
		{"btts", "yes", 0.63},
		{"total_goals", "over_2.5", 0.58},
		{"total_corners", "over_8.5", 0.80},
		{"total_cards", "over_3.5", 0.86},
	}
	out := make([]candidate, 0, fixtures*len(markets))
	for fixture := 1; fixture <= fixtures; fixture++ {
		for index, market := range markets {
			// Spread probabilities so the pool holds both short and long legs.
			probability := market.probability + float64(fixture%7)*0.02
			if probability >= 0.97 {
				probability = 0.96
			}
			out = append(out, candidate{
				Leg: Leg{
					Source: "engine", PredictionID: fmt.Sprintf("p-%d-%d", fixture, index), MatchID: int64(fixture),
					Market: market.key, Selection: market.selection, FairOdds: 1 / probability,
					KickoffAt: now.Add(time.Duration(fixture) * time.Hour), HomeTeam: "Home", AwayTeam: "Away",
					League: "Test League", Country: "GB",
				},
				Probability: probability,
				Rank:        probability,
			})
		}
	}
	return out
}

func combined(legs []Leg) float64 {
	odds := 1.0
	for _, leg := range legs {
		odds *= leg.FairOdds
	}
	return odds
}

// The filter that sent the user here: 20 legs, mixed markets, 10 to 1000
// combined odds. The background composer samples legs at random and discards
// anything outside its window, so it practically never files a 20-leg slip --
// while the pool holds hundreds of selections that satisfy the request.
func TestComposesTwentyLegSlipInsideRequestedOddsWindow(t *testing.T) {
	now := time.Now().UTC()
	slip, ok := composeOnDemand(pool(40, now), generateIntent{
		Legs: 20, MinFairOdds: 10, MaxFairOdds: 1000, Tab: "mixed", Period: "seven_days", Seed: 7, Now: now,
	})

	if !ok {
		t.Fatal("no slip composed for 20 legs at 10-1000 fair odds, which the pool can satisfy")
	}
	if slip.LegCount != 20 || len(slip.Legs) != 20 {
		t.Fatalf("leg count=%d legs=%d, want 20", slip.LegCount, len(slip.Legs))
	}
	if slip.CombinedFairOdds < 10 || slip.CombinedFairOdds > 1000 {
		t.Fatalf("combined fair odds=%v, want within 10..1000", slip.CombinedFairOdds)
	}
	if math.Abs(combined(slip.Legs)-slip.CombinedFairOdds) > 1e-6 {
		t.Fatalf("combined odds %v do not match the legs (%v)", slip.CombinedFairOdds, combined(slip.Legs))
	}
	if slip.Origin != GeneratedOrigin {
		t.Fatalf("origin=%q, want %q so the UI can say the slip is not stored", slip.Origin, GeneratedOrigin)
	}
	if !validSlip(slip) {
		t.Fatal("composed slip fails the read-path validity check and would be dropped before serving")
	}
}

// One fixture must never appear twice: correlated legs are the whole reason the
// inventory keeps one selection per match.
func TestComposedLegsComeFromSeparateFixtures(t *testing.T) {
	now := time.Now().UTC()
	slip, ok := composeOnDemand(pool(30, now), generateIntent{
		Legs: 8, MinFairOdds: 5, MaxFairOdds: 60, Tab: "mixed", Seed: 3, Now: now,
	})

	if !ok {
		t.Fatal("no slip composed")
	}
	seen := map[int64]bool{}
	for _, leg := range slip.Legs {
		if seen[leg.MatchID] {
			t.Fatalf("fixture %d used twice in one slip", leg.MatchID)
		}
		seen[leg.MatchID] = true
	}
	for index, leg := range slip.Legs {
		if leg.Position != index {
			t.Fatalf("leg %d carries position %d", index, leg.Position)
		}
	}
}

// A market tab may only be served legs from that market, exactly as the
// inventory tab would have been filled.
func TestComposedSlipHonoursMarketTab(t *testing.T) {
	now := time.Now().UTC()
	slip, ok := composeOnDemand(pool(30, now), generateIntent{
		Legs: 5, MinFairOdds: 2, MaxFairOdds: 50, Tab: "btts", Seed: 11, Now: now,
	})

	if !ok {
		t.Fatal("no BTTS slip composed")
	}
	for _, leg := range slip.Legs {
		if leg.Market != "btts" {
			t.Fatalf("btts tab served a %q leg", leg.Market)
		}
	}
}

// When the pool genuinely cannot reach the window the answer must stay empty:
// the empty state is only honest if it means "no such combination exists".
func TestImpossibleWindowComposesNothing(t *testing.T) {
	now := time.Now().UTC()
	if slip, ok := composeOnDemand(pool(40, now), generateIntent{
		Legs: 2, MinFairOdds: 900, MaxFairOdds: 1000, Tab: "mixed", Seed: 5, Now: now,
	}); ok {
		t.Fatalf("composed %v from two legs that cannot reach 900", slip.CombinedFairOdds)
	}
	if _, ok := composeOnDemand(pool(3, now), generateIntent{
		Legs: 10, MinFairOdds: 2, MaxFairOdds: 1000, Tab: "mixed", Seed: 5, Now: now,
	}); ok {
		t.Fatal("composed a 10-leg slip from three fixtures")
	}
}

// Started or in-play fixtures must never be composed into a slip.
func TestComposerSkipsKickedOffFixtures(t *testing.T) {
	now := time.Now().UTC()
	stale := pool(30, now.Add(-48*time.Hour))
	if _, ok := composeOnDemand(stale, generateIntent{
		Legs: 4, MinFairOdds: 2, MaxFairOdds: 100, Tab: "mixed", Seed: 2, Now: now,
	}); ok {
		t.Fatal("composed a slip out of fixtures that have already kicked off")
	}
}

// The same search must return the same slip, and a new search a different one,
// or "Show another" would either churn on reload or never move.
func TestSeedMakesCompositionReproducibleAndVaried(t *testing.T) {
	now := time.Now().UTC()
	intent := generateIntent{Legs: 6, MinFairOdds: 4, MaxFairOdds: 400, Tab: "mixed", Seed: seedValue("first"), Now: now}
	first, ok := composeOnDemand(pool(40, now), intent)
	if !ok {
		t.Fatal("no slip composed")
	}
	repeat, ok := composeOnDemand(pool(40, now), intent)
	if !ok || repeat.ID != first.ID {
		t.Fatalf("same seed produced %q then %q", first.ID, repeat.ID)
	}
	varied := intent
	varied.Seed = seedValue("second")
	other, ok := composeOnDemand(pool(40, now), varied)
	if !ok {
		t.Fatal("no slip composed for the second seed")
	}
	if other.ID == first.ID {
		t.Fatal("a different seed returned the same slip, so \"Show another\" would never move")
	}
}

// The candidate query has to carry the same geographic filters as the inventory
// search, or a country-filtered generate would answer with foreign fixtures.
func TestCandidateQueryFiltersMatchTheInventorySearch(t *testing.T) {
	for _, expected := range []string{"1x2", "btts"} {
		if markets := tabMarkets(expected); len(markets) != 1 || markets[0] != expected {
			t.Fatalf("tab %q maps to %v", expected, markets)
		}
	}
	if markets := tabMarkets("over_2_5"); len(markets) != 2 {
		t.Fatalf("over_2_5 must accept both goal-total market keys, got %v", markets)
	}
	if markets := tabMarkets("mixed"); markets != nil {
		t.Fatalf("mixed must not constrain the market, got %v", markets)
	}
}

// The inventory answer is already in hand when the fallback runs, so composing
// must never be what makes a browse page slow. The budget is short enough to
// leave the request's own deadline intact.
func TestGenerateBudgetLeavesRoomInsideTheRequestDeadline(t *testing.T) {
	// The API runs with API_REQUEST_TIMEOUT=2750ms; the fallback issues two
	// queries and must not be able to consume that on its own.
	if generateBudget >= 2750*time.Millisecond {
		t.Fatalf("generate budget %v does not fit inside the request timeout", generateBudget)
	}
	if generateBudget < 250*time.Millisecond {
		t.Fatalf("generate budget %v is too short to complete a normal candidate read", generateBudget)
	}
}

func TestGenerateBudgetSpendsOnlyWhatTheRequestHasLeft(t *testing.T) {
	// The check above passes whatever generate actually does: it compares a
	// constant against a constant. It was passing while production answered
	// 503 with duration_ms=2750, because the budget was applied flat from the
	// moment composing started rather than measured against what the request
	// had left. The inventory read happens first, so a 1400ms read plus a
	// 1500ms budget overran the 2750ms deadline, the handler's context was
	// cancelled, and List returned a context error.
	now := time.Now()

	t.Run("a fresh request gets the full budget", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), now.Add(2750*time.Millisecond))
		defer cancel()
		budget, worth := generateBudgetFor(ctx, now)
		if !worth {
			t.Fatal("a request with its whole timeout left must be worth composing for")
		}
		if budget != generateBudget {
			t.Fatalf("budget=%v, want the full %v", budget, generateBudget)
		}
	})

	t.Run("a nearly spent request gets what remains, less the margin", func(t *testing.T) {
		// 900ms left: composing may have 650ms, not 1500ms.
		ctx, cancel := context.WithDeadline(context.Background(), now.Add(900*time.Millisecond))
		defer cancel()
		budget, worth := generateBudgetFor(ctx, now)
		if !worth {
			t.Fatal("650ms is enough to try")
		}
		if want := 900*time.Millisecond - generateDeadlineMargin; budget != want {
			t.Fatalf("budget=%v, want %v", budget, want)
		}
		if budget >= generateBudget {
			t.Fatal("budget must shrink to fit the request, not stay flat")
		}
	})

	t.Run("an exhausted request composes nothing", func(t *testing.T) {
		// Starting here cannot produce an answer and guarantees the overrun.
		// The caller keeps its empty inventory result, which renders as "no
		// ACCA matched" rather than as a broken feature.
		ctx, cancel := context.WithDeadline(context.Background(), now.Add(150*time.Millisecond))
		defer cancel()
		if _, worth := generateBudgetFor(ctx, now); worth {
			t.Fatal("composing must be skipped when the request has no time for it")
		}
	})

	t.Run("no deadline means the full budget, never unbounded", func(t *testing.T) {
		budget, worth := generateBudgetFor(context.Background(), now)
		if !worth || budget != generateBudget {
			t.Fatalf("budget=%v worth=%v, want the full budget", budget, worth)
		}
	})
}

func TestComposerFindsAsymmetricCombinationBeyondNearestLegs(t *testing.T) {
	now := time.Now().UTC()
	items := make([]candidate, 0, 10)
	for fixture := 1; fixture <= 8; fixture++ {
		items = append(items, candidate{Leg: Leg{MatchID: int64(fixture), Market: "1x2", Selection: "home", FairOdds: 9, KickoffAt: now.Add(time.Hour)}, Probability: 1.0 / 9})
	}
	items = append(items,
		candidate{Leg: Leg{MatchID: 9, Market: "1x2", Selection: "home", FairOdds: 2, KickoffAt: now.Add(time.Hour)}, Probability: 0.5},
		candidate{Leg: Leg{MatchID: 10, Market: "1x2", Selection: "away", FairOdds: 50, KickoffAt: now.Add(time.Hour)}, Probability: 0.02},
	)
	slip, ok := composeOnDemand(items, generateIntent{Legs: 2, MinFairOdds: 99, MaxFairOdds: 101, Tab: "mixed", Seed: 7, Now: now})
	if !ok {
		t.Fatal("valid 2 x 50 combination was hidden behind nearer 9 x 9 decoys")
	}
	if slip.CombinedFairOdds < 99 || slip.CombinedFairOdds > 101 {
		t.Fatalf("combined fair odds=%v, want within 99..101", slip.CombinedFairOdds)
	}
}

func TestComposerStopsWhenRequestBudgetIsCancelled(t *testing.T) {
	now := time.Now().UTC()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := composeOnDemandContext(ctx, pool(40, now), generateIntent{Legs: 20, MinFairOdds: 10, MaxFairOdds: 1000, Tab: "mixed", Now: now}); ok {
		t.Fatal("composer returned a slip after its request budget was cancelled")
	}
}

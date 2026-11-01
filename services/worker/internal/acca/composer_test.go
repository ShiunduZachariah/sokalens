package acca

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func TestComposeRejectsNonFiniteOrCertainProbabilities(t *testing.T) {
	now := time.Now().UTC()
	for _, probability := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), 1, 0.99999999, 0, -0.1, 1.1} {
		candidates := []Candidate{
			{MatchID: 1, Market: "btts", Probability: probability, Kickoff: now.Add(time.Hour)},
			{MatchID: 2, Market: "btts", Probability: .8, Kickoff: now.Add(time.Hour)},
		}
		if got := Compose(candidates, Intent{Target: 1, Legs: 2, Tab: "btts", Now: now}); len(got) != 0 {
			t.Errorf("probability %v entered inventory: %+v", probability, got)
		}
	}
}

func TestSystemAccaOddsFloorScalesWithLegCount(t *testing.T) {
	wants := map[int]float64{2: 1.5, 4: 2.25, 10: 7.59375, 20: 57.6650390625}
	for legs, want := range wants {
		if got := MinimumSystemCombinedFairOdds(legs); math.Abs(got-want) > 1e-9 {
			t.Fatalf("MinimumSystemCombinedFairOdds(%d)=%v, want %v", legs, got, want)
		}
	}
}

func TestComposeRejectsSystemSlipsBelowLegAndScaledCombinedFloors(t *testing.T) {
	now := time.Now().UTC()
	tooShort := make([]Candidate, 4)
	for index := range tooShort {
		tooShort[index] = Candidate{MatchID: int64(index + 1), PredictionID: stringID(index), Market: "1x2", Selection: "home", Probability: 1 / 1.01, Kickoff: now.Add(time.Hour)}
	}
	if slips := Compose(tooShort, Intent{Target: 1, Legs: 4, MinFairOdds: 1, Seed: 1, Now: now}); len(slips) != 0 {
		t.Fatalf("1.01 legs produced a system ACCA: %+v", slips)
	}

	belowCombinedFloor := make([]Candidate, 4)
	for index := range belowCombinedFloor {
		belowCombinedFloor[index] = Candidate{MatchID: int64(index + 1), PredictionID: stringID(index), Market: "1x2", Selection: "home", Probability: 1 / 1.20, Kickoff: now.Add(time.Hour)}
	}
	if slips := Compose(belowCombinedFloor, Intent{Target: 1, Legs: 4, MinFairOdds: 1, Seed: 1, Now: now}); len(slips) != 0 {
		t.Fatalf("combined odds below the 4-leg floor produced a system ACCA: %+v", slips)
	}
}

func TestDedicatedCompositionIgnoresOtherMarketCandidates(t *testing.T) {
	now := time.Now().UTC()
	candidates := make([]Candidate, 0, 100)
	for i := 1; i <= 100; i++ {
		candidates = append(candidates, Candidate{MatchID: int64(i), Market: "btts", Probability: .8, Kickoff: now.Add(time.Hour)})
	}
	intent := Intent{Target: 10, Legs: 2, Tab: "btts", Now: now, Seed: 42}
	want := Compose(candidates, intent)
	for i := 101; i <= 3000; i++ {
		candidates = append(candidates, Candidate{MatchID: int64(i), Market: "total_goals", Probability: .8, Kickoff: now.Add(time.Hour)})
	}
	if got := Compose(candidates, intent); !reflect.DeepEqual(got, want) {
		t.Fatal("unrelated markets alter dedicated composition; filter the source pool before attempts")
	}
}

func TestContentHashTracksPredictionVersionAndKickoff(t *testing.T) {
	now := time.Now().UTC()
	slip := Slip{Legs: []Leg{{PredictionVersion: "v1", Kickoff: now}}, EarliestKickoff: now, LatestKickoff: now}
	original := hashContent(slip)
	slip.Legs[0].PredictionVersion = "v2"
	if hashContent(slip) == original {
		t.Fatal("prediction version change was lost")
	}
	slip.Legs[0].PredictionVersion = "v1"
	slip.Legs[0].Kickoff = now.Add(time.Hour)
	if hashContent(slip) == original {
		t.Fatal("rescheduled kickoff was lost")
	}
}

func TestInventoryTabsUseDistinctQualifiedCandidateMarkets(t *testing.T) {
	candidates := []Candidate{
		{Market: "btts"}, {Market: "1x2"}, {Market: "btts"},
		{Market: "double_chance"}, {Market: ""},
	}
	want := []string{"mixed", "1x2", "btts", "double_chance"}
	if got := InventoryTabs(candidates); !reflect.DeepEqual(got, want) {
		t.Fatalf("InventoryTabs()=%v, want %v", got, want)
	}
}

func TestDedicatedInventoryTabAcceptsOnlyExactMarket(t *testing.T) {
	for _, tc := range []struct {
		tab, market string
		want        bool
	}{
		{"double_chance", "double_chance", true},
		{"double_chance", "1x2", false},
		{"total_cards", "total_cards", true},
		{"total_cards", "cards", false},
	} {
		if got := tabAllows(tc.tab, tc.market); got != tc.want {
			t.Fatalf("tabAllows(%q,%q)=%v, want %v", tc.tab, tc.market, got, tc.want)
		}
	}
}

func TestComposeBoundedUniqueAndDeterministic(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	candidates := make([]Candidate, 0, 1500)
	for i := 0; i < 1500; i++ {
		candidates = append(candidates, Candidate{Source: "engine", PredictionID: stringID(i), MatchID: int64(i), Market: []string{"1x2", "btts", "ou_2_5", "corners", "cards"}[i%5], Selection: "yes", Probability: .55 + float64(i%10)/100, Kickoff: now.Add(time.Duration(i+1) * time.Hour), Rank: float64(1500 - i)})
	}
	inv := Compose(candidates, Intent{Target: 400, Legs: 5, Seed: 42, Now: now})
	if len(inv) != 400 {
		t.Fatalf("got %d slips", len(inv))
	}
	for _, s := range inv {
		if len(s.Legs) != 5 || s.CombinedFairOdds > 1000 {
			t.Fatalf("invalid slip: %+v", s)
		}
		if len(uniqueMatches(s.Legs)) != 5 {
			t.Fatal("duplicate match")
		}
	}
	if len(uniqueHashes(inv)) != len(inv) {
		t.Fatal("duplicate composition hash")
	}
	if len(Compose(candidates, Intent{Target: 400, Legs: 5, Seed: 42, Now: now})) != len(inv) {
		t.Fatal("seeded run changed size")
	}
}

func TestComposeRejectsStartedAndInvalidProbability(t *testing.T) {
	now := time.Now().UTC()
	cs := []Candidate{{MatchID: 1, PredictionID: "a", Probability: .8, Market: "1x2", Kickoff: now.Add(-time.Minute)}, {MatchID: 2, PredictionID: "b", Probability: 0, Market: "1x2", Kickoff: now.Add(time.Hour)}}
	if got := Compose(cs, Intent{Target: 1, Legs: 2, Now: now}); len(got) != 0 {
		t.Fatal("invalid candidates entered inventory")
	}
}

func TestCoreIntentsAllocateDedicatedTabsWithinCeiling(t *testing.T) {
	tabs := []string{"mixed", "1x2", "btts", "double_chance", "draw_no_bet", "total_goals"}
	intents := CoreIntents(3001, 0, "weekend", "core", tabs)
	// Was 6*19 -- tabs by leg counts. Odds windows are a third dimension now,
	// so the count is 6 * 19 * (windows reachable at each leg count) and there
	// is no single product to assert. What matters is that every tab and every
	// leg count is still represented and the ceiling still holds, which the
	// checks below cover.
	if len(intents) < 6*19 {
		t.Fatalf("got %d intents, fewer than one per tab and leg count", len(intents))
	}
	total := 0
	seen := map[string]bool{}
	legCounts := map[int]bool{}
	for _, in := range intents {
		total += in.Target
		seen[in.Tab] = true
		legCounts[in.Legs] = true
		if in.Target <= 0 || in.Period != "weekend" {
			t.Fatalf("invalid intent: %+v", in)
		}
	}
	if total != MaxActiveInventory || len(seen) != 6 || len(legCounts) != 19 {
		t.Fatalf("total=%d tabs=%v legCounts=%v", total, seen, legCounts)
	}
}

func TestCoreIntentsCanTargetOneExplicitLegCount(t *testing.T) {
	tabs := []string{"mixed", "1x2", "btts", "double_chance", "draw_no_bet", "total_goals"}
	intents := CoreIntents(300, 20, "seven_days", "adaptive", tabs)
	// One per tab per odds window, rather than one per tab.
	if len(intents) < 6 {
		t.Fatalf("got %d intents, fewer than one per tab", len(intents))
	}
	seenTabs := map[string]bool{}
	for _, in := range intents {
		seenTabs[in.Tab] = true
	}
	if len(seenTabs) != 6 {
		t.Fatalf("tabs covered=%d, want 6", len(seenTabs))
	}
	for _, intent := range intents {
		if intent.Legs != 20 {
			t.Fatalf("unexpected legs: %+v", intent)
		}
	}
}

func TestMixedInventoryHasMarketDiversity(t *testing.T) {
	now := time.Now().UTC()
	cs := make([]Candidate, 0, 80)
	markets := []string{"1x2", "btts", "ou_2_5", "corners", "cards"}
	for i := 0; i < 80; i++ {
		cs = append(cs, Candidate{MatchID: int64(i + 1), PredictionID: stringID(i), Market: markets[i%len(markets)], Selection: "yes", Probability: .7, Kickoff: now.Add(time.Hour * time.Duration(i+1)), Rank: 1})
	}
	for _, s := range Compose(cs, Intent{Target: 10, Legs: 5, Tab: "mixed", Seed: 7, Now: now}) {
		if len(marketFamilies(s.Legs)) < 3 {
			t.Fatalf("mixed slip has %d families", len(marketFamilies(s.Legs)))
		}
	}
}

func stringID(i int) string { return "p" + string(rune(i%26+'a')) + string(rune(i/26%26+'a')) }
func uniqueMatches(legs []Leg) map[int64]struct{} {
	m := map[int64]struct{}{}
	for _, l := range legs {
		m[l.MatchID] = struct{}{}
	}
	return m
}
func uniqueHashes(ss []Slip) map[string]struct{} {
	m := map[string]struct{}{}
	for _, s := range ss {
		m[s.CompositionHash] = struct{}{}
	}
	return m
}

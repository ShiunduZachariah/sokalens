package basispoints

import (
	"math"
	"testing"
)

func TestDistributeAlwaysSumsToTotal(t *testing.T) {
	// The property that matters: a client must never be able to render 99.99%.
	cases := []struct {
		name       string
		selections map[string]float64
	}{
		{"thirds", map[string]float64{"home": 1.0 / 3, "draw": 1.0 / 3, "away": 1.0 / 3}},
		{"1x2 typical", map[string]float64{"home": 0.5123, "draw": 0.2456, "away": 0.2421}},
		{"two way", map[string]float64{"yes": 0.6667, "no": 0.3333}},
		{"sevenths", map[string]float64{"a": 1.0 / 7, "b": 2.0 / 7, "c": 4.0 / 7}},
		{"near certain", map[string]float64{"home": 0.99991, "draw": 0.00005, "away": 0.00004}},
		{"unnormalised", map[string]float64{"home": 0.4, "draw": 0.2, "away": 0.2}},
		{"single outcome", map[string]float64{"only": 0.42}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Distribute(tc.selections)
			sum := 0
			for _, v := range got {
				sum += v
				if v < 0 {
					t.Fatalf("negative basis points: %v", got)
				}
			}
			if sum != Total {
				t.Fatalf("sum = %d, want %d (%v)", sum, Total, got)
			}
			if len(got) != len(tc.selections) {
				t.Fatalf("got %d selections, want %d", len(got), len(tc.selections))
			}
		})
	}
}

// A line ladder is many mutually exclusive PAIRS, not one set. Apportioning them
// together divided every number by the number of lines: an over_1.5 the model put
// at 81.65% went onto the wire as 1633 basis points. This is the regression.
func TestDistributeApportionsEachLinePairIndependently(t *testing.T) {
	ladder := map[string]float64{
		"over_1.5": 0.8165, "under_1.5": 0.1835,
		"over_2.5": 0.5339, "under_2.5": 0.4661,
		"over_3.5": 0.2571, "under_3.5": 0.7429,
	}
	got := Distribute(ladder)
	for _, line := range []string{"1.5", "2.5", "3.5"} {
		sum := got["over_"+line] + got["under_"+line]
		if sum != Total {
			t.Fatalf("line %s sums to %d, want %d (%v)", line, sum, Total, got)
		}
	}
	if got["over_1.5"] != 8165 {
		t.Fatalf("over_1.5 = %d, want 8165 -- the value, not a fraction of the ladder", got["over_1.5"])
	}
}

// A ladder alongside an ordinary market: the plain selections form one group and
// each line forms its own, so neither distorts the other.
func TestDistributeSeparatesPlainSelectionsFromLines(t *testing.T) {
	got := Distribute(map[string]float64{
		"yes": 0.7, "no": 0.3,
		"over_2.5": 0.6, "under_2.5": 0.4,
	})
	if got["yes"]+got["no"] != Total {
		t.Fatalf("plain group sums to %d", got["yes"]+got["no"])
	}
	if got["over_2.5"]+got["under_2.5"] != Total {
		t.Fatalf("line group sums to %d", got["over_2.5"]+got["under_2.5"])
	}
	if got["yes"] != 7000 || got["over_2.5"] != 6000 {
		t.Fatalf("groups distorted each other: %v", got)
	}
}

// Half a pair is not a complete market. Scaling it to the total would assert a
// certainty the model did not state.
func TestDistributeDoesNotPromoteALoneLineSideToCertainty(t *testing.T) {
	got := Distribute(map[string]float64{"over_2.5": 0.98})
	if got["over_2.5"] != 9800 {
		t.Fatalf("over_2.5 = %d, want 9800 preserved", got["over_2.5"])
	}
}

func TestDistributeStaysWithinOneBasisPointOfTruth(t *testing.T) {
	// Largest remainder guarantees this; a naive scale-and-floor does not.
	selections := map[string]float64{"home": 0.5123, "draw": 0.2456, "away": 0.2421}
	got := Distribute(selections)
	for name, p := range selections {
		exact := p * Total
		if diff := math.Abs(float64(got[name]) - exact); diff > 1 {
			t.Fatalf("%s: got %d, exact %.4f, off by %.4f", name, got[name], exact, diff)
		}
	}
}

func TestDistributeIsDeterministicUnderTies(t *testing.T) {
	// Map iteration order is random in Go. Without the name tie-break the
	// spare basis point would land on a different selection per run, and a
	// client would see a market flicker with no new data behind it.
	selections := map[string]float64{"home": 1.0 / 3, "draw": 1.0 / 3, "away": 1.0 / 3}
	first := Distribute(selections)
	for i := 0; i < 200; i++ {
		if got := Distribute(selections); got["home"] != first["home"] ||
			got["draw"] != first["draw"] || got["away"] != first["away"] {
			t.Fatalf("run %d differed: %v vs %v", i, got, first)
		}
	}
	// "away" sorts first among equal remainders, so it takes the spare unit.
	if first["away"] != 3334 || first["draw"] != 3333 || first["home"] != 3333 {
		t.Fatalf("unexpected apportionment: %v", first)
	}
}

func TestDistributeEdgeCases(t *testing.T) {
	if got := Distribute(nil); len(got) != 0 {
		t.Fatalf("nil should give an empty map, got %v", got)
	}
	// All-zero cannot be apportioned; zeros are the honest answer, not 10000
	// arbitrarily assigned to one selection.
	zero := Distribute(map[string]float64{"yes": 0, "no": 0})
	if zero["yes"] != 0 || zero["no"] != 0 {
		t.Fatalf("zero-sum should give zeros, got %v", zero)
	}
	// A negative probability is clamped, not propagated.
	neg := Distribute(map[string]float64{"yes": -0.5, "no": 1.0})
	if neg["yes"] != 0 || neg["no"] != Total {
		t.Fatalf("negative should clamp to zero, got %v", neg)
	}
	single := Distribute(map[string]float64{"only": 0.42})
	if single["only"] != Total {
		t.Fatalf("a lone selection takes the whole total, got %v", single)
	}
}

func TestFromProbabilityClampsAndRounds(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want int
	}{
		{-1, 0}, {0, 0}, {0.00005, 1}, {0.5, 5000}, {0.6412, 6412},
		{0.99995, 10000}, {1, Total}, {2, Total},
	} {
		if got := FromProbability(tc.in); got != tc.want {
			t.Fatalf("FromProbability(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

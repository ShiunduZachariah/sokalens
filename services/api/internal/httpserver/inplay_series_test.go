package httpserver

import (
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/predictions/entitlement"
)

func seriesFixture() []entitlement.SeriesPoint {
	return []entitlement.SeriesPoint{
		{Sequence: 1, Minute: 0, Trigger: "timer", HomeGoals: 0, AwayGoals: 0,
			Markets: map[string]map[string]int{"match_result_in_play": {"home": 4800, "draw": 2700, "away": 2500}}},
		{Sequence: 2, Minute: 63, Trigger: "post_goal", EventKey: "evt-63", HomeGoals: 2, AwayGoals: 1,
			Markets: map[string]map[string]int{"match_result_in_play": {"home": 6800, "draw": 2100, "away": 1100}}},
	}
}

// An unentitled reader must not receive probabilities, and must still receive
// the shape of the graph.
//
// The same rule the board already follows: a locked market keeps its key and
// loses its value, so an anonymous page renders completely with locked cells
// rather than with holes in it. A graph that vanished entirely would read as a
// broken feature rather than as a reason to sign up.
func TestSeriesWithholdsProbabilitiesFromAnUnentitledViewer(t *testing.T) {
	out := inPlaySeriesResponse(seriesFixture(), false)
	if len(out) != 2 {
		t.Fatalf("got %d points, want 2 -- the shape must survive the gate", len(out))
	}
	for i, point := range out {
		if _, present := point["markets"]; present {
			t.Errorf("point %d carries probabilities to an unentitled viewer", i)
		}
		if point["locked"] != true {
			t.Errorf("point %d is not marked locked, so the client cannot tell withheld from absent", i)
		}
		if _, present := point["minute"]; !present {
			t.Errorf("point %d lost its minute; the gate withholds values, not the graph", i)
		}
		if _, present := point["score"]; !present {
			t.Errorf("point %d lost its score, which is not gated anywhere else", i)
		}
	}
}

func TestSeriesServesProbabilitiesToAnEntitledViewer(t *testing.T) {
	out := inPlaySeriesResponse(seriesFixture(), true)
	markets, ok := out[1]["markets"].(map[string]map[string]int)
	if !ok {
		t.Fatalf("an entitled viewer did not receive markets: %#v", out[1]["markets"])
	}
	result := markets["match_result_in_play"]
	if result["home"] != 6800 || result["draw"] != 2100 || result["away"] != 1100 {
		t.Errorf("vector altered in transit: %#v", result)
	}
	// Basis points, as the engine wrote them. Rounding to percent here would
	// throw away precision the chart needs to draw a smooth line.
	if sum := result["home"] + result["draw"] + result["away"]; sum != 10000 {
		t.Errorf("vector sums to %d, not 10000; it is no longer basis points", sum)
	}
	if _, present := out[1]["locked"]; present {
		t.Error("an entitled point is marked locked")
	}
}

// The event key is what lets the chart put a marker on a goal and show the
// before/after figures the design draws. Without it every point looks like a
// timer tick.
func TestSeriesCarriesTheEventThatCausedThePoint(t *testing.T) {
	out := inPlaySeriesResponse(seriesFixture(), true)
	if out[1]["event_key"] != "evt-63" {
		t.Errorf("event key missing: %#v", out[1]["event_key"])
	}
	if out[1]["trigger"] != "post_goal" {
		t.Errorf("trigger missing: %#v", out[1]["trigger"])
	}
	if _, present := out[0]["event_key"]; present {
		t.Error("a timer point carries an event key it does not have")
	}
}

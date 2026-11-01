package httpserver

import (
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/predictions/entitlement"
)

// A marker on the probability graph should say what it marks.
//
// Before this, a goal and a red card were both an unlabelled vertical rule: the
// series carried the trigger but nothing about who it happened to. The incident
// is not gated -- the markets behind the line are the paid product, but who
// scored in the 22nd minute is public record, and a marker a guest cannot read
// is just an unexplained line.
func TestInPlaySeriesNamesTheIncident(t *testing.T) {
	points := []entitlement.SeriesPoint{{
		Sequence: 4,
		Minute:   22,
		Trigger:  "post_goal",
		Event: &entitlement.SeriesEvent{
			Type:   "Goal",
			Detail: "Normal Goal",
			Side:   "away",
			Player: "D. Silva",
		},
	}}

	for _, entitled := range []bool{true, false} {
		event, ok := inPlaySeriesResponse(points, entitled)[0]["event"].(map[string]any)
		if !ok {
			t.Fatalf("entitled=%v: no event on an event-triggered point", entitled)
		}
		if event["type"] != "Goal" || event["side"] != "away" {
			t.Fatalf("entitled=%v: event = %v", entitled, event)
		}
		if event["player"] != "D. Silva" || event["detail"] != "Normal Goal" {
			t.Fatalf("entitled=%v: event = %v", entitled, event)
		}
	}
}

func TestInPlaySeriesOmitsTheEventOnATimerTick(t *testing.T) {
	points := []entitlement.SeriesPoint{{Sequence: 1, Minute: 10, Trigger: "timer"}}

	if _, present := inPlaySeriesResponse(points, true)[0]["event"]; present {
		t.Fatal("a timer tick carries an event; want it omitted")
	}
}

// Side is what makes the marker readable. A type without one would assert that
// something happened without saying to whom, so the source only fills Event
// when the join resolved both.
func TestInPlaySeriesEventAlwaysCarriesASide(t *testing.T) {
	points := []entitlement.SeriesPoint{{
		Sequence: 2,
		Minute:   6,
		Trigger:  "post_goal",
		Event:    &entitlement.SeriesEvent{Type: "Goal", Side: "home"},
	}}

	event := inPlaySeriesResponse(points, true)[0]["event"].(map[string]any)
	if event["side"] != "home" {
		t.Fatalf("side = %v", event["side"])
	}
	// Absent rather than empty: an empty string renders as a blank name.
	if _, present := event["player"]; present {
		t.Fatal("player present when the provider named nobody; want it omitted")
	}
	if _, present := event["detail"]; present {
		t.Fatal("detail present when empty; want it omitted")
	}
}

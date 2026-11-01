package httpserver

import (
	"testing"
	"time"
)

func baseMatch() MatchSummary {
	return MatchSummary{
		ID:        1538375,
		HomeTeam:  "Bayern München",
		AwayTeam:  "Bodø/Glimt",
		KickoffAt: time.Date(2026, 9, 17, 19, 0, 0, 0, time.UTC),
		Status:    "live",
	}
}

// A match with no events must not grow a field. Upcoming fixtures are most of
// the detail views served, and an empty array on every one of them is payload
// spent saying nothing.
func TestTimelineIsAbsentWhenThereAreNoEvents(t *testing.T) {
	response := matchSummaryResponse(baseMatch())
	if _, present := response["timeline"]; present {
		t.Errorf("timeline present on a match with no events: %v", response["timeline"])
	}
}

func TestTimelineRendersOneEventPerEntry(t *testing.T) {
	extra := 3
	match := baseMatch()
	match.Timeline = []MatchEvent{
		{Minute: 23, Clock: "23'", Kind: "goal", Type: "Goal", Detail: "Normal Goal", Side: "home", PlayerName: "Kane", AssistName: "Musiala"},
		{Minute: 90, Extra: &extra, Clock: "90+3'", Kind: "yellow_card", Type: "Card", Detail: "Yellow Card", Side: "away", PlayerName: "Hauge"},
	}

	entries, ok := matchSummaryResponse(match)["timeline"].([]map[string]any)
	if !ok {
		t.Fatalf("timeline is not a list of objects: %T", matchSummaryResponse(match)["timeline"])
	}
	if len(entries) != 2 {
		t.Fatalf("len(timeline) = %d, want 2", len(entries))
	}

	goal := entries[0]
	if goal["event_kind"] != "goal" || goal["clock"] != "23'" || goal["player"] != "Kane" || goal["assist"] != "Musiala" || goal["side"] != "home" {
		t.Errorf("goal entry wrong: %v", goal)
	}
	if goal["retracted"] != false {
		t.Errorf("an ordinary goal must not be marked retracted: %v", goal["retracted"])
	}
	// Absent, not null: the provider omits added time on most events and a
	// payload full of nulls makes an ordinary event look partial.
	if _, present := goal["extra"]; present {
		t.Errorf("extra present on an event outside added time: %v", goal["extra"])
	}

	card := entries[1]
	if card["extra"] != 3 || card["clock"] != "90+3'" {
		t.Errorf("added time lost: %v", card)
	}
	if _, present := card["assist"]; present {
		t.Errorf("assist present on a card: %v", card["assist"])
	}
}

// A cancelled goal stays on the timeline, flagged. Dropping it leaves a reader
// unable to explain why the score does not match the goal they saw given, and
// the retraction is itself what they want to know.
func TestRetractedGoalIsKeptAndFlagged(t *testing.T) {
	match := baseMatch()
	match.Timeline = []MatchEvent{
		{Minute: 41, Clock: "41'", Kind: "goal", Type: "Goal", Detail: "Normal Goal", Side: "away", PlayerName: "Høgh", Retracted: true},
		{Minute: 43, Clock: "43'", Kind: "var_retraction", Type: "Var", Detail: "Goal cancelled", Side: "away", Comments: "Offside"},
	}

	entries := matchSummaryResponse(match)["timeline"].([]map[string]any)
	if len(entries) != 2 {
		t.Fatalf("a retracted goal was dropped: %v", entries)
	}
	if entries[0]["retracted"] != true {
		t.Errorf("cancelled goal is not flagged: %v", entries[0])
	}
	if entries[1]["event_kind"] != "var_retraction" {
		t.Errorf("the VAR decision that explains the gap is missing: %v", entries[1])
	}
	if entries[1]["comments"] != "Offside" {
		t.Errorf("the reason is not carried: %v", entries[1])
	}
}

// An event whose team we have never mapped is rendered without a side rather
// than being attributed to one of them by default.
func TestUnresolvedSideIsOmittedRatherThanGuessed(t *testing.T) {
	match := baseMatch()
	match.Timeline = []MatchEvent{{Minute: 12, Clock: "12'", Kind: "unknown", Type: "Whistle", Detail: "Something new"}}

	entry := matchSummaryResponse(match)["timeline"].([]map[string]any)[0]
	if _, present := entry["side"]; present {
		t.Errorf("side guessed for an unmapped team: %v", entry["side"])
	}
	// The raw pair survives so an unrecognised event is still readable.
	if entry["type"] != "Whistle" || entry["detail"] != "Something new" {
		t.Errorf("raw provider values lost: %v", entry)
	}
}

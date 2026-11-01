package matches

import (
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/httpserver"
)

// The writer derives the same kinds in services/worker/internal/provider/
// apifootball (classifyEvent). The two cannot share code across the module
// boundary, so this table is the contract between them: every case here is a
// case there, and a provider-shaped input that classifies differently on the
// two sides shows up as a failure here rather than as a wrong timeline.
func TestEventKindMatchesTheWriter(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		eventType  string
		detail     string
		want       string
		whyItBites string
	}{
		{"ordinary goal", "Goal", "Normal Goal", "goal", ""},
		{"own goal is not a goal for the scorer", "Goal", "Own Goal", "own_goal",
			"both arrive as type=Goal; crediting an own goal to the scoring team is the error a reader always notices"},
		{"penalty scored", "Goal", "Penalty", "penalty_goal", ""},
		{"missed penalty is not a goal", "Goal", "Missed Penalty", "penalty_missed",
			"contains 'penalty', so order of the checks decides whether a miss becomes a goal"},
		{"yellow", "Card", "Yellow Card", "yellow_card", ""},
		{"red", "Card", "Red Card", "red_card", ""},
		{"second yellow is not a plain yellow", "Card", "Second Yellow card", "second_yellow_card",
			"contains 'yellow', so it must be checked before the yellow case"},
		{"substitution arrives lowercase", "subst", "Substitution 1", "substitution",
			"the provider capitalises Goal, Card and Var but not subst; a case-sensitive switch drops every substitution"},
		{"substitution spelled out", "Substitution", "Substitution 2", "substitution", ""},
		{"VAR cancelling a goal", "Var", "Goal cancelled", "var_retraction", ""},
		{"VAR disallowing a goal", "Var", "Goal Disallowed - offside", "var_retraction", ""},
		{"VAR that confirms rather than withdraws", "Var", "Penalty confirmed", "var_other", ""},
		{"unknown type is not silently dropped", "Whistle", "Something new", "unknown", ""},
		{"unknown card detail", "Card", "Blue Card", "unknown", ""},
		{"casing is ignored throughout", "gOaL", "oWn GoAl", "own_goal", ""},
		{"surrounding whitespace is ignored", "  Goal  ", "  Penalty  ", "penalty_goal", ""},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := EventKind(testCase.eventType, testCase.detail); got != testCase.want {
				t.Errorf("EventKind(%q, %q) = %q, want %q. %s",
					testCase.eventType, testCase.detail, got, testCase.want, testCase.whyItBites)
			}
		})
	}
}

// 90+3 arrives from the provider as two numbers. Collapsing them to 93' moves
// an injury-time winner to a minute that was never played.
func TestClockLabelKeepsAddedTimeDistinct(t *testing.T) {
	extra := func(value int) *int { return &value }

	for _, testCase := range []struct {
		name   string
		minute int
		extra  *int
		want   string
	}{
		{"no added time", 67, nil, "67'"},
		{"explicit zero added time reads as the plain minute", 90, extra(0), "90'"},
		{"added time", 90, extra(3), "90+3'"},
		{"first half added time", 45, extra(1), "45+1'"},
		{"kickoff", 0, nil, "0'"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := clockLabel(testCase.minute, testCase.extra); got != testCase.want {
				t.Errorf("clockLabel(%d, %v) = %q, want %q", testCase.minute, testCase.extra, got, testCase.want)
			}
		})
	}
}

func TestSortTimelinePutsAddedTimeAfterTheMinuteItExtends(t *testing.T) {
	extra := func(value int) *int { return &value }
	events := []httpserver.MatchEvent{
		{Minute: 90, Extra: extra(3), PlayerName: "injury time winner"},
		{Minute: 12, PlayerName: "opener"},
		{Minute: 90, PlayerName: "ninety flat"},
		{Minute: 45, Extra: extra(1), PlayerName: "first half stoppage"},
		{Minute: 45, PlayerName: "forty five flat"},
	}

	sortTimeline(events)

	want := []string{"opener", "forty five flat", "first half stoppage", "ninety flat", "injury time winner"}
	for index, name := range want {
		if events[index].PlayerName != name {
			t.Fatalf("position %d = %q, want %q (order: %v)", index, events[index].PlayerName, name, names(events))
		}
	}
}

// Two events in the same minute that the provider sent in a given order stay
// in that order: a substitution recorded after the goal it responded to should
// not jump ahead of it.
func TestSortTimelineIsStableWithinAMinute(t *testing.T) {
	events := []httpserver.MatchEvent{
		{Minute: 70, PlayerName: "goal"},
		{Minute: 70, PlayerName: "substitution"},
	}

	sortTimeline(events)

	if events[0].PlayerName != "goal" || events[1].PlayerName != "substitution" {
		t.Errorf("stable order lost: %v", names(events))
	}
}

// A fixture that has not kicked off has no events, so reading its timeline is
// a guaranteed-empty query on the most common detail view there is.
func TestTimelineIsOnlyReadForMatchesThatStarted(t *testing.T) {
	for _, status := range []string{"live", "halftime", "second_half", "extra_time", "penalties", "break", "finished", "abandoned", "suspended", "interrupted", "awarded", "walkover"} {
		if !timelineWorthReading(status) {
			t.Errorf("status %q should have a timeline", status)
		}
	}
	for _, status := range []string{"scheduled", "upcoming", "delayed", "postponed", "canceled"} {
		if timelineWorthReading(status) {
			t.Errorf("status %q should not trigger a timeline read", status)
		}
	}
}

func names(events []httpserver.MatchEvent) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.PlayerName)
	}
	return out
}

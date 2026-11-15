package marketing

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPickLabelReadsLikeAFan(t *testing.T) {
	cases := []struct{ market, selection, want string }{
		{"1x2", "home", "Arsenal to win"},
		{"1x2", "away", "Chelsea to win"},
		{"1x2", "draw", "Draw"},
		{"double_chance", "home_or_draw", "Arsenal or draw"},
		{"double_chance", "draw_or_away", "Chelsea or draw"},
		{"double_chance", "home_or_away", "Either team to win"},
		{"draw_no_bet", "away", "Chelsea to win (draw no bet)"},
		{"btts", "yes", "Both teams to score"},
		{"btts", "no", "Not both teams to score"},
		{"total_goals", "over_2.5", "Over 2.5 goals"},
		{"total_goals", "under_3.5", "Under 3.5 goals"},
		{"total_goals", "sideways_2.5", ""},
		{"correct_score", "2-1", ""},
	}
	for _, c := range cases {
		if got := PickLabel(c.market, c.selection, "Arsenal", "Chelsea"); got != c.want {
			t.Errorf("PickLabel(%q, %q) = %q, want %q", c.market, c.selection, got, c.want)
		}
	}
}

// The grader must agree with prediction_engine/settlement.py, or a results
// card could call a pick won that the public record calls lost.
func TestGradeMirrorsEngineSettlement(t *testing.T) {
	cases := []struct {
		market, selection string
		home, away        int
		want              string
	}{
		{"1x2", "home", 2, 1, ResultWon},
		{"1x2", "home", 1, 1, ResultLost},
		{"1x2", "draw", 0, 0, ResultWon},
		{"double_chance", "home_or_draw", 1, 1, ResultWon},
		{"double_chance", "home_or_draw", 0, 1, ResultLost},
		{"double_chance", "home_or_away", 1, 1, ResultLost},
		{"double_chance", "draw_or_away", 0, 2, ResultWon},
		{"draw_no_bet", "home", 1, 1, ResultVoid},
		{"draw_no_bet", "away", 0, 1, ResultWon},
		{"btts", "yes", 1, 1, ResultWon},
		{"btts", "yes", 2, 0, ResultLost},
		{"btts", "no", 2, 0, ResultWon},
		{"total_goals", "over_2.5", 2, 1, ResultWon},
		{"total_goals", "over_2.5", 1, 1, ResultLost},
		{"total_goals", "under_2.5", 1, 1, ResultWon},
	}
	for _, c := range cases {
		got, ok := Grade(c.market, c.selection, c.home, c.away)
		if !ok || got != c.want {
			t.Errorf("Grade(%s, %s, %d-%d) = %q,%v want %q", c.market, c.selection, c.home, c.away, got, ok, c.want)
		}
	}
	if _, ok := Grade("correct_score", "2-1", 2, 1); ok {
		t.Error("an unsupported market must not be graded")
	}
}

func TestLinkCarriesTracking(t *testing.T) {
	link := Link("https://sokalens.com", "/results", "telegram", "daily_results", "")
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if parsed.Path != "/results" || query.Get("utm_source") != "telegram" || query.Get("utm_medium") != "social" || query.Get("utm_campaign") != "daily_results" {
		t.Fatalf("unexpected link %s", link)
	}
}

func samplePick() Pick {
	return Pick{
		PredictionID: "p1", FixtureID: 101, Market: "1x2", Selection: "home", Confidence: 0.736,
		KickoffAt: time.Date(2026, 11, 11, 16, 30, 0, 0, time.UTC), LeagueID: 1, League: "Premier League",
		HomeTeamID: 10, AwayTeamID: 20, Home: "Arsenal", Away: "Chelsea",
	}
}

func TestPicksCaptionIsCompliantAndTracked(t *testing.T) {
	nairobi, _ := time.LoadLocation("Africa/Nairobi")
	caption := PicksCaption([]Pick{samplePick()}, "Wed 11 Nov", nairobi, "https://sokalens.com", "telegram")
	for _, want := range []string{
		"Arsenal to win · 74% confidence",
		"19:30 EAT",
		"/predictions/premier-league/arsenal-vs-chelsea-101?",
		"utm_source=telegram",
		Compliance,
	} {
		if !strings.Contains(caption, want) {
			t.Errorf("caption missing %q:\n%s", want, caption)
		}
	}
	for _, banned := range []string{"sure", "guarantee", "fixed"} {
		if strings.Contains(strings.ToLower(caption), banned) {
			t.Errorf("caption must never say %q", banned)
		}
	}
}

func TestResultsHeadlineCountsEveryGradedPick(t *testing.T) {
	two, one := 2, 1
	graded := []GradedPick{
		{Pick: samplePick(), Result: ResultWon, HomeGoals: &two, AwayGoals: &one},
		{Pick: samplePick(), Result: ResultLost},
		{Pick: samplePick(), Result: ResultLost},
		{Pick: samplePick(), Result: ResultVoid},
		{Pick: samplePick(), Result: ResultPending},
	}
	if got := ResultsHeadline(graded); got != "1 of 3 correct" {
		t.Fatalf("headline = %q", got)
	}
	caption := ResultsCaption(graded, "Wed 11 Nov", "https://sokalens.com", "facebook")
	if !strings.Contains(caption, "LOST") || !strings.Contains(caption, "Arsenal 2-1 Chelsea") || !strings.Contains(caption, "PENDING") {
		t.Fatalf("results caption must show losses, scores and pending picks:\n%s", caption)
	}
}

func TestGoalCaptionShowsLiveChances(t *testing.T) {
	goal := Goal{FixtureID: 101, Minute: 90, Extra: 3, HomeGoals: 2, AwayGoals: 1, League: "Premier League", Home: "Arsenal", Away: "Chelsea",
		Scorer: "B. Saka", Probs: &CardProbs{Home: 9412, Draw: 500, Away: 88}}
	caption := GoalCaption(goal, "https://sokalens.com", "telegram")
	for _, want := range []string{"GOAL 90+3'", "Arsenal 2-1 Chelsea", "(B. Saka)", "Arsenal 94%", "Draw 5%", "Chelsea 1%", Compliance} {
		if !strings.Contains(caption, want) {
			t.Errorf("goal caption missing %q:\n%s", want, caption)
		}
	}
}

func TestPreviewLinesOnlyStateWhatHistorySupports(t *testing.T) {
	p := samplePick()
	win := FormResult{HomeTeamID: 10, AwayTeamID: 99, HomeGoals: 2, AwayGoals: 0}
	awayLoss := FormResult{HomeTeamID: 98, AwayTeamID: 10, HomeGoals: 1, AwayGoals: 0}
	draw := FormResult{HomeTeamID: 20, AwayTeamID: 97, HomeGoals: 1, AwayGoals: 1}
	meeting := FormResult{HomeTeamID: 20, AwayTeamID: 10, HomeGoals: 0, AwayGoals: 3}

	lines := PreviewLines(p, []FormResult{win, awayLoss, win}, []FormResult{draw, draw, draw}, []FormResult{meeting, meeting})
	want := []string{
		"Arsenal, last 3: W L W",
		"Chelsea, last 3: D D D",
		"Last 2 meetings: Arsenal 2, draws 0, Chelsea 0",
	}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("lines = %q", lines)
	}
	if lines := PreviewLines(p, []FormResult{win, win}, nil, nil); len(lines) != 0 {
		t.Fatalf("two matches are not a form line: %q", lines)
	}
}

func TestResultsWaitForTheLastMatchButNotForever(t *testing.T) {
	kickoff := time.Date(2026, 11, 11, 17, 0, 0, 0, time.UTC)
	p := samplePick()
	p.KickoffAt = kickoff
	pending := []GradedPick{{Pick: p, Result: ResultWon}, {Pick: p, Result: ResultPending}}
	done := []GradedPick{{Pick: p, Result: ResultWon}, {Pick: p, Result: ResultLost}}

	if resultsDue(done, kickoff.Add(90*time.Minute)) {
		t.Error("a result posted before the match can have finished is wrong")
	}
	if !resultsDue(done, kickoff.Add(2*time.Hour)) {
		t.Error("all picks graded: post")
	}
	if resultsDue(pending, kickoff.Add(3*time.Hour)) {
		t.Error("a pending pick should hold the card for a while")
	}
	if !resultsDue(pending, kickoff.Add(6*time.Hour+time.Minute)) {
		t.Error("six hours on, a stuck feed must not swallow the card")
	}
}

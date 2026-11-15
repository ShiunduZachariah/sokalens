package marketing

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/seourl"
)

// Compliance is appended to every public caption. The cards carry "18+" in
// their footer; a caption is often read without the image (notification
// previews, forwards), so it says the rest.
const Compliance = "18+ · Analysis, not betting advice. SokaLens is not a bookmaker."

// SupportedMarkets are the markets a fan reads without a glossary. The picks
// query only selects these, and Grade mirrors the engine's settlement for
// exactly these (services/prediction-engine/prediction_engine/settlement.py).
var SupportedMarkets = []string{"1x2", "double_chance", "draw_no_bet", "btts", "total_goals"}

// Pick is one published prediction chosen for a post.
type Pick struct {
	PredictionID string    `json:"prediction_id"`
	FixtureID    int64     `json:"fixture_id"`
	Market       string    `json:"market"`
	Selection    string    `json:"selection"`
	Confidence   float64   `json:"confidence"`
	KickoffAt    time.Time `json:"kickoff_at"`
	LeagueID     int64     `json:"league_id"`
	HomeTeamID   int64     `json:"home_team_id"`
	AwayTeamID   int64     `json:"away_team_id"`
	League       string    `json:"league"`
	Home         string    `json:"home"`
	Away         string    `json:"away"`
	HomeLogo     string    `json:"home_logo,omitempty"`
	AwayLogo     string    `json:"away_logo,omitempty"`
}

// Results.
const (
	ResultWon     = "won"
	ResultLost    = "lost"
	ResultVoid    = "void"
	ResultPending = "pending"
)

// PickLabel says the pick the way a fan would: "Arsenal to win", not
// "Match Result: Home".
func PickLabel(market, selection, home, away string) string {
	selection = strings.ToLower(strings.TrimSpace(selection))
	switch market {
	case "1x2":
		switch selection {
		case "home":
			return home + " to win"
		case "away":
			return away + " to win"
		case "draw":
			return "Draw"
		}
	case "double_chance":
		switch selection {
		case "home_or_draw":
			return home + " or draw"
		case "draw_or_away":
			return away + " or draw"
		case "home_or_away":
			return "Either team to win"
		}
	case "draw_no_bet":
		switch selection {
		case "home":
			return home + " to win (draw no bet)"
		case "away":
			return away + " to win (draw no bet)"
		}
	case "btts":
		switch selection {
		case "yes":
			return "Both teams to score"
		case "no":
			return "Not both teams to score"
		}
	case "total_goals":
		if side, line, ok := overUnder(selection); ok {
			return strings.ToUpper(side[:1]) + side[1:] + " " + line + " goals"
		}
	}
	return ""
}

func overUnder(selection string) (side, line string, ok bool) {
	side, line, found := strings.Cut(selection, "_")
	if !found || (side != "over" && side != "under") {
		return "", "", false
	}
	if _, err := strconv.ParseFloat(line, 64); err != nil {
		return "", "", false
	}
	return side, line, true
}

// Grade settles a supported market from a final score, exactly as the engine
// does. ok is false for a market or selection it does not know.
func Grade(market, selection string, homeGoals, awayGoals int) (result string, ok bool) {
	selection = strings.ToLower(strings.TrimSpace(selection))
	outcome := "draw"
	if homeGoals > awayGoals {
		outcome = "home"
	} else if awayGoals > homeGoals {
		outcome = "away"
	}
	won := func(correct bool) (string, bool) {
		if correct {
			return ResultWon, true
		}
		return ResultLost, true
	}
	switch market {
	case "1x2":
		if selection != "home" && selection != "draw" && selection != "away" {
			return "", false
		}
		return won(selection == outcome)
	case "double_chance":
		winning := map[string][]string{
			"home": {"home_or_draw", "home_or_away"},
			"draw": {"home_or_draw", "draw_or_away"},
			"away": {"draw_or_away", "home_or_away"},
		}[outcome]
		switch selection {
		case "home_or_draw", "draw_or_away", "home_or_away":
			return won(selection == winning[0] || selection == winning[1])
		}
		return "", false
	case "draw_no_bet":
		if selection != "home" && selection != "away" {
			return "", false
		}
		if outcome == "draw" {
			return ResultVoid, true
		}
		return won(selection == outcome)
	case "btts":
		both := "no"
		if homeGoals > 0 && awayGoals > 0 {
			both = "yes"
		}
		if selection != "yes" && selection != "no" {
			return "", false
		}
		return won(selection == both)
	case "total_goals":
		side, line, valid := overUnder(selection)
		if !valid {
			return "", false
		}
		value, _ := strconv.ParseFloat(line, 64)
		total := float64(homeGoals + awayGoals)
		if total == value {
			// A whole-number line can push; the engine only publishes .5
			// lines, so this is defensive rather than expected.
			return ResultVoid, true
		}
		return won((side == "over") == (total > value))
	}
	return "", false
}

// Link builds a tracked public URL. Every automated post carries one, so
// visitor_acquisition can say which channel and which card brought a reader.
func Link(siteURL, path, source, campaign, content string) string {
	query := url.Values{}
	query.Set("utm_source", source)
	query.Set("utm_medium", "social")
	query.Set("utm_campaign", campaign)
	if content != "" {
		query.Set("utm_content", content)
	}
	if path == "" {
		path = "/"
	}
	return siteURL + path + "?" + query.Encode()
}

// FixturePath is the public page for a pick, built by the same function the
// sitemap uses so the link never takes a redirect.
func FixturePath(p Pick) string {
	return seourl.FixturePath(p.League, p.Home, p.Away, p.FixtureID)
}

// LocalTime formats an instant for the caption: "19:30 EAT".
func LocalTime(t time.Time, location *time.Location) string {
	return t.In(location).Format("15:04 MST")
}

// LocalDay formats a date for headings: "Tue 11 Nov".
func LocalDay(t time.Time, location *time.Location) string {
	return t.In(location).Format("Mon 2 Jan")
}

// Percent renders a 0-1 confidence as a whole percentage.
func Percent(value float64) int {
	if value <= 0 {
		return 0
	}
	if value >= 1 {
		return 100
	}
	return int(value*100 + 0.5)
}

// PicksCaption is the text that goes with the morning card.
func PicksCaption(picks []Pick, day string, location *time.Location, siteURL, source string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Today's %d picks · %s\n", len(picks), day)
	for i, p := range picks {
		fmt.Fprintf(&b, "\n%d. %s vs %s (%s, %s)\n", i+1, p.Home, p.Away, p.League, LocalTime(p.KickoffAt, location))
		fmt.Fprintf(&b, "Pick: %s · %d%% confidence\n", PickLabel(p.Market, p.Selection, p.Home, p.Away), Percent(p.Confidence))
		fmt.Fprintf(&b, "%s\n", Link(siteURL, FixturePath(p), source, "daily_picks", strconv.FormatInt(p.FixtureID, 10)))
	}
	b.WriteString("\nEvery pick is graded tonight, wins and losses.\n")
	b.WriteString(Compliance)
	return b.String()
}

// GradedPick is a posted pick with its outcome.
type GradedPick struct {
	Pick
	Result    string
	HomeGoals *int
	AwayGoals *int
}

// Score is "2-1", or "" before the match has a final score.
func (g GradedPick) Score() string {
	if g.HomeGoals == nil || g.AwayGoals == nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", *g.HomeGoals, *g.AwayGoals)
}

// Tally counts wins out of the picks that were actually graded.
func Tally(graded []GradedPick) (won, decided int) {
	for _, g := range graded {
		switch g.Result {
		case ResultWon:
			won++
			decided++
		case ResultLost:
			decided++
		}
	}
	return won, decided
}

func resultMark(result string) string {
	switch result {
	case ResultWon:
		return "WON"
	case ResultLost:
		return "LOST"
	case ResultVoid:
		return "VOID"
	}
	return "PENDING"
}

// ResultsHeadline is the one line that has to be honest: it counts every
// graded pick, so a bad day reads as a bad day.
func ResultsHeadline(graded []GradedPick) string {
	won, decided := Tally(graded)
	if decided == 0 {
		return "No picks graded yet"
	}
	return fmt.Sprintf("%d of %d correct", won, decided)
}

// ResultsCaption goes with the evening card.
func ResultsCaption(graded []GradedPick, day, siteURL, source string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Results · %s picks: %s\n\n", day, ResultsHeadline(graded))
	for _, g := range graded {
		score := g.Score()
		if score != "" {
			score = " (" + g.Home + " " + score + " " + g.Away + ")"
		} else {
			score = " (" + g.Home + " vs " + g.Away + ")"
		}
		fmt.Fprintf(&b, "%s · %s%s\n", resultMark(g.Result), PickLabel(g.Market, g.Selection, g.Home, g.Away), score)
	}
	b.WriteString("\nWe post every result, the misses too. Full public record:\n")
	b.WriteString(Link(siteURL, "/results", source, "daily_results", "") + "\n\n")
	b.WriteString(Compliance)
	return b.String()
}

// GoalCaption goes with a live goal card.
func GoalCaption(g Goal, siteURL, source string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "GOAL %s · %s %d-%d %s", g.MinuteLabel(), g.Home, g.HomeGoals, g.AwayGoals, g.Away)
	if g.Scorer != "" {
		fmt.Fprintf(&b, " (%s)", g.Scorer)
	}
	b.WriteString("\n")
	if g.Probs != nil {
		fmt.Fprintf(&b, "Live win chances now: %s %s · Draw %s · %s %s\n",
			g.Home, basisPoints(g.Probs.Home), basisPoints(g.Probs.Draw), g.Away, basisPoints(g.Probs.Away))
	}
	fmt.Fprintf(&b, "Follow it live: %s\n\n", Link(siteURL, g.Path(), source, "live_goal", strconv.FormatInt(g.FixtureID, 10)))
	b.WriteString(Compliance)
	return b.String()
}

// CorrectionCaption is posted when a goal we announced is taken away. The
// provider is the source of truth and corrections are visible: we never
// delete the original post, we say it changed.
func CorrectionCaption(g Goal, siteURL, source string) string {
	return fmt.Sprintf("CORRECTION · VAR: goal ruled out. %s %d-%d %s.\nFollow it live: %s\n\n%s",
		g.Home, g.HomeGoals, g.AwayGoals, g.Away,
		Link(siteURL, g.Path(), source, "live_goal", strconv.FormatInt(g.FixtureID, 10)), Compliance)
}

func basisPoints(value int) string {
	return strconv.Itoa((value+50)/100) + "%"
}

// Goal is a scoreline change in a watched match, read from inplay_snapshots.
type Goal struct {
	FixtureID  int64
	Sequence   int64
	Minute     int
	Extra      int
	HomeGoals  int
	AwayGoals  int
	League     string
	Home       string
	Away       string
	HomeLogo   string
	AwayLogo   string
	Scorer     string
	Probs      *CardProbs
	CapturedAt time.Time
}

// MinuteLabel is "67'" or "90+3'".
func (g Goal) MinuteLabel() string {
	if g.Minute <= 0 {
		return ""
	}
	if g.Extra > 0 {
		return fmt.Sprintf("%d+%d'", g.Minute, g.Extra)
	}
	return fmt.Sprintf("%d'", g.Minute)
}

// Path is the fixture page for the goal.
func (g Goal) Path() string {
	return seourl.FixturePath(g.League, g.Home, g.Away, g.FixtureID)
}

// Key identifies a scoreline in a match: two snapshots of the same goal share it.
func (g Goal) Key() string {
	return fmt.Sprintf("goal:%d:%d-%d", g.FixtureID, g.HomeGoals, g.AwayGoals)
}

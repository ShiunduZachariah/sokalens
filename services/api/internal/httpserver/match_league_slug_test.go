package httpserver

import (
	"testing"
	"time"
)

// A fixture page links its competition through league_slug. Resolving the
// display name instead was capped at 100 leagues and ambiguous across
// countries ("Second League" exists in five), which left most fixture pages
// without a league link.
func TestMatchSummaryResponseCarriesLeagueSlug(t *testing.T) {
	base := MatchSummary{ID: 7, LeagueName: "Second League", HomeTeam: "A", AwayTeam: "B", KickoffAt: time.Date(2026, 9, 24, 13, 30, 0, 0, time.UTC), Status: "scheduled"}

	withSlug := base
	withSlug.LeagueSlug = "league-egypt-second-league"
	if got := matchSummaryResponse(withSlug)["league_slug"]; got != "league-egypt-second-league" {
		t.Fatalf("league_slug = %v, want league-egypt-second-league", got)
	}

	if _, present := matchSummaryResponse(base)["league_slug"]; present {
		t.Fatal("league_slug present for a match with no league slug; want it omitted")
	}
}

func TestMatchSummaryResponseCarriesDisciplineOnlyWhenKnown(t *testing.T) {
	match := MatchSummary{ID: 7, LeagueName: "L", HomeTeam: "A", AwayTeam: "B", KickoffAt: time.Date(2026, 9, 24, 13, 30, 0, 0, time.UTC), Status: "scheduled"}
	if _, present := matchSummaryResponse(match)["discipline"]; present {
		t.Fatal("discipline present without a record")
	}
	match.Discipline = &MatchDiscipline{Home: TeamDiscipline{Matches: 10, YellowCards: 21, RedCards: 1, MatchCards: 44}}
	discipline, ok := matchSummaryResponse(match)["discipline"].(map[string]any)
	if !ok {
		t.Fatal("discipline missing")
	}
	home := discipline["home"].(map[string]any)
	if home["matches"] != 10 || home["yellow_cards"] != 21 || home["red_cards"] != 1 || home["match_cards"] != 44 {
		t.Fatalf("home = %v", home)
	}
	if away := discipline["away"].(map[string]any); away["matches"] != 0 {
		t.Fatalf("away = %v, want zero record", away)
	}
}

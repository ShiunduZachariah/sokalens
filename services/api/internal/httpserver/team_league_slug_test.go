package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A team page names its competition but could not link to it.
//
// The response carried league_name and no slug, and league URLs are
// slug-addressed -- so the one obvious journey off a team page, into the
// competition it plays in, had nowhere to go. Resolving by display name is not
// an option: "Second League" exists in five countries.
func TestTeamResponseCarriesLeagueSlug(t *testing.T) {
	detail := TeamDetail{
		TeamSummary: TeamSummary{ID: 9, Slug: "arsenal", Name: "Arsenal", Country: "England"},
		LeagueName:  "Premier League",
		LeagueSlug:  "league-england-premier-league",
	}

	body := teamResponseBody(t, detail)
	if body["league_slug"] != "league-england-premier-league" {
		t.Fatalf("league_slug = %v, want league-england-premier-league", body["league_slug"])
	}
	if body["league_name"] != "Premier League" {
		t.Fatalf("league_name = %v", body["league_name"])
	}
}

// Omitted, not empty. An empty string concatenated into a league URL points at
// the directory, so a team whose league could not be resolved would render a
// link that silently goes somewhere else.
func TestTeamResponseOmitsUnresolvedLeagueSlug(t *testing.T) {
	detail := TeamDetail{
		TeamSummary: TeamSummary{ID: 9, Slug: "arsenal", Name: "Arsenal"},
		LeagueName:  "Premier League",
	}

	if _, present := teamResponseBody(t, detail)["league_slug"]; present {
		t.Fatal("league_slug present for a team with no resolved league; want it omitted")
	}
}

func teamResponseBody(t *testing.T, detail TeamDetail) map[string]any {
	t.Helper()

	recorder := httptest.NewRecorder()
	teamDetailHandler(favoriteCatalogSource{team: detail}).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, "/v1/teams/arsenal", nil),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

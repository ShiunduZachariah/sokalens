package acca

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestLegCarriesTeamLogos pins the leg payload's logo fields.
//
// The web side has always read home_team_logo/away_team_logo (accaApi.ts) and
// TeamIdentity falls back to a generated initials crest when the logo is
// empty. Because the query selected only the team NAMES from the joins it
// already had, every leg on every card rendered the fallback -- which looked
// like a missing-logo bug in the UI and was a missing-column one in SQL.
func TestLegCarriesTeamLogos(t *testing.T) {
	query := legsQuery("public")

	for _, want := range []string{"'home_team_logo'", "'away_team_logo'"} {
		if !strings.Contains(query, want) {
			t.Errorf("legs query does not emit %s", want)
		}
	}
	// Read from the joins the query already has, rather than adding new ones.
	if !strings.Contains(query, "ht.logo_url") || !strings.Contains(query, "at.logo_url") {
		t.Error("logos must come from the existing ht/at joins")
	}
}

// TestLegLogosSurviveJSONRoundTrip guards the struct tags the web reader keys
// on. A rename here silently returns every card to initials.
func TestLegLogosSurviveJSONRoundTrip(t *testing.T) {
	encoded, err := json.Marshal(Leg{HomeTeam: "Charlotte", HomeTeamLogo: "https://cdn/c.png", AwayTeamLogo: "https://cdn/h.png"})
	if err != nil {
		t.Fatal(err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["home_team_logo"] != "https://cdn/c.png" || decoded["away_team_logo"] != "https://cdn/h.png" {
		t.Fatalf("logo keys not serialized as expected: %s", encoded)
	}
}

// A team with no crest on file must stay empty rather than becoming "null" or
// a broken image URL: TeamIdentity keys on the empty string to draw its crest.
func TestMissingLogoStaysEmpty(t *testing.T) {
	encoded, err := json.Marshal(Leg{HomeTeam: "Charlotte"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"home_team_logo":""`) {
		t.Fatalf("absent logo should serialize as an empty string: %s", encoded)
	}
}

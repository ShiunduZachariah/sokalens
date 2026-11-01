package httpserver

import (
	"testing"
	"time"
)

// The crest contract for the three list payloads the public feed renders rows
// from, following the precedent set by acca.Leg (internal/acca/leg_logo_test.go):
// flat `home_team_logo`/`away_team_logo` beside the names they belong to,
// always present, empty rather than null when the team has no crest on record.
// The clients key on the empty string to decide between an <img> and a drawn
// initials shield, so an absent key would make them handle the same gap twice.
//
// These assert the SERIALISER, not the database: the queries that feed them are
// covered where they live. What breaks silently is a rename or an `omitempty`
// creeping onto one of the two keys, which returns a whole surface to initials
// and looks like a rendering bug rather than a payload one.

func TestProviderPredictionRowCarriesTeamLogos(t *testing.T) {
	response := providerPredictionSummaryResponse(ProviderPredictionSummary{
		MatchID:      21,
		HomeTeam:     "Gor Mahia",
		AwayTeam:     "AFC Leopards",
		HomeTeamLogo: "https://cdn/gor.png",
		AwayTeamLogo: "https://cdn/afc.png",
		PublishedAt:  time.Date(2026, 5, 1, 9, 30, 0, 0, time.UTC),
	})

	if response["home_team_logo"] != "https://cdn/gor.png" {
		t.Errorf("home_team_logo = %v", response["home_team_logo"])
	}
	if response["away_team_logo"] != "https://cdn/afc.png" {
		t.Errorf("away_team_logo = %v", response["away_team_logo"])
	}
}

// A provider fallback row for a team with no crest still renders. The row is
// never excluded and the key is never dropped -- the logo is decoration, and
// the client draws a shield from the name it already has.
func TestProviderPredictionRowWithoutLogosKeepsEmptyKeys(t *testing.T) {
	response := providerPredictionSummaryResponse(ProviderPredictionSummary{
		MatchID:     21,
		HomeTeam:    "Gor Mahia",
		AwayTeam:    "AFC Leopards",
		PublishedAt: time.Date(2026, 5, 1, 9, 30, 0, 0, time.UTC),
	})

	for _, key := range []string{"home_team_logo", "away_team_logo"} {
		value, present := response[key]
		if !present {
			t.Fatalf("%s is absent; it must be present and empty", key)
		}
		if value != "" {
			t.Errorf("%s = %v, want an empty string", key, value)
		}
	}
}

// The engine prediction row is the other half of the same list: engine and
// provider rows sit side by side in Today/Tomorrow/Weekend, the market hubs and
// the Best Bets pool, so they have to spell the crest the same way.
func TestPredictionRowCarriesTeamLogos(t *testing.T) {
	response := predictionSummaryResponse(PredictionSummary{
		ID:           7,
		MatchID:      21,
		HomeTeam:     "Gor Mahia",
		AwayTeam:     "AFC Leopards",
		HomeTeamLogo: "https://cdn/gor.png",
		AwayTeamLogo: "https://cdn/afc.png",
		KickoffAt:    time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		PublishedAt:  time.Date(2026, 5, 1, 9, 30, 0, 0, time.UTC),
	})

	if response["home_team_logo"] != "https://cdn/gor.png" || response["away_team_logo"] != "https://cdn/afc.png" {
		t.Fatalf("prediction row logos = %v / %v", response["home_team_logo"], response["away_team_logo"])
	}

	empty := predictionSummaryResponse(PredictionSummary{ID: 7, MatchID: 21})
	for _, key := range []string{"home_team_logo", "away_team_logo"} {
		value, present := empty[key]
		if !present || value != "" {
			t.Errorf("%s = %v (present=%v), want present and empty", key, value, present)
		}
	}
}

// The shared match serialiser behind the league-hub fixture rows, the in-play
// live/starting-soon/finished rows and the match page. It omits the key when
// there is no crest rather than sending an empty one -- the convention the rest
// of this payload already follows for optional fields, and the one
// liveMatches.ts/catalog.ts already read through `?? ""`. Pinned because Phase
// D's crests depend on the key spelling either way.
func TestMatchSummaryCarriesTeamLogos(t *testing.T) {
	response := matchSummaryResponse(MatchSummary{
		ID:           21,
		HomeTeam:     "Gor Mahia",
		AwayTeam:     "AFC Leopards",
		HomeTeamLogo: "https://cdn/gor.png",
		AwayTeamLogo: "https://cdn/afc.png",
		KickoffAt:    time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
	})

	if response["home_team_logo"] != "https://cdn/gor.png" || response["away_team_logo"] != "https://cdn/afc.png" {
		t.Fatalf("match summary logos = %v / %v", response["home_team_logo"], response["away_team_logo"])
	}

	// No crest on record: the match is still serialised in full.
	empty := matchSummaryResponse(MatchSummary{
		ID:        21,
		HomeTeam:  "Gor Mahia",
		AwayTeam:  "AFC Leopards",
		KickoffAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
	})
	for _, key := range []string{"home_team_logo", "away_team_logo"} {
		if value, present := empty[key]; present && value != "" {
			t.Errorf("%s = %v, want absent or empty", key, value)
		}
	}
	if empty["home_team"] != "Gor Mahia" {
		t.Error("a match with no crests must still serialise its teams")
	}
}

// The results row was the last section 3 surface without crests, and the only
// one where the wire had to change: it served a pre-joined "Home vs Away" title
// and nothing else, so the client had no name to hash a fallback shield from.
// Splitting that string back apart would have been guessing at structure the
// value had already lost, so the two sides are carried beside the title rather
// than instead of it.
func TestPredictionResultRowCarriesBothSidesAndTheirLogos(t *testing.T) {
	payload := predictionResultSummaryResponse(PredictionResultSummary{
		PredictionID: 9,
		MatchID:      4517266,
		MatchTitle:   "Arsenal vs Chelsea",
		HomeTeam:     "Arsenal",
		AwayTeam:     "Chelsea",
		HomeTeamLogo: "https://example.test/arsenal.png",
		AwayTeamLogo: "https://example.test/chelsea.png",
	})

	for key, want := range map[string]any{
		// The joined title stays: it is the single-line fallback and the share
		// text, and removing it would break a client that still reads it.
		"match_title":    "Arsenal vs Chelsea",
		"home_team":      "Arsenal",
		"away_team":      "Chelsea",
		"home_team_logo": "https://example.test/arsenal.png",
		"away_team_logo": "https://example.test/chelsea.png",
	} {
		if payload[key] != want {
			t.Errorf("%s = %v, want %v", key, payload[key], want)
		}
	}
}

// A crestless team still produces a row. The client keys on the empty string to
// draw its initials shield, so an absent logo must never drop the fixture.
func TestPredictionResultRowWithoutLogosKeepsTheNames(t *testing.T) {
	payload := predictionResultSummaryResponse(PredictionResultSummary{
		MatchTitle: "Arsenal vs Chelsea",
		HomeTeam:   "Arsenal",
		AwayTeam:   "Chelsea",
	})

	if payload["home_team"] != "Arsenal" || payload["away_team"] != "Chelsea" {
		t.Fatalf("names must survive a missing crest: %v", payload)
	}
	if payload["home_team_logo"] != "" || payload["away_team_logo"] != "" {
		t.Fatalf("an absent crest is empty, not omitted: %v", payload)
	}
}

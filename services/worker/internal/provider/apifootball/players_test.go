package apifootball

import (
	"testing"
)

func TestParsePlayersFromSavedFixture(t *testing.T) {
	players, err := ParsePlayers(readFixture(t, "players.success.json"))
	if err != nil {
		t.Fatalf("ParsePlayers returned error: %v", err)
	}
	if len(players) != 2 {
		t.Fatalf("len(players) = %d, want 2", len(players))
	}

	first := players[0]
	if first.ID != 882 || first.Name != "P. Cech" || first.Age != 42 {
		t.Fatalf("first player = %+v, want id 882 P. Cech age 42", first)
	}
	if first.ProviderTeamID != 33 || first.Position != "Goalkeeper" || first.Number != 1 {
		t.Fatalf("first player statistics = %+v, want team 33 Goalkeeper number 1", first)
	}
	if first.PhotoURL == "" || first.Nationality != "Czech Republic" {
		t.Fatalf("first player photo/nationality = %q/%q", first.PhotoURL, first.Nationality)
	}

	// A player with empty statistics must parse with no team/position/number.
	second := players[1]
	if second.ID != 278 || second.ProviderTeamID != 0 || second.Position != "" || second.Number != 0 {
		t.Fatalf("second player = %+v, want empty statistics tolerated", second)
	}
}

func TestParsePlayersRequiresIDAndName(t *testing.T) {
	if _, err := ParsePlayers([]byte(`{"errors":[],"response":[{"player":{"id":0,"name":"X"},"statistics":[]}]}`)); err == nil {
		t.Fatal("ParsePlayers returned nil error for missing player id")
	}
	if _, err := ParsePlayers([]byte(`{"errors":[],"response":[{"player":{"id":1,"name":""},"statistics":[]}]}`)); err == nil {
		t.Fatal("ParsePlayers returned nil error for missing player name")
	}
}

func TestParseVenuesFromSavedFixture(t *testing.T) {
	venues, err := ParseVenues(readFixture(t, "venues.success.json"))
	if err != nil {
		t.Fatalf("ParseVenues returned error: %v", err)
	}
	if len(venues) != 1 {
		t.Fatalf("len(venues) = %d, want 1", len(venues))
	}
	venue := venues[0]
	if venue.ID != 556 || venue.Name != "Old Trafford" || venue.City != "Manchester" {
		t.Fatalf("venue = %+v, want id 556 Old Trafford Manchester", venue)
	}
	if venue.Address != "Sir Matt Busby Way" || venue.Country != "England" || venue.Capacity != 76212 || venue.Surface != "grass" {
		t.Fatalf("venue attributes = %+v", venue)
	}
	if venue.ImageURL == "" {
		t.Fatalf("venue image = %q, want non-empty", venue.ImageURL)
	}
}

func TestParseVenuesRequiresIDAndName(t *testing.T) {
	if _, err := ParseVenues([]byte(`{"errors":[],"response":[{"id":0,"name":"X"}]}`)); err == nil {
		t.Fatal("ParseVenues returned nil error for missing venue id")
	}
	if _, err := ParseVenues([]byte(`{"errors":[],"response":[{"id":1,"name":""}]}`)); err == nil {
		t.Fatal("ParseVenues returned nil error for missing venue name")
	}
}

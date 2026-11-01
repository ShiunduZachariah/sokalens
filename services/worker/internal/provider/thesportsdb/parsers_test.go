package thesportsdb

import "testing"

func TestParseTeamsPreservesLogoAndBadgeURLs(t *testing.T) {
	body := []byte(`{
	  "teams": [{
	    "idTeam": "133604",
	    "strTeam": "Arsenal FC",
	    "strTeamShort": "ARS",
	    "strCountry": "England",
	    "strLeague": "English Premier League",
	    "strTeamBadge": "https://thesportsdb.com/images/media/team/badge/arsenal.png",
	    "strTeamLogo": "https://thesportsdb.com/images/media/team/logo/arsenal.png",
	    "strStadium": "Emirates Stadium",
	    "intFormedYear": "1886"
	  }]
	}`)

	teams, err := ParseTeams(body)
	if err != nil {
		t.Fatalf("ParseTeams returned error: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("len = %d, want 1", len(teams))
	}
	got := teams[0]
	if got.ID != 133604 || got.Name != "Arsenal FC" || got.Country != "England" || got.League != "English Premier League" {
		t.Fatalf("team = %+v, want parsed identity", got)
	}
	if got.Logo != "https://thesportsdb.com/images/media/team/logo/arsenal.png" {
		t.Fatalf("logo = %q, want strTeamLogo URL", got.Logo)
	}
	if got.Badge != "https://thesportsdb.com/images/media/team/badge/arsenal.png" {
		t.Fatalf("badge = %q, want strTeamBadge URL", got.Badge)
	}
}

func TestParseLeaguesPreservesBadgeAndLogoURLs(t *testing.T) {
	body := []byte(`{
	  "leagues": [{
	    "idLeague": "4328",
	    "strLeague": "English Premier League",
	    "strSport": "Soccer",
	    "strCountry": "England",
	    "strBadge": "https://thesportsdb.com/images/media/league/badge/epl.png",
	    "strLogo": "https://thesportsdb.com/images/media/league/logo/epl.png"
	  }]
	}`)

	leagues, err := ParseLeagues(body)
	if err != nil {
		t.Fatalf("ParseLeagues returned error: %v", err)
	}
	if len(leagues) != 1 {
		t.Fatalf("len = %d, want 1", len(leagues))
	}
	got := leagues[0]
	if got.ID != 4328 || got.Name != "English Premier League" || got.Sport != "Soccer" || got.Country != "England" {
		t.Fatalf("league = %+v, want parsed identity", got)
	}
	if got.Logo != "https://thesportsdb.com/images/media/league/logo/epl.png" {
		t.Fatalf("logo = %q, want strLogo URL", got.Logo)
	}
	if got.Badge != "https://thesportsdb.com/images/media/league/badge/epl.png" {
		t.Fatalf("badge = %q, want strBadge URL", got.Badge)
	}
}

func TestParseEventsToleratesStringAndNumberScores(t *testing.T) {
	body := []byte(`{
	  "event": [
	    {
	      "idEvent": "1174485",
	      "strEvent": "Arsenal vs Chelsea",
	      "strHomeTeam": "Arsenal",
	      "strAwayTeam": "Chelsea",
	      "intHomeScore": "2",
	      "intAwayScore": 1,
	      "dateEvent": "2026-08-15",
	      "strTime": "14:00",
	      "strLeague": "English Premier League",
	      "strStatus": "FT",
	      "strVenue": "Emirates Stadium"
	    },
	    {
	      "idEvent": "1174486",
	      "strEvent": "Liverpool vs Everton",
	      "strHomeTeam": "Liverpool",
	      "strAwayTeam": "Everton",
	      "intHomeScore": null,
	      "intAwayScore": null,
	      "dateEvent": "2026-08-16",
	      "strTime": "16:30",
	      "strLeague": "English Premier League",
	      "strStatus": "NS",
	      "strVenue": ""
	    }
	  ]
	}`)

	events, err := ParseEvents(body)
	if err != nil {
		t.Fatalf("ParseEvents returned error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("len = %d, want 2", len(events))
	}
	finished := events[0]
	if finished.ID != 1174485 || finished.Name != "Arsenal vs Chelsea" ||
		finished.Home != "Arsenal" || finished.Away != "Chelsea" ||
		finished.HomeScore != 2 || finished.AwayScore != 1 {
		t.Fatalf("finished event = %+v, want string/number scores parsed", finished)
	}
	if finished.Date != "2026-08-15" || finished.Time != "14:00" || finished.Status != "FT" || finished.Venue != "Emirates Stadium" {
		t.Fatalf("finished event metadata = %+v", finished)
	}
	scheduled := events[1]
	if scheduled.HomeScore != 0 || scheduled.AwayScore != 0 {
		t.Fatalf("scheduled event scores = %d/%d, want 0 for null scores", scheduled.HomeScore, scheduled.AwayScore)
	}
}

func TestParseEventsAcceptsLastEventsResultsEnvelope(t *testing.T) {
	body := []byte(`{"results":[{"idEvent":"99","strEvent":"Home vs Away","strHomeTeam":"Home","strAwayTeam":"Away","intHomeScore":"2","intAwayScore":"1","dateEvent":"2026-08-20","strTime":"19:00:00","strStatus":"Match Finished"}]}`)

	events, err := ParseEvents(body)
	if err != nil {
		t.Fatalf("ParseEvents returned error: %v", err)
	}
	if len(events) != 1 || events[0].ID != 99 || events[0].HomeScore != 2 || events[0].AwayScore != 1 {
		t.Fatalf("events = %+v, want last-events result", events)
	}
}

func TestParseTeamsToleratesNullAndMissingListKeys(t *testing.T) {
	for name, body := range map[string][]byte{
		"null list":    []byte(`{"teams":null}`),
		"empty list":   []byte(`{"teams":[]}`),
		"missing key":  []byte(`{"error":"no teams found"}`),
		"empty object": []byte(`{}`),
	} {
		t.Run(name, func(t *testing.T) {
			teams, err := ParseTeams(body)
			if err != nil {
				t.Fatalf("ParseTeams returned error: %v", err)
			}
			if len(teams) != 0 {
				t.Fatalf("len = %d, want 0", len(teams))
			}
		})
	}
}

func TestParseLeaguesRejectsMalformedJSON(t *testing.T) {
	if _, err := ParseLeagues([]byte(`{"leagues": [`)); err == nil {
		t.Fatal("ParseLeagues returned nil error for truncated JSON")
	}
}

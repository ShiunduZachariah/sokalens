package footballdata

import "testing"

func TestParseCompetitionsPreservesFreePlanCatalogFields(t *testing.T) {
	body := []byte(`{
	  "count": 1,
	  "competitions": [{
	    "id": 2021,
	    "area": {"id": 2072, "name": "England", "code": "ENG"},
	    "name": "Premier League",
	    "code": "PL",
	    "type": "LEAGUE",
	    "emblem": "https://crests.football-data.org/PL.png",
	    "plan": "TIER_ONE",
	    "currentSeason": {"id": 2400, "startDate": "2026-08-15", "endDate": "2027-05-24", "currentMatchday": 1}
	  }]
	}`)

	competitions, err := ParseCompetitions(body)
	if err != nil {
		t.Fatalf("ParseCompetitions returned error: %v", err)
	}
	if len(competitions) != 1 {
		t.Fatalf("len = %d, want 1", len(competitions))
	}
	got := competitions[0]
	if got.ID != 2021 || got.Code != "PL" || got.Area.Name != "England" || got.Plan != "TIER_ONE" {
		t.Fatalf("competition = %+v", got)
	}
}

func TestParseMatchesAllowsNullableScoresForScheduledFixtures(t *testing.T) {
	body := []byte(`{
	  "resultSet": {"count": 1, "played": 0},
	  "matches": [{
	    "area": {"id": 2072, "name": "England", "code": "ENG"},
	    "competition": {"id": 2021, "name": "Premier League", "code": "PL", "type": "LEAGUE"},
	    "season": {"id": 2400, "startDate": "2026-08-15", "endDate": "2027-05-24", "currentMatchday": 1},
	    "id": 501,
	    "utcDate": "2026-08-15T14:00:00Z",
	    "status": "TIMED",
	    "minute": null,
	    "venue": null,
	    "matchday": 1,
	    "stage": "REGULAR_SEASON",
	    "homeTeam": {"id": 57, "name": "Arsenal FC", "shortName": "Arsenal", "tla": "ARS"},
	    "awayTeam": {"id": 61, "name": "Chelsea FC", "shortName": "Chelsea", "tla": "CHE"},
	    "score": {"winner": null, "duration": "REGULAR", "fullTime": {"home": null, "away": null}, "halfTime": {"home": null, "away": null}}
	  }]
	}`)

	matches, err := ParseMatches(body)
	if err != nil {
		t.Fatalf("ParseMatches returned error: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("len = %d, want 1", len(matches))
	}
	got := matches[0]
	if got.Score.FullTime.Home != nil || got.Score.HalfTime.Away != nil {
		t.Fatalf("scores should remain nil for scheduled fixture: %+v", got.Score)
	}
	if got.Status != "TIMED" {
		t.Fatalf("status = %q, want TIMED", got.Status)
	}
}

func TestParseMatchesAllowsScheduledPlaceholderPairings(t *testing.T) {
	body := []byte(`{
	  "resultSet": {"count": 1, "played": 0},
	  "matches": [{
	    "area": {"id": 2267, "name": "World", "code": "INT"},
	    "competition": {"id": 2000, "name": "FIFA World Cup", "code": "WC", "type": "CUP"},
	    "season": {"id": 2407, "startDate": "2026-06-11", "endDate": "2026-07-19"},
	    "id": 9001,
	    "utcDate": "2026-07-14T19:00:00Z",
	    "status": "TIMED",
	    "stage": "FINAL",
	    "homeTeam": {"id": null, "name": null, "shortName": null, "tla": null},
	    "awayTeam": {"id": null, "name": null, "shortName": null, "tla": null},
	    "score": {"winner": null, "duration": "REGULAR", "fullTime": {"home": null, "away": null}}
	  }]
	}`)

	matches, err := ParseMatches(body)
	if err != nil {
		t.Fatalf("ParseMatches returned error for scheduled placeholder pairing: %v", err)
	}
	if len(matches) != 1 || matches[0].HomeTeam.ID != 0 || matches[0].AwayTeam.Name != "" {
		t.Fatalf("matches = %+v, want retained scheduled placeholder for job-level accounting", matches)
	}
}

func TestParseMatchesRejectsPlayedMatchWithoutTeams(t *testing.T) {
	body := []byte(`{"matches":[{"area":{"id":2267,"name":"World","code":"INT"},"competition":{"id":2000,"name":"FIFA World Cup","code":"WC"},"season":{"id":2407,"startDate":"2026-06-11"},"id":9002,"utcDate":"2026-07-14T19:00:00Z","status":"FINISHED","homeTeam":{"id":null,"name":null},"awayTeam":{"id":61,"name":"Chelsea FC"},"score":{"fullTime":{"home":1,"away":0}}}]}`)

	if _, err := ParseMatches(body); err == nil {
		t.Fatal("ParseMatches returned nil error for played match without a home team")
	}
}

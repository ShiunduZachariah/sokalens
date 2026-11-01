package apifootball

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestParseStatusFromSavedFixture(t *testing.T) {
	status, err := ParseStatus(readFixture(t, "status.success.json"))
	if err != nil {
		t.Fatalf("ParseStatus returned error: %v", err)
	}
	if status.Subscription.Plan != "Pro" || !status.Subscription.Active {
		t.Fatalf("subscription = %+v, want active Pro plan", status.Subscription)
	}
	if status.Requests.Current != 42 || status.Requests.LimitDay != 7500 {
		t.Fatalf("requests = %+v, want current 42 limit 7500", status.Requests)
	}
}

func TestParseLeaguesFromSavedFixture(t *testing.T) {
	leagues, err := ParseLeagues(readFixture(t, "leagues.success.json"))
	if err != nil {
		t.Fatalf("ParseLeagues returned error: %v", err)
	}
	if len(leagues) != 1 {
		t.Fatalf("len(leagues) = %d, want 1", len(leagues))
	}
	league := leagues[0]
	if league.League.ID != 39 || league.League.Name != "Premier League" || league.League.Type != "League" {
		t.Fatalf("league = %+v, want Premier League id 39", league.League)
	}
	if league.Country.Name != "England" || league.Country.Code != "GB" {
		t.Fatalf("country = %+v, want England GB", league.Country)
	}
	if len(league.Seasons) != 1 || league.Seasons[0].Year != 2025 || !league.Seasons[0].Coverage.Fixtures.Events {
		t.Fatalf("seasons = %+v, want 2025 with fixture events coverage", league.Seasons)
	}
}

func TestParseTeamsFromSavedFixture(t *testing.T) {
	teams, err := ParseTeams(readFixture(t, "teams.success.json"))
	if err != nil {
		t.Fatalf("ParseTeams returned error: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("len(teams) = %d, want 1", len(teams))
	}
	team := teams[0]
	if team.Team.ID != 33 || team.Team.Name != "Manchester United" || team.Team.Code != "MUN" {
		t.Fatalf("team = %+v, want Manchester United id 33", team.Team)
	}
	if team.Venue.Name != "Old Trafford" || team.Venue.City != "Manchester" {
		t.Fatalf("venue = %+v, want Old Trafford in Manchester", team.Venue)
	}
}

func TestParseFixturesFromSavedFixture(t *testing.T) {
	fixtures, err := ParseFixtures(readFixture(t, "fixtures.success.json"))
	if err != nil {
		t.Fatalf("ParseFixtures returned error: %v", err)
	}
	if len(fixtures) != 1 {
		t.Fatalf("len(fixtures) = %d, want 1", len(fixtures))
	}
	fixture := fixtures[0]
	if fixture.Fixture.ID != 867946 || fixture.Fixture.Status.Short != "FT" {
		t.Fatalf("fixture = %+v, want id 867946 FT", fixture.Fixture)
	}
	if fixture.League.ID != 39 || fixture.Teams.Home.ID != 33 || fixture.Teams.Away.Name != "Chelsea" {
		t.Fatalf("fixture teams/league = %+v %+v, want Premier League Manchester United v Chelsea", fixture.League, fixture.Teams)
	}
	if fixture.Goals.Home == nil || *fixture.Goals.Home != 2 {
		t.Fatalf("home goals = %v, want 2", fixture.Goals.Home)
	}
	if fixture.Score.Fulltime.Home == nil || *fixture.Score.Fulltime.Home != 2 {
		t.Fatalf("fulltime home score = %v, want 2", fixture.Score.Fulltime.Home)
	}
}

func TestParseOddsFromSavedFixture(t *testing.T) {
	odds, err := ParseOdds(readFixture(t, "odds.success.json"))
	if err != nil {
		t.Fatalf("ParseOdds returned error: %v", err)
	}
	if len(odds) != 1 {
		t.Fatalf("len(odds) = %d, want 1", len(odds))
	}
	item := odds[0]
	if item.Fixture.ID != 867946 || item.League.ID != 39 {
		t.Fatalf("odds item = %+v, want fixture 867946 league 39", item)
	}
	if len(item.Bookmakers) != 1 || item.Bookmakers[0].Name != "Bet365" {
		t.Fatalf("bookmakers = %+v, want Bet365", item.Bookmakers)
	}
	if got := item.Bookmakers[0].Bets[0].Values[0].Odd; got != "2.20" {
		t.Fatalf("first odd = %q, want 2.20", got)
	}
}

func TestParseOddsAcceptsNumericOddsValues(t *testing.T) {
	odds, err := ParseOdds([]byte(`{"errors":[],"response":[{"fixture":{"id":867946,"date":"2026-05-14T18:00:00+00:00"},"league":{"id":39,"name":"Premier League"},"bookmakers":[{"id":1,"name":"Bet365","bets":[{"id":1,"name":"Match Winner","values":[{"value":"Home","odd":2.2},{"value":"Draw","odd":"3.4"},{"value":"Away","odd":3.1}]}]}]}]}`))
	if err != nil {
		t.Fatalf("ParseOdds returned error: %v", err)
	}
	if len(odds) != 1 || len(odds[0].Bookmakers) != 1 {
		t.Fatalf("odds = %+v, want one bookmaker item", odds)
	}
	if got := odds[0].Bookmakers[0].Bets[0].Values[0].Odd; got != "2.2" {
		t.Fatalf("first odd = %q, want 2.2", got)
	}
}

func TestParsersRejectSavedProviderErrors(t *testing.T) {
	body := readFixture(t, "provider_error.json")
	tests := parserErrorCases()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.parse(body)
			var providerErr *ProviderError
			if !errors.As(err, &providerErr) {
				t.Fatalf("error = %v, want typed ProviderError", err)
			}
			if providerErr.Category != "token" || providerErr.Message != "invalid api key" {
				t.Fatalf("provider error = %+v, want token/invalid api key", providerErr)
			}
		})
	}
}

func TestProviderErrorEnvelopePreservesPlanCategoryAndMessage(t *testing.T) {
	_, err := ParseFixtures([]byte(`{"errors":{"plan":"Free plans do not have access to the Last parameter."},"response":[]}`))
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error = %v, want typed ProviderError", err)
	}
	if providerErr.Category != "plan" || providerErr.Message != "Free plans do not have access to the Last parameter." {
		t.Fatalf("provider error = %+v", providerErr)
	}
}

func TestProviderErrorRetryabilityMatchesProviderCategory(t *testing.T) {
	tests := []struct {
		category string
		message  string
		want     bool
	}{
		{category: "plan", message: "upgrade required", want: false},
		{category: "token", message: "invalid api key", want: false},
		{category: "parameters", message: "invalid last parameter", want: false},
		{category: "rate", message: "too many requests", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.category, func(t *testing.T) {
			err := &ProviderError{Category: tt.category, Message: tt.message}
			if got := err.Retryable(); got != tt.want {
				t.Fatalf("Retryable() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParsersRejectSavedInvalidJSON(t *testing.T) {
	body := readFixture(t, "invalid_json.json")
	tests := parserErrorCases()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.parse(body); err == nil {
				t.Fatal("parser returned nil error for invalid JSON")
			}
		})
	}
}

func TestParsersRejectSavedMissingCriticalFields(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		parse   func([]byte) error
		want    string
	}{
		{name: "status", fixture: "status.missing_plan.json", parse: parseStatusError, want: "subscription plan"},
		{name: "leagues", fixture: "leagues.missing_id.json", parse: parseLeaguesError, want: "league id"},
		{name: "teams", fixture: "teams.missing_id.json", parse: parseTeamsError, want: "team id"},
		{name: "fixtures", fixture: "fixtures.missing_id.json", parse: parseFixturesError, want: "fixture id"},
		{name: "odds", fixture: "odds.missing_fixture_id.json", parse: parseOddsError, want: "fixture id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.parse(readFixture(t, tt.fixture))
			assertErrorContains(t, err, tt.want)
		})
	}
}

type parserErrorCase struct {
	name  string
	parse func([]byte) error
}

func parserErrorCases() []parserErrorCase {
	return []parserErrorCase{
		{name: "status", parse: parseStatusError},
		{name: "leagues", parse: parseLeaguesError},
		{name: "teams", parse: parseTeamsError},
		{name: "fixtures", parse: parseFixturesError},
		{name: "odds", parse: parseOddsError},
	}
}

func parseStatusError(body []byte) error {
	_, err := ParseStatus(body)
	return err
}

func parseLeaguesError(body []byte) error {
	_, err := ParseLeagues(body)
	return err
}

func parseTeamsError(body []byte) error {
	_, err := ParseTeams(body)
	return err
}

func parseFixturesError(body []byte) error {
	_, err := ParseFixtures(body)
	return err
}

func parseOddsError(body []byte) error {
	_, err := ParseOdds(body)
	return err
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("parser returned nil error, want %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

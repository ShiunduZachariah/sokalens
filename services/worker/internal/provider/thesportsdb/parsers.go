package thesportsdb

import (
	"encoding/json"
	"strconv"
	"strings"
)

// TeamItem is a TheSportsDB team search result with the fields the logo
// backfill cares about. strTeamLogo/strTeamBadge are full image URLs.
type TeamItem struct {
	ID      int
	Name    string
	Country string
	League  string
	Logo    string
	Badge   string
}

// LeagueItem is a TheSportsDB all_leagues result. strBadge/strLogo are full
// image URLs.
type LeagueItem struct {
	ID      int
	Name    string
	Sport   string
	Country string
	Logo    string
	Badge   string
}

// EventItem is a TheSportsDB eventsday result (date is YYYY-MM-DD, time is
// HH:MM local to the event venue).
type EventItem struct {
	ID        int
	Name      string
	Home      string
	Away      string
	HomeScore int
	AwayScore int
	Date      string
	Time      string
	League    string
	Status    string
	Venue     string
}

type rawTeam struct {
	IDTeam       string `json:"idTeam"`
	StrTeam      string `json:"strTeam"`
	StrCountry   string `json:"strCountry"`
	StrLeague    string `json:"strLeague"`
	StrTeamLogo  string `json:"strTeamLogo"`
	StrTeamBadge string `json:"strTeamBadge"`
}

type rawLeague struct {
	IDLeague   string `json:"idLeague"`
	StrLeague  string `json:"strLeague"`
	StrSport   string `json:"strSport"`
	StrCountry string `json:"strCountry"`
	StrBadge   string `json:"strBadge"`
	StrLogo    string `json:"strLogo"`
}

type rawEvent struct {
	IDEvent      string      `json:"idEvent"`
	StrEvent     string      `json:"strEvent"`
	StrHomeTeam  string      `json:"strHomeTeam"`
	StrAwayTeam  string      `json:"strAwayTeam"`
	IntHomeScore flexibleInt `json:"intHomeScore"`
	IntAwayScore flexibleInt `json:"intAwayScore"`
	DateEvent    string      `json:"dateEvent"`
	StrTime      string      `json:"strTime"`
	StrLeague    string      `json:"strLeague"`
	StrStatus    string      `json:"strStatus"`
	StrVenue     string      `json:"strVenue"`
}

// flexibleInt tolerates TheSportsDB's inconsistent score encodings: numbers,
// numeric strings, or null.
type flexibleInt int

func (f *flexibleInt) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*f = 0
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		value, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil {
			*f = 0
			return nil
		}
		*f = flexibleInt(value)
		return nil
	}
	var number int
	if err := json.Unmarshal(data, &number); err != nil {
		*f = 0
		return nil
	}
	*f = flexibleInt(number)
	return nil
}

// ParseTeams parses a searchteams.php/lookup_all_teams.php envelope.
func ParseTeams(body []byte) ([]TeamItem, error) {
	raw, err := parseList[rawTeam](body, "teams")
	if err != nil {
		return nil, err
	}
	items := make([]TeamItem, 0, len(raw))
	for _, item := range raw {
		items = append(items, TeamItem{
			ID:      parseEntityID(item.IDTeam),
			Name:    strings.TrimSpace(item.StrTeam),
			Country: strings.TrimSpace(item.StrCountry),
			League:  strings.TrimSpace(item.StrLeague),
			Logo:    strings.TrimSpace(item.StrTeamLogo),
			Badge:   strings.TrimSpace(item.StrTeamBadge),
		})
	}
	return items, nil
}

// ParseLeagues parses an all_leagues.php envelope.
func ParseLeagues(body []byte) ([]LeagueItem, error) {
	raw, err := parseList[rawLeague](body, "leagues")
	if err != nil {
		return nil, err
	}
	items := make([]LeagueItem, 0, len(raw))
	for _, item := range raw {
		items = append(items, LeagueItem{
			ID:      parseEntityID(item.IDLeague),
			Name:    strings.TrimSpace(item.StrLeague),
			Sport:   strings.TrimSpace(item.StrSport),
			Country: strings.TrimSpace(item.StrCountry),
			Logo:    strings.TrimSpace(item.StrLogo),
			Badge:   strings.TrimSpace(item.StrBadge),
		})
	}
	return items, nil
}

// ParseEvents parses eventsday.php ("event") and eventslast.php ("results")
// envelopes.
func ParseEvents(body []byte) ([]EventItem, error) {
	raw, err := parseList[rawEvent](body, "event")
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		raw, err = parseList[rawEvent](body, "results")
		if err != nil {
			return nil, err
		}
	}
	items := make([]EventItem, 0, len(raw))
	for _, item := range raw {
		items = append(items, EventItem{
			ID:        parseEntityID(item.IDEvent),
			Name:      strings.TrimSpace(item.StrEvent),
			Home:      strings.TrimSpace(item.StrHomeTeam),
			Away:      strings.TrimSpace(item.StrAwayTeam),
			HomeScore: int(item.IntHomeScore),
			AwayScore: int(item.IntAwayScore),
			Date:      strings.TrimSpace(item.DateEvent),
			Time:      strings.TrimSpace(item.StrTime),
			League:    strings.TrimSpace(item.StrLeague),
			Status:    strings.TrimSpace(item.StrStatus),
			Venue:     strings.TrimSpace(item.StrVenue),
		})
	}
	return items, nil
}

// parseList unmarshals a TheSportsDB list envelope. The envelope keys map to
// lists; a missing key or an explicit top-level "null" for the list key is an
// empty result, not an error (TheSportsDB returns e.g. "teams":null for no
// search hits).
func parseList[T any](body []byte, key string) ([]T, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	raw, ok := envelope[key]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var items []T
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func parseEntityID(raw string) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return value
}

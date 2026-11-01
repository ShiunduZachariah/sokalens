package footballdata

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Area struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Code string `json:"code"`
	Flag string `json:"flag"`
}

type Season struct {
	ID              int      `json:"id"`
	StartDate       string   `json:"startDate"`
	EndDate         string   `json:"endDate"`
	CurrentMatchday *int     `json:"currentMatchday"`
	Winner          *TeamRef `json:"winner"`
}

type Competition struct {
	ID            int    `json:"id"`
	Area          Area   `json:"area"`
	Name          string `json:"name"`
	Code          string `json:"code"`
	Type          string `json:"type"`
	Emblem        string `json:"emblem"`
	Plan          string `json:"plan"`
	CurrentSeason Season `json:"currentSeason"`
	LastUpdated   string `json:"lastUpdated"`
}

type TeamRef struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	ShortName string `json:"shortName"`
	TLA       string `json:"tla"`
	Crest     string `json:"crest"`
}

type Team struct {
	ID         int    `json:"id"`
	Area       Area   `json:"area"`
	Name       string `json:"name"`
	ShortName  string `json:"shortName"`
	TLA        string `json:"tla"`
	Crest      string `json:"crest"`
	Address    string `json:"address"`
	Website    string `json:"website"`
	Founded    *int   `json:"founded"`
	ClubColors string `json:"clubColors"`
	Venue      string `json:"venue"`
}

type Match struct {
	Area        Area        `json:"area"`
	Competition Competition `json:"competition"`
	Season      Season      `json:"season"`
	ID          int         `json:"id"`
	UTCDate     string      `json:"utcDate"`
	Status      string      `json:"status"`
	Minute      *int        `json:"minute"`
	Attendance  *int        `json:"attendance"`
	Venue       string      `json:"venue"`
	Matchday    *int        `json:"matchday"`
	Stage       string      `json:"stage"`
	Group       string      `json:"group"`
	LastUpdated string      `json:"lastUpdated"`
	HomeTeam    TeamRef     `json:"homeTeam"`
	AwayTeam    TeamRef     `json:"awayTeam"`
	Score       Score       `json:"score"`
}

func (m Match) HasCompleteTeams() bool {
	return m.HomeTeam.ID > 0 && m.HomeTeam.Name != "" && m.AwayTeam.ID > 0 && m.AwayTeam.Name != ""
}

type Score struct {
	Winner    *string   `json:"winner"`
	Duration  string    `json:"duration"`
	FullTime  ScoreLine `json:"fullTime"`
	HalfTime  ScoreLine `json:"halfTime"`
	Regular   ScoreLine `json:"regularTime"`
	ExtraTime ScoreLine `json:"extraTime"`
	Penalties ScoreLine `json:"penalties"`
}

type ScoreLine struct {
	Home *int `json:"home"`
	Away *int `json:"away"`
}

type StandingTable struct {
	Stage string          `json:"stage"`
	Type  string          `json:"type"`
	Group string          `json:"group"`
	Table []StandingEntry `json:"table"`
}

type StandingEntry struct {
	Position       int     `json:"position"`
	Team           TeamRef `json:"team"`
	PlayedGames    int     `json:"playedGames"`
	Form           string  `json:"form"`
	Won            int     `json:"won"`
	Draw           int     `json:"draw"`
	Lost           int     `json:"lost"`
	Points         int     `json:"points"`
	GoalsFor       int     `json:"goalsFor"`
	GoalsAgainst   int     `json:"goalsAgainst"`
	GoalDifference int     `json:"goalDifference"`
}

type Scorer struct {
	Player    Person  `json:"player"`
	Team      TeamRef `json:"team"`
	Goals     *int    `json:"goals"`
	Assists   *int    `json:"assists"`
	Penalties *int    `json:"penalties"`
}

type Person struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	FirstName   string `json:"firstName"`
	LastName    string `json:"lastName"`
	Nationality string `json:"nationality"`
	Position    string `json:"position"`
}

func ParseCompetitions(body []byte) ([]Competition, error) {
	var envelope struct {
		Competitions []Competition `json:"competitions"`
		Error        string        `json:"error"`
		Message      string        `json:"message"`
	}
	if err := parseEnvelope(body, &envelope); err != nil {
		return nil, err
	}
	for index := range envelope.Competitions {
		trimCompetition(&envelope.Competitions[index])
		if envelope.Competitions[index].ID <= 0 {
			return nil, fmt.Errorf("competition id is required at index %d", index)
		}
		if envelope.Competitions[index].Name == "" {
			return nil, fmt.Errorf("competition name is required at index %d", index)
		}
	}
	return envelope.Competitions, nil
}

func ParseTeams(body []byte) ([]Team, error) {
	var envelope struct {
		Teams []Team `json:"teams"`
	}
	if err := parseEnvelope(body, &envelope); err != nil {
		return nil, err
	}
	for index := range envelope.Teams {
		trimTeam(&envelope.Teams[index])
		if envelope.Teams[index].ID <= 0 {
			return nil, fmt.Errorf("team id is required at index %d", index)
		}
		if envelope.Teams[index].Name == "" {
			return nil, fmt.Errorf("team name is required at index %d", index)
		}
	}
	return envelope.Teams, nil
}

func ParseMatches(body []byte) ([]Match, error) {
	var envelope struct {
		Matches []Match `json:"matches"`
	}
	if err := parseEnvelope(body, &envelope); err != nil {
		return nil, err
	}
	for index := range envelope.Matches {
		trimMatch(&envelope.Matches[index])
		if err := validateMatch(envelope.Matches[index], index); err != nil {
			return nil, err
		}
	}
	return envelope.Matches, nil
}

func ParseStandings(body []byte) ([]StandingTable, error) {
	var envelope struct {
		Standings []StandingTable `json:"standings"`
	}
	if err := parseEnvelope(body, &envelope); err != nil {
		return nil, err
	}
	return envelope.Standings, nil
}

func ParseScorers(body []byte) ([]Scorer, error) {
	var envelope struct {
		Scorers []Scorer `json:"scorers"`
	}
	if err := parseEnvelope(body, &envelope); err != nil {
		return nil, err
	}
	return envelope.Scorers, nil
}

func parseEnvelope(body []byte, target any) error {
	var errEnvelope struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &errEnvelope); err != nil {
		return err
	}
	if strings.TrimSpace(errEnvelope.Error) != "" {
		return errors.New(strings.TrimSpace(errEnvelope.Error))
	}
	if strings.TrimSpace(errEnvelope.Message) != "" {
		return errors.New(strings.TrimSpace(errEnvelope.Message))
	}
	return json.Unmarshal(body, target)
}

func trimCompetition(item *Competition) {
	item.Area.Name = strings.TrimSpace(item.Area.Name)
	item.Area.Code = strings.ToUpper(strings.TrimSpace(item.Area.Code))
	item.Name = strings.TrimSpace(item.Name)
	item.Code = strings.ToUpper(strings.TrimSpace(item.Code))
	item.Type = strings.TrimSpace(item.Type)
	item.Emblem = strings.TrimSpace(item.Emblem)
	item.Plan = strings.TrimSpace(item.Plan)
}

func trimTeam(item *Team) {
	item.Area.Name = strings.TrimSpace(item.Area.Name)
	item.Area.Code = strings.ToUpper(strings.TrimSpace(item.Area.Code))
	item.Name = strings.TrimSpace(item.Name)
	item.ShortName = strings.TrimSpace(item.ShortName)
	item.TLA = strings.ToUpper(strings.TrimSpace(item.TLA))
	item.Crest = strings.TrimSpace(item.Crest)
	item.Venue = strings.TrimSpace(item.Venue)
}

func trimTeamRef(item *TeamRef) {
	item.Name = strings.TrimSpace(item.Name)
	item.ShortName = strings.TrimSpace(item.ShortName)
	item.TLA = strings.ToUpper(strings.TrimSpace(item.TLA))
	item.Crest = strings.TrimSpace(item.Crest)
}

func trimMatch(item *Match) {
	trimCompetition(&item.Competition)
	item.Area.Name = strings.TrimSpace(item.Area.Name)
	item.Area.Code = strings.ToUpper(strings.TrimSpace(item.Area.Code))
	item.UTCDate = strings.TrimSpace(item.UTCDate)
	item.Status = strings.ToUpper(strings.TrimSpace(item.Status))
	item.Venue = strings.TrimSpace(item.Venue)
	item.Stage = strings.ToUpper(strings.TrimSpace(item.Stage))
	item.Group = strings.ToUpper(strings.TrimSpace(item.Group))
	trimTeamRef(&item.HomeTeam)
	trimTeamRef(&item.AwayTeam)
}

func validateMatch(item Match, index int) error {
	if item.ID <= 0 {
		return fmt.Errorf("match id is required at index %d", index)
	}
	if item.UTCDate == "" {
		return fmt.Errorf("match utcDate is required at index %d", index)
	}
	if item.Status == "" {
		return fmt.Errorf("match status is required at index %d", index)
	}
	if item.Competition.ID <= 0 {
		return fmt.Errorf("competition id is required at index %d", index)
	}
	if !item.HasCompleteTeams() && isScheduledPlaceholderStatus(item.Status) {
		return nil
	}
	if item.HomeTeam.ID <= 0 || item.HomeTeam.Name == "" {
		return fmt.Errorf("home team is required at index %d", index)
	}
	if item.AwayTeam.ID <= 0 || item.AwayTeam.Name == "" {
		return fmt.Errorf("away team is required at index %d", index)
	}
	return nil
}

func isScheduledPlaceholderStatus(status string) bool {
	switch status {
	case "SCHEDULED", "TIMED":
		return true
	default:
		return false
	}
}

package apifootball

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ProviderError struct {
	Category   string
	Message    string
	retryAfter time.Duration
}

func (e *ProviderError) Retryable() bool {
	if e == nil {
		return false
	}
	category := strings.ToLower(strings.TrimSpace(e.Category))
	message := strings.ToLower(strings.TrimSpace(e.Message))
	switch category {
	case "plan", "token", "auth", "authentication", "access", "permission", "parameter", "parameters", "validation", "invalid":
		return false
	}
	for _, marker := range []string{"rate", "limit", "throttl", "too many request", "temporar", "timeout"} {
		if strings.Contains(category, marker) || strings.Contains(message, marker) {
			return true
		}
	}
	switch category {
	case "server", "network", "requests", "unavailable":
		return true
	default:
		return false
	}
}

func (e *ProviderError) RetryAfter() time.Duration {
	if e == nil || e.retryAfter < 0 {
		return 0
	}
	return e.retryAfter
}

// Deferrable distinguishes temporary provider capacity from an ordinary job
// failure. Rate-limit responses should wait for capacity without exhausting
// the job's retry budget.
func (e *ProviderError) Deferrable() bool {
	if e == nil || !e.Retryable() {
		return false
	}
	category := strings.ToLower(strings.TrimSpace(e.Category))
	message := strings.ToLower(strings.TrimSpace(e.Message))
	for _, marker := range []string{"rate", "limit", "throttl", "too many request"} {
		if strings.Contains(category, marker) || strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	category := strings.TrimSpace(e.Category)
	message := strings.TrimSpace(e.Message)
	if category == "" {
		category = "provider"
	}
	if message == "" {
		return "provider error: " + category
	}
	return fmt.Sprintf("provider error %s: %s", category, message)
}

type ProviderStatus struct {
	Account      StatusAccount      `json:"account"`
	Subscription StatusSubscription `json:"subscription"`
	Requests     StatusRequests     `json:"requests"`
}

type StatusAccount struct {
	FirstName string `json:"firstname"`
	LastName  string `json:"lastname"`
	Email     string `json:"email"`
}

type StatusSubscription struct {
	Plan   string `json:"plan"`
	End    string `json:"end"`
	Active bool   `json:"active"`
}

type StatusRequests struct {
	Current  int `json:"current"`
	LimitDay int `json:"limit_day"`
}

type LeagueItem struct {
	League  LeagueInfo     `json:"league"`
	Country Country        `json:"country"`
	Seasons []LeagueSeason `json:"seasons"`
}

type LeagueInfo struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Logo string `json:"logo"`
}

type LeagueSeason struct {
	Year     int            `json:"year"`
	Start    string         `json:"start"`
	End      string         `json:"end"`
	Current  bool           `json:"current"`
	Coverage LeagueCoverage `json:"coverage"`
}

type LeagueCoverage struct {
	Fixtures    FixtureCoverage `json:"fixtures"`
	Standings   bool            `json:"standings"`
	Players     bool            `json:"players"`
	TopScorers  bool            `json:"top_scorers"`
	TopAssists  bool            `json:"top_assists"`
	TopCards    bool            `json:"top_cards"`
	Injuries    bool            `json:"injuries"`
	Predictions bool            `json:"predictions"`
	Odds        bool            `json:"odds"`
}

type FixtureCoverage struct {
	Events             bool `json:"events"`
	Lineups            bool `json:"lineups"`
	StatisticsFixtures bool `json:"statistics_fixtures"`
	StatisticsPlayers  bool `json:"statistics_players"`
}

type TeamItem struct {
	Team  TeamInfo `json:"team"`
	Venue Venue    `json:"venue"`
}

type TeamInfo struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Code     string `json:"code"`
	Country  string `json:"country"`
	Founded  int    `json:"founded"`
	National bool   `json:"national"`
	Logo     string `json:"logo"`
}

type Venue struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	City     string `json:"city"`
	Capacity int    `json:"capacity"`
	Surface  string `json:"surface"`
	Image    string `json:"image"`
}

type FixtureItem struct {
	Fixture FixtureInfo   `json:"fixture"`
	League  FixtureLeague `json:"league"`
	Teams   FixtureTeams  `json:"teams"`
	Goals   FixtureGoals  `json:"goals"`
	Score   FixtureScore  `json:"score"`
}

type FixtureInfo struct {
	ID        int            `json:"id"`
	Referee   string         `json:"referee"`
	Timezone  string         `json:"timezone"`
	Date      string         `json:"date"`
	Timestamp int            `json:"timestamp"`
	Periods   FixturePeriods `json:"periods"`
	Venue     FixtureVenue   `json:"venue"`
	Status    FixtureStatus  `json:"status"`
}

type FixturePeriods struct {
	First  *int `json:"first"`
	Second *int `json:"second"`
}

type FixtureVenue struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	City string `json:"city"`
}

type FixtureStatus struct {
	Long    string `json:"long"`
	Short   string `json:"short"`
	Elapsed *int   `json:"elapsed"`
}

type FixtureLeague struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Country string `json:"country"`
	Logo    string `json:"logo"`
	Flag    string `json:"flag"`
	Season  int    `json:"season"`
	Round   string `json:"round"`
}

type FixtureTeams struct {
	Home FixtureTeam `json:"home"`
	Away FixtureTeam `json:"away"`
}

type FixtureTeam struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Logo   string `json:"logo"`
	Winner *bool  `json:"winner"`
}

type FixtureGoals struct {
	Home *int `json:"home"`
	Away *int `json:"away"`
}

type FixtureScore struct {
	Halftime  FixtureScoreLine `json:"halftime"`
	Fulltime  FixtureScoreLine `json:"fulltime"`
	Extratime FixtureScoreLine `json:"extratime"`
	Penalty   FixtureScoreLine `json:"penalty"`
}

type FixtureScoreLine struct {
	Home *int `json:"home"`
	Away *int `json:"away"`
}

type OddsItem struct {
	League     OddsLeague  `json:"league"`
	Fixture    OddsFixture `json:"fixture"`
	Update     string      `json:"update"`
	Bookmakers []Bookmaker `json:"bookmakers"`
}

type OddsLeague struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Country string `json:"country"`
	Logo    string `json:"logo"`
	Flag    string `json:"flag"`
	Season  int    `json:"season"`
}

type OddsFixture struct {
	ID        int    `json:"id"`
	Timezone  string `json:"timezone"`
	Date      string `json:"date"`
	Timestamp int    `json:"timestamp"`
}

type Bookmaker struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Bets []Bet  `json:"bets"`
}

type Bet struct {
	ID     int        `json:"id"`
	Name   string     `json:"name"`
	Values []OddValue `json:"values"`
}

type OddValue struct {
	Value string `json:"value"`
	Odd   string `json:"odd"`
}

func (o *OddValue) UnmarshalJSON(data []byte) error {
	type rawOddValue struct {
		Value any `json:"value"`
		Odd   any `json:"odd"`
	}
	var raw rawOddValue
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	value, err := stringifyProviderField(raw.Value)
	if err != nil {
		return fmt.Errorf("odd value label: %w", err)
	}
	odd, err := stringifyProviderField(raw.Odd)
	if err != nil {
		return fmt.Errorf("odd price: %w", err)
	}
	o.Value = value
	o.Odd = odd
	return nil
}

func ParseStatus(body []byte) (ProviderStatus, error) {
	if err := rejectProviderErrors(body); err != nil {
		return ProviderStatus{}, err
	}
	var envelope statusEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return ProviderStatus{}, err
	}
	status := trimStatus(envelope.Response)
	if status.Subscription.Plan == "" {
		return ProviderStatus{}, errors.New("subscription plan is required")
	}
	if status.Requests.LimitDay <= 0 {
		return ProviderStatus{}, errors.New("request limit_day is required")
	}
	return status, nil
}

func ParseLeagues(body []byte) ([]LeagueItem, error) {
	var envelope listEnvelope[LeagueItem]
	if err := parseProviderEnvelope(body, &envelope); err != nil {
		return nil, err
	}
	for index := range envelope.Response {
		trimLeagueItem(&envelope.Response[index])
		if envelope.Response[index].League.ID <= 0 {
			return nil, fmt.Errorf("league id is required at index %d", index)
		}
		if envelope.Response[index].League.Name == "" {
			return nil, fmt.Errorf("league name is required at index %d", index)
		}
	}
	return envelope.Response, nil
}

func ParseTeams(body []byte) ([]TeamItem, error) {
	var envelope listEnvelope[TeamItem]
	if err := parseProviderEnvelope(body, &envelope); err != nil {
		return nil, err
	}
	for index := range envelope.Response {
		trimTeamItem(&envelope.Response[index])
		if envelope.Response[index].Team.ID <= 0 {
			return nil, fmt.Errorf("team id is required at index %d", index)
		}
		if envelope.Response[index].Team.Name == "" {
			return nil, fmt.Errorf("team name is required at index %d", index)
		}
	}
	return envelope.Response, nil
}

func ParseFixtures(body []byte) ([]FixtureItem, error) {
	var envelope listEnvelope[FixtureItem]
	if err := parseProviderEnvelope(body, &envelope); err != nil {
		return nil, err
	}
	for index := range envelope.Response {
		trimFixtureItem(&envelope.Response[index])
		if err := validateFixtureItem(envelope.Response[index], index); err != nil {
			return nil, err
		}
	}
	return envelope.Response, nil
}

func ParseOdds(body []byte) ([]OddsItem, error) {
	var envelope listEnvelope[OddsItem]
	if err := parseProviderEnvelope(body, &envelope); err != nil {
		return nil, err
	}
	for index := range envelope.Response {
		trimOddsItem(&envelope.Response[index])
		if envelope.Response[index].Fixture.ID <= 0 {
			return nil, fmt.Errorf("fixture id is required at index %d", index)
		}
	}
	return envelope.Response, nil
}

type statusEnvelope struct {
	Errors   json.RawMessage `json:"errors"`
	Response ProviderStatus  `json:"response"`
}

type listEnvelope[T any] struct {
	Errors   json.RawMessage `json:"errors"`
	Response []T             `json:"response"`
}

func parseProviderEnvelope[T any](body []byte, envelope *listEnvelope[T]) error {
	if err := rejectProviderErrors(body); err != nil {
		return err
	}
	return json.Unmarshal(body, envelope)
}

func rejectProviderErrors(body []byte) error {
	var envelope struct {
		Errors json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	if providerErr := parseProviderError(envelope.Errors); providerErr != nil {
		return providerErr
	}
	return nil
}

func parseProviderError(raw json.RawMessage) *ProviderError {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "[]" || trimmed == "{}" || trimmed == "null" || trimmed == "false" {
		return nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err == nil && len(fields) > 0 {
		categories := make([]string, 0, len(fields))
		for category := range fields {
			categories = append(categories, category)
		}
		sort.Strings(categories)
		for _, category := range categories {
			message := providerErrorMessage(fields[category])
			if strings.TrimSpace(message) != "" {
				return &ProviderError{Category: strings.TrimSpace(category), Message: message}
			}
		}
		return &ProviderError{Category: strings.TrimSpace(categories[0]), Message: "provider rejected request"}
	}

	message := providerErrorMessage(raw)
	if message == "" {
		message = trimmed
	}
	return &ProviderError{Category: "provider", Message: message}
}

func providerErrorMessage(raw json.RawMessage) string {
	var message string
	if err := json.Unmarshal(raw, &message); err == nil {
		return strings.TrimSpace(message)
	}
	var messages []string
	if err := json.Unmarshal(raw, &messages); err == nil {
		trimmed := messages[:0]
		for _, item := range messages {
			if item = strings.TrimSpace(item); item != "" {
				trimmed = append(trimmed, item)
			}
		}
		return strings.Join(trimmed, "; ")
	}
	return strings.Trim(strings.TrimSpace(string(raw)), `"`)
}

func trimStatus(status ProviderStatus) ProviderStatus {
	status.Account.FirstName = strings.TrimSpace(status.Account.FirstName)
	status.Account.LastName = strings.TrimSpace(status.Account.LastName)
	status.Account.Email = strings.TrimSpace(status.Account.Email)
	status.Subscription.Plan = strings.TrimSpace(status.Subscription.Plan)
	status.Subscription.End = strings.TrimSpace(status.Subscription.End)
	return status
}

func trimLeagueItem(item *LeagueItem) {
	item.League.Name = strings.TrimSpace(item.League.Name)
	item.League.Type = strings.TrimSpace(item.League.Type)
	item.League.Logo = strings.TrimSpace(item.League.Logo)
	item.Country.Name = strings.TrimSpace(item.Country.Name)
	item.Country.Code = strings.ToUpper(strings.TrimSpace(item.Country.Code))
	item.Country.Flag = strings.TrimSpace(item.Country.Flag)
	for index := range item.Seasons {
		item.Seasons[index].Start = strings.TrimSpace(item.Seasons[index].Start)
		item.Seasons[index].End = strings.TrimSpace(item.Seasons[index].End)
	}
}

func trimTeamItem(item *TeamItem) {
	item.Team.Name = strings.TrimSpace(item.Team.Name)
	item.Team.Code = strings.ToUpper(strings.TrimSpace(item.Team.Code))
	item.Team.Country = strings.TrimSpace(item.Team.Country)
	item.Team.Logo = strings.TrimSpace(item.Team.Logo)
	item.Venue.Name = strings.TrimSpace(item.Venue.Name)
	item.Venue.Address = strings.TrimSpace(item.Venue.Address)
	item.Venue.City = strings.TrimSpace(item.Venue.City)
	item.Venue.Surface = strings.TrimSpace(item.Venue.Surface)
	item.Venue.Image = strings.TrimSpace(item.Venue.Image)
}

func trimFixtureItem(item *FixtureItem) {
	item.Fixture.Referee = strings.TrimSpace(item.Fixture.Referee)
	item.Fixture.Timezone = strings.TrimSpace(item.Fixture.Timezone)
	item.Fixture.Date = strings.TrimSpace(item.Fixture.Date)
	item.Fixture.Venue.Name = strings.TrimSpace(item.Fixture.Venue.Name)
	item.Fixture.Venue.City = strings.TrimSpace(item.Fixture.Venue.City)
	item.Fixture.Status.Long = strings.TrimSpace(item.Fixture.Status.Long)
	item.Fixture.Status.Short = strings.ToUpper(strings.TrimSpace(item.Fixture.Status.Short))
	item.League.Name = strings.TrimSpace(item.League.Name)
	item.League.Country = strings.TrimSpace(item.League.Country)
	item.League.Logo = strings.TrimSpace(item.League.Logo)
	item.League.Flag = strings.TrimSpace(item.League.Flag)
	item.League.Round = strings.TrimSpace(item.League.Round)
	item.Teams.Home.Name = strings.TrimSpace(item.Teams.Home.Name)
	item.Teams.Home.Logo = strings.TrimSpace(item.Teams.Home.Logo)
	item.Teams.Away.Name = strings.TrimSpace(item.Teams.Away.Name)
	item.Teams.Away.Logo = strings.TrimSpace(item.Teams.Away.Logo)
}

func validateFixtureItem(item FixtureItem, index int) error {
	if item.Fixture.ID <= 0 {
		return fmt.Errorf("fixture id is required at index %d", index)
	}
	if item.Fixture.Date == "" {
		return fmt.Errorf("fixture date is required at index %d", index)
	}
	if item.Fixture.Status.Short == "" {
		return fmt.Errorf("fixture status short code is required at index %d", index)
	}
	if item.League.ID <= 0 {
		return fmt.Errorf("league id is required at index %d", index)
	}
	if item.Teams.Home.ID <= 0 {
		return fmt.Errorf("home team id is required at index %d", index)
	}
	if item.Teams.Home.Name == "" {
		return fmt.Errorf("home team name is required at index %d", index)
	}
	if item.Teams.Away.ID <= 0 {
		return fmt.Errorf("away team id is required at index %d", index)
	}
	if item.Teams.Away.Name == "" {
		return fmt.Errorf("away team name is required at index %d", index)
	}
	return nil
}

func trimOddsItem(item *OddsItem) {
	item.League.Name = strings.TrimSpace(item.League.Name)
	item.League.Country = strings.TrimSpace(item.League.Country)
	item.League.Logo = strings.TrimSpace(item.League.Logo)
	item.League.Flag = strings.TrimSpace(item.League.Flag)
	item.Fixture.Timezone = strings.TrimSpace(item.Fixture.Timezone)
	item.Fixture.Date = strings.TrimSpace(item.Fixture.Date)
	item.Update = strings.TrimSpace(item.Update)
	for bookmakerIndex := range item.Bookmakers {
		bookmaker := &item.Bookmakers[bookmakerIndex]
		bookmaker.Name = strings.TrimSpace(bookmaker.Name)
		for betIndex := range bookmaker.Bets {
			bet := &bookmaker.Bets[betIndex]
			bet.Name = strings.TrimSpace(bet.Name)
			for valueIndex := range bet.Values {
				bet.Values[valueIndex].Value = strings.TrimSpace(bet.Values[valueIndex].Value)
				bet.Values[valueIndex].Odd = strings.TrimSpace(bet.Values[valueIndex].Odd)
			}
		}
	}
}

func stringifyProviderField(value any) (string, error) {
	switch v := value.(type) {
	case nil:
		return "", nil
	case string:
		return strings.TrimSpace(v), nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case json.Number:
		return v.String(), nil
	default:
		return "", fmt.Errorf("unsupported type %T", value)
	}
}

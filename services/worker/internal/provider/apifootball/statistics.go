package apifootball

import (
	"fmt"
	"strings"
)

// TeamStatistics is API-Football's per-team statistics block for one fixture.
// TeamID is the provider team id; the ingestion job resolves it to a canonical
// team id via the provider entity map.
type TeamStatistics struct {
	TeamID     int
	Statistics []StatisticItem
}

// StatisticItem is a single named statistic. Value is the raw provider value
// normalized to a string: an integer (counts), a string with a trailing '%'
// (possession, pass accuracy), or a numeric string (expected goals).
type StatisticItem struct {
	Type  string
	Value string
}

type fixtureStatisticsTeam struct {
	Team struct {
		ID int `json:"id"`
	} `json:"team"`
	Statistics []struct {
		Type  string `json:"type"`
		Value any    `json:"value"`
	} `json:"statistics"`
}

// ParseFixtureStatistics parses the API-Football /fixtures/statistics response
// into per-team statistics. It is tolerant of empty statistics arrays and
// missing fields: a team with no statistics is retained (callers decide whether
// to skip it), and null values collapse to the empty string.
func ParseFixtureStatistics(body []byte) ([]TeamStatistics, error) {
	var envelope listEnvelope[fixtureStatisticsTeam]
	if err := parseProviderEnvelope(body, &envelope); err != nil {
		return nil, err
	}
	teams := make([]TeamStatistics, 0, len(envelope.Response))
	for index, item := range envelope.Response {
		if item.Team.ID <= 0 {
			return nil, fmt.Errorf("team id is required at index %d", index)
		}
		team := TeamStatistics{
			TeamID:     item.Team.ID,
			Statistics: make([]StatisticItem, 0, len(item.Statistics)),
		}
		for _, stat := range item.Statistics {
			value, _ := stringifyProviderField(stat.Value)
			team.Statistics = append(team.Statistics, StatisticItem{
				Type:  strings.TrimSpace(stat.Type),
				Value: strings.TrimSpace(value),
			})
		}
		teams = append(teams, team)
	}
	return teams, nil
}

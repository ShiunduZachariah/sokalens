package apifootball

import (
	"fmt"
	"strings"
)

// PlayerRecord is a normalized API-Football player. ProviderTeamID is the
// provider team id pulled from statistics[0]; the canonical team id is resolved
// by the ingestion job via the entity map.
type PlayerRecord struct {
	ProviderTeamID int64
	ID             int64
	Name           string
	Age            int
	Number         int
	Position       string
	PhotoURL       string
	Nationality    string
}

// VenueRecord is a normalized API-Football venue.
type VenueRecord struct {
	ID       int64
	Name     string
	Address  string
	City     string
	Country  string
	Capacity int
	Surface  string
	ImageURL string
}

type playerItem struct {
	Player struct {
		ID          int    `json:"id"`
		Name        string `json:"name"`
		Age         int    `json:"age"`
		Nationality string `json:"nationality"`
		Photo       string `json:"photo"`
	} `json:"player"`
	Statistics []struct {
		Team struct {
			ID int `json:"id"`
		} `json:"team"`
		Position string `json:"position"`
		Number   int    `json:"number"`
	} `json:"statistics"`
}

type venueItem struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	City     string `json:"city"`
	Country  string `json:"country"`
	Capacity int    `json:"capacity"`
	Surface  string `json:"surface"`
	Image    string `json:"image"`
}

func ParsePlayers(body []byte) ([]PlayerRecord, error) {
	var envelope listEnvelope[playerItem]
	if err := parseProviderEnvelope(body, &envelope); err != nil {
		return nil, err
	}
	records := make([]PlayerRecord, 0, len(envelope.Response))
	for index, item := range envelope.Response {
		record := PlayerRecord{
			ID:          int64(item.Player.ID),
			Name:        strings.TrimSpace(item.Player.Name),
			Age:         item.Player.Age,
			Nationality: strings.TrimSpace(item.Player.Nationality),
			PhotoURL:    strings.TrimSpace(item.Player.Photo),
		}
		if record.ID <= 0 {
			return nil, fmt.Errorf("player id is required at index %d", index)
		}
		if record.Name == "" {
			return nil, fmt.Errorf("player name is required at index %d", index)
		}
		if len(item.Statistics) > 0 {
			record.ProviderTeamID = int64(item.Statistics[0].Team.ID)
			record.Position = strings.TrimSpace(item.Statistics[0].Position)
			record.Number = item.Statistics[0].Number
		}
		records = append(records, record)
	}
	return records, nil
}

func ParseVenues(body []byte) ([]VenueRecord, error) {
	var envelope listEnvelope[venueItem]
	if err := parseProviderEnvelope(body, &envelope); err != nil {
		return nil, err
	}
	records := make([]VenueRecord, 0, len(envelope.Response))
	for index, item := range envelope.Response {
		record := VenueRecord{
			ID:       int64(item.ID),
			Name:     strings.TrimSpace(item.Name),
			Address:  strings.TrimSpace(item.Address),
			City:     strings.TrimSpace(item.City),
			Country:  strings.TrimSpace(item.Country),
			Capacity: item.Capacity,
			Surface:  strings.TrimSpace(item.Surface),
			ImageURL: strings.TrimSpace(item.Image),
		}
		if record.ID <= 0 {
			return nil, fmt.Errorf("venue id is required at index %d", index)
		}
		if record.Name == "" {
			return nil, fmt.Errorf("venue name is required at index %d", index)
		}
		records = append(records, record)
	}
	return records, nil
}

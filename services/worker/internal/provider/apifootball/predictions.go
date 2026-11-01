package apifootball

import (
	"fmt"
	"strconv"
	"strings"
)

// PredictionItem is API-Football's native /predictions output for one fixture.
type PredictionItem struct {
	Predictions struct {
		Winner struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"winner"`
		WinOrDraw bool   `json:"win_or_draw"`
		UnderOver string `json:"under_over"`
		Advice    string `json:"advice"`
		Percent   struct {
			Home string `json:"home"`
			Draw string `json:"draw"`
			Away string `json:"away"`
		} `json:"percent"`
	} `json:"predictions"`
	Fixture struct {
		ID int `json:"id"`
	} `json:"fixture"`
}

// ParsePredictions parses the API-Football /predictions response.
func ParsePredictions(body []byte) ([]PredictionItem, error) {
	var envelope listEnvelope[PredictionItem]
	if err := parseProviderEnvelope(body, &envelope); err != nil {
		return nil, err
	}
	return envelope.Response, nil
}

// ParsePercent converts API-Football's "45%" strings into numeric percentages.
func ParsePercent(raw string) (float64, error) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), "%"))
	if trimmed == "" {
		return 0, nil
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid percent value %q", raw)
	}
	if value < 0 || value > 100 {
		return 0, fmt.Errorf("percent value out of range %q", raw)
	}
	return value, nil
}

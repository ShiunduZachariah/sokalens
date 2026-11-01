package httpserver

import (
	"math"
	"strings"
)

// PopulateProviderPrimitives maps provider-native fields onto the canonical
// prediction fields shared by the API and web application.
func PopulateProviderPrimitives(summary *ProviderPredictionSummary) {
	if summary == nil {
		return
	}
	summary.MarketKey = "1x2"
	summary.Selection = providerSelection(*summary)
	summary.Confidence = providerConfidence(*summary)
	summary.PublishedVersion = 0
}

// CanonicalProviderMatchPredictions exposes every useful provider primitive on
// match detail. API-Football supplies a 1x2 winner and, when present, a totals
// line such as "Over 2.5".
func CanonicalProviderMatchPredictions(summary ProviderPredictionSummary) []MatchPredictionSummary {
	PopulateProviderPrimitives(&summary)
	base := MatchPredictionSummary{
		Source:    "provider",
		MarketKey: "1x2", Selection: summary.Selection, Confidence: summary.Confidence,
		PublishedAt: summary.PublishedAt, UpdatedAt: summary.PublishedAt, LastEvaluatedAt: summary.PublishedAt,
	}
	result := []MatchPredictionSummary{base}
	if selection := canonicalTotalGoalsSelection(summary.UnderOver); selection != "" {
		total := base
		total.MarketKey = "total_goals"
		total.Selection = selection
		result = append(result, total)
	}
	return result
}

func providerSelection(summary ProviderPredictionSummary) string {
	if strings.EqualFold(summary.PredictedWinner, summary.HomeTeam) {
		return "Home"
	}
	if strings.EqualFold(summary.PredictedWinner, summary.AwayTeam) {
		return "Away"
	}
	return "Draw"
}

func providerConfidence(summary ProviderPredictionSummary) float64 {
	best := math.Max(summary.PercentHome, math.Max(summary.PercentDraw, summary.PercentAway))
	return math.Round(best*100) / 100
}

func canonicalTotalGoalsSelection(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 2 {
		return ""
	}
	direction := strings.ToLower(fields[0])
	if direction != "over" && direction != "under" {
		return ""
	}
	return direction + "_" + fields[1]
}

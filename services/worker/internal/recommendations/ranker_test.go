package recommendations

import (
	"strings"
	"testing"
	"time"
)

func TestRankUsesOnlyStoredSignalsAndDeterministicOrdering(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	inputs := []Input{
		{ItemType: "prediction", ItemID: "b", MatchID: 2, LeagueID: 1, HomeTeamID: 20, AwayTeamID: 21, Market: "1x2", Confidence: 0.80, StartsAt: now.Add(2 * time.Hour), FreshnessAt: now},
		{ItemType: "prediction", ItemID: "a", MatchID: 1, LeagueID: 1, HomeTeamID: 10, AwayTeamID: 11, Market: "btts", Confidence: 0.72, StartsAt: now.Add(time.Hour), FreshnessAt: now},
	}
	signals := Signals{
		Favorites: map[string]bool{"team:10": true}, Interests: map[string]float64{},
		RecentSearches: map[string]bool{}, RecentEngagement: map[string]float64{},
		Viewed: map[string]bool{}, Dismissed: map[string]bool{},
	}
	weights := map[string]float64{"favorite_team": 70, "favorite_league": 45, "high_confidence": 20, "starting_soon": 15, "exploration": 5}
	candidates := Rank(inputs, signals, RankOptions{Weights: weights, Now: now, Maximum: 10})
	if len(candidates) != 2 || candidates[0].ItemID != "a" {
		t.Fatalf("candidates=%+v, want favorite-team candidate first", candidates)
	}
	if candidates[0].ReasonKey != "favorite_team" || candidates[0].Score <= candidates[1].Score {
		t.Fatalf("top candidate=%+v", candidates[0])
	}
}

func TestRankFiltersDismissedAndDeduplicates(t *testing.T) {
	now := time.Now().UTC()
	inputs := []Input{
		{ItemType: "prediction", ItemID: "same", StartsAt: now.Add(time.Hour)},
		{ItemType: "prediction", ItemID: "same", StartsAt: now.Add(2 * time.Hour)},
		{ItemType: "prediction", ItemID: "hidden", StartsAt: now.Add(time.Hour)},
	}
	signals := Signals{Dismissed: map[string]bool{"prediction:hidden": true}}
	candidates := Rank(inputs, signals, RankOptions{Weights: map[string]float64{"starting_soon": 15, "exploration": 5}, Now: now})
	if len(candidates) != 1 || candidates[0].ItemID != "same" {
		t.Fatalf("candidates=%+v", candidates)
	}
}

func TestStoredInputQueryCannotCallProviderTables(t *testing.T) {
	lower := strings.ToLower(storedInputsSQL)
	for _, forbidden := range []string{"provider_request", "provider_raw", "http://", "https://"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("stored input SQL contains forbidden provider dependency %q", forbidden)
		}
	}
	for _, required := range []string{"engine_predictions", "fixtures", "superseded_at is null"} {
		if !strings.Contains(lower, required) {
			t.Fatalf("stored input SQL missing %q", required)
		}
	}
}

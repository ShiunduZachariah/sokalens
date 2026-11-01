package apifootball

import (
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
)

func TestCatalogMapsCoreEndpointsToAPIManifest(t *testing.T) {
	catalog := EndpointCatalog()
	want := map[provider.EndpointKey]string{
		provider.EndpointStatus:             "status",
		provider.EndpointCountries:          "countries",
		provider.EndpointLeagues:            "leagues",
		provider.EndpointTeams:              "teams",
		provider.EndpointFixtures:           "fixtures",
		provider.EndpointFixtureEvents:      "fixtures/events",
		provider.EndpointFixtureStatistics:  "fixtures/statistics",
		provider.EndpointStandings:          "standings",
		provider.EndpointOddsPreMatch:       "odds",
		provider.EndpointOddsLive:           "odds/live",
		provider.EndpointProviderPrediction: "predictions",
		provider.EndpointPlayers:            "players",
		provider.EndpointVenues:             "venues",
	}

	for key, path := range want {
		spec, ok := catalog.Lookup(key)
		if !ok {
			t.Fatalf("catalog missing endpoint key %q", key)
		}
		if spec.Provider != ProviderName {
			t.Fatalf("Provider for %q = %q, want %q", key, spec.Provider, ProviderName)
		}
		if spec.Path != path {
			t.Fatalf("Path for %q = %q, want %q", key, spec.Path, path)
		}
		if err := spec.Validate(); err != nil {
			t.Fatalf("spec %q is invalid: %v", key, err)
		}
	}
}

func TestCatalogDocumentsPlannedPredictionFeatureSources(t *testing.T) {
	catalog := EndpointCatalog()
	want := []provider.PredictionFeatureSource{
		{FeatureKey: provider.FeatureCorners, EndpointKey: provider.EndpointFixtureStatistics},
		{FeatureKey: provider.FeatureCards, EndpointKey: provider.EndpointFixtureEvents},
		{FeatureKey: provider.FeatureFouls, EndpointKey: provider.EndpointFixtureStatistics},
		{FeatureKey: provider.FeatureThrowIns, EndpointKey: provider.EndpointFixtureStatistics},
		{FeatureKey: provider.FeatureTeamOverUnderGoals, EndpointKey: provider.EndpointOddsPreMatch},
		{FeatureKey: provider.FeatureHalfTimeFullTime, EndpointKey: provider.EndpointFixtures},
		{FeatureKey: provider.FeatureOverUnderGoals, EndpointKey: provider.EndpointOddsPreMatch},
		{FeatureKey: provider.FeatureMultiMatchBuilder, EndpointKey: provider.EndpointOddsPreMatch},
	}

	for _, source := range want {
		if !catalog.HasFeatureSource(source.FeatureKey, source.EndpointKey) {
			t.Fatalf("catalog missing feature source %+v", source)
		}
	}
}

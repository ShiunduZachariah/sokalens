package footballdata

import (
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
)

func TestCatalogMapsFootballDataEndpoints(t *testing.T) {
	catalog := NewCatalog()
	tests := map[provider.EndpointKey]string{
		provider.EndpointLeagues:   "competitions",
		provider.EndpointTeams:     "competitions/{competition}/teams",
		provider.EndpointFixtures:  "matches",
		provider.EndpointStandings: "competitions/{competition}/standings",
	}
	for key, wantPath := range tests {
		spec, ok := catalog.Lookup(key)
		if !ok {
			t.Fatalf("missing endpoint spec %q", key)
		}
		if spec.Provider != ProviderName || spec.Path != wantPath {
			t.Fatalf("spec %q = %+v, want provider=%q path=%q", key, spec, ProviderName, wantPath)
		}
		if err := spec.Validate(); err != nil {
			t.Fatalf("spec %q did not validate: %v", key, err)
		}
	}
}

func TestCatalogDocumentsFreePlanPredictionSources(t *testing.T) {
	catalog := NewCatalog()
	if !catalog.HasFeatureSource(provider.FeatureOverUnderGoals, provider.EndpointFixtures) {
		t.Fatal("over/under goals should be sourced from normalized football-data.org fixture scores")
	}
	if catalog.HasFeatureSource(provider.FeatureCorners, provider.EndpointFixtures) {
		t.Fatal("corners should not be advertised as free football-data.org fixture coverage")
	}
}

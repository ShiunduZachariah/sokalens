package footballdata

import "github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"

func NewCatalog() provider.Catalog {
	return provider.NewCatalog([]provider.EndpointSpec{
		{Key: provider.EndpointLeagues, Provider: ProviderName, Path: "competitions", EntityType: provider.EntityLeague, Group: "reference", QuotaCost: 1},
		{Key: provider.EndpointTeams, Provider: ProviderName, Path: "competitions/{competition}/teams", EntityType: provider.EntityTeam, Group: "reference", QuotaCost: 1},
		{Key: provider.EndpointFixtures, Provider: ProviderName, Path: "matches", EntityType: provider.EntityMatch, Group: "fixtures", QuotaCost: 1},
		{Key: provider.EndpointStandings, Provider: ProviderName, Path: "competitions/{competition}/standings", EntityType: provider.EntityStanding, Group: "performance", QuotaCost: 1},
	}, []provider.PredictionFeatureSource{
		{FeatureKey: provider.FeatureHalfTimeFullTime, EndpointKey: provider.EndpointFixtures, Notes: "Available when football-data.org score blocks include halftime and fulltime scores."},
		{FeatureKey: provider.FeatureOverUnderGoals, EndpointKey: provider.EndpointFixtures, Notes: "Available from normalized finished-match scores; no odds lines in the free plan."},
		{FeatureKey: provider.FeatureTeamOverUnderGoals, EndpointKey: provider.EndpointFixtures, Notes: "Available from normalized team scores; market odds require API-Football or an odds add-on later."},
	})
}

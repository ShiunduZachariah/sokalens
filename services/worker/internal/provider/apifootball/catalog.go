package apifootball

import "github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"

func EndpointCatalog() provider.Catalog {
	specs := []provider.EndpointSpec{
		{Key: provider.EndpointStatus, Provider: ProviderName, Path: "status", EntityType: provider.EntityProviderStatus, Group: "operations", QuotaCost: 1},
		{Key: provider.EndpointCountries, Provider: ProviderName, Path: "countries", EntityType: provider.EntityCountry, Group: "reference", QuotaCost: 1},
		{Key: provider.EndpointLeagues, Provider: ProviderName, Path: "leagues", EntityType: provider.EntityLeague, Group: "reference", QuotaCost: 1, SupportsPage: true},
		{Key: provider.EndpointTeams, Provider: ProviderName, Path: "teams", EntityType: provider.EntityTeam, Group: "reference", QuotaCost: 1, SupportsPage: true},
		{Key: provider.EndpointFixtures, Provider: ProviderName, Path: "fixtures", EntityType: provider.EntityMatch, Group: "fixtures", QuotaCost: 1, SupportsPage: true},
		{Key: provider.EndpointFixtureEvents, Provider: ProviderName, Path: "fixtures/events", EntityType: provider.EntityMatchEvent, Group: "fixtures", QuotaCost: 1},
		{Key: provider.EndpointFixtureStatistics, Provider: ProviderName, Path: "fixtures/statistics", EntityType: provider.EntityMatchStatistic, Group: "fixtures", QuotaCost: 1},
		{Key: provider.EndpointStandings, Provider: ProviderName, Path: "standings", EntityType: provider.EntityStanding, Group: "performance", QuotaCost: 1, SupportsPage: true},
		{Key: provider.EndpointOddsPreMatch, Provider: ProviderName, Path: "odds", EntityType: provider.EntityOdds, Group: "odds", QuotaCost: 1, SupportsPage: true},
		{Key: provider.EndpointOddsLive, Provider: ProviderName, Path: "odds/live", EntityType: provider.EntityOdds, Group: "odds", QuotaCost: 1, SupportsPage: true},
		{Key: provider.EndpointProviderPrediction, Provider: ProviderName, Path: "predictions", EntityType: provider.EntityProviderPrediction, Group: "benchmark", QuotaCost: 1},
		{Key: provider.EndpointPlayers, Provider: ProviderName, Path: "players", EntityType: provider.EntityPlayer, Group: "reference", QuotaCost: 1, SupportsPage: true},
		{Key: provider.EndpointVenues, Provider: ProviderName, Path: "venues", EntityType: provider.EntityVenue, Group: "reference", QuotaCost: 1, SupportsPage: true},
	}
	featureSources := []provider.PredictionFeatureSource{
		{FeatureKey: provider.FeatureCorners, EndpointKey: provider.EndpointFixtureStatistics, Notes: "Use only where fixture statistics include corner values."},
		{FeatureKey: provider.FeatureCards, EndpointKey: provider.EndpointFixtureEvents, Notes: "Cards settle from fixture events when available."},
		{FeatureKey: provider.FeatureFouls, EndpointKey: provider.EndpointFixtureStatistics, Notes: "Use null-safe handling because not every league has foul stats."},
		{FeatureKey: provider.FeatureThrowIns, EndpointKey: provider.EndpointFixtureStatistics, Notes: "Validate provider coverage before exposing this market."},
		{FeatureKey: provider.FeatureTeamOverUnderGoals, EndpointKey: provider.EndpointOddsPreMatch, Notes: "Odds source only; model features come from normalized fixtures/results."},
		{FeatureKey: provider.FeatureHalfTimeFullTime, EndpointKey: provider.EndpointFixtures, Notes: "Fixtures include halftime and fulltime score blocks after settlement."},
		{FeatureKey: provider.FeatureOverUnderGoals, EndpointKey: provider.EndpointOddsPreMatch, Notes: "Validate 0.5, 1.5, 2.5, 3.5, 4.5, and 5.5 coverage per bookmaker/league."},
		{FeatureKey: provider.FeatureMultiMatchBuilder, EndpointKey: provider.EndpointOddsPreMatch, Notes: "Bundle recommendations depend on supported underlying markets only."},
	}
	return provider.NewCatalog(specs, featureSources)
}

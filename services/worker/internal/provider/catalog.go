package provider

import (
	"errors"
	"fmt"
	"strings"
)

type EndpointKey string

const (
	EndpointStatus             EndpointKey = "status"
	EndpointCountries          EndpointKey = "countries"
	EndpointLeagues            EndpointKey = "leagues"
	EndpointTeams              EndpointKey = "teams"
	EndpointFixtures           EndpointKey = "fixtures"
	EndpointFixtureEvents      EndpointKey = "fixtures.events"
	EndpointFixtureStatistics  EndpointKey = "fixtures.statistics"
	EndpointStandings          EndpointKey = "standings"
	EndpointOddsPreMatch       EndpointKey = "odds.pre_match"
	EndpointOddsLive           EndpointKey = "odds.live"
	EndpointProviderPrediction EndpointKey = "predictions.provider"
	EndpointPlayers            EndpointKey = "players"
	EndpointVenues             EndpointKey = "venues"
)

type EntityType string

const (
	EntityProviderStatus     EntityType = "provider_status"
	EntityCountry            EntityType = "country"
	EntityLeague             EntityType = "league"
	EntityTeam               EntityType = "team"
	EntityMatch              EntityType = "match"
	EntityMatchEvent         EntityType = "match_event"
	EntityMatchStatistic     EntityType = "match_statistic"
	EntityStanding           EntityType = "standing"
	EntityOdds               EntityType = "odds"
	EntityProviderPrediction EntityType = "provider_prediction"
	EntityPlayer             EntityType = "player"
	EntityVenue              EntityType = "venue"
)

type PredictionFeatureKey string

const (
	FeatureCorners            PredictionFeatureKey = "corners"
	FeatureCards              PredictionFeatureKey = "cards"
	FeatureFouls              PredictionFeatureKey = "fouls"
	FeatureThrowIns           PredictionFeatureKey = "throw_ins"
	FeatureTeamOverUnderGoals PredictionFeatureKey = "team_over_under_goals"
	FeatureHalfTimeFullTime   PredictionFeatureKey = "half_time_full_time"
	FeatureOverUnderGoals     PredictionFeatureKey = "over_under_goals"
	FeatureMultiMatchBuilder  PredictionFeatureKey = "multi_match_builder"
)

type EndpointSpec struct {
	Key          EndpointKey
	Provider     string
	Path         string
	EntityType   EntityType
	Group        string
	QuotaCost    int
	SupportsPage bool
}

func (s EndpointSpec) Validate() error {
	if s.Key == "" {
		return errors.New("endpoint key is required")
	}
	if strings.TrimSpace(s.Provider) == "" {
		return fmt.Errorf("provider is required for endpoint %q", s.Key)
	}
	if strings.TrimSpace(s.Path) == "" {
		return fmt.Errorf("path is required for endpoint %q", s.Key)
	}
	if strings.HasPrefix(s.Path, "/") {
		return fmt.Errorf("path for endpoint %q must be relative", s.Key)
	}
	if s.EntityType == "" {
		return fmt.Errorf("entity type is required for endpoint %q", s.Key)
	}
	if s.QuotaCost <= 0 {
		return fmt.Errorf("quota cost for endpoint %q must be positive", s.Key)
	}
	return nil
}

type PredictionFeatureSource struct {
	FeatureKey  PredictionFeatureKey
	EndpointKey EndpointKey
	Notes       string
}

type Catalog struct {
	specs          map[EndpointKey]EndpointSpec
	featureSources []PredictionFeatureSource
}

func NewCatalog(specs []EndpointSpec, featureSources []PredictionFeatureSource) Catalog {
	byKey := make(map[EndpointKey]EndpointSpec, len(specs))
	for _, spec := range specs {
		byKey[spec.Key] = spec
	}
	return Catalog{specs: byKey, featureSources: append([]PredictionFeatureSource(nil), featureSources...)}
}

func (c Catalog) Lookup(key EndpointKey) (EndpointSpec, bool) {
	spec, ok := c.specs[key]
	return spec, ok
}

func (c Catalog) HasFeatureSource(feature PredictionFeatureKey, endpoint EndpointKey) bool {
	for _, source := range c.featureSources {
		if source.FeatureKey == feature && source.EndpointKey == endpoint {
			return true
		}
	}
	return false
}

func (c Catalog) Specs() []EndpointSpec {
	specs := make([]EndpointSpec, 0, len(c.specs))
	for _, spec := range c.specs {
		specs = append(specs, spec)
	}
	return specs
}

func (c Catalog) FeatureSources() []PredictionFeatureSource {
	return append([]PredictionFeatureSource(nil), c.featureSources...)
}

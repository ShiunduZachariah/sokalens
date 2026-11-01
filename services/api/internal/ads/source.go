// Package ads exposes advertising and policy-control read models. It answers
// two questions for the runtime: which campaigns are active for a placement,
// and whether server-side restriction rules or feature flags suppress ads for
// a given country/placement. All reads are PostgreSQL-backed and never touch
// a provider.
package ads

import (
	"context"
	"database/sql"
	"time"
)

// Campaign is a stored ad campaign row.
type Campaign struct {
	ID           int64
	CampaignKey  string
	Name         string
	Status       string
	PlacementKey string
	StartsAt     *time.Time
	EndsAt       *time.Time
	CreatedAt    time.Time
}

// RestrictionRule is an active server-side policy restriction.
type RestrictionRule struct {
	ID           int64
	FeatureKey   string
	Action       string
	CountryCode  string
	RegionCode   string
	Platform     string
	PlacementKey string
	StartsAt     time.Time
	EndsAt       *time.Time
	Reason       string
}

// FeatureFlag is a server-side feature switch.
type FeatureFlag struct {
	FeatureKey    string
	Enabled       bool
	DefaultAction string
	Description   string
	UpdatedAt     time.Time
}

// PlacementDecision is the server-side answer for one ad placement request.
type PlacementDecision struct {
	PlacementKey       string
	Allowed            bool
	Reason             string
	GamblingAdsEnabled bool
	Campaigns          []Campaign
	Restrictions       []RestrictionRule
}

// Source reads ads/policy state from PostgreSQL.
type Source struct {
	db *sql.DB
}

func NewSource(db *sql.DB) Source {
	return Source{db: db}
}

// ListCampaigns returns stored ad campaigns newest-first.
func (s Source) ListCampaigns(ctx context.Context, limit int) ([]Campaign, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, campaign_key, name, COALESCE(status, ''), COALESCE(placement_key, ''), starts_at, ends_at, created_at
FROM ad_campaigns
ORDER BY id DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	campaigns := []Campaign{}
	for rows.Next() {
		var campaign Campaign
		var startsAt, endsAt sql.NullTime
		if err := rows.Scan(&campaign.ID, &campaign.CampaignKey, &campaign.Name, &campaign.Status, &campaign.PlacementKey, &startsAt, &endsAt, &campaign.CreatedAt); err != nil {
			return nil, err
		}
		if startsAt.Valid {
			campaign.StartsAt = &startsAt.Time
		}
		if endsAt.Valid {
			campaign.EndsAt = &endsAt.Time
		}
		campaigns = append(campaigns, campaign)
	}
	return campaigns, rows.Err()
}

// ListRestrictionRules returns stored restriction rules newest-first.
func (s Source) ListRestrictionRules(ctx context.Context, limit int) ([]RestrictionRule, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, feature_key, action, COALESCE(country_code, ''), COALESCE(region_code, ''), COALESCE(platform, ''), COALESCE(placement_key, ''), starts_at, ends_at, COALESCE(reason, '')
FROM restriction_rules
ORDER BY id DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rules := []RestrictionRule{}
	for rows.Next() {
		var rule RestrictionRule
		var endsAt sql.NullTime
		if err := rows.Scan(&rule.ID, &rule.FeatureKey, &rule.Action, &rule.CountryCode, &rule.RegionCode, &rule.Platform, &rule.PlacementKey, &rule.StartsAt, &endsAt, &rule.Reason); err != nil {
			return nil, err
		}
		if endsAt.Valid {
			rule.EndsAt = &endsAt.Time
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

// ListFeatureFlags returns all stored feature flags.
func (s Source) ListFeatureFlags(ctx context.Context) ([]FeatureFlag, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT feature_key, enabled, COALESCE(default_action, ''), COALESCE(description, ''), updated_at
FROM feature_flags
ORDER BY feature_key ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	flags := []FeatureFlag{}
	for rows.Next() {
		var flag FeatureFlag
		if err := rows.Scan(&flag.FeatureKey, &flag.Enabled, &flag.DefaultAction, &flag.Description, &flag.UpdatedAt); err != nil {
			return nil, err
		}
		flags = append(flags, flag)
	}
	return flags, rows.Err()
}

// FeatureFlagEnabled reports whether a feature flag is enabled. Unknown flags
// are treated as disabled.
func (s Source) FeatureFlagEnabled(ctx context.Context, featureKey string) (bool, error) {
	var enabled bool
	err := s.db.QueryRowContext(ctx, `SELECT enabled FROM feature_flags WHERE feature_key = $1`, featureKey).Scan(&enabled)
	if errorsIsNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return enabled, nil
}

// ActiveRestrictions returns currently-active restriction rules that apply to
// a country (or globally when countryCode is empty). Only deny rules within
// their date window are returned.
func (s Source) ActiveRestrictions(ctx context.Context, countryCode string) ([]RestrictionRule, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, feature_key, action, COALESCE(country_code, ''), COALESCE(region_code, ''), COALESCE(platform, ''), COALESCE(placement_key, ''), starts_at, ends_at, COALESCE(reason, '')
FROM restriction_rules
WHERE action = 'deny'
  AND starts_at <= now()
  AND (ends_at IS NULL OR ends_at > now())
  AND (country_code IS NULL OR country_code = '' OR country_code = $1)
ORDER BY id DESC`, countryCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rules := []RestrictionRule{}
	for rows.Next() {
		var rule RestrictionRule
		var endsAt sql.NullTime
		if err := rows.Scan(&rule.ID, &rule.FeatureKey, &rule.Action, &rule.CountryCode, &rule.RegionCode, &rule.Platform, &rule.PlacementKey, &rule.StartsAt, &endsAt, &rule.Reason); err != nil {
			return nil, err
		}
		if endsAt.Valid {
			rule.EndsAt = &endsAt.Time
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

// ResolvePlacement decides whether ads may render for one placement key in one
// country and returns the active campaigns plus any blocking restrictions.
func (s Source) ResolvePlacement(ctx context.Context, placementKey, countryCode string) (PlacementDecision, error) {
	decision := PlacementDecision{PlacementKey: placementKey, Campaigns: []Campaign{}, Restrictions: []RestrictionRule{}}

	campaigns, err := s.activeCampaigns(ctx, placementKey)
	if err != nil {
		return PlacementDecision{}, err
	}
	decision.Campaigns = campaigns

	restrictions, err := s.placementRestrictions(ctx, placementKey, countryCode)
	if err != nil {
		return PlacementDecision{}, err
	}
	decision.Restrictions = restrictions

	gamblingAds, err := s.FeatureFlagEnabled(ctx, "gambling_ads")
	if err != nil {
		return PlacementDecision{}, err
	}
	decision.GamblingAdsEnabled = gamblingAds

	switch {
	case len(restrictions) > 0:
		decision.Allowed = false
		decision.Reason = "restricted"
		if restrictions[0].Reason != "" {
			decision.Reason = restrictions[0].Reason
		}
	default:
		decision.Allowed = true
		if len(campaigns) == 0 {
			// A missing direct campaign is not a denial. It advances the
			// decision flow to affiliate/network inventory; the provider router
			// decides whether any eligible fallback remains.
			decision.Reason = "provider_fallback"
		}
	}
	return decision, nil
}

func (s Source) activeCampaigns(ctx context.Context, placementKey string) ([]Campaign, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, campaign_key, name, COALESCE(status, ''), COALESCE(placement_key, ''), starts_at, ends_at, created_at
FROM ad_campaigns
WHERE placement_key = $1
  AND status = 'active'
  AND (starts_at IS NULL OR starts_at <= now())
  AND (ends_at IS NULL OR ends_at > now())
ORDER BY id DESC`, placementKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	campaigns := []Campaign{}
	for rows.Next() {
		var campaign Campaign
		var startsAt, endsAt sql.NullTime
		if err := rows.Scan(&campaign.ID, &campaign.CampaignKey, &campaign.Name, &campaign.Status, &campaign.PlacementKey, &startsAt, &endsAt, &campaign.CreatedAt); err != nil {
			return nil, err
		}
		if startsAt.Valid {
			campaign.StartsAt = &startsAt.Time
		}
		if endsAt.Valid {
			campaign.EndsAt = &endsAt.Time
		}
		campaigns = append(campaigns, campaign)
	}
	return campaigns, rows.Err()
}

func (s Source) placementRestrictions(ctx context.Context, placementKey, countryCode string) ([]RestrictionRule, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, feature_key, action, COALESCE(country_code, ''), COALESCE(region_code, ''), COALESCE(platform, ''), COALESCE(placement_key, ''), starts_at, ends_at, COALESCE(reason, '')
FROM restriction_rules
WHERE action = 'deny'
  AND starts_at <= now()
  AND (ends_at IS NULL OR ends_at > now())
  AND (placement_key IS NULL OR placement_key = '' OR placement_key = $1)
  AND (country_code IS NULL OR country_code = '' OR country_code = $2)
ORDER BY id DESC`, placementKey, countryCode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rules := []RestrictionRule{}
	for rows.Next() {
		var rule RestrictionRule
		var endsAt sql.NullTime
		if err := rows.Scan(&rule.ID, &rule.FeatureKey, &rule.Action, &rule.CountryCode, &rule.RegionCode, &rule.Platform, &rule.PlacementKey, &rule.StartsAt, &endsAt, &rule.Reason); err != nil {
			return nil, err
		}
		if endsAt.Valid {
			rule.EndsAt = &endsAt.Time
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func errorsIsNoRows(err error) bool {
	return err == sql.ErrNoRows
}

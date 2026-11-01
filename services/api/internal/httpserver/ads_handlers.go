package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/access"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/admin"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/ads"
)

var adPlacementKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,79}$`)

// adFeatureKeys are restriction-rule feature keys that suppress advertising.
var adFeatureKeys = map[string]bool{
	"gambling_ads":    true,
	"paid_promotions": true,
	"bookmaker_ctas":  true,
	"affiliate_links": true,
}

// countryCodeFromRequest returns the Cloudflare-supplied ISO country code for
// the visitor, or an empty string when it is not available (local/dev).
func countryCodeFromRequest(r *http.Request) string {
	country := strings.ToUpper(strings.TrimSpace(r.Header.Get("X-SokaLens-Country")))
	if len(country) == 2 {
		return country
	}
	return strings.ToUpper(strings.TrimSpace(r.Header.Get("CF-IPCountry")))
}

// contextHandler returns client runtime context: geo, entitlements, and the
// ads decision, all derived from server-side state. It is safe for anonymous
// visitors and never calls a provider.
func (d accountDependencies) contextHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		country := countryCodeFromRequest(r)
		response := map[string]any{
			"geo":          map[string]string{"country_code": country},
			"restrictions": []any{},
		}

		adsAllowed := true
		adsReason := ""
		if principal, ok := d.optionalUser(r); ok {
			adsAllowed = principal.access.AdsAllowed
			if !adsAllowed {
				adsReason = "pro_no_ads"
			}
			response["entitlements"] = map[string]any{
				"authenticated": true,
				"tier":          principal.access.Principal.Tier,
				"ads_hidden":    !principal.access.AdsAllowed,
				"capabilities":  principal.access.CapabilityNames(),
			}
		} else {
			response["entitlements"] = map[string]any{
				"authenticated": false,
				"tier":          string(access.TierGuest),
				"ads_hidden":    false,
				"capabilities":  []string{access.CapabilityPublicRead},
			}
		}
		// With ads switched off nothing may render one, whatever the tier or
		// country. Restrictions are still reported: they are policy, not ads.
		if !d.adsEnabled {
			adsAllowed = false
			adsReason = "ads_disabled"
		}

		if d.ads != nil {
			restrictions, err := d.ads.ActiveRestrictions(r.Context(), country)
			if err != nil {
				slog.Error("context ads restrictions unavailable", "error", err)
			} else {
				response["restrictions"] = restrictionsResponse(restrictions)
				if adsAllowed && adsBlockedByRules(restrictions) {
					adsAllowed = false
					adsReason = "country_restricted"
				}
			}
			gamblingAds, err := d.ads.ListFeatureFlags(r.Context())
			if err != nil {
				slog.Error("context feature flags unavailable", "error", err)
			} else {
				response["ads"] = map[string]any{
					"allowed":              adsAllowed,
					"reason":               adsReason,
					"gambling_ads_enabled": featureFlagEnabled(gamblingAds, "gambling_ads"),
				}
			}
		}
		if _, ok := response["ads"]; !ok {
			response["ads"] = map[string]any{"allowed": adsAllowed, "reason": adsReason, "gambling_ads_enabled": false}
		}

		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, response)
	}
}

func adsBlockedByRules(rules []ads.RestrictionRule) bool {
	for _, rule := range rules {
		// Placement-specific rules are enforced by the placement endpoint. A
		// runtime-context read has no placement and must not turn one denied slot
		// into a site-wide advertising ban.
		if rule.PlacementKey == "" && adFeatureKeys[rule.FeatureKey] {
			return true
		}
	}
	return false
}

func featureFlagEnabled(flags []ads.FeatureFlag, key string) bool {
	for _, flag := range flags {
		if flag.FeatureKey == key {
			return flag.Enabled
		}
	}
	return false
}

// adsHidden answers an ad endpoint while ADS_ENABLED is off exactly as an
// unknown path is answered, so the hidden surface is indistinguishable from
// one that does not exist.
func adsHidden(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, apiError("not_found", "Endpoint not found."))
}

func (d accountDependencies) adsPlacementHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		if d.ads == nil || d.adRouting == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ads_source_unavailable"})
			return
		}
		placementKey := strings.TrimPrefix(r.URL.Path, "/v1/ads/placements/")
		if !adPlacementKeyPattern.MatchString(placementKey) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_placement_key"})
			return
		}
		sessionID, allowed, reason, err := d.adSession(r, w)
		if err != nil {
			slog.Error("ad session unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ads_session_unavailable"})
			return
		}
		if !allowed {
			w.Header().Set("Cache-Control", "private, no-store")
			writeJSON(w, http.StatusOK, map[string]any{
				"placement_key": placementKey, "allowed": false, "reason": reason,
				"providers": []any{}, "campaigns": []any{}, "restrictions": []any{},
			})
			return
		}
		country := countryCodeFromRequest(r)
		decision, err := d.ads.ResolvePlacement(r.Context(), placementKey, country)
		if err != nil {
			slog.Error("ads placement unavailable", "error", err, "placement_key", placementKey)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ads_source_unavailable"})
			return
		}
		if !decision.Allowed {
			w.Header().Set("Cache-Control", "private, no-store")
			response := placementDecisionResponse(decision)
			response["providers"] = []any{}
			writeJSON(w, http.StatusOK, response)
			return
		}
		route, err := d.adRouting.Resolve(r.Context(), ads.ResolveRequest{
			SessionID: sessionID, PlacementKey: placementKey, CountryCode: country,
		})
		if err != nil {
			slog.Error("ad provider routing unavailable", "error", err, "placement_key", placementKey)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ads_routing_unavailable"})
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		response := placementDecisionResponse(decision)
		response["allowed"] = !route.Hidden
		response["reason"] = decision.Reason
		if route.Hidden {
			response["reason"] = route.Reason
		}
		response["preferred_provider"] = route.PreferredProvider
		response["active_provider"] = route.ActiveProvider
		response["providers"] = providerAttemptsResponse(route.Attempts, placementKey)
		writeJSON(w, http.StatusOK, response)
	}
}

func (d accountDependencies) adsOutcomeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if d.adRouting == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ads_routing_unavailable"})
			return
		}
		sessionID, allowed, _, err := d.adSession(r, w)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ads_session_unavailable"})
			return
		}
		if !allowed {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var report ads.OutcomeReport
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&report); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_ad_outcome"})
			return
		}
		if _, err := d.adRouting.Report(r.Context(), sessionID, report); err != nil {
			switch {
			case errors.Is(err, ads.ErrDuplicateReport):
				w.WriteHeader(http.StatusNoContent)
			case errors.Is(err, ads.ErrInvalidToken):
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_ad_attempt"})
			default:
				slog.Error("ad outcome unavailable", "error", err)
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ads_outcome_unavailable"})
			}
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d accountDependencies) adSession(r *http.Request, w http.ResponseWriter) (string, bool, string, error) {
	if principal, ok := d.optionalUser(r); ok {
		if !principal.access.AdsAllowed {
			return "", false, "pro_no_ads", nil
		}
		cookie, _ := r.Cookie(userSessionCookie)
		return "user:" + adSessionDigest(cookie.Value), true, "", nil
	}
	if cookie, err := r.Cookie(guestCookie); err == nil && cookie.Value != "" {
		return "guest:" + adSessionDigest(cookie.Value), true, "", nil
	}
	if d.guest == nil {
		return "", false, "", errors.New("guest access unavailable")
	}
	token, _, _, err := d.ensureGuest(w, r)
	if err != nil {
		return "", false, "", err
	}
	return "guest:" + adSessionDigest(token), true, "", nil
}

func adSessionDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func providerAttemptsResponse(attempts []ads.ProviderAttempt, placementKey string) []map[string]any {
	response := make([]map[string]any, 0, len(attempts))
	for _, attempt := range attempts {
		response = append(response, map[string]any{
			"code": attempt.Provider.Code, "name": attempt.Provider.Name,
			"adapter_key":   attempt.Provider.AdapterKey,
			"timeout_ms":    attempt.Provider.RequestTimeoutMS,
			"public_config": placementProviderConfig(attempt.Provider.PublicConfig, placementKey),
			"attempt_token": attempt.Token,
		})
	}
	return response
}

func placementProviderConfig(raw json.RawMessage, placementKey string) map[string]any {
	config := map[string]any{}
	if json.Unmarshal(raw, &config) != nil {
		return map[string]any{}
	}
	placements, _ := config["placements"].(map[string]any)
	delete(config, "placements")
	if placement, ok := placements[placementKey].(map[string]any); ok {
		for key, value := range placement {
			config[key] = value
		}
	}
	return config
}

func placementDecisionResponse(decision ads.PlacementDecision) map[string]any {
	return map[string]any{
		"placement_key":        decision.PlacementKey,
		"allowed":              decision.Allowed,
		"reason":               decision.Reason,
		"gambling_ads_enabled": decision.GamblingAdsEnabled,
		"campaigns":            campaignsResponse(decision.Campaigns),
		"restrictions":         restrictionsResponse(decision.Restrictions),
	}
}

func campaignsResponse(campaigns []ads.Campaign) []map[string]any {
	response := make([]map[string]any, 0, len(campaigns))
	for _, campaign := range campaigns {
		item := map[string]any{
			"id":            campaign.ID,
			"campaign_key":  campaign.CampaignKey,
			"name":          campaign.Name,
			"status":        campaign.Status,
			"placement_key": campaign.PlacementKey,
			"created_at":    campaign.CreatedAt.UTC().Format(time.RFC3339),
		}
		if campaign.StartsAt != nil {
			item["starts_at"] = campaign.StartsAt.UTC().Format(time.RFC3339)
		}
		if campaign.EndsAt != nil {
			item["ends_at"] = campaign.EndsAt.UTC().Format(time.RFC3339)
		}
		response = append(response, item)
	}
	return response
}

func restrictionsResponse(rules []ads.RestrictionRule) []map[string]any {
	response := make([]map[string]any, 0, len(rules))
	for _, rule := range rules {
		item := map[string]any{
			"id":          rule.ID,
			"feature_key": rule.FeatureKey,
			"action":      rule.Action,
			"starts_at":   rule.StartsAt.UTC().Format(time.RFC3339),
			"reason":      rule.Reason,
		}
		if rule.CountryCode != "" {
			item["country_code"] = rule.CountryCode
		}
		if rule.RegionCode != "" {
			item["region_code"] = rule.RegionCode
		}
		if rule.Platform != "" {
			item["platform"] = rule.Platform
		}
		if rule.PlacementKey != "" {
			item["placement_key"] = rule.PlacementKey
		}
		if rule.EndsAt != nil {
			item["ends_at"] = rule.EndsAt.UTC().Format(time.RFC3339)
		}
		response = append(response, item)
	}
	return response
}

func adminAdsHandler(auth AdminAuthenticator, source AdsSource, routing AdRoutingService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin_ads_unavailable"})
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionManageContent); !ok {
			return
		}
		campaigns, err := source.ListCampaigns(r.Context(), boundedQueryLimit(r, 100, 200))
		if err != nil {
			slog.Error("admin ads unavailable", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin_ads_unavailable"})
			return
		}
		providers := []ads.Provider{}
		if routing != nil {
			providers, err = routing.ListProviders(r.Context())
			if err != nil {
				slog.Error("admin ad providers unavailable", "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin_ads_unavailable"})
				return
			}
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{
			"campaigns": campaignsResponse(campaigns),
			"providers": adProvidersResponse(providers),
		})
	}
}

func adProvidersResponse(providers []ads.Provider) []map[string]any {
	response := make([]map[string]any, 0, len(providers))
	for _, provider := range providers {
		item := map[string]any{
			"code": provider.Code, "name": provider.Name, "adapter_key": provider.AdapterKey,
			"enabled": provider.Enabled, "global_priority": provider.GlobalPriority,
			"traffic_weight": provider.TrafficWeight, "health_status": provider.HealthStatus,
			"failure_threshold": provider.FailureThreshold, "cooldown_seconds": provider.CooldownSeconds,
			"request_timeout_ms": provider.RequestTimeoutMS, "public_config": provider.PublicConfig,
		}
		if provider.LastHealthCheck != nil {
			item["last_health_check"] = provider.LastHealthCheck.UTC().Format(time.RFC3339)
		}
		response = append(response, item)
	}
	return response
}

func adminPolicyRulesHandler(auth AdminAuthenticator, source AdsSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin_policy_unavailable"})
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionManagePolicy); !ok {
			return
		}
		rules, err := source.ListRestrictionRules(r.Context(), boundedQueryLimit(r, 100, 200))
		if err != nil {
			slog.Error("admin policy rules unavailable", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin_policy_unavailable"})
			return
		}
		flags, err := source.ListFeatureFlags(r.Context())
		if err != nil {
			slog.Error("admin feature flags unavailable", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin_policy_unavailable"})
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{"rules": restrictionsResponse(rules), "feature_flags": featureFlagsResponse(flags)})
	}
}

func featureFlagsResponse(flags []ads.FeatureFlag) []map[string]any {
	response := make([]map[string]any, 0, len(flags))
	for _, flag := range flags {
		response = append(response, map[string]any{
			"feature_key":    flag.FeatureKey,
			"enabled":        flag.Enabled,
			"default_action": flag.DefaultAction,
			"description":    flag.Description,
			"updated_at":     flag.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return response
}

func adminSettingsHandler(auth AdminAuthenticator, source OpsSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin_settings_unavailable"})
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionManagePolicy); !ok {
			return
		}
		settings, err := source.ListSettings(r.Context())
		if err != nil {
			slog.Error("admin settings unavailable", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin_settings_unavailable"})
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{"settings": settings})
	}
}

func adminMatchesHandler(auth AdminAuthenticator, source MatchSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "match_source_unavailable"})
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionReadAnalytics); !ok {
			return
		}
		query, err := matchQueryFromRequest(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_match_filter"})
			return
		}
		if query.Limit == 0 {
			query.Limit = 100
		}
		total, err := source.CountMatches(r.Context(), query)
		if err != nil {
			slog.Error("admin matches count unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "match_source_unavailable"})
			return
		}
		matches, err := source.ListMatches(r.Context(), query)
		if err != nil {
			slog.Error("admin matches unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "match_source_unavailable"})
			return
		}
		items := make([]map[string]any, 0, len(matches))
		for _, match := range matches {
			items = append(items, matchSummaryResponse(match))
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{
			"matches": items,
			"page":    matchPage(query.Page),
			"limit":   matchLimit(query.Limit),
			"total":   total,
		})
	}
}

func adminLeaguesPriorityHandler(auth AdminAuthenticator, source CatalogSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "catalog_source_unavailable"})
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionReadAnalytics); !ok {
			return
		}
		leagues, err := source.ListLeagues(r.Context(), boundedQueryLimit(r, 100, 200))
		if err != nil {
			slog.Error("admin leagues priority unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "catalog_source_unavailable"})
			return
		}
		items := make([]map[string]any, 0, len(leagues))
		for _, league := range leagues {
			items = append(items, leagueSummaryResponse(league))
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{"leagues": items})
	}
}

func adminNotificationsHandler(auth AdminAuthenticator, source OpsSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin_notifications_unavailable"})
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionReadNotifications); !ok {
			return
		}
		limit := boundedQueryLimit(r, 100, 200)
		notifications, err := source.ListNotifications(r.Context(), limit)
		if err != nil {
			slog.Error("admin notifications unavailable", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin_notifications_unavailable"})
			return
		}
		outbox, err := source.ListNotificationOutbox(r.Context(), limit)
		if err != nil {
			slog.Error("admin notification outbox unavailable", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin_notifications_unavailable"})
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{
			"notifications": notifications,
			"outbox":        outbox,
		})
	}
}

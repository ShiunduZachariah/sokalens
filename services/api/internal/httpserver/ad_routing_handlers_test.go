package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/ads"
)

type adPolicyStub struct {
	decision ads.PlacementDecision
	country  string
}

func (stub *adPolicyStub) ResolvePlacement(_ context.Context, placement, country string) (ads.PlacementDecision, error) {
	stub.country = country
	stub.decision.PlacementKey = placement
	return stub.decision, nil
}
func (*adPolicyStub) ActiveRestrictions(context.Context, string) ([]ads.RestrictionRule, error) {
	return nil, nil
}
func (*adPolicyStub) ListCampaigns(context.Context, int) ([]ads.Campaign, error) { return nil, nil }
func (*adPolicyStub) ListRestrictionRules(context.Context, int) ([]ads.RestrictionRule, error) {
	return nil, nil
}
func (*adPolicyStub) ListFeatureFlags(context.Context) ([]ads.FeatureFlag, error) { return nil, nil }

type adRoutingStub struct {
	report  ads.OutcomeReport
	request ads.ResolveRequest
	resolve ads.ProviderRoute
}

func (stub *adRoutingStub) Resolve(_ context.Context, request ads.ResolveRequest) (ads.ProviderRoute, error) {
	stub.request = request
	return stub.resolve, nil
}
func (stub *adRoutingStub) Report(_ context.Context, _ string, report ads.OutcomeReport) (ads.ProviderRoute, error) {
	stub.report = report
	return ads.ProviderRoute{}, nil
}
func (*adRoutingStub) ListProviders(context.Context) ([]ads.Provider, error) { return nil, nil }

func TestAdsPlacementBuildsPolicyCheckedProviderChain(t *testing.T) {
	policy := &adPolicyStub{decision: ads.PlacementDecision{Allowed: true, Reason: "provider_fallback"}}
	routing := &adRoutingStub{resolve: ads.ProviderRoute{
		PreferredProvider: "adsense",
		ActiveProvider:    "adsense",
		Attempts: []ads.ProviderAttempt{{
			Provider: ads.Provider{
				Code: "adsense", Name: "Google AdSense", AdapterKey: "adsense", RequestTimeoutMS: 1200,
				PublicConfig: json.RawMessage(`{"client_id":"ca-pub-5331089211665627","placements":{"homepage-mid-content":{"slot_id":"1234567890"}}}`),
			},
			Token: "signed.attempt",
		}},
	}}
	dependencies := accountDependencies{ads: policy, adRouting: routing}
	request := httptest.NewRequest(http.MethodGet, "/v1/ads/placements/homepage-mid-content", nil)
	request.AddCookie(&http.Cookie{Name: guestCookie, Value: "signed-guest-token"})
	request.Header.Set("X-SokaLens-Country", "KE")
	recorder := httptest.NewRecorder()

	dependencies.adsPlacementHandler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if policy.country != "KE" || routing.request.CountryCode != "KE" {
		t.Fatalf("country policy=%q routing=%q", policy.country, routing.request.CountryCode)
	}
	if routing.request.SessionID == "" || strings.Contains(recorder.Body.String(), routing.request.SessionID) {
		t.Fatal("ad session must be stable internally and absent from the public response")
	}
	var response struct {
		Allowed   bool `json:"allowed"`
		Providers []struct {
			AttemptToken string         `json:"attempt_token"`
			PublicConfig map[string]any `json:"public_config"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Allowed || len(response.Providers) != 1 || response.Providers[0].AttemptToken != "signed.attempt" {
		t.Fatalf("response = %+v", response)
	}
	if response.Providers[0].PublicConfig["slot_id"] != "1234567890" {
		t.Fatalf("placement config = %+v", response.Providers[0].PublicConfig)
	}
	if _, leaked := response.Providers[0].PublicConfig["placements"]; leaked {
		t.Fatal("the full placement registry must not be sent to the browser")
	}
}

func TestAdsPlacementRestrictionStopsBeforeProviderRouting(t *testing.T) {
	policy := &adPolicyStub{decision: ads.PlacementDecision{Allowed: false, Reason: "country_restricted"}}
	routing := &adRoutingStub{}
	dependencies := accountDependencies{ads: policy, adRouting: routing}
	request := httptest.NewRequest(http.MethodGet, "/v1/ads/placements/match-sidebar", nil)
	request.AddCookie(&http.Cookie{Name: guestCookie, Value: "signed-guest-token"})
	recorder := httptest.NewRecorder()

	dependencies.adsPlacementHandler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || routing.request.SessionID != "" {
		t.Fatalf("status=%d routing=%+v body=%s", recorder.Code, routing.request, recorder.Body.String())
	}
}

func TestAdsOutcomeAcceptsPublicAttemptContract(t *testing.T) {
	routing := &adRoutingStub{}
	dependencies := accountDependencies{adRouting: routing}
	request := httptest.NewRequest(http.MethodPost, "/v1/ads/outcomes", strings.NewReader(`{"attempt_token":"signed.attempt","outcome":"failure","failure_reason":"NO_FILL","duration_ms":850}`))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: guestCookie, Value: "signed-guest-token"})
	recorder := httptest.NewRecorder()

	dependencies.adsOutcomeHandler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if routing.report.Token != "signed.attempt" || routing.report.FailureReason != ads.FailureNoFill {
		t.Fatalf("report = %+v", routing.report)
	}
}

func TestRuntimeContextDoesNotPromotePlacementRuleToGlobalDenial(t *testing.T) {
	if adsBlockedByRules([]ads.RestrictionRule{{FeatureKey: "paid_promotions", PlacementKey: "match-sidebar"}}) {
		t.Fatal("a placement restriction must be evaluated only for that placement")
	}
	if !adsBlockedByRules([]ads.RestrictionRule{{FeatureKey: "paid_promotions"}}) {
		t.Fatal("a global advertising restriction must block runtime ads")
	}
}

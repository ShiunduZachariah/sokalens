package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/ads"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/config"
)

// The site shows no ads, so every ad endpoint is hidden behind ADS_ENABLED and
// must answer exactly like a path that does not exist until it is switched on.
func TestAdEndpointsAreHiddenUnlessEnabled(t *testing.T) {
	requests := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/ads/placements/homepage-mid-content", ""},
		{http.MethodPost, "/v1/ads/outcomes", `{"attempt_token":"signed.attempt","outcome":"success","duration_ms":10}`},
		{http.MethodGet, "/v1/admin/ads", ""},
	}
	for _, enabled := range []bool{false, true} {
		handler := New(ServerOptions{
			Config:    config.Config{Environment: "test", AdsEnabled: enabled},
			Ads:       &adPolicyStub{decision: ads.PlacementDecision{Allowed: true}},
			AdRouting: &adRoutingStub{},
		})
		for _, request := range requests {
			req := httptest.NewRequest(request.method, request.path, strings.NewReader(request.body))
			req.Header.Set("Content-Type", "application/json")
			req.AddCookie(&http.Cookie{Name: guestCookie, Value: "signed-guest-token"})
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)

			hidden := recorder.Code == http.StatusNotFound && strings.Contains(recorder.Body.String(), `"not_found"`)
			if !enabled && !hidden {
				t.Errorf("ADS_ENABLED=false: %s %s = %d %s, want the unknown-endpoint 404", request.method, request.path, recorder.Code, recorder.Body.String())
			}
			if enabled && recorder.Code == http.StatusNotFound {
				t.Errorf("ADS_ENABLED=true: %s %s = 404, want the ad handler", request.method, request.path)
			}
		}
	}
}

func TestRuntimeContextReportsAdsDisabled(t *testing.T) {
	dependencies := accountDependencies{ads: &adPolicyStub{}, adsEnabled: false}
	recorder := httptest.NewRecorder()
	dependencies.contextHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/access/context", nil))

	var response struct {
		Ads struct {
			Allowed bool   `json:"allowed"`
			Reason  string `json:"reason"`
		} `json:"ads"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Ads.Allowed || response.Ads.Reason != "ads_disabled" {
		t.Fatalf("ads = %+v, want allowed=false reason=ads_disabled", response.Ads)
	}

	dependencies.adsEnabled = true
	recorder = httptest.NewRecorder()
	dependencies.contextHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/access/context", nil))
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Ads.Allowed {
		t.Fatalf("ADS_ENABLED=true with no restriction must allow ads, got %+v", response.Ads)
	}
}

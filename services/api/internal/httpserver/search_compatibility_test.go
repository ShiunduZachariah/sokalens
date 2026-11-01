package httpserver

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

type compatibilitySearchSource struct{}

func (compatibilitySearchSource) Search(context.Context, string, int) ([]SearchResult, error) {
	return []SearchResult{{Type: "team", ID: 1, Name: "Arsenal"}}, nil
}

func (compatibilitySearchSource) SearchWithRecommendations(context.Context, string, int) ([]SearchResult, error) {
	return []SearchResult{{Type: "prediction", ID: 2, Name: "Arsenal vs Chelsea"}, {Type: "acca", Key: "slip-1", Name: "2-leg ACCA"}}, nil
}

func TestSearchRecommendationsRequireExplicitOptIn(t *testing.T) {
	for _, route := range []string{"/v1/search", "/v1/search/suggest"} {
		for _, test := range []struct{ query, want string }{
			{"", "team"}, {"&include_recommendations=false", "team"},
			{"&include_recommendations=garbage", "team"}, {"&include_recommendations=true", "prediction"},
		} {
			t.Run(route+test.query, func(t *testing.T) {
				response := httptest.NewRecorder()
				searchHandler(compatibilitySearchSource{})(response, httptest.NewRequest("GET", route+"?q=Arsenal"+test.query, nil))
				var payload struct {
					Results []SearchResult `json:"results"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				if response.Code != 200 || len(payload.Results) == 0 || payload.Results[0].Type != test.want {
					t.Fatalf("status=%d body=%s, want first type %s", response.Code, response.Body.String(), test.want)
				}
			})
		}
	}
}

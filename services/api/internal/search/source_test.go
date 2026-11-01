package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/httpserver"
)

func TestAccaSearchExcludesInventoryBelowPublicationStandard(t *testing.T) {
	query := accaSearchQuery("public", "true", 1)
	for _, predicate := range []string{"combined_fair_odds", "power(1.5", "fair_odds_at_generation < 1.10"} {
		if !strings.Contains(query, predicate) {
			t.Fatalf("ACCA search query missing %q: %s", predicate, query)
		}
	}
}

func TestDeduplicateEntityResultsPrefersCanonicalCountryTeam(t *testing.T) {
	results := []httpserver.SearchResult{
		{Type: "team", ID: 1, Name: "Manchester City", Slug: "team-world-manchester-city", Subtitle: "World"},
		{Type: "team", ID: 2, Name: "Manchester City", Slug: "team-england-manchester-city", Subtitle: "England", LogoURL: "crest.png"},
		{Type: "match", ID: 3, Name: "Manchester City vs Arsenal"},
	}
	deduped := deduplicateEntityResults(results, 10)
	if len(deduped) != 2 || deduped[0].ID != 2 || deduped[1].ID != 3 {
		t.Fatalf("deduplicated results = %+v", deduped)
	}
}

func TestTypesenseSearchLeavesTimeForDatabaseFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusGatewayTimeout)
		}
	}))
	defer server.Close()

	source := Source{typesense: NewTypesenseClient(server.URL, "test-key")}
	started := time.Now()
	results, ok := source.typesenseSearch(context.Background(), "arsenal", 20)
	if ok || results != nil {
		t.Fatalf("expected unavailable Typesense to trigger the database fallback")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Typesense fallback took %s; expected it to remain below one second", elapsed)
	}
}

func TestWithSchemaUsesQAInventoryAndRejectsInjection(t *testing.T) {
	source := NewSource(nil).WithSchema("acca_qa")
	if source.schema != "acca_qa" {
		t.Fatalf("schema = %q, want acca_qa", source.schema)
	}
	if got := source.WithSchema("acca_qa; DROP TABLE acca_slips").schema; got != "acca_qa" {
		t.Fatalf("invalid schema changed configured schema to %q", got)
	}
	if got := NewSource(nil).WithSchema("").schema; got != "public" {
		t.Fatalf("empty schema = %q, want public", got)
	}
}

func TestSearchResultLimitKeepsPredictionAndAccaDiscovery(t *testing.T) {
	results := make([]httpserver.SearchResult, 0, 24)
	for i := 1; i <= 22; i++ {
		results = append(results, httpserver.SearchResult{Type: "team", ID: int64(i), Name: "Team " + string(rune('A'+i))})
	}
	results = append(results,
		httpserver.SearchResult{Type: "prediction", ID: 101, MatchID: 501, Name: "Home vs Away"},
		httpserver.SearchResult{Type: "acca", Key: "acca-1", Name: "5-leg ACCA"},
	)

	got := deduplicateEntityResults(results, 20)
	seen := map[string]bool{}
	for _, result := range got {
		seen[result.Type] = true
	}
	if len(got) != 20 || !seen["prediction"] || !seen["acca"] {
		t.Fatalf("balanced results = %+v, want 20 including prediction and acca", got)
	}
}

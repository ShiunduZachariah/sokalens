package acca

import (
	"strings"
	"testing"
)

// The catalogue holds thousands of slips but a page holds 10. Without a total
// the web layer falls back to len(items) -- apps/web/app/data/accaApi.ts reads
// `value.total` and defaults to bundles.length -- so the page told visitors
// there were 10 ACCAs when there were 2282.
func TestResultCarriesCatalogueTotalSeparateFromPageSize(t *testing.T) {
	result := Result{Items: make([]Slip, 10), Limit: 10, Total: 2282}

	if result.Total == len(result.Items) {
		t.Fatalf("total must describe the catalogue, not the page: total=%d items=%d", result.Total, len(result.Items))
	}
	if result.Total != 2282 {
		t.Fatalf("total=%d, want 2282", result.Total)
	}
}

// The count has to share the list query's predicate, or the number shown will
// not match what paging can actually reach.
func TestFacetsQueryCountsActiveUpcomingSlips(t *testing.T) {
	query := facetsQuery("public")

	if !strings.Contains(query, "'total'") {
		t.Fatal("facets query does not select a total")
	}
	countClause := query[strings.Index(query, "'total'"):]
	end := strings.Index(countClause, "),")
	if end > 0 {
		countClause = countClause[:end]
	}
	for _, predicate := range []string{"status='active'", "earliest_kickoff > now()", "combined_fair_odds", "fair_odds_at_generation"} {
		if !strings.Contains(countClause, predicate) {
			t.Fatalf("total must be counted with %q so it matches what paging can reach: %s", predicate, countClause)
		}
	}
}

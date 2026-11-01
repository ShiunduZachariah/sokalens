package acca

import (
	"os"
	"strings"
	"testing"
)

func TestRegenerationReactivatesUnchangedRetiredComposition(t *testing.T) {
	source, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "OR %s.status <> 'active'") {
		t.Fatal("upsert must reactivate freshly eligible retired slips even when content is unchanged")
	}
	if !strings.Contains(string(source), "prediction_version=EXCLUDED.prediction_version") {
		t.Fatal("refreshed eligible prediction version must replace the stale leg version")
	}
}

func TestGenerationExpiresStartedInventory(t *testing.T) {
	source, err := os.ReadFile("job.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "j.Repository.Expire(ctx)") {
		t.Fatal("generation never expires started inventory before enforcing the active cap")
	}
}

func TestGenerationInvalidatesStaleInventoryBeforeComposition(t *testing.T) {
	source, err := os.ReadFile("job.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	invalidate := strings.Index(text, "j.Repository.InvalidateStale(ctx)")
	if invalidate < 0 || invalidate > strings.Index(text, "slips := Compose") {
		t.Fatal("generation must invalidate stale inventory before composing replacements")
	}
}

func TestStaleInvalidationUsesQualifiedPredictionIdentity(t *testing.T) {
	source, err := os.ReadFile("repository.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, contract := range []string{
		"func staleInventoryQuery", "eligible_acca_predictions_v", "s.status='active'",
		// Rounded to the precision combined_fair_odds is stored at. Comparing the
		// raw power() against a stored numeric(_,4) excluded slips that are equal
		// to the floor once rounded, which is what #115 fixed; this contract
		// keeps the rounding from being dropped again.
		"s.combined_fair_odds < round(power(1.5", "fair_odds_at_generation < 1.10",
		"NOT EXISTS", "e.prediction_source=l.prediction_source", "e.prediction_id=l.prediction_id",
		"e.prediction_version IS NOT DISTINCT FROM l.prediction_version", "e.match_id=l.match_id",
		"e.market_key=l.market_key", "e.selection=l.selection", "e.market_key <> 'correct_score'",
		"e.probability > 0", "e.probability < 1", "e.fair_odds > 1",
	} {
		if !strings.Contains(text, contract) {
			t.Errorf("stale invalidation missing %q", contract)
		}
	}
}

func TestRepositorySchemaIsStrictlyQualified(t *testing.T) {
	if _, err := NewRepository(nil, "public"); err == nil {
		t.Fatal("nil DB accepted")
	}
	if schemaPattern.MatchString("public; DROP TABLE matches") {
		t.Fatal("unsafe schema accepted")
	}
	if got := QualifiedName("acca_qa", "acca_slips"); got != "acca_qa.acca_slips" {
		t.Fatal(got)
	}
}

func TestOverflowRetirementEnforcesGlobalActiveCeiling(t *testing.T) {
	query := retireOverflowQuery("acca_qa.acca_slips")
	for _, contract := range []string{"ROW_NUMBER()", "status='active'", "ranking_score DESC", "r.position > $1", "status='invalid'"} {
		if !strings.Contains(query, contract) {
			t.Fatalf("ceiling query missing %q: %s", contract, query)
		}
	}
}

func TestRetireOverflowKeepsOneOfEachCellBeforeASecondOfAny(t *testing.T) {
	// The ceiling used to be applied by ranking_score across the whole pool,
	// and ranking_score is confidence. A long-odds slip is built from
	// low-confidence legs, so every high-odds slip sorted to the bottom and was
	// retired in the same run that composed it. The inventory converged on the
	// cheapest slip clearing the floor at every leg count: 201 cells of a
	// 1,596-cell grid, every 5-leg slip between 2.76 and 3.55, and a slip
	// composed at 70.03 for a reader who asked for 70-100 invalidated
	// immediately.
	//
	// The query must therefore take the best of each cell before a second from
	// any, so a thin cell survives a fat one.
	query := retireOverflowQuery("public.acca_slips")

	if !strings.Contains(query, "PARTITION BY market_tab, leg_count, odds_band") {
		t.Fatal("overflow must rank within a cell, or the cap collapses the grid onto the cheapest slips")
	}
	if !strings.Contains(query, "ORDER BY depth ASC") {
		t.Fatal("cells must be taken round-robin by depth, or a fat cell still starves a thin one")
	}
	// Confidence still decides within a cell and breaks ties across them.
	if !strings.Contains(query, "ranking_score DESC") {
		t.Fatal("confidence must still order slips inside a cell")
	}
	// The ceiling itself is unchanged: still a position cut against the bound.
	if !strings.Contains(query, "r.position > $1") {
		t.Fatal("the ceiling must still be enforced")
	}
}

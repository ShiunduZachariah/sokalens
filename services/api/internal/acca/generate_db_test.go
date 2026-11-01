package acca

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// This exercises the generate fallback against a real inventory schema, which
// is the only place the candidate SQL itself can be proven. It stays opt-in:
// set ACCA_TEST_DATABASE_URL (and optionally ACCA_TEST_SCHEMA) to a database
// holding an ACCA inventory, e.g.
//
//	ACCA_TEST_DATABASE_URL=postgres://... ACCA_TEST_SCHEMA=acca_qa go test ./services/api/internal/acca/
func TestGenerateAgainstRealInventory(t *testing.T) {
	url := os.Getenv("ACCA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ACCA_TEST_DATABASE_URL to run the inventory-backed generate test")
	}
	schema := os.Getenv("ACCA_TEST_SCHEMA")
	if schema == "" {
		schema = "public"
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	source := NewSource(db, schema)

	legs := 20
	minOdds, maxOdds := 10.0, 1000.0
	query := Query{Tab: "mixed", Period: "seven_days", Legs: &legs, MinFairOdds: &minOdds, MaxFairOdds: &maxOdds, Limit: 1, Seed: "integration"}

	stored, err := source.List(ctx, query)
	if err != nil {
		t.Fatalf("inventory search: %v", err)
	}
	query.Generate = true
	generated, err := source.List(ctx, query)
	if err != nil {
		t.Fatalf("generate search: %v", err)
	}
	t.Logf("inventory returned %d slips, generate returned %d", len(stored.Items), len(generated.Items))
	if len(generated.Items) == 0 {
		candidates, err := source.candidates(ctx, query)
		if err != nil {
			t.Fatalf("candidate query: %v", err)
		}
		fixtures := map[int64]bool{}
		for _, c := range candidates {
			fixtures[c.MatchID] = true
		}
		if len(fixtures) >= legs {
			t.Fatalf("pool holds %d candidates across %d fixtures yet no 20-leg slip was composed", len(candidates), len(fixtures))
		}
		t.Skipf("pool holds only %d fixtures, fewer than the %d legs requested", len(fixtures), legs)
	}
	slip := generated.Items[0]
	if slip.Origin != GeneratedOrigin || slip.LegCount != legs {
		t.Fatalf("origin=%q legs=%d", slip.Origin, slip.LegCount)
	}
	if slip.CombinedFairOdds < minOdds || slip.CombinedFairOdds > maxOdds {
		t.Fatalf("combined fair odds=%v outside the requested window", slip.CombinedFairOdds)
	}
	seen := map[int64]bool{}
	for _, leg := range slip.Legs {
		if seen[leg.MatchID] {
			t.Fatalf("fixture %d appears twice", leg.MatchID)
		}
		seen[leg.MatchID] = true
		if leg.HomeTeam == "" || leg.AwayTeam == "" || leg.League == "" {
			t.Fatalf("leg is missing display data: %+v", leg)
		}
		if !leg.KickoffAt.After(time.Now()) {
			t.Fatalf("leg %d has already kicked off", leg.MatchID)
		}
	}
	t.Logf("composed %d legs at combined fair odds %.2f", slip.LegCount, slip.CombinedFairOdds)
}

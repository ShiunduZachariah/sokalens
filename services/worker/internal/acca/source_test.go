package acca

import (
	"os"
	"strings"
	"testing"
)

// The qualified candidate projection must not leak markets intentionally
// excluded from public ACCA inventory. Keep this structural red test close to
// the SQL until the source has an injectable query builder.
func TestEligibleCandidatesExcludesCorrectScoreMarket(t *testing.T) {
	b, err := os.ReadFile("source.go")
	if err != nil { t.Fatal(err) }
	source := string(b)
	if strings.Contains(source, "market_key <> 'correct_score'") || strings.Contains(source, "market_key != 'correct_score'") {
		return
	}
	if strings.Contains(source, "NOT IN ('correct_score')") || strings.Contains(source, "NOT IN (\"correct_score\")") {
		return
	}
	t.Fatalf("EligibleCandidates SQL does not exclude correct_score")
}

func TestEligibleCandidatesUsesBoundedPerMarketSelection(t *testing.T) {
	b, err := os.ReadFile("source.go")
	if err != nil {
		t.Fatal(err)
	}
	source := strings.ToLower(string(b))
	if !strings.Contains(source, "partition by market_key") || !strings.Contains(source, "row_number()") {
		t.Fatal("EligibleCandidates must use a bounded per-market ranking so qualified markets are not starved by the global ranking")
	}
}

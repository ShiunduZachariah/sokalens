package predictions

import (
	"os"
	"strings"
	"testing"
)

// The provider fallback queries must select the crest they already have.
//
// Both provider queries already LEFT JOIN teams for the home and away NAMES, so
// the crest is one column off a join that is paid for -- no new join, no extra
// round trip, and no change to the list's query shape. Selecting only the names
// is what left every provider fallback row rendering a drawn initials shield
// beside engine rows with a real crest: the same class of defect as the ACCA
// legs before 2026-09-04, where the column was missing and it read as a
// rendering bug.
//
// Asserted against the source text rather than a live database because these
// are string constants inside the query calls and there is no database in this
// package's tests.
func TestProviderPredictionQueriesSelectTeamCrests(t *testing.T) {
	body, err := os.ReadFile("source.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)

	// One per provider query: ListTodayProviderPredictions (the public feed) and
	// ListProviderPredictions (/v1/predictions/provider).
	const wantOccurrences = 2
	for _, column := range []string{"COALESCE(home.logo_url, '')", "COALESCE(away.logo_url, '')"} {
		if got := strings.Count(source, column); got < wantOccurrences {
			t.Errorf("%s appears %d times, want at least %d (one per provider query)", column, got, wantOccurrences)
		}
	}
	// Scanned into the flat fields the serialiser emits as home_team_logo /
	// away_team_logo.
	for _, field := range []string{"&summary.HomeTeamLogo", "&summary.AwayTeamLogo"} {
		if got := strings.Count(source, field); got < wantOccurrences {
			t.Errorf("%s scanned %d times, want at least %d", field, got, wantOccurrences)
		}
	}
	// LEFT, so a fixture whose team row is missing still returns its prediction.
	// A crest is decoration and must never filter a row out.
	if strings.Count(source, "LEFT JOIN teams AS home ON home.id = f.home_team_id") < wantOccurrences {
		t.Error("provider queries must keep the crest joins outer")
	}
}

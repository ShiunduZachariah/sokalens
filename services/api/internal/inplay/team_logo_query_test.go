package inplay

import (
	"strings"
	"testing"
)

// The crest columns on both in-play reads.
//
// The two queries are separate by design -- the list answers "what is happening
// now", the retained read answers "what was recorded for this match" -- but
// scanStates is shared, so the column list has to agree between them. A crest
// added to one and not the other is a scan error at runtime, not a compile one,
// which is why this is asserted on both.
//
// The teams joins live inside the CTEs, which are bounded (LIMIT, or one id),
// so the two extra joins are primary-key lookups over at most the page of
// fixtures being served -- not a per-fixture round trip. The N+1 shape is the
// one thing a live list must not grow: it gets slower exactly when the most
// matches are on.
func TestInPlayQueriesSelectTeamCrests(t *testing.T) {
	for name, query := range map[string]string{
		"live list":        selectInPlay,
		"retained fixture": selectRetainedFixture,
	} {
		for _, want := range []string{"home_team_logo", "away_team_logo"} {
			if !strings.Contains(query, want) {
				t.Errorf("%s query does not select %s", name, want)
			}
		}
		// COALESCE to '' rather than NULL, as acca.Leg does: the clients key on
		// the empty string to draw an initials shield, and a NULL would reach
		// them as the string "null".
		if !strings.Contains(query, "COALESCE(home.logo_url, '')") ||
			!strings.Contains(query, "COALESCE(away.logo_url, '')") {
			t.Errorf("%s query must COALESCE the crest to an empty string", name)
		}
		// Resolved through teams.id off the fixture's own team ids. The crest is
		// decoration; the canonical identity stays the team id.
		if !strings.Contains(query, "LEFT JOIN teams home ON home.id = m.home_team_id") ||
			!strings.Contains(query, "LEFT JOIN teams away ON away.id = m.away_team_id") {
			t.Errorf("%s query must resolve crests through teams.id", name)
		}
		// LEFT, not inner: a fixture whose teams are unmapped or crestless must
		// still appear on the board.
		if strings.Contains(query, "JOIN teams home ON") && !strings.Contains(query, "LEFT JOIN teams home ON") {
			t.Errorf("%s query must not drop fixtures whose team row is missing", name)
		}
	}
}

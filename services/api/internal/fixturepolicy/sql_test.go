package fixturepolicy

import (
	"strings"
	"testing"
	"time"
)

var clauseMoment = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)

func TestUnresolvedEarlierFixtureClause(t *testing.T) {
	clause := UnresolvedEarlierFixtureClause("f", clauseMoment)
	for _, expected := range []string{
		"earlier_unresolved.kickoff_at < f.kickoff_at",
		"earlier_unresolved.kickoff_at < '2026-09-20T12:00:00Z'",
		"earlier_unresolved.kickoff_at >= '2026-09-20T06:00:00Z'",
		"earlier_unresolved.home_team_id IN (f.home_team_id, f.away_team_id)",
		"earlier_unresolved.home_goals_ft IS NULL",
		"'postponed'",
	} {
		if !strings.Contains(clause, expected) {
			t.Fatalf("fixture eligibility clause missing %q", expected)
		}
	}
	// The old fortnight lookback is what let one un-reconciled row suppress
	// both its teams for two weeks. It must not come back.
	if strings.Contains(clause, "CURRENT_DATE - 14") {
		t.Fatal("unresolved-fixture clause must bound the wait to the grace window, not 14 days")
	}
	if strings.Contains(clause, "CAST(earlier_unresolved.kickoff_at AS DATE)") {
		t.Fatal("unresolved-fixture clause must keep kickoff_at sargable")
	}
}

// The matches table spells its scores differently from fixtures. Interpolating
// the wrong pair produces SQL that fails at query time, in an endpoint, not
// here.
func TestUnresolvedEarlierFixtureClauseUsesEachTableScoreColumns(t *testing.T) {
	fixtures := UnresolvedEarlierFixtureClause("f", clauseMoment)
	if !strings.Contains(fixtures, "earlier_unresolved.away_goals_ft IS NULL") {
		t.Fatal("fixtures clause must test home_goals_ft/away_goals_ft")
	}
	matches := UnresolvedEarlierFixtureClause("m", clauseMoment)
	if !strings.Contains(matches, "earlier_unresolved.home_score IS NULL") ||
		!strings.Contains(matches, "earlier_unresolved.away_score IS NULL") {
		t.Fatal("matches clause must test home_score/away_score")
	}
	if strings.Contains(matches, "goals_ft") {
		t.Fatal("matches has no goals_ft columns")
	}
}

// matchstate.terminal is the platform's definition of "will not change again".
// The clause used to disagree with it on three values, and each disagreement
// cost predictions.
func TestResolvedStatusListMirrorsMatchstateTerminal(t *testing.T) {
	for _, status := range []string{
		"'finished'", "'postponed'", "'canceled'", "'cancelled'",
		"'abandoned'", "'awarded'", "'walkover'", "'unknown'",
		"'ft'", "'aet'", "'pen'",
	} {
		if !strings.Contains(ResolvedStatusSQLList, status) {
			t.Fatalf("resolved status list is missing %s; it must mirror matchstate.terminal", status)
		}
	}
	for _, status := range []string{"'live'", "'halftime'", "'second_half'", "'upcoming'", "'scheduled'"} {
		if strings.Contains(ResolvedStatusSQLList, status) {
			t.Fatalf("resolved status list must not contain the non-terminal status %s", status)
		}
	}
}

func TestUnresolvedEarlierFixtureClauseRejectsUnknownAlias(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected unsupported alias to panic")
		}
	}()
	UnresolvedEarlierFixtureClause("user_input", clauseMoment)
}

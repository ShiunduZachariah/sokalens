package inplay

import (
	"strings"
	"testing"
)

// The per-fixture read must serve a match's retained in-play history whatever
// its status.
//
// It did not. Both reads shared one query whose CTE filtered on
// `m.status = ANY($1)` with the live statuses, so asking for a finished match
// returned nothing and the handler answered 404 "That match is not in play" --
// for fixtures that had, in production, 89 snapshots and 15 events sitting in
// the database. The record existed; nothing could reach it.
//
// The status filter was only half of it. The list bounds snapshots at
// `now() - snapshotLookback`, which is correct for a board of matches happening
// now and useless for a match that finished yesterday: its snapshots were
// captured around ITS kickoff. Fixing the status alone would still have
// returned an empty history for anything older than six hours.

func TestRetainedFixtureQueryIgnoresMatchStatus(t *testing.T) {
	if strings.Contains(selectRetainedFixture, "status = ANY(") {
		t.Error("the retained read filters on status again; a finished match cannot answer for itself")
	}
	if !strings.Contains(selectRetainedFixture, "WHERE m.id = $1") {
		t.Error("the retained read is no longer a by-id lookup")
	}
}

func TestRetainedFixtureWindowIsAnchoredToKickoff(t *testing.T) {
	// The bound must stay -- an unbounded scan reads every partition of
	// fixture_live_snapshots, which is the cost snapshotLookback exists to avoid
	// -- but it has to be anchored to the match, not to the clock.
	if !strings.Contains(selectRetainedFixture, "fixture.kickoff_at - $2::interval") ||
		!strings.Contains(selectRetainedFixture, "fixture.kickoff_at + $3::interval") {
		t.Error("the snapshot window is not anchored to the fixture's own kickoff; " +
			"a match older than the lookback will report no history")
	}
	if strings.Contains(selectRetainedFixture, "s.captured_at > now()") {
		t.Error("the retained read bounds snapshots against now(), which is the bug it replaces")
	}
	if retainedWindowAfter <= retainedWindowBefore {
		t.Errorf("the window after kickoff (%s) must exceed the window before it (%s); "+
			"a match is played after it starts", retainedWindowAfter, retainedWindowBefore)
	}
}

func TestLiveListStillOnlyReturnsLiveFixtures(t *testing.T) {
	// The other half of the contract. The board answers "what is happening now",
	// and widening it to finished matches would quietly fill the live list with
	// matches that ended.
	if !strings.Contains(selectInPlay, "m.status = ANY($1)") {
		t.Error("the live list no longer filters on status; finished matches will appear on the in-play board")
	}
}

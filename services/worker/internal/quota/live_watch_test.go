package quota

import (
	"testing"
)

// Kickoff used to be missed because live polling could not start until the
// database already said a match was live, and the database only learned that
// from a live poll. The watch window comes from the fixture calendar instead,
// so it is non-zero before anything is recorded as in play.
func TestLivePollingStartsBeforeTheDatabaseKnowsAMatchIsLive(t *testing.T) {
	decision := planWith(70000, DemandSnapshot{Live: 0, LiveWatch: 3, Upcoming: 40})

	if decision.LiveWatch != 3 {
		t.Fatalf("LiveWatch = %d, want 3", decision.LiveWatch)
	}
	if decision.LiveMatches != 0 {
		t.Fatalf("LiveMatches = %d, want 0 -- nothing is recorded as live yet", decision.LiveMatches)
	}
	if decision.ProjectedLive == 0 {
		t.Fatal("a match about to kick off must be budgeted for before it is observed")
	}
}

func TestNoWatchWindowMeansNoLiveSpendProjected(t *testing.T) {
	decision := planWith(70000, DemandSnapshot{Live: 0, LiveWatch: 0, Upcoming: 40})

	if decision.ProjectedLive != 0 {
		t.Fatalf("ProjectedLive = %d on a day with nothing in the window", decision.ProjectedLive)
	}
	if decision.LiveWatch != 0 {
		t.Fatalf("LiveWatch = %d", decision.LiveWatch)
	}
}

// A match stranded in second_half used to keep LiveMatches above zero forever.
// That kept live polling scheduled for a match nobody was playing and held the
// whole rest of the day's live spend in the projection, squeezing backfill out
// of quota it could have used. The demand query now excludes expired claims,
// so the planner sees the truth.
func TestAnExpiredLiveClaimNoLongerHoldsTheLiveBudgetOpen(t *testing.T) {
	stranded := planWith(70000, DemandSnapshot{Live: 0, LiveWatch: 0, Upcoming: 40})
	genuine := planWith(70000, DemandSnapshot{Live: 1, LiveWatch: 1, Upcoming: 40})

	if stranded.ProjectedLive != 0 {
		t.Fatalf("a stranded match still projected %d live requests", stranded.ProjectedLive)
	}
	if genuine.ProjectedLive <= 0 {
		t.Fatal("a genuinely live match must still be budgeted for")
	}
	if stranded.SafeDiscretionary <= genuine.SafeDiscretionary {
		t.Fatal("removing a phantom live match must release budget for backfill")
	}
}

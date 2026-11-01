package inplay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// repoFile reads a file relative to the repository root.
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(body)
}

func TestEveryInPlayStageTheSchemaAllowsIsAlsoServed(t *testing.T) {
	// This is the bug that held in-play rows at zero, one layer further on.
	//
	// Migration 000071 widened the lifecycle CHECK constraint to eight
	// stages, but the Python enum still carried three -- so the column
	// accepted values no code could produce, and the model, the feature
	// assembly and the migration were each individually correct while
	// production had nothing in it.
	//
	// The same gap can open here. A stage the engine writes and this reader
	// does not list is a prediction that exists in the database and is never
	// served, with nothing anywhere reporting an error.
	migration := repoFile(t, "infra/db/migrations/000071_inplay_prediction_lifecycle.up.sql")

	// The in-play stages from the constraint. FINAL is excluded deliberately:
	// it is the settled end state, not a live one.
	for _, stage := range []string{"IN_PLAY_1H", "HALF_TIME", "IN_PLAY_2H", "EXTRA_TIME"} {
		if !strings.Contains(migration, "'"+stage+"'") {
			t.Fatalf("%s is served by this reader but the schema does not allow it", stage)
		}
		found := false
		for _, served := range inPlayStages {
			if served == stage {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s is a live stage the schema allows, but this reader never serves it", stage)
		}
	}
}

func TestFinalIsNotServedAsInPlay(t *testing.T) {
	// FINAL is a settled match. Serving it from the in-play surface would put
	// a finished game back on a page that advertises a running clock.
	for _, stage := range inPlayStages {
		if stage == "FINAL" || stage == "PRE_MATCH" || stage == "LINEUP_CONFIRMED" {
			t.Fatalf("%s is not an in-play stage", stage)
		}
	}
}

func TestUnknownIsNeverServedAsLive(t *testing.T) {
	// "unknown" means the reaper gave up on a match. Serving it here would
	// put a stalled clock straight back onto the page that the whole
	// freshness mechanism exists to keep it off -- the "second half, three
	// hours" defect, reintroduced at the read side.
	for _, status := range liveStatuses {
		if status == "unknown" {
			t.Fatal("unknown is not a live status; it means the match stopped being observed")
		}
	}
}

func TestStaleThresholdMatchesTheWorker(t *testing.T) {
	// The constant is duplicated across module boundaries, so nothing but a
	// test keeps the two honest. A reader that is more generous than the
	// worker would present a clock the worker has already judged stale.
	freshness := repoFile(t, "services/worker/internal/matchstate/freshness.go")
	if !strings.Contains(freshness, "StaleAfter = 90 * time.Second") {
		t.Fatal("the worker's StaleAfter moved; staleAfter here must move with it")
	}
	if staleAfter != 90*time.Second {
		t.Fatalf("staleAfter = %s, want the worker's 90s", staleAfter)
	}
}

func TestSnapshotLookbackCoversAWholeMatch(t *testing.T) {
	// The bound exists to let Postgres prune partitions, but it also decides
	// which snapshots are visible. Too short and a long match loses its own
	// score: 90 minutes, extra time, penalties and feed lag all have to fit.
	if snapshotLookback < 4*time.Hour {
		t.Fatalf("snapshotLookback = %s; a match with extra time and penalties would lose its snapshot", snapshotLookback)
	}
}

func TestTheQueryStaysBoundedPerRequest(t *testing.T) {
	// One query for the whole list. An N+1 shape gets slower exactly when the
	// most matches are live, which is the worst moment for it to degrade.
	if strings.Count(selectInPlay, "SELECT") < 3 {
		t.Fatal("the query no longer joins snapshots and predictions in one statement")
	}
	if !strings.Contains(selectInPlay, "LIMIT $3") {
		t.Fatal("the fixture list is unbounded; one busy Saturday would serve every live match at once")
	}
	// The partition bound is what keeps the snapshot lookup off every
	// partition in the table.
	if strings.Count(selectInPlay, "$2::interval") < 2 {
		t.Fatal("the snapshot lookup lost its captured_at bound and will scan every partition")
	}
}

func TestInPlayReadersUseOneLatestPublishedMarketSet(t *testing.T) {
	for name, query := range map[string]string{"live": selectInPlay, "retained": selectRetainedFixture} {
		if !strings.Contains(query, "p.market = ANY(pub.current_markets)") {
			t.Errorf("%s reader ignores the latest engine publication membership", name)
		}
		if !strings.Contains(query, "pub.fixture_id IS NULL AND p.generated_at = (") ||
			!strings.Contains(query, "SELECT MAX(legacy.generated_at)") {
			t.Errorf("%s reader can mix historical lines on fixtures without publication state", name)
		}
		if !strings.Contains(query, "p.superseded_at IS NULL") {
			t.Errorf("%s reader can expose a superseded prediction", name)
		}
	}
}

func TestListLimitIsClamped(t *testing.T) {
	// A caller-supplied limit reaches SQL, so its bounds are not cosmetic.
	source := &Source{}
	for _, limit := range []int{0, -1, 10_000} {
		if _, err := source.ListInPlayFixtures(t.Context(), limit); err == nil {
			t.Fatalf("limit %d: want the nil-database error, got none", limit)
		}
	}
}

func TestPostgresArrayQuotesItsValues(t *testing.T) {
	got := pgTextArray([]string{"live", "second_half"})
	if got != `{"live","second_half"}` {
		t.Fatalf("pgTextArray = %s", got)
	}
	if pgTextArray(nil) != "{}" {
		t.Fatalf("empty array = %s, want {}", pgTextArray(nil))
	}
}

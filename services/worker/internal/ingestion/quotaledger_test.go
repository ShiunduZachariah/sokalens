package ingestion

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The measured case. On 2026-10-16 production had made 361 live polls and 70
// backfill requests, and the dashboard read live 0 and backfill 0 with 1,194
// against fixtures -- because every one of these jobs calls the provider's
// `fixtures` endpoint, so the endpoint string cannot tell them apart.
//
// The reason can, and the scheduler already sets it. Each case below is a
// (reason, endpoint) pair taken from production's own request log.
func TestClassifyEndpointGroupUsesTheReasonTheEndpointCannotDistinguish(t *testing.T) {
	cases := []struct {
		reason, endpoint, want string
	}{
		{"live", "fixtures", EndpointGroupLive},
		{"backfill", "fixtures", EndpointGroupBackfill},
		{"team_history", "fixtures", EndpointGroupBackfill},
		{"reconcile", "fixtures", EndpointGroupFixtures},
		{"prematch", "fixtures", EndpointGroupFixtures},
		{"inplay_statistics", "fixtures", EndpointGroupFixtures},
		{"statistics", "fixtures/statistics", EndpointGroupFixtures},
		{"reference", "teams", EndpointGroupReference},
		{"odds", "odds", EndpointGroupOdds},
		{"quota_sync", "status", EndpointGroupOther},
		{"LIVE", "fixtures", EndpointGroupLive},
		{"  backfill  ", "fixtures", EndpointGroupBackfill},
	}
	for _, c := range cases {
		if got := classifyEndpointGroup(c.reason, c.endpoint); got != c.want {
			t.Errorf("classifyEndpointGroup(%q, %q) = %q, want %q", c.reason, c.endpoint, got, c.want)
		}
	}
}

// Without the reason fix, live and backfill are indistinguishable from the
// date sweeps. This asserts the three do not collapse, which is the property
// the burn-down chart depends on and the one that was silently false.
func TestLiveAndBackfillDoNotCollapseIntoFixtures(t *testing.T) {
	live := classifyEndpointGroup("live", "fixtures")
	backfill := classifyEndpointGroup("backfill", "fixtures")
	sweep := classifyEndpointGroup("prematch", "fixtures")
	if live == sweep || backfill == sweep || live == backfill {
		t.Fatalf("groups collapsed: live=%q backfill=%q prematch=%q", live, backfill, sweep)
	}
}

// The endpoint remains the fallback for a request with no reason, and an
// unmapped reason falls through to it rather than being guessed at.
func TestClassifyEndpointGroupFallsBackToTheEndpoint(t *testing.T) {
	cases := []struct {
		reason, endpoint, want string
	}{
		{"", "fixtures", EndpointGroupFixtures},
		{"", "odds", EndpointGroupOdds},
		{"", "countries", EndpointGroupReference},
		{"", "leagues", EndpointGroupReference},
		{"", "players", EndpointGroupReference},
		{"", "FIXTURES", EndpointGroupFixtures},
		{"", "  odds  ", EndpointGroupOdds},
		{"", "transfers", EndpointGroupOther},
		{"", "", EndpointGroupOther},
		// A reason nobody mapped must not invent a bucket.
		{"something-new", "fixtures", EndpointGroupFixtures},
		{"something-new", "transfers", EndpointGroupOther},
		// Qualified endpoints still work if an adapter ever writes one.
		{"", "fixtures.live", EndpointGroupLive},
		{"", "fixtures.backfill", EndpointGroupBackfill},
	}
	for _, c := range cases {
		if got := classifyEndpointGroup(c.reason, c.endpoint); got != c.want {
			t.Errorf("classifyEndpointGroup(%q, %q) = %q, want %q", c.reason, c.endpoint, got, c.want)
		}
	}
}

func TestClassifyPriorityDoesNotGuessP0ForUnsetPriority(t *testing.T) {
	// An unlabelled request must not inflate P0. "P0 class throttled at all" is
	// a P0 alert in the architecture document, so a derivation that quietly
	// files unknown spend as live-critical would page someone for reference data.
	if got := classifyPriority(0); got != "unknown" {
		t.Fatalf("classifyPriority(0) = %q, want unknown", got)
	}
	if got := classifyPriority(-5); got != "unknown" {
		t.Fatalf("classifyPriority(-5) = %q, want unknown", got)
	}
	if got := classifyPriority(10); got != "P0" {
		t.Fatalf("classifyPriority(10) = %q, want P0 (the live-poll priority)", got)
	}
	if got := classifyPriority(30); got != "P2" {
		t.Fatalf("classifyPriority(30) = %q, want P2 (the fixtures priority)", got)
	}
	if got := classifyPriority(100); got != "P5" {
		t.Fatalf("classifyPriority(100) = %q, want P5", got)
	}
}

func TestLedgerErrorCountCountsFailuresNotPreflightBlocks(t *testing.T) {
	if got := ledgerErrorCount(200, ""); got != 0 {
		t.Errorf("clean 200 counted as an error")
	}
	if got := ledgerErrorCount(500, ""); got != 1 {
		t.Errorf("500 not counted as an error")
	}
	if got := ledgerErrorCount(429, "rate_limited"); got != 1 {
		t.Errorf("429 not counted as an error")
	}
	// A request the client refused to send never reached the provider. It has
	// no status and no error category, and counting it would make the error
	// rate track our own quota governor rather than the provider's health.
	if got := ledgerErrorCount(0, ""); got != 0 {
		t.Errorf("preflight block counted as a provider error")
	}
	// But a client-side failure that did carry a category is a real fault.
	if got := ledgerErrorCount(0, "transport"); got != 1 {
		t.Errorf("categorised transport failure not counted as an error")
	}
}

func TestInsertWritesTheLedgerRollupAfterTheRawLog(t *testing.T) {
	db := &fakeRequestLogDB{}
	repo := NewRequestLogRepository(db)

	at := time.Date(2026, 9, 8, 14, 35, 41, 987_000_000, time.UTC)
	err := repo.Insert(context.Background(), RequestLog{
		Provider:      "api-football",
		Endpoint:      "fixtures.live",
		StatusCode:    200,
		ResponseBytes: 2048,
		Priority:      10,
		RequestedAt:   at,
	})
	if err != nil {
		t.Fatalf("Insert returned error: %v", err)
	}
	if len(db.calls) != 2 {
		t.Fatalf("issued %d statements, want 2 (raw log then rollup)", len(db.calls))
	}

	rollup := db.call(t, 1)
	if !strings.Contains(rollup.query, "provider_quota_minute") {
		t.Fatalf("second query = %q, want the rollup upsert", rollup.query)
	}
	if got, want := rollup.args[0], "api-football"; got != want {
		t.Errorf("provider = %v, want %v", got, want)
	}
	// Truncated to the minute, not rounded: 14:35:41.987 belongs to 14:35.
	if got, want := rollup.args[1], time.Date(2026, 9, 8, 14, 35, 0, 0, time.UTC); got != want {
		t.Errorf("minute bucket = %v, want %v", got, want)
	}
	if got, want := rollup.args[2], "P0"; got != want {
		t.Errorf("priority class = %v, want %v", got, want)
	}
	if got, want := rollup.args[3], EndpointGroupLive; got != want {
		t.Errorf("endpoint group = %v, want %v", got, want)
	}
	if got, want := rollup.args[4], 0; got != want {
		t.Errorf("error count = %v, want %v", got, want)
	}
	if got, want := rollup.args[5], 2048; got != want {
		t.Errorf("bytes = %v, want %v", got, want)
	}
}

func TestInsertPrefersExplicitLedgerCoordinatesOverDerivation(t *testing.T) {
	db := &fakeRequestLogDB{}
	repo := NewRequestLogRepository(db)

	err := repo.Insert(context.Background(), RequestLog{
		Provider:      "api-football",
		Endpoint:      "fixtures",
		Priority:      30,
		PriorityClass: "P0",
		EndpointGroup: EndpointGroupLive,
		StatusCode:    200,
		RequestedAt:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Insert returned error: %v", err)
	}
	rollup := db.call(t, 1)
	if got := rollup.args[2]; got != "P0" {
		t.Errorf("priority class = %v, want the explicit P0 rather than the derived P2", got)
	}
	if got := rollup.args[3]; got != EndpointGroupLive {
		t.Errorf("endpoint group = %v, want the explicit live rather than the derived fixtures", got)
	}
}

func TestInsertFailsWhenTheRollupFails(t *testing.T) {
	// The rollup is not best-effort. If it silently swallowed failures the
	// ledger would drift from the raw log, and a burn-down chart that is quietly
	// missing spend is worse than no chart.
	db := &fakeRequestLogDB{err: errors.New("boom")}
	repo := NewRequestLogRepository(db)

	err := repo.Insert(context.Background(), RequestLog{
		Provider:    "api-football",
		Endpoint:    "countries",
		RequestedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("Insert returned nil error when the database failed")
	}
}

func TestAddRowsWrittenSharesTheBucketWithTheRequestThatEarnedIt(t *testing.T) {
	db := &fakeRequestLogDB{}
	repo := NewRequestLogRepository(db)

	log := RequestLog{
		Provider:    "api-football",
		Endpoint:    "fixtures.live",
		Priority:    10,
		StatusCode:  200,
		RequestedAt: time.Date(2026, 9, 8, 14, 35, 41, 0, time.UTC),
	}
	if err := repo.Insert(context.Background(), log); err != nil {
		t.Fatalf("Insert returned error: %v", err)
	}
	if err := repo.AddRowsWritten(context.Background(), log, 7); err != nil {
		t.Fatalf("AddRowsWritten returned error: %v", err)
	}

	rollup := db.call(t, 1)
	yield := db.call(t, 2)
	if !strings.Contains(yield.query, "rows_written") {
		t.Fatalf("third query = %q, want the yield upsert", yield.query)
	}
	// Yield is useless unless it lands on the same row as the request. Compare
	// all four key columns rather than trusting that both call ledgerEntryFor.
	for i := 0; i < 4; i++ {
		if rollup.args[i] != yield.args[i] {
			t.Fatalf("key column %d differs: request %v, yield %v", i, rollup.args[i], yield.args[i])
		}
	}
	if got, want := yield.args[4], 7; got != want {
		t.Errorf("rows written = %v, want %v", got, want)
	}
}

func TestAddRowsWrittenIgnoresAnEmptyResult(t *testing.T) {
	// A response that changed nothing is the normal outcome under adaptive TTL.
	// Writing a zero row for it would create buckets with no requests in them,
	// making "nothing was fetched" and "something was fetched and yielded
	// nothing" indistinguishable, which is the exact signal the yield chart in
	// 12.2 exists to show.
	db := &fakeRequestLogDB{}
	repo := NewRequestLogRepository(db)

	if err := repo.AddRowsWritten(context.Background(), RequestLog{
		Provider: "api-football", Endpoint: "fixtures", RequestedAt: time.Now().UTC(),
	}, 0); err != nil {
		t.Fatalf("AddRowsWritten returned error: %v", err)
	}
	if len(db.calls) != 0 {
		t.Fatalf("issued %d statements for a zero yield, want none", len(db.calls))
	}
}

func TestAddRowsWrittenRefusesToOrphanYieldFromAnUnstampedRequest(t *testing.T) {
	// Insert fills a missing RequestedAt with now() on its own copy of the
	// struct, which the caller never sees. If AddRowsWritten simply truncated
	// the zero value, the request would land in the current minute and its
	// yield in year one -- two rows that never join, and a yield chart that
	// reads as though every request returned nothing.
	db := &fakeRequestLogDB{}
	repo := NewRequestLogRepository(db)

	err := repo.AddRowsWritten(context.Background(), RequestLog{
		Provider: "api-football",
		Endpoint: "fixtures",
	}, 5)
	if err == nil {
		t.Fatal("AddRowsWritten accepted a RequestLog with no RequestedAt")
	}
	if len(db.calls) != 0 {
		t.Fatalf("wrote %d statements for an unattributable yield, want none", len(db.calls))
	}
}

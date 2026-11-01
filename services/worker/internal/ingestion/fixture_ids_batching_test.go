package ingestion

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
)

func idsJob(t *testing.T) (FixturesJob, *fakeProviderClient) {
	t.Helper()
	body := []byte(`{"errors":[],"response":[]}`)
	client := &fakeProviderClient{response: provider.Response{
		Provider:      "api-football",
		Endpoint:      "fixtures",
		StatusCode:    200,
		Body:          body,
		Latency:       30 * time.Millisecond,
		ResponseBytes: len(body),
	}}
	job := NewFixturesJob(FixturesJobOptions{
		Client:      client,
		RequestLogs: &fakeRequestLogger{},
		EntityMaps:  &fakeEntityMapResolver{},
		Catalog:     &fakeCatalog{},
	})
	return job, client
}

func TestFixturesJobBatchesSpecificIDsIntoOneRequest(t *testing.T) {
	job, client := idsJob(t)
	if err := job.Run(context.Background(), jobs.Job{Payload: []byte(`{"ids":[1234567,1234568,1234569]}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if client.endpoint != "fixtures" {
		t.Fatalf("endpoint = %q, want fixtures", client.endpoint)
	}
	if got := client.query.Get("ids"); got != "1234567-1234568-1234569" {
		t.Fatalf("ids query = %q", got)
	}
	// Three fixtures cost one request, which is the whole point of the branch.
	if client.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", client.calls)
	}
	// A batch is not a date refresh: it must not also pull the whole day.
	if got := client.query.Get("date"); got != "" {
		t.Fatalf("date query = %q, want empty for an id batch", got)
	}
}

// The dedupe lease and the request log both key on a hash of the outgoing
// params. Without canonical ordering, `ids=5-3-9` and `ids=9-5-3` are two
// different requests for identical data: two provider calls, two log rows, and
// a lease that fails to prevent the concurrent double-fire it exists for.
func TestIDOrderingDoesNotChangeTheRequestFingerprint(t *testing.T) {
	forward := url.Values{"ids": {fixtureIDsParam(normalizeFixtureIDs([]int64{5, 3, 9}))}}
	reversed := url.Values{"ids": {fixtureIDsParam(normalizeFixtureIDs([]int64{9, 5, 3}))}}

	if forward.Get("ids") != "3-5-9" {
		t.Fatalf("canonical form = %q, want 3-5-9", forward.Get("ids"))
	}
	if got, want := RequestFingerprint("api-football", "fixtures", reversed), RequestFingerprint("api-football", "fixtures", forward); got != want {
		t.Fatalf("fingerprints differ for the same id set:\n %s\n %s", got, want)
	}
}

func TestNormalizeFixtureIDsDropsDuplicatesAndInvalidIDs(t *testing.T) {
	// A duplicate id would spend part of a 20-slot batch on a fixture the
	// request already covers.
	got := normalizeFixtureIDs([]int64{9, 3, 9, 0, -1, 5, 3})
	if len(got) != 3 || got[0] != 3 || got[1] != 5 || got[2] != 9 {
		t.Fatalf("normalizeFixtureIDs = %v, want [3 5 9]", got)
	}
	if invalid := normalizeFixtureIDs([]int64{0, -5}); len(invalid) != 0 {
		t.Fatalf("invalid-only input produced %v", invalid)
	}
}

func TestFixturesJobRejectsMoreIDsThanTheProviderAccepts(t *testing.T) {
	// Rejected rather than silently truncated: a caller that quietly loses the
	// twenty-first fixture leaves it stranded with no signal anything was
	// dropped, which is the failure this batching exists to prevent.
	ids := make([]string, 0, MaxFixtureIDsPerRequest+1)
	for id := 1; id <= MaxFixtureIDsPerRequest+1; id++ {
		ids = append(ids, strconv.Itoa(id))
	}
	payload := []byte(`{"ids":[` + strings.Join(ids, ",") + `]}`)

	job, client := idsJob(t)
	err := job.Run(context.Background(), jobs.Job{Payload: payload})
	if err == nil {
		t.Fatal("expected an error for an oversized batch")
	}
	if !strings.Contains(err.Error(), "at most 20") {
		t.Fatalf("error = %v", err)
	}
	if client.calls != 0 {
		t.Fatalf("the provider was called %d times for a rejected batch", client.calls)
	}
}

func TestFixturesJobRejectsAnEmptyIDBatch(t *testing.T) {
	job, client := idsJob(t)
	if err := job.Run(context.Background(), jobs.Job{Payload: []byte(`{"ids":[0,-3]}`)}); err == nil {
		t.Fatal("expected an error when no id survives normalisation")
	}
	if client.calls != 0 {
		t.Fatalf("the provider was called %d times for an empty batch", client.calls)
	}
}

func TestIDBatchesAreClassifiedAsCorrectiveWork(t *testing.T) {
	reason, priority := fixturesJobPayload{IDs: []int64{1}}.requestReason()
	if reason != "reconcile" {
		t.Fatalf("reason = %q, want reconcile", reason)
	}
	livePriority := 0
	prematchPriority := 10
	if priority <= livePriority || priority >= prematchPriority {
		// Something is already wrong, so it outranks routine pre-match work --
		// but not the live poll that would have made it unnecessary.
		t.Fatalf("priority = %d, want between live (%d) and prematch (%d)", priority, livePriority, prematchPriority)
	}
}

package ingestion

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
)

// countingClient records how many provider requests were actually made. The
// whole point of adaptive TTL is that this number goes down, so it is the thing
// worth asserting on rather than any internal state.
type countingClient struct {
	fakeProviderClient
	gets int
}

func (c *countingClient) Get(ctx context.Context, endpoint string, query url.Values) (provider.Response, error) {
	c.gets++
	return c.fakeProviderClient.Get(ctx, endpoint, query)
}

// stubResourceStateDB serves one preloaded row and records writes.
type stubResourceStateDB struct {
	*fakeResourceStateDB
	scan func(dest ...any) error
}

func (s *stubResourceStateDB) QueryRowContext(_ context.Context, _ string, _ ...any) rowScanner {
	return scanFunc(s.scan)
}

type scanFunc func(dest ...any) error

func (f scanFunc) Scan(dest ...any) error { return f(dest...) }

func statsJobWithState(t *testing.T, db resourceStateDB, client ProviderClient, now time.Time) FixtureStatisticsJob {
	t.Helper()
	repo := NewResourceStateRepository(db)
	return NewFixtureStatisticsJob(FixtureStatisticsJobOptions{
		Client:        client,
		RequestLogs:   &fakeRequestLogger{},
		EntityMaps:    &fakeEntityMapResolver{},
		Statistics:    &fakeTTLStatisticsWriter{},
		ResourceState: &repo,
		TTL:           TTLTier{Min: time.Minute, Max: 4 * time.Minute},
		Now:           func() time.Time { return now },
	})
}

func TestStatisticsJobSpendsNoRequestWhenTheResourceIsNotDue(t *testing.T) {
	// The saving is the request that never happens. Asserting on the call count
	// rather than on a log line means the test still fails if the skip is moved
	// to after the fetch.
	now := at(0)
	db := &stubResourceStateDB{fakeResourceStateDB: &fakeResourceStateDB{}, scan: func(dest ...any) error {
		// last_fetched_at, hash, streak, interval_s, next_eligible_at, immutable
		*(dest[0].(*time.Time)) = now.Add(-30 * time.Second)
		hash := "abc"
		*(dest[1].(**string)) = &hash
		*(dest[2].(*int)) = 3
		*(dest[3].(*int)) = 240
		*(dest[4].(*time.Time)) = now.Add(90 * time.Second)
		*(dest[5].(*bool)) = false
		return nil
	}}
	client := &countingClient{}
	job := statsJobWithState(t, db, client, now)

	if err := job.Run(context.Background(), jobs.Job{ID: 1, Payload: []byte(`{"fixture_id":42}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if client.gets != 0 {
		t.Fatalf("made %d provider requests for a resource that is not due, want 0", client.gets)
	}
	if len(db.calls) != 0 {
		t.Fatalf("wrote %d rows on a skipped fetch, want 0", len(db.calls))
	}
}

func TestStatisticsJobFetchesWhenTheLadderCannotBeRead(t *testing.T) {
	// The ladder is a scheduling cache, not a source of truth. If it cannot be
	// read the job must fetch: refusing would make a transient database problem
	// silently stop ingestion, which is far worse than one extra request.
	now := at(0)
	db := &stubResourceStateDB{fakeResourceStateDB: &fakeResourceStateDB{}, scan: func(...any) error { return errors.New("unreadable") }}
	client := &countingClient{}
	job := statsJobWithState(t, db, client, now)

	_ = job.Run(context.Background(), jobs.Job{ID: 1, Payload: []byte(`{"fixture_id":42}`)})
	if client.gets != 1 {
		t.Fatalf("made %d provider requests when the ladder was unreadable, want 1", client.gets)
	}
}

func TestStatisticsJobWithNoLadderConfiguredFetchesEveryTime(t *testing.T) {
	// Adaptive TTL is opt-in. With ResourceState nil the job must behave exactly
	// as it did before this existed.
	client := &countingClient{}
	job := NewFixtureStatisticsJob(FixtureStatisticsJobOptions{
		Client:      client,
		RequestLogs: &fakeRequestLogger{},
		EntityMaps:  &fakeEntityMapResolver{},
		Statistics:  &fakeTTLStatisticsWriter{},
		Now:         func() time.Time { return at(0) },
	})

	_ = job.Run(context.Background(), jobs.Job{ID: 1, Payload: []byte(`{"fixture_id":42}`)})
	if client.gets != 1 {
		t.Fatalf("made %d provider requests with no ladder configured, want 1", client.gets)
	}
}

type fakeTTLStatisticsWriter struct{}

func (fakeTTLStatisticsWriter) Upsert(context.Context, int64, int64, StatRecord) error { return nil }

// The adaptive TTL must actually be wired into the production adapter.
//
// Every field on it is optional, so an unwired ladder is not a compile error
// or a test failure -- it is a job that silently fetches everything, every
// time. That is what happened: 94,220 statistics calls in a day across 8,757
// fixtures, roughly eleven refetches each, 96% of all provider traffic and
// more than the entire 75,000 daily allowance. The exhausted quota then froze
// live ingestion for fifteen hours.
//
// So the wiring itself is the thing under test.
func TestProductionAdapterWiresTheStatisticsTTLLadder(t *testing.T) {
	adapter := NewAPIFootballAdapter(nil, &fakeProviderClient{}, jobs.Repository{}, nil, "test", nil, nil, nil, false)

	handler, ok := adapter.Queues["provider-fixtures"][StatisticsJobKey()]
	if !ok {
		t.Fatal("statistics job is not registered on provider-fixtures")
	}
	job, ok := handler.(FixtureStatisticsJob)
	if !ok {
		t.Fatalf("statistics handler is %T, cannot inspect its options", handler)
	}
	if job.opts.ResourceState == nil {
		t.Fatal("statistics job has no ResourceState: the TTL ladder is not connected, " +
			"so every fixture is refetched every time it is handed over")
	}
	if job.opts.TTL.Min <= 0 || job.opts.TTL.Max <= job.opts.TTL.Min {
		t.Fatalf("statistics TTL tier is unusable: min=%s max=%s", job.opts.TTL.Min, job.opts.TTL.Max)
	}
}

type stubSettled struct{ settled bool }

func (s stubSettled) Settled(context.Context, int64) (bool, error) { return s.settled, nil }

// A finished fixture's statistics must become immutable, not merely infrequent.
//
// Backing off on an unchanged hash is not enough on its own: it converges on
// the tier maximum and stays there, so a match played months ago is still
// re-read every cycle forever. Across ~9,400 stored fixtures that is tens of
// thousands of requests a day spent re-learning numbers that stopped changing
// at full time -- and on 2026-09-21 statistics spent 94,220 calls, more than
// the entire 75,000 daily allowance, which is what exhausted the quota and
// froze live ingestion for fifteen hours.
func TestFinishedFixtureStatisticsBecomeImmutable(t *testing.T) {
	now := at(0)
	db := &stubResourceStateDB{
		fakeResourceStateDB: &fakeResourceStateDB{},
		scan: func(dest ...any) error {
			*(dest[0].(*time.Time)) = now.Add(-time.Hour)
			hash := "previous"
			*(dest[1].(**string)) = &hash
			*(dest[2].(*int)) = 1
			*(dest[3].(*int)) = 60
			*(dest[4].(*time.Time)) = now.Add(-time.Minute) // due
			*(dest[5].(*bool)) = false
			return nil
		},
	}
	repo := NewResourceStateRepository(db)
	job := NewFixtureStatisticsJob(FixtureStatisticsJobOptions{
		Client:        &countingClient{fakeProviderClient: fakeProviderClient{response: provider.Response{Provider: "api-football", Endpoint: "fixtures/statistics", StatusCode: 200, Body: []byte(`{"response":[]}`)}}},
		RequestLogs:   &fakeRequestLogger{},
		EntityMaps:    &fakeEntityMapResolver{lookup: map[string]string{"api-football:match:42": "42"}},
		Statistics:    &fakeTTLStatisticsWriter{},
		ResourceState: &repo,
		Settled:       stubSettled{settled: true},
		TTL:           TTLTier{Min: 6 * time.Hour, Max: 24 * time.Hour},
		Now:           func() time.Time { return now },
	})

	if err := job.Run(context.Background(), jobs.Job{ID: 1, Payload: []byte(`{"fixture_id":42}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !savedImmutable(t, db.fakeResourceStateDB) {
		t.Fatal("a settled fixture was not marked immutable, so its statistics " +
			"will keep being refetched for as long as the row exists")
	}
}

// An unfinished fixture must stay refetchable -- the statistics are still moving.
func TestUnfinishedFixtureStatisticsStayRefetchable(t *testing.T) {
	now := at(0)
	db := &stubResourceStateDB{
		fakeResourceStateDB: &fakeResourceStateDB{},
		scan: func(dest ...any) error {
			*(dest[0].(*time.Time)) = now.Add(-time.Hour)
			hash := "previous"
			*(dest[1].(**string)) = &hash
			*(dest[2].(*int)) = 1
			*(dest[3].(*int)) = 60
			*(dest[4].(*time.Time)) = now.Add(-time.Minute)
			*(dest[5].(*bool)) = false
			return nil
		},
	}
	repo := NewResourceStateRepository(db)
	job := NewFixtureStatisticsJob(FixtureStatisticsJobOptions{
		Client:        &countingClient{fakeProviderClient: fakeProviderClient{response: provider.Response{Provider: "api-football", Endpoint: "fixtures/statistics", StatusCode: 200, Body: []byte(`{"response":[]}`)}}},
		RequestLogs:   &fakeRequestLogger{},
		EntityMaps:    &fakeEntityMapResolver{lookup: map[string]string{"api-football:match:42": "42"}},
		Statistics:    &fakeTTLStatisticsWriter{},
		ResourceState: &repo,
		Settled:       stubSettled{settled: false},
		TTL:           TTLTier{Min: 6 * time.Hour, Max: 24 * time.Hour},
		Now:           func() time.Time { return now },
	})

	if err := job.Run(context.Background(), jobs.Job{ID: 1, Payload: []byte(`{"fixture_id":42}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if savedImmutable(t, db.fakeResourceStateDB) {
		t.Fatal("an unfinished fixture was frozen; its statistics are still changing")
	}
}

// savedImmutable reports the immutable flag the job persisted, by reading the
// upsert's arguments. Index 7 matches the argument order in Save.
func savedImmutable(t *testing.T, db *fakeResourceStateDB) bool {
	t.Helper()
	for _, call := range db.calls {
		if strings.Contains(call.query, "provider_resource_state") && len(call.args) >= 8 {
			flag, ok := call.args[7].(bool)
			if !ok {
				t.Fatalf("immutable argument is %T, not bool", call.args[7])
			}
			return flag
		}
	}
	t.Fatal("the job never persisted any adaptive-TTL state")
	return false
}

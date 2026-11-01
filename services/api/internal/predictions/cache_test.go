package predictions_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/httpserver"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/predictions"
)

type countingSource struct {
	httpserver.PredictionSource
	calls   atomic.Int64
	release chan struct{}
	fail    error
	rows    []httpserver.PredictionSummary
}

type resultsSource struct {
	*countingSource
}

func (s *resultsSource) ListPredictionResults(context.Context, httpserver.PredictionFilter) ([]httpserver.PredictionResultSummary, error) {
	return []httpserver.PredictionResultSummary{{PredictionID: 7, MatchID: 42, Outcome: "won"}}, nil
}

func (s *resultsSource) CountPredictionResults(context.Context, httpserver.PredictionFilter) (int, error) {
	return 1, nil
}

func TestCachedSourcePreservesPublicResultsEndpoint(t *testing.T) {
	origin := &resultsSource{countingSource: &countingSource{}}
	cached := predictions.NewCachedSource(origin, newMemoryCache(), "test", time.Minute)
	handler := httpserver.New(httpserver.ServerOptions{Config: config.Config{Environment: "test", Port: 8080}, Predictions: cached})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/predictions/results", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"outcome":"won"`) {
		t.Fatalf("status/body = %d %s", recorder.Code, recorder.Body.String())
	}
}

func (c *countingSource) ListTodayPredictions(context.Context, httpserver.PredictionFilter) ([]httpserver.PredictionSummary, error) {
	c.calls.Add(1)
	if c.release != nil {
		<-c.release
	}
	if c.fail != nil {
		return nil, c.fail
	}
	return c.rows, nil
}

func (c *countingSource) CountTodayPredictions(context.Context, httpserver.PredictionFilter) (int, error) {
	c.calls.Add(1)
	return len(c.rows), nil
}

type memoryCache struct {
	mu     sync.Mutex
	values map[string][]byte
	broken bool
}

func newMemoryCache() *memoryCache { return &memoryCache{values: map[string][]byte{}} }

func (m *memoryCache) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.broken {
		return nil, errors.New("cache down")
	}
	value, ok := m.values[key]
	if !ok {
		return nil, errors.New("miss")
	}
	return value, nil
}

func (m *memoryCache) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.broken {
		return errors.New("cache down")
	}
	m.values[key] = value
	return nil
}

func sampleRows() []httpserver.PredictionSummary {
	return []httpserver.PredictionSummary{{
		ID: 7, MatchID: 99, HomeTeam: "Arsenal", AwayTeam: "Chelsea",
		MarketKey: "1x2", Selection: "home", Confidence: 71.5,
		KickoffAt: time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC),
	}}
}

func TestSecondReadIsServedFromCache(t *testing.T) {
	origin := &countingSource{rows: sampleRows()}
	source := predictions.NewCachedSource(origin, newMemoryCache(), "test", time.Minute)

	first, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{Limit: 10})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	second, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{Limit: 10})
	if err != nil {
		t.Fatalf("second read: %v", err)
	}

	if origin.calls.Load() != 1 {
		t.Fatalf("expected the database to be read once, got %d", origin.calls.Load())
	}
	if len(second) != len(first) || second[0].HomeTeam != "Arsenal" || second[0].Confidence != 71.5 {
		t.Fatalf("cached value did not round-trip: %+v", second)
	}
}

func TestADifferentFilterIsADifferentEntry(t *testing.T) {
	origin := &countingSource{rows: sampleRows()}
	source := predictions.NewCachedSource(origin, newMemoryCache(), "test", time.Minute)

	_, _ = source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{Limit: 10})
	_, _ = source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{Limit: 10, Page: 2})
	_, _ = source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{Limit: 10, MarketKey: "btts"})

	if origin.calls.Load() != 3 {
		t.Fatalf("filters must not share a cache entry, got %d reads", origin.calls.Load())
	}
}

// The failure this whole file exists to prevent: 40 concurrent requests for the
// same missing key ran 40 identical queries, each taking the full request
// budget, and the endpoint returned 503 rather than merely being slow.
func TestAStampedeOnAColdKeyRunsOneQuery(t *testing.T) {
	release := make(chan struct{})
	origin := &countingSource{rows: sampleRows(), release: release}
	source := predictions.NewCachedSource(origin, newMemoryCache(), "test", time.Minute)

	const callers = 40
	var started, done sync.WaitGroup
	started.Add(callers)
	done.Add(callers)
	results := make([][]httpserver.PredictionSummary, callers)

	for i := range callers {
		go func() {
			defer done.Done()
			started.Done()
			rows, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{Limit: 10})
			if err != nil {
				t.Errorf("caller %d: %v", i, err)
				return
			}
			results[i] = rows
		}()
	}

	started.Wait()
	time.Sleep(50 * time.Millisecond) // let every caller reach the load
	close(release)
	done.Wait()

	if calls := origin.calls.Load(); calls != 1 {
		t.Fatalf("expected 1 query for %d concurrent callers, got %d", callers, calls)
	}
	for i, rows := range results {
		if len(rows) != 1 || rows[0].HomeTeam != "Arsenal" {
			t.Fatalf("caller %d got %+v", i, rows)
		}
	}
}

func TestAFailureIsNotCached(t *testing.T) {
	origin := &countingSource{fail: errors.New("deadline exceeded")}
	source := predictions.NewCachedSource(origin, newMemoryCache(), "test", time.Minute)

	if _, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{}); err == nil {
		t.Fatal("expected the error to surface")
	}
	origin.fail = nil
	origin.rows = sampleRows()

	rows, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{})
	if err != nil {
		t.Fatalf("recovery read: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("a cached failure would have pinned the outage; got %+v", rows)
	}
}

func TestADeadCacheStillServesFromPostgres(t *testing.T) {
	origin := &countingSource{rows: sampleRows()}
	cache := newMemoryCache()
	cache.broken = true
	source := predictions.NewCachedSource(origin, cache, "test", time.Minute)

	rows, err := source.ListTodayPredictions(context.Background(), httpserver.PredictionFilter{})
	if err != nil {
		t.Fatalf("Redis being down must not fail the request: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %+v", rows)
	}
}

// contextSource reports whether the load it was given was still live.
type contextSource struct {
	httpserver.PredictionSource
	calls     atomic.Int64
	release   chan struct{}
	cancelled atomic.Bool
}

func (c *contextSource) ListTodayPredictions(ctx context.Context, _ httpserver.PredictionFilter) ([]httpserver.PredictionSummary, error) {
	c.calls.Add(1)
	select {
	case <-c.release:
	case <-ctx.Done():
		c.cancelled.Store(true)
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		c.cancelled.Store(true)
		return nil, err
	}
	return sampleRows(), nil
}

// The regression that left a p95 of 2,751ms after the cache shipped: forty
// callers collapse onto one query, and the query inherited the context of
// whichever caller happened to win the race. When that one caller's deadline
// expired -- or its client simply disconnected -- the shared query was
// cancelled and every waiter failed with it, for a result that was nearly
// ready. A stampede failing as a group is worse than one failing individually.
func TestOneCallerGivingUpDoesNotFailTheOthers(t *testing.T) {
	release := make(chan struct{})
	origin := &contextSource{release: release}
	source := predictions.NewCachedSource(origin, newMemoryCache(), "test", time.Minute)

	winner, cancelWinner := context.WithCancel(context.Background())
	loser := context.Background()

	var done sync.WaitGroup
	done.Add(2)
	var winnerErr, loserErr error
	var loserRows []httpserver.PredictionSummary

	go func() {
		defer done.Done()
		_, winnerErr = source.ListTodayPredictions(winner, httpserver.PredictionFilter{})
	}()
	time.Sleep(40 * time.Millisecond) // let the first caller win the group
	go func() {
		defer done.Done()
		loserRows, loserErr = source.ListTodayPredictions(loser, httpserver.PredictionFilter{})
	}()
	time.Sleep(40 * time.Millisecond)

	cancelWinner()                    // the caller that triggered the load walks away
	time.Sleep(40 * time.Millisecond) // give an inherited context time to kill the query
	close(release)                    // the query would have returned here
	done.Wait()

	if origin.cancelled.Load() {
		t.Fatal("the shared load inherited a caller's cancellation")
	}
	if loserErr != nil {
		t.Fatalf("a waiter failed because another caller gave up: %v", loserErr)
	}
	if len(loserRows) != 1 {
		t.Fatalf("waiter got %+v", loserRows)
	}
	_ = winnerErr // the caller that gave up may legitimately see either outcome
}

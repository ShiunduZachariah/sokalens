package ingestion

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
)

type fakeRequestLogCall struct {
	query string
	args  []any
}

// Insert now issues two statements: the raw request log, then the per-minute
// ledger rollup. Recording only the last one would let a change to either go
// unnoticed, so every call is kept and the tests say which they mean.
type fakeRequestLogDB struct {
	calls []fakeRequestLogCall
	err   error
}

func (f *fakeRequestLogDB) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	f.calls = append(f.calls, fakeRequestLogCall{query: query, args: args})
	return fakeSQLResult(1), f.err
}

// call returns the nth statement, failing the test rather than panicking with
// an index error when the statement was never issued.
func (f *fakeRequestLogDB) call(t *testing.T, n int) fakeRequestLogCall {
	t.Helper()
	if len(f.calls) <= n {
		t.Fatalf("wanted statement %d, only %d were issued", n, len(f.calls))
	}
	return f.calls[n]
}

type fakeSQLResult int64

func (f fakeSQLResult) LastInsertId() (int64, error) { return int64(f), nil }
func (f fakeSQLResult) RowsAffected() (int64, error) { return int64(f), nil }

func TestRequestLogRepositoryInsertsProviderResponseMetadata(t *testing.T) {
	db := &fakeRequestLogDB{}
	repo := NewRequestLogRepository(db)

	err := repo.Insert(context.Background(), RequestLog{
		JobID:         42,
		Provider:      "api-football",
		Endpoint:      "countries",
		Method:        "GET",
		ParamsHash:    "hash123",
		StatusCode:    200,
		QuotaCost:     1,
		Latency:       25 * time.Millisecond,
		ResponseBytes: 128,
		RateLimit: provider.RateLimitHeaders{
			DailyLimit:      100,
			DailyRemaining:  99,
			MinuteLimit:     10,
			MinuteRemaining: 9,
			Raw: map[string]string{
				"x-ratelimit-requests-limit": "100",
			},
		},
		RequestedAt: time.Date(2026, 4, 28, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Insert returned error: %v", err)
	}
	logged := db.call(t, 0)
	if !strings.Contains(logged.query, "provider_request_logs") {
		t.Fatalf("query = %q, want provider_request_logs insert", logged.query)
	}
	if got, want := len(logged.args), 18; got != want {
		t.Fatalf("len(args) = %d, want %d: %#v", got, want, logged.args)
	}
	if logged.args[0] != "api-football" || logged.args[1] != "countries" || logged.args[2] != int64(42) || logged.args[3] != "GET" {
		t.Fatalf("first args = %#v, want provider endpoint job method", logged.args[:4])
	}
	if logged.args[5] != 200 || logged.args[6] != 1 || logged.args[7] != int64(25) {
		t.Fatalf("status/quota/latency args = %#v, want 200/1/25", logged.args[5:8])
	}
	if logged.args[15] != "" || logged.args[16] != 0 {
		t.Fatalf("reason/priority args = %#v, want empty/0", logged.args[15:17])
	}
	if !strings.Contains(logged.query, "job_id") {
		t.Fatalf("query = %q, want job_id column", logged.query)
	}
}

func TestRequestLogRepositoryPreservesZeroQuotaCost(t *testing.T) {
	db := &fakeRequestLogDB{}
	repo := NewRequestLogRepository(db)

	err := repo.Insert(context.Background(), RequestLog{
		Provider:   "api-football",
		Endpoint:   "fixtures",
		StatusCode: 0,
		QuotaCost:  0,
	})
	if err != nil {
		t.Fatalf("Insert returned error: %v", err)
	}
	if got := db.call(t, 0).args[6]; got != 0 {
		t.Fatalf("quota cost = %v, want 0 for a client-side preflight block", got)
	}
}

func TestRequestLogRepositoryRequiresProviderAndEndpoint(t *testing.T) {
	repo := NewRequestLogRepository(&fakeRequestLogDB{})

	if err := repo.Insert(context.Background(), RequestLog{Endpoint: "countries"}); err == nil {
		t.Fatal("Insert returned nil error without provider")
	}
	if err := repo.Insert(context.Background(), RequestLog{Provider: "api-football"}); err == nil {
		t.Fatal("Insert returned nil error without endpoint")
	}
}

// A call our own governor refused must not be recorded as a provider request.
//
// It fed a runaway: the refusal was written to provider_request_logs AND
// incremented provider_quota_minute.request_count, which is the counter the
// daily reserve is measured against, so every refusal made the next more
// likely. Production reached 1,103,638 counted api-football requests in a day
// against a 75,000 limit -- 1,103,517 of them errors, i.e. about 121 real
// calls and a million refusals counting themselves -- and every provider job
// had been deferred for fifteen hours.
func TestRefusedRequestsAreNotRecordedAgainstTheQuotaTheyWereRefusedBy(t *testing.T) {
	db := &fakeRequestLogDB{}
	repo := NewRequestLogRepository(db)

	err := repo.Insert(context.Background(), RequestLog{
		Provider:      "api-football",
		Endpoint:      "fixtures",
		Method:        "GET",
		StatusCode:    0,            // never sent
		ErrorCategory: "rate_limit", // our daily reserve said no
		RequestedAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Insert returned error: %v", err)
	}
	if len(db.calls) != 0 {
		t.Fatalf("a refused call wrote %d statement(s); it must write none, or "+
			"the quota counter counts its own refusals", len(db.calls))
	}
}

// The guard must not swallow a rate limit the provider itself applied: that is
// a real call, it consumed real quota, and it has to stay on the ledger.
func TestProviderAppliedRateLimitIsStillRecorded(t *testing.T) {
	db := &fakeRequestLogDB{}
	repo := NewRequestLogRepository(db)

	err := repo.Insert(context.Background(), RequestLog{
		Provider:      "api-football",
		Endpoint:      "fixtures",
		Method:        "GET",
		StatusCode:    429, // the provider answered
		ErrorCategory: "rate_limit",
		RequestedAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Insert returned error: %v", err)
	}
	if len(db.calls) == 0 {
		t.Fatal("a 429 from the provider was dropped; it is a real call and must be recorded")
	}
}

// A transport failure also reached the provider, or may have, so it counts.
func TestTransportFailureIsStillRecorded(t *testing.T) {
	db := &fakeRequestLogDB{}
	repo := NewRequestLogRepository(db)

	err := repo.Insert(context.Background(), RequestLog{
		Provider:      "api-football",
		Endpoint:      "fixtures",
		Method:        "GET",
		StatusCode:    0,
		ErrorCategory: "provider_transport",
		RequestedAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("Insert returned error: %v", err)
	}
	if len(db.calls) == 0 {
		t.Fatal("a transport failure was dropped; the request may have reached the provider")
	}
}

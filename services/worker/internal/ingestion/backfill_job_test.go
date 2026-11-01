package ingestion

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

type fakeBackfillStore struct {
	request     DataBackfillRequest
	count       int
	completed   bool
	unavailable bool
	attempts    []string
}

func (f *fakeBackfillStore) Start(context.Context, int64) (DataBackfillRequest, error) {
	return f.request, nil
}

func (f *fakeBackfillStore) Count(context.Context, DataBackfillRequest) (int, error) {
	return f.count, nil
}

func (f *fakeBackfillStore) RecordAttempt(_ context.Context, _ int64, provider string, available bool) error {
	f.attempts = append(f.attempts, provider)
	return nil
}

func (f *fakeBackfillStore) Complete(context.Context, DataBackfillRequest) error {
	f.completed = true
	return nil
}

func (f *fakeBackfillStore) ConfirmUnavailable(context.Context, DataBackfillRequest) error {
	f.unavailable = true
	return nil
}

type fakeBackfillProvider struct {
	name      string
	available bool
	err       error
	after     int
	store     *fakeBackfillStore
	calls     *[]string
}

type fakeBackfillPayloads struct {
	payloads [][]byte
	err      error
}

func (f fakeBackfillPayloads) BuildProviderPayloads(context.Context, string, DataBackfillRequest) ([][]byte, error) {
	return f.payloads, f.err
}

type captureBackfillHandler struct {
	payloads [][]byte
}

func (c *captureBackfillHandler) Run(_ context.Context, job jobs.Job) error {
	c.payloads = append(c.payloads, job.Payload)
	return nil
}

func (f fakeBackfillProvider) Name() string { return f.name }

func (f fakeBackfillProvider) Supports(group string) bool {
	return group == "results" || f.name == "api-football"
}

func (f fakeBackfillProvider) Backfill(context.Context, jobs.Job, DataBackfillRequest) (bool, error) {
	*f.calls = append(*f.calls, f.name)
	if f.err != nil {
		return false, f.err
	}
	if f.after > 0 {
		f.store.count = f.after
	}
	return f.available, nil
}

func TestBackfillTeamHistoryUsesFreeProvidersBeforeAPIFootball(t *testing.T) {
	store := &fakeBackfillStore{request: DataBackfillRequest{ID: 7, FeatureGroup: "results", MatchesNeeded: 5}}
	calls := []string{}
	handler := NewBackfillTeamHistoryJob(BackfillTeamHistoryJobOptions{
		Requests: store,
		Providers: []BackfillProvider{
			fakeBackfillProvider{name: "football-data.org", available: true, store: store, calls: &calls},
			fakeBackfillProvider{name: "thesportsdb", available: true, after: 5, store: store, calls: &calls},
			fakeBackfillProvider{name: "api-football", available: true, store: store, calls: &calls},
		},
	})

	err := handler.Run(context.Background(), jobs.Job{ID: 11, Payload: []byte(`{"request_id":7}`)})

	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got, want := len(calls), 2; got != want || calls[0] != "football-data.org" || calls[1] != "thesportsdb" {
		t.Fatalf("provider calls = %v, want free providers only in order", calls)
	}
	if !store.completed || store.unavailable {
		t.Fatalf("completion state = completed:%v unavailable:%v", store.completed, store.unavailable)
	}
}

func TestBackfillTeamHistoryConfirmsUnavailableOnlyAfterEveryProvider(t *testing.T) {
	store := &fakeBackfillStore{request: DataBackfillRequest{ID: 7, FeatureGroup: "results", MatchesNeeded: 5}}
	calls := []string{}
	providers := []BackfillProvider{
		fakeBackfillProvider{name: "football-data.org", store: store, calls: &calls},
		fakeBackfillProvider{name: "thesportsdb", store: store, calls: &calls},
		fakeBackfillProvider{name: "api-football", store: store, calls: &calls},
	}
	handler := NewBackfillTeamHistoryJob(BackfillTeamHistoryJobOptions{Requests: store, Providers: providers})

	err := handler.Run(context.Background(), jobs.Job{Payload: []byte(`{"request_id":7}`)})

	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !store.unavailable || store.completed {
		t.Fatalf("completion state = completed:%v unavailable:%v", store.completed, store.unavailable)
	}
	if len(calls) != 3 {
		t.Fatalf("provider calls = %v, want all providers", calls)
	}
}

func TestBackfillTeamHistoryLeavesTransientProviderFailureRetryable(t *testing.T) {
	store := &fakeBackfillStore{request: DataBackfillRequest{ID: 7, FeatureGroup: "results", MatchesNeeded: 5}}
	calls := []string{}
	want := errors.New("rate limited")
	handler := NewBackfillTeamHistoryJob(BackfillTeamHistoryJobOptions{
		Requests: store,
		Providers: []BackfillProvider{
			fakeBackfillProvider{name: "football-data.org", err: want, store: store, calls: &calls},
		},
	})

	err := handler.Run(context.Background(), jobs.Job{Payload: []byte(`{"request_id":7}`)})

	if !errors.Is(err, want) {
		t.Fatalf("Run error = %v, want %v", err, want)
	}
	if store.unavailable || store.completed {
		t.Fatal("transient error must not become a terminal request state")
	}
}

func TestJobBackfillProviderReusesRegisteredProviderHandler(t *testing.T) {
	handler := &captureBackfillHandler{}
	provider := NewJobBackfillProvider(
		"football-data.org",
		[]string{"results"},
		handler,
		fakeBackfillPayloads{payloads: [][]byte{[]byte(`{"competition":"PL"}`)}},
	)

	available, err := provider.Backfill(
		context.Background(),
		jobs.Job{ID: 11},
		DataBackfillRequest{ID: 7, FeatureGroup: "results"},
	)

	if err != nil || !available {
		t.Fatalf("Backfill = available:%v error:%v", available, err)
	}
	if got := string(handler.payloads[0]); got != `{"competition":"PL"}` {
		t.Fatalf("handler payload = %s", got)
	}
}

// notClaimableStore stands in for a request that is not due, has expired, or
// has already completed.
type notClaimableStore struct{ fakeBackfillStore }

func (notClaimableStore) Start(context.Context, int64) (DataBackfillRequest, error) {
	return DataBackfillRequest{}, sql.ErrNoRows
}

// A request that cannot be claimed right now is not a failed job.
//
// Start is an UPDATE ... RETURNING matching only a row that is pending or
// in_progress, due and unexpired, so one that has completed, expired, or had
// its next attempt pushed forward matches nothing and the driver reports
// `sql: no rows in result set`. Treating that as an error failed the job,
// retried it twice, and failed it again: 1,169 such failures in three hours on
// 2026-09-22 against 875 successes, with nothing actually wrong in any of them.
//
// The coordinator is scheduled independently of when a request becomes due, so
// arriving early is normal and the right answer is to do nothing.
func TestBackfillTeamHistoryTreatsAnUnclaimableRequestAsANoOp(t *testing.T) {
	store := &notClaimableStore{}
	job := NewBackfillTeamHistoryJob(BackfillTeamHistoryJobOptions{
		Requests:  store,
		Providers: []BackfillProvider{},
	})

	err := job.Run(context.Background(), jobs.Job{ID: 1, Payload: []byte(`{"request_id":42}`)})
	if err != nil {
		t.Fatalf("an unclaimable request failed the job: %v", err)
	}
	if store.completed || store.unavailable || len(store.attempts) != 0 {
		t.Fatalf("an unclaimable request must change nothing: completed=%v unavailable=%v attempts=%v",
			store.completed, store.unavailable, store.attempts)
	}
}

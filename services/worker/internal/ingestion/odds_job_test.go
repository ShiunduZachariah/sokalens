package ingestion

import (
	"context"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
)

type fakeOddsRepository struct {
	marketID          int64
	modelID           int64
	snapshots         []OddsSnapshotRecord
	signals           []PreparedSignal
	failureRecords    []string
	clearFailureCount int
}

func (f *fakeOddsRepository) LookupMarketID(_ context.Context, _ string) (int64, error) {
	return f.marketID, nil
}

func (f *fakeOddsRepository) LookupModelID(_ context.Context, _ string) (int64, error) {
	return f.modelID, nil
}

func (f *fakeOddsRepository) InsertOddsSnapshot(_ context.Context, record OddsSnapshotRecord) error {
	f.snapshots = append(f.snapshots, record)
	return nil
}

func (f *fakeOddsRepository) UpsertFeatureSnapshot(_ context.Context, signal PreparedSignal) error {
	f.signals = append(f.signals, signal)
	return nil
}

func (f *fakeOddsRepository) RecordGenerationFailure(_ context.Context, _ int64, _ int64, _ int64, reasonCode, _ string) error {
	f.failureRecords = append(f.failureRecords, reasonCode)
	return nil
}

func (f *fakeOddsRepository) ClearGenerationFailure(_ context.Context, _ int64, _ int64, _ int64) error {
	f.clearFailureCount++
	return nil
}

type fakePredictionJobEnqueuer struct {
	jobs []jobs.NewJob
}

func (f *fakePredictionJobEnqueuer) Enqueue(_ context.Context, job jobs.NewJob) (int64, error) {
	f.jobs = append(f.jobs, job)
	return int64(len(f.jobs)), nil
}

func TestOddsJobStoresNumericOddsAndQueuesPredictionRefresh(t *testing.T) {
	body := []byte(`{"errors":[],"response":[{"fixture":{"id":1001,"date":"2026-05-14T18:00:00+00:00"},"update":"2026-05-14T12:00:00+00:00","league":{"id":39,"name":"Premier League"},"bookmakers":[{"id":8,"name":"Bet365","bets":[{"id":1,"name":"Match Winner","values":[{"value":"Home","odd":2.2},{"value":"Draw","odd":"3.4"},{"value":"Away","odd":3.1}]}]}]}]}`)
	client := &fakeProviderClient{response: provider.Response{
		Provider:      "api-football",
		Endpoint:      "odds",
		StatusCode:    200,
		Body:          body,
		Latency:       25 * time.Millisecond,
		ResponseBytes: len(body),
	}}
	entityMaps := &fakeEntityMapResolver{
		lookup: map[string]string{
			"api-football:match:1001": "77",
		},
	}
	oddsRepo := &fakeOddsRepository{marketID: 9, modelID: 3}
	jobEnqueuer := &fakePredictionJobEnqueuer{}
	requestLogs := &fakeRequestLogger{}

	job := NewOddsJob(OddsJobOptions{
		Client:      client,
		RequestLogs: requestLogs,
		EntityMaps:  entityMaps,
		Odds:        oddsRepo,
		Jobs:        jobEnqueuer,
		Now: func() time.Time {
			return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
		},
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 901, Payload: []byte(`{"date":"2026-05-14","page":1}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if client.endpoint != "odds" {
		t.Fatalf("endpoint = %q, want odds", client.endpoint)
	}
	if len(oddsRepo.snapshots) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(oddsRepo.snapshots))
	}
	if len(oddsRepo.signals) != 1 {
		t.Fatalf("signals = %d, want 1", len(oddsRepo.signals))
	}
	if oddsRepo.clearFailureCount != 1 {
		t.Fatalf("clear failure count = %d, want 1", oddsRepo.clearFailureCount)
	}
	if len(jobEnqueuer.jobs) != 1 {
		t.Fatalf("enqueued jobs = %d, want 1", len(jobEnqueuer.jobs))
	}
	if jobEnqueuer.jobs[0].JobKey != "predictions.generate.upcoming" {
		t.Fatalf("job key = %q, want predictions.generate.upcoming", jobEnqueuer.jobs[0].JobKey)
	}
	if jobEnqueuer.jobs[0].Queue != "predictions" {
		t.Fatalf("job queue = %q, want predictions", jobEnqueuer.jobs[0].Queue)
	}
	if jobEnqueuer.jobs[0].IdempotencyKey != "schedule:predictions:generate:20260514T120000Z" {
		t.Fatalf("idempotency key = %q, want global refresh bucket", jobEnqueuer.jobs[0].IdempotencyKey)
	}
	if !jobEnqueuer.jobs[0].DeduplicateCompleted {
		t.Fatal("prediction refresh job must deduplicate its completed 15-minute bucket")
	}
	if len(requestLogs.logs) != 1 || requestLogs.logs[0].JobID != 901 {
		t.Fatalf("request logs = %+v, want source job id 901", requestLogs.logs)
	}
}

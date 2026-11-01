package paidmock

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"
)

func recordedQuery() url.Values { return url.Values{"season": {"2026"}} }

func TestSuccessReplaysRecordedResponseAndCapabilityWithoutNetwork(t *testing.T) {
	adapter, err := New(ScenarioSuccess)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	capability := adapter.Capability()
	if err := capability.Validate(); err != nil {
		t.Fatalf("Capability.Validate: %v", err)
	}
	if capability.QuotaCost != 2 || capability.ObservedAt.IsZero() || capability.ValidUntil == nil {
		t.Fatalf("capability does not preserve quota/timestamps: %+v", capability)
	}
	if !capability.Licensing.MayPersistRaw || !capability.Licensing.MayUseForTraining || capability.Licensing.MayServePublicly || !capability.Licensing.MayServeToPaidUsers {
		t.Fatalf("capability does not preserve explicit licensing: %+v", capability.Licensing)
	}

	resp, err := adapter.Get(context.Background(), "fixtures", recordedQuery())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.StatusCode != 200 || resp.Provider != ProviderName || resp.Latency != 84*time.Millisecond {
		t.Fatalf("unexpected response metadata: %+v", resp)
	}
	if resp.ResponseBytes != len(resp.Body) || resp.RateLimit.DailyRemaining != 7498 || resp.RateLimit.MinuteRemaining != 298 {
		t.Fatalf("unexpected response size/rate limit: %+v", resp)
	}
	if adapter.Calls() != 1 {
		t.Fatalf("Calls = %d, want 1", adapter.Calls())
	}
}

func TestRecordedProviderErrorIsClassified(t *testing.T) {
	adapter, err := New(ScenarioProviderError)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := adapter.Get(context.Background(), "fixtures", recordedQuery())
	if !errors.Is(err, ErrProviderResponse) {
		t.Fatalf("Get error = %v, want ErrProviderResponse", err)
	}
	if resp.StatusCode != 503 || len(resp.Body) == 0 {
		t.Fatalf("recorded provider error response not preserved: %+v", resp)
	}
}

func TestRecordedRateLimitPreservesQuotaAndRetryMetadata(t *testing.T) {
	adapter, err := New(ScenarioRateLimit)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := adapter.Get(context.Background(), "fixtures", recordedQuery())
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Get error = %v, want ErrRateLimited", err)
	}
	if resp.StatusCode != 429 || resp.RateLimit.DailyRemaining != 0 || resp.RateLimit.MinuteRemaining != 0 || resp.RateLimit.Raw["Retry-After"] != "60" {
		t.Fatalf("recorded rate limit response not preserved: %+v", resp)
	}
}

func TestRecordedSchemaChangeFailsClosedWithPayloadForDiagnostics(t *testing.T) {
	adapter, err := New(ScenarioSchemaChange)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := adapter.Get(context.Background(), "fixtures", recordedQuery())
	if !errors.Is(err, ErrSchemaChanged) {
		t.Fatalf("Get error = %v, want ErrSchemaChanged", err)
	}
	if resp.StatusCode != 200 || len(resp.Body) == 0 {
		t.Fatalf("schema-change payload should be retained for diagnostics: %+v", resp)
	}
}

func TestRequestMismatchAndCancellationDoNotReplayFixture(t *testing.T) {
	adapter, err := New(ScenarioSuccess)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := adapter.Get(context.Background(), "odds", recordedQuery()); !errors.Is(err, ErrRequestMismatch) {
		t.Fatalf("endpoint mismatch error = %v, want ErrRequestMismatch", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := adapter.Calls()
	if _, err := adapter.Get(ctx, "fixtures", recordedQuery()); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Get error = %v, want context.Canceled", err)
	}
	if adapter.Calls() != before {
		t.Fatal("canceled call must not replay a fixture")
	}
}

func TestEveryRecordedScenarioLoads(t *testing.T) {
	for _, scenario := range []Scenario{ScenarioSuccess, ScenarioProviderError, ScenarioRateLimit, ScenarioSchemaChange} {
		t.Run(string(scenario), func(t *testing.T) {
			if _, err := New(scenario); err != nil {
				t.Fatalf("New(%q): %v", scenario, err)
			}
		})
	}
}

func TestAdapterSupportsConcurrentFixtureReplay(t *testing.T) {
	adapter, err := New(ScenarioSuccess)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const workers = 24
	var wg sync.WaitGroup
	errorsByWorker := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := adapter.Get(context.Background(), "fixtures", recordedQuery())
			errorsByWorker <- err
		}()
	}
	wg.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Fatalf("concurrent Get: %v", err)
		}
	}
	if adapter.Calls() != workers {
		t.Fatalf("Calls = %d, want %d", adapter.Calls(), workers)
	}
}

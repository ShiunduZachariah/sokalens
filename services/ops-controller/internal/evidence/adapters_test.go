package evidence

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func collectRequest() CollectRequest {
	now := testTime()
	return CollectRequest{IncidentID: "incident-001", CollectedAt: now, WindowStart: now.Add(-5 * time.Minute), WindowEnd: now}
}

func TestMockAdaptersReturnDeterministicPreparedFixtures(t *testing.T) {
	adapters := []DiagnosticAdapter{
		NewMockPrometheus(FixtureSuccess), NewMockLoki(FixtureSuccess), NewMockTempo(FixtureSuccess),
		NewMockPostgreSQL(FixtureSuccess), NewMockRedis(FixtureSuccess), NewMockDeployment(FixtureSuccess),
	}
	wantSources := []Source{SourcePrometheus, SourceLoki, SourceTempo, SourcePostgreSQL, SourceRedis, SourceDeployment}
	for i, adapter := range adapters {
		t.Run(string(adapter.Source()), func(t *testing.T) {
			if adapter.Source() != wantSources[i] {
				t.Fatalf("Source() = %q, want %q", adapter.Source(), wantSources[i])
			}
			one, err := adapter.Collect(context.Background(), collectRequest())
			if err != nil {
				t.Fatalf("Collect() error = %v", err)
			}
			two, err := adapter.Collect(context.Background(), collectRequest())
			if err != nil {
				t.Fatalf("second Collect() error = %v", err)
			}
			if !reflect.DeepEqual(one.Value(), two.Value()) {
				t.Fatal("fixture changed between calls")
			}
			bundle := one.Value()
			if err := bundle.Validate(); err != nil {
				t.Fatalf("fixture invalid: %v", err)
			}
			if len(bundle.Records) != 1 || bundle.Records[0].Source != adapter.Source() {
				t.Fatalf("unexpected fixture record: %+v", bundle.Records)
			}
		})
	}
}

func TestMockAdaptersReturnDeterministicErrors(t *testing.T) {
	adapters := []*MockAdapter{
		NewMockPrometheus(FixtureError), NewMockLoki(FixtureError), NewMockTempo(FixtureError),
		NewMockPostgreSQL(FixtureError), NewMockRedis(FixtureError), NewMockDeployment(FixtureError),
	}
	for _, adapter := range adapters {
		t.Run(string(adapter.Source()), func(t *testing.T) {
			_, first := adapter.Collect(context.Background(), collectRequest())
			_, second := adapter.Collect(context.Background(), collectRequest())
			if !errors.Is(first, ErrFixtureUnavailable) || !errors.Is(second, ErrFixtureUnavailable) {
				t.Fatalf("errors = %v, %v", first, second)
			}
			if first.Error() != second.Error() {
				t.Fatalf("error fixture is not deterministic: %q != %q", first, second)
			}
		})
	}
}

func TestMockAdapterFailsClosed(t *testing.T) {
	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := NewMockRedis(FixtureSuccess).Collect(ctx, collectRequest()); !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
	})
	t.Run("invalid request", func(t *testing.T) {
		request := collectRequest()
		request.IncidentID = ""
		if _, err := NewMockLoki(FixtureSuccess).Collect(context.Background(), request); err == nil {
			t.Fatal("Collect unexpectedly accepted invalid request")
		}
	})
	t.Run("invalid fixture mode", func(t *testing.T) {
		if _, err := newMock(SourceTempo, FixtureMode("other")).Collect(context.Background(), collectRequest()); err == nil {
			t.Fatal("Collect unexpectedly accepted invalid mode")
		}
	})
	t.Run("unknown source", func(t *testing.T) {
		if _, err := newMock(Source("unknown"), FixtureSuccess).Collect(context.Background(), collectRequest()); err == nil {
			t.Fatal("Collect unexpectedly accepted unknown source")
		}
	})
}

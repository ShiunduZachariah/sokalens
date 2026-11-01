package ingestion

import (
	"context"
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

func TestProviderRegistryMergesQueueScopedHandlers(t *testing.T) {
	first := ProviderAdapter{
		Provider: "api-football",
		Queues: map[string]map[string]JobHandler{
			DefaultProviderQueue: {
				ProviderJobKey("api-football", "countries"): fakeProviderHandler{name: "countries"},
			},
		},
	}
	second := ProviderAdapter{
		Provider: "other-provider",
		Queues: map[string]map[string]JobHandler{
			DefaultProviderQueue: {
				ProviderJobKey("other-provider", "countries"): fakeProviderHandler{name: "other-countries"},
			},
			"provider-odds": {
				ProviderJobKey("other-provider", "odds"): fakeProviderHandler{name: "other-odds"},
			},
		},
	}

	registry, err := NewProviderRegistry(first, second)

	if err != nil {
		t.Fatalf("NewProviderRegistry returned error: %v", err)
	}
	referenceHandlers := registry.Handlers(DefaultProviderQueue)
	if len(referenceHandlers) != 2 {
		t.Fatalf("reference handlers = %d, want 2", len(referenceHandlers))
	}
	if referenceHandlers[ProviderJobKey("api-football", "countries")] == nil {
		t.Fatalf("api-football countries handler missing")
	}
	if registry.Handlers("provider-odds")[ProviderJobKey("other-provider", "odds")] == nil {
		t.Fatalf("other-provider odds handler missing")
	}
}

func TestProviderRegistryRejectsDuplicateJobKeysInSameQueue(t *testing.T) {
	_, err := NewProviderRegistry(
		ProviderAdapter{
			Provider: "api-football",
			Queues: map[string]map[string]JobHandler{
				DefaultProviderQueue: {
					ProviderJobKey("api-football", "countries"): fakeProviderHandler{name: "first"},
				},
			},
		},
		ProviderAdapter{
			Provider: "duplicate",
			Queues: map[string]map[string]JobHandler{
				DefaultProviderQueue: {
					ProviderJobKey("api-football", "countries"): fakeProviderHandler{name: "second"},
				},
			},
		},
	)

	if err == nil {
		t.Fatal("NewProviderRegistry returned nil error for duplicate job key")
	}
}

type fakeProviderHandler struct {
	name string
}

func (f fakeProviderHandler) Run(context.Context, jobs.Job) error {
	return nil
}

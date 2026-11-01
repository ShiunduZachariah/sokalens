package ingestion

import (
	"context"
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
)

type recordingRateLimits struct{ updates []ProviderRateLimitUpdate }

func (r *recordingRateLimits) UpdateFromHeaders(_ context.Context, u ProviderRateLimitUpdate) error {
	r.updates = append(r.updates, u)
	return nil
}

type recordingBudget struct {
	limit     int
	remaining int
	observed  bool
}

func (r *recordingBudget) Observe(limit, remaining int) {
	r.limit, r.remaining, r.observed = limit, remaining, true
}

const statusBody = `{"response":{"account":{"firstname":"x"},
"subscription":{"plan":"Ultra","active":true},
"requests":{"current":121,"limit_day":75000}}}`

// The provider's own accounting is the source of truth, and asking for it must
// not depend on having budget to ask with.
//
// On 2026-09-22 our stored remaining was 33 while the provider's dashboard
// showed the day at 0% used. Nothing reconciled the two, so the wrong number
// governed for fifteen hours.
func TestQuotaSyncWritesTheProvidersOwnAccounting(t *testing.T) {
	client := &fakeProviderClient{response: provider.Response{
		Provider: "api-football", Endpoint: "status", StatusCode: 200,
		Body: []byte(statusBody),
	}}
	limits := &recordingRateLimits{}
	budget := &recordingBudget{}

	job := NewQuotaSyncJob(QuotaSyncJobOptions{
		Client: client, RateLimits: limits, Budget: budget,
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 1}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if client.endpoint != "status" {
		t.Fatalf("asked %q, want the status endpoint", client.endpoint)
	}
	if len(limits.updates) != 1 {
		t.Fatalf("wrote %d rate-limit updates, want 1", len(limits.updates))
	}
	got := limits.updates[0]
	if got.DailyLimit != 75000 || got.DailyRemaining != 74879 {
		t.Fatalf("persisted limit=%d remaining=%d, want 75000/74879",
			got.DailyLimit, got.DailyRemaining)
	}
	if !budget.observed || budget.remaining != 74879 {
		t.Fatalf("live budget not corrected: observed=%v remaining=%d",
			budget.observed, budget.remaining)
	}
}

// A status response we cannot read must leave the stored quota alone.
//
// It fails loudly and writes nothing, which is the important half: this row is
// what the next worker start seeds its budget from, so a guess written here
// becomes authoritative and governs until something contradicts it. That is
// the whole shape of the outage this job exists to prevent.
func TestQuotaSyncLeavesStateAloneWhenTheStatusIsUnusable(t *testing.T) {
	client := &fakeProviderClient{response: provider.Response{
		Provider: "api-football", Endpoint: "status", StatusCode: 200,
		Body: []byte(`{"response":{"subscription":{"plan":"Ultra","active":true},
"requests":{"current":0,"limit_day":0}}}`),
	}}
	limits := &recordingRateLimits{}
	budget := &recordingBudget{}

	job := NewQuotaSyncJob(QuotaSyncJobOptions{
		Client: client, RateLimits: limits, Budget: budget,
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 1}); err == nil {
		t.Fatal("an unreadable status response was accepted")
	}
	if len(limits.updates) != 0 || budget.observed {
		t.Fatal("an unreadable status response overwrote the stored quota")
	}
}

// A failed request must not be mistaken for a zero quota.
func TestQuotaSyncDoesNotWriteWhenTheRequestFails(t *testing.T) {
	client := &fakeProviderClient{
		response: provider.Response{Provider: "api-football", Endpoint: "status"},
		err:      context.DeadlineExceeded,
	}
	limits := &recordingRateLimits{}
	budget := &recordingBudget{}

	job := NewQuotaSyncJob(QuotaSyncJobOptions{
		Client: client, RateLimits: limits, Budget: budget,
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 1}); err == nil {
		t.Fatal("a failed status request was treated as success")
	}
	if len(limits.updates) != 0 || budget.observed {
		t.Fatal("a failed status request wrote a quota value")
	}
}

package notifications

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeStore struct {
	messages       []Message
	claimed        ClaimOptions
	claimSizes     []int
	sent           []int64
	retried        []int64
	suppressed     []int64
	rescheduled    []int64
	rescheduleAt   time.Time
	retryAvailable time.Time
	mu             sync.Mutex
}

func (f *fakeStore) ClaimBatch(_ context.Context, opts ClaimOptions) ([]Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimed = opts
	f.claimSizes = append(f.claimSizes, opts.BatchSize)
	count := min(opts.BatchSize, len(f.messages))
	messages := append([]Message(nil), f.messages[:count]...)
	f.messages = f.messages[count:]
	return messages, nil
}

func (f *fakeStore) MarkSent(_ context.Context, message Message, _ Delivery, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, message.ID)
	return nil
}

func (f *fakeStore) MarkRetry(_ context.Context, message Message, retry Retry, _ time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retried = append(f.retried, message.ID)
	f.retryAvailable = retry.AvailableAt
	if message.Attempts >= message.MaxAttempts {
		return "dead_letter", nil
	}
	return "retry", nil
}

func (f *fakeStore) Reschedule(_ context.Context, message Message, availableAt time.Time, _ string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rescheduled = append(f.rescheduled, message.ID)
	f.rescheduleAt = availableAt
	return nil
}

func (f *fakeStore) Suppress(_ context.Context, message Message, _ string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.suppressed = append(f.suppressed, message.ID)
	return nil
}

type fakeTransport struct {
	err       error
	inFlight  atomic.Int64
	maxFlight atomic.Int64
}

func (f *fakeTransport) Send(_ context.Context, request DeliveryRequest) (Delivery, error) {
	current := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		maximum := f.maxFlight.Load()
		if current <= maximum || f.maxFlight.CompareAndSwap(maximum, current) {
			break
		}
	}
	time.Sleep(time.Millisecond)
	return Delivery{ProviderMessageID: request.IdempotencyKey}, f.err
}

type guardFunc func(context.Context, Message) (GuardDecision, error)

func (f guardFunc) CheckAccount(ctx context.Context, message Message) (GuardDecision, error) {
	return f(ctx, message)
}

type preferenceGuardFunc func(context.Context, Message) (GuardDecision, error)

func (f preferenceGuardFunc) CheckPreference(ctx context.Context, message Message) (GuardDecision, error) {
	return f(ctx, message)
}

func TestWorkerUsesBatchClaimsFencingAndBoundedConcurrency(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{messages: []Message{
		{ID: 1, DedupeKey: "one", Attempts: 1, MaxAttempts: 5, LeaseToken: "11111111-1111-1111-1111-111111111111"},
		{ID: 2, DedupeKey: "two", Attempts: 1, MaxAttempts: 5, LeaseToken: "22222222-2222-2222-2222-222222222222"},
		{ID: 3, DedupeKey: "three", Attempts: 1, MaxAttempts: 5, LeaseToken: "33333333-3333-3333-3333-333333333333"},
	}}
	transport := &fakeTransport{}
	worker, err := NewWorker(WorkerOptions{
		Store: store, Transport: transport, Channel: "email", BatchSize: 3, MaxConcurrent: 2,
		Now: func() time.Time { return now }, Jitter: func(time.Duration) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatalf("NewWorker returned error: %v", err)
	}
	processed, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if processed != 3 || len(store.sent) != 3 {
		t.Fatalf("processed=%d sent=%v, want three", processed, store.sent)
	}
	if transport.maxFlight.Load() > 2 {
		t.Fatalf("max concurrent sends=%d, want <=2", transport.maxFlight.Load())
	}
	if len(store.claimSizes) != 2 || store.claimSizes[0] != 2 || store.claimSizes[1] != 1 || store.claimed.Channel != "email" {
		t.Fatalf("claim sizes=%v options=%+v", store.claimSizes, store.claimed)
	}
}

func TestWorkerDeliveryAllowListPermitsOnlyTransactionalAccountEmail(t *testing.T) {
	store := &fakeStore{messages: []Message{
		{ID: 1, Category: "email_verification", DedupeKey: "verify-1", Attempts: 1, MaxAttempts: 5},
		{ID: 2, Category: "password_reset", DedupeKey: "reset-1", Attempts: 1, MaxAttempts: 5},
		{ID: 3, Category: "favorite_alert", DedupeKey: "favorite-1", Attempts: 1, MaxAttempts: 5},
	}}
	transport := &fakeTransport{}
	worker, err := NewWorker(WorkerOptions{
		Store: store, Transport: transport, Channel: "email", BatchSize: 3, MaxConcurrent: 1,
		AllowedCategories: map[string]bool{"email_verification": true, "password_reset": true},
		Now:               func() time.Time { return time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if processed != 3 || len(store.sent) != 2 || store.sent[0] != 1 || store.sent[1] != 2 {
		t.Fatalf("processed=%d sent=%v", processed, store.sent)
	}
	if len(store.suppressed) != 1 || store.suppressed[0] != 3 {
		t.Fatalf("suppressed=%v", store.suppressed)
	}
}

func TestWorkerEmptyAllowListDisablesChannel(t *testing.T) {
	store := &fakeStore{messages: []Message{{ID: 9, Category: "email_verification", DedupeKey: "verify-9", Attempts: 1, MaxAttempts: 5}}}
	worker, err := NewWorker(WorkerOptions{Store: store, Transport: &fakeTransport{}, Channel: "in_app", AllowedCategories: map[string]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.sent) != 0 || len(store.suppressed) != 1 || store.suppressed[0] != 9 {
		t.Fatalf("sent=%v suppressed=%v", store.sent, store.suppressed)
	}
}

func TestWorkerSuppressesAfterServerSideAccountRecheck(t *testing.T) {
	store := &fakeStore{messages: []Message{{ID: 7, DedupeKey: "blocked", Attempts: 1, MaxAttempts: 5}}}
	transport := &fakeTransport{}
	worker, err := NewWorker(WorkerOptions{
		Store: store, Transport: transport, Channel: "email",
		AccountGuard: guardFunc(func(context.Context, Message) (GuardDecision, error) {
			return GuardDecision{Allowed: false, Reason: "account deleted"}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if len(store.suppressed) != 1 || len(store.sent) != 0 {
		t.Fatalf("suppressed=%v sent=%v", store.suppressed, store.sent)
	}
}

func TestWorkerSuppressesMessageWithoutStableDedupeKey(t *testing.T) {
	store := &fakeStore{messages: []Message{{ID: 17, DedupeKey: "  ", Attempts: 1, MaxAttempts: 5}}}
	transport := &fakeTransport{}
	worker, err := NewWorker(WorkerOptions{Store: store, Transport: transport, Channel: "email"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if len(store.suppressed) != 1 || len(store.sent) != 0 || transport.maxFlight.Load() != 0 {
		t.Fatalf("suppressed=%v sent=%v transport calls=%d", store.suppressed, store.sent, transport.maxFlight.Load())
	}
}

func TestWorkerRetriesTransportFailuresAndDeadLettersFinalAttempt(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{messages: []Message{{ID: 8, DedupeKey: "retry", Attempts: 5, MaxAttempts: 5}}}
	worker, err := NewWorker(WorkerOptions{
		Store: store, Transport: &fakeTransport{err: errors.New("SMTP unavailable")}, Channel: "email",
		BaseRetryDelay: time.Minute, MaxRetryDelay: time.Hour,
		Now: func() time.Time { return now }, Jitter: func(time.Duration) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = worker.RunOnce(context.Background())
	if err == nil || !strings.Contains(err.Error(), "dead-lettered") {
		t.Fatalf("RunOnce error=%v, want dead-letter error", err)
	}
	if len(store.retried) != 1 || store.retryAvailable != now.Add(16*time.Minute) {
		t.Fatalf("retried=%v available=%s", store.retried, store.retryAvailable)
	}
}

func TestWorkerReschedulesQuietHoursWithoutCallingTransport(t *testing.T) {
	now := time.Date(2026, 8, 5, 20, 0, 0, 0, time.UTC)
	deferUntil := now.Add(8 * time.Hour)
	store := &fakeStore{messages: []Message{{ID: 9, DedupeKey: "quiet", Attempts: 1, MaxAttempts: 5}}}
	transport := &fakeTransport{}
	worker, err := NewWorker(WorkerOptions{
		Store: store, Transport: transport, Channel: "email",
		PreferenceGuard: preferenceGuardFunc(func(context.Context, Message) (GuardDecision, error) {
			return GuardDecision{Allowed: true, Reason: "quiet hours", DeferUntil: deferUntil}, nil
		}),
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce returned error: %v", err)
	}
	if len(store.rescheduled) != 1 || store.rescheduleAt != deferUntil {
		t.Fatalf("rescheduled=%v available=%s, want message 9 at %s", store.rescheduled, store.rescheduleAt, deferUntil)
	}
	if len(store.sent) != 0 || transport.maxFlight.Load() != 0 {
		t.Fatalf("sent=%v transport calls=%d, want no delivery", store.sent, transport.maxFlight.Load())
	}
}

func TestClaimSQLUsesSkipLockedAndPerRowUUIDLease(t *testing.T) {
	for _, required := range []string{"FOR UPDATE SKIP LOCKED", "gen_random_uuid()", "lease_token::text", "status = 'sending'"} {
		if !strings.Contains(claimBatchSQL, required) {
			t.Fatalf("claim SQL missing %q", required)
		}
	}
}

func TestQuietHoursRescheduleIsFencedAndDoesNotConsumeAttempt(t *testing.T) {
	for _, required := range []string{
		"status = 'pending'",
		"attempts = GREATEST(attempts - 1, 0)",
		"lease_token = $2::uuid",
	} {
		if !strings.Contains(rescheduleSQL, required) {
			t.Fatalf("reschedule SQL missing %q", required)
		}
	}
}

package billingevents

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

type fakeReminderStore struct {
	days          []int
	subscriptions []ReminderSubscription
	intents       []QueuedReminderIntent
	now           time.Time
	queryDays     []int
	limit         int
}

func (f *fakeReminderStore) LoadReminderDays(context.Context) ([]int, error) {
	return f.days, nil
}

func (f *fakeReminderStore) ListReminderSubscriptions(_ context.Context, now time.Time, days []int, limit int) ([]ReminderSubscription, error) {
	f.now, f.queryDays, f.limit = now, append([]int(nil), days...), limit
	return f.subscriptions, nil
}

func (f *fakeReminderStore) EnqueueReminderIntents(_ context.Context, intents []QueuedReminderIntent) (int, error) {
	f.intents = append(f.intents, intents...)
	return len(intents), nil
}

func TestReminderServicePlansRenewalExpiryCancellationAndEndedIntents(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	renewal := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	expiry := time.Date(2026, 8, 8, 18, 0, 0, 0, time.UTC)
	ended := now.Add(-time.Hour)
	store := &fakeReminderStore{
		days: []int{3, 7, 3, 1},
		subscriptions: []ReminderSubscription{
			{ID: 1, UserID: 10, PlanKey: "pro", Status: "active", CurrentPeriodEnd: &renewal, Email: "one@example.com", EmailVerified: true},
			{ID: 2, UserID: 20, PlanKey: "pro", Status: "scheduled_to_cancel", CurrentPeriodEnd: &expiry, Email: "two@example.com", EmailVerified: true},
			{ID: 3, UserID: 30, PlanKey: "pro", Status: "expired", CurrentPeriodEnd: &ended},
		},
	}
	service, err := NewReminderService(store, ReminderOptions{BatchSize: 50}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := service.Queue(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 7 || len(store.intents) != 7 {
		t.Fatalf("inserted=%d intents=%d, want 7", inserted, len(store.intents))
	}
	if store.limit != 50 || !store.now.Equal(now) {
		t.Fatalf("query now=%s limit=%d", store.now, store.limit)
	}
	if len(store.queryDays) != 3 || store.queryDays[0] != 7 || store.queryDays[1] != 3 || store.queryDays[2] != 1 {
		t.Fatalf("query days=%v", store.queryDays)
	}

	categories := map[string]int{}
	keys := map[string]bool{}
	for _, intent := range store.intents {
		categories[intent.Category]++
		if keys[intent.DedupeKey] {
			t.Fatalf("duplicate key %q", intent.DedupeKey)
		}
		keys[intent.DedupeKey] = true
	}
	if categories["subscription_renewal_reminder"] != 2 ||
		categories["subscription_cancellation_scheduled"] != 2 ||
		categories["subscription_expiry_reminder"] != 2 ||
		categories["subscription_ended"] != 1 {
		t.Fatalf("categories=%v", categories)
	}
}

func TestReminderKeysAreStableAcrossRuns(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	periodEnd := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	subscriptions := []ReminderSubscription{{ID: 1, UserID: 10, PlanKey: "pro", Status: "active", CurrentPeriodEnd: &periodEnd}}
	first, err := planReminderIntents(subscriptions, []int{7, 3, 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := planReminderIntents(subscriptions, []int{7, 3, 1}, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 || first[0].DedupeKey != second[0].DedupeKey {
		t.Fatalf("keys=%v and %v, want stable single key", first, second)
	}
	if !strings.Contains(first[0].DedupeKey, ":renewal_reminder-7d:in_app") {
		t.Fatalf("unexpected reminder key %q", first[0].DedupeKey)
	}
}

func TestNormalizeReminderDaysUsesDefaultsAndRejectsInvalidValues(t *testing.T) {
	got, err := normalizeReminderDays(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != 7 || got[1] != 3 || got[2] != 1 {
		t.Fatalf("default days=%v", got)
	}
	if _, err := normalizeReminderDays([]int{7, 0, 1}); err == nil {
		t.Fatal("normalizeReminderDays returned nil error for zero offset")
	}
}

func TestReminderJobRejectsUnboundedPayload(t *testing.T) {
	service, err := NewReminderService(&fakeReminderStore{}, ReminderOptions{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	err = (ReminderJob{Service: service}).Run(context.Background(), jobs.Job{Payload: []byte(`{"limit":1001}`)})
	if err == nil {
		t.Fatal("ReminderJob returned nil error for an unbounded payload")
	}
}

package billingevents

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeStore struct{ tx *fakeTransaction }

func (f fakeStore) WithinTransaction(ctx context.Context, work func(TransactionStore) error) error {
	return work(f.tx)
}

type fakeTransaction struct {
	event        InboxEvent
	hasEvent     bool
	subscription Subscription
	hasSub       bool
	upserted     bool
	appended     bool
	synced       bool
	notified     bool
	disputed     bool
	processed    bool
	ignored      bool
	failed       bool
	failReason   string
}

func (f *fakeTransaction) ClaimNext(context.Context) (InboxEvent, bool, error) {
	return f.event, f.hasEvent, nil
}
func (f *fakeTransaction) LoadSubscriptionForUpdate(context.Context, InboxEvent, string) (Subscription, bool, error) {
	return f.subscription, f.hasSub, nil
}
func (f *fakeTransaction) UpsertSubscription(_ context.Context, _ InboxEvent, update SubscriptionUpdate) (Subscription, error) {
	f.upserted = true
	return Subscription{ID: 44, UserID: update.UserID, PlanKey: update.PlanKey, Status: update.Status, ProviderEventTime: update.EffectiveAt}, nil
}
func (f *fakeTransaction) AppendSubscriptionEvent(context.Context, InboxEvent, Subscription, SubscriptionUpdate, string) error {
	f.appended = true
	return nil
}
func (f *fakeTransaction) SyncEntitlements(context.Context, Subscription, SubscriptionUpdate, bool, time.Time) error {
	f.synced = true
	return nil
}
func (f *fakeTransaction) RecordDispute(context.Context, InboxEvent, Subscription, DisputeUpdate) error {
	f.disputed = true
	return nil
}
func (f *fakeTransaction) EnqueueNotification(context.Context, int64, NotificationIntent) error {
	f.notified = true
	return nil
}
func (f *fakeTransaction) MarkProcessed(context.Context, int64, time.Time) error {
	f.processed = true
	return nil
}
func (f *fakeTransaction) MarkIgnored(context.Context, int64, string, time.Time) error {
	f.ignored = true
	return nil
}
func (f *fakeTransaction) MarkFailed(_ context.Context, _ int64, reason string, _ time.Time) error {
	f.failed = true
	f.failReason = reason
	return nil
}

type fakeDecoder struct {
	update  SubscriptionUpdate
	err     error
	compare int
}

func (f fakeDecoder) Decode(InboxEvent) (SubscriptionUpdate, error) { return f.update, f.err }
func (f fakeDecoder) CompareRevision(string, string) int            { return f.compare }

func TestServiceAppliesSubscriptionGrantAndNotificationAtomically(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	tx := &fakeTransaction{hasEvent: true, event: InboxEvent{ID: 7, Provider: "sandbox", Environment: "test", SignatureVerified: true}}
	decoder := fakeDecoder{update: SubscriptionUpdate{
		UserID: 3, PlanKey: "pro_monthly", ProviderSubscriptionID: "sub-1",
		Status: "active", EffectiveAt: now, Revision: "1",
	}}
	service, err := NewService(fakeStore{tx: tx}, map[string]Decoder{"sandbox": decoder}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ProcessNext(context.Background())
	if err != nil {
		t.Fatalf("ProcessNext returned error: %v", err)
	}
	if !result.Applied || !tx.upserted || !tx.appended || !tx.synced || !tx.notified || !tx.processed {
		t.Fatalf("result=%+v transaction=%+v", result, tx)
	}
}

func TestServiceIgnoresOutOfOrderEventWithoutChangingEntitlements(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	tx := &fakeTransaction{
		hasEvent: true, event: InboxEvent{ID: 8, Provider: "sandbox", SignatureVerified: true},
		hasSub: true, subscription: Subscription{ID: 5, UserID: 3, Status: "active", ProviderRevision: "2", ProviderEventTime: now},
	}
	decoder := fakeDecoder{update: SubscriptionUpdate{
		UserID: 3, PlanKey: "pro_monthly", ProviderSubscriptionID: "sub-1",
		Status: "cancelled", EffectiveAt: now.Add(-time.Hour), Revision: "1",
	}}
	service, _ := NewService(fakeStore{tx: tx}, map[string]Decoder{"sandbox": decoder}, func() time.Time { return now })
	result, err := service.ProcessNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ignored || !tx.ignored || tx.upserted || tx.synced || tx.notified {
		t.Fatalf("result=%+v transaction=%+v", result, tx)
	}
}

func TestServiceRecordsDecoderFailureWithoutPartialState(t *testing.T) {
	tx := &fakeTransaction{hasEvent: true, event: InboxEvent{ID: 9, Provider: "sandbox", SignatureVerified: true}}
	service, _ := NewService(fakeStore{tx: tx}, map[string]Decoder{"sandbox": fakeDecoder{err: errors.New("bad payload")}}, time.Now)
	result, err := service.ProcessNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Failed || !tx.failed || tx.upserted || !strings.Contains(tx.failReason, "bad payload") {
		t.Fatalf("result=%+v transaction=%+v", result, tx)
	}
}

func TestChargebackSuspendsEntitlementAndRecordsDisputeForReview(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	tx := &fakeTransaction{hasEvent: true, event: InboxEvent{ID: 10, Provider: "sandbox", Environment: "test", SignatureVerified: true}}
	service, _ := NewService(fakeStore{tx: tx}, map[string]Decoder{"sandbox": fakeDecoder{update: SubscriptionUpdate{
		UserID: 3, PlanKey: "pro_monthly", ProviderSubscriptionID: "sub-1", Status: "suspended",
		EffectiveAt: now, Revision: "3", Dispute: &DisputeUpdate{ProviderDisputeID: "dp-1", Status: "open", Reason: "chargeback"},
	}}}, func() time.Time { return now })
	result, err := service.ProcessNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || !tx.disputed || !tx.synced || !tx.notified {
		t.Fatalf("chargeback result=%+v transaction=%+v", result, tx)
	}
}

func TestSQLClaimsOnlyVerifiedInboxRowsAndSerializesSubscriptionAggregate(t *testing.T) {
	for _, required := range []string{"signature_verified = true", "FOR UPDATE SKIP LOCKED", "status IN ('pending', 'failed')"} {
		if !strings.Contains(claimNextSQL, required) {
			t.Fatalf("claim SQL missing %q", required)
		}
	}
	if !strings.Contains(subscriptionLockSQL, "pg_advisory_xact_lock") {
		t.Fatal("subscription lock SQL does not use a transaction-scoped advisory lock")
	}
}

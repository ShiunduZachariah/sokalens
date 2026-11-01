package notifications

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// digestStore is a Store recording only what the digest exercises. It is
// separate from fakeStore because the digest cares about the backlog and the
// once-for-all-time key, which fakeStore stubs out.
type digestStore struct {
	mu sync.Mutex

	backlog    []Notification
	backlogErr error

	enqueued    []Notification
	enqueueErr  error
	onceKeys    map[string]bool
	digested    []string
	digestedErr error
}

func newDigestStore() *digestStore { return &digestStore{onceKeys: map[string]bool{}} }

func (s *digestStore) Enqueue(context.Context, Notification) (EnqueueResult, error) {
	return EnqueueResult{}, errors.New("digest must use EnqueueOnce")
}

func (s *digestStore) EnqueueOnce(_ context.Context, n Notification) (EnqueueResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enqueueErr != nil {
		return EnqueueResult{}, s.enqueueErr
	}
	if s.onceKeys[n.DedupKey] {
		return EnqueueResult{ID: n.DedupKey, Created: false}, nil
	}
	s.onceKeys[n.DedupKey] = true
	s.enqueued = append(s.enqueued, n)
	return EnqueueResult{ID: n.DedupKey, Created: true, Tier: n.Tier}, nil
}

func (s *digestStore) Touch(context.Context, string, time.Time) error { return nil }

func (s *digestStore) Claim(context.Context, int, time.Duration, time.Time) ([]Notification, error) {
	return nil, nil
}

func (s *digestStore) MarkDelivered(context.Context, string, string, string, time.Time) error {
	return nil
}
func (s *digestStore) MarkRetry(context.Context, string, string, time.Time, time.Time) error {
	return nil
}
func (s *digestStore) MarkTerminal(context.Context, string, string, string, time.Time) error {
	return nil
}

func (s *digestStore) DigestBacklog(_ context.Context, _, _ time.Time, _ int) ([]Notification, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backlogErr != nil {
		return nil, s.backlogErr
	}
	return append([]Notification(nil), s.backlog...), nil
}

func (s *digestStore) MarkDigested(_ context.Context, ids []string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.digestedErr != nil {
		return s.digestedErr
	}
	s.digested = append(s.digested, ids...)
	return nil
}

func (s *digestStore) sent() []Notification {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Notification(nil), s.enqueued...)
}

func (s *digestStore) closed() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.digested...)
}

// staticSummary is a SummarySource returning fixed sections.
type staticSummary []Section

func (s staticSummary) DigestSummary(context.Context) []Section { return []Section(s) }

func testLocation(t *testing.T) *time.Location {
	t.Helper()
	// Fixed offset rather than a tzdata lookup: the test must not depend on
	// the host carrying the zone database.
	return time.FixedZone("EAT", 3*60*60)
}

func newTestDigest(t *testing.T, store Store, source SummarySource, loc *time.Location) *Digest {
	t.Helper()
	d, err := NewDigest(store, source, nil, DigestConfig{Hour: 8, Location: loc})
	if err != nil {
		t.Fatalf("NewDigest: %v", err)
	}
	return d
}

func TestDigestEnqueuesOnceADay(t *testing.T) {
	loc := testLocation(t)
	store := newDigestStore()
	digest := newTestDigest(t, store, staticSummary{{Label: "Health", Value: "🟢 Healthy"}}, loc)

	morning := time.Date(2026, 9, 10, 8, 0, 0, 0, loc)
	for i := 0; i < 3; i++ {
		if err := digest.RunOnce(context.Background(), morning.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
	}
	if got := len(store.sent()); got != 1 {
		t.Fatalf("three runs on one day produced %d digests, want 1", got)
	}

	// The next day is a different message, not a suppressed duplicate.
	if err := digest.RunOnce(context.Background(), morning.AddDate(0, 0, 1)); err != nil {
		t.Fatalf("RunOnce next day: %v", err)
	}
	sent := store.sent()
	if len(sent) != 2 {
		t.Fatalf("the following day produced %d digests in total, want 2", len(sent))
	}
	if sent[0].DedupKey == sent[1].DedupKey || sent[0].Fingerprint == sent[1].Fingerprint {
		t.Fatalf("two days share an identity: %q / %q", sent[0].DedupKey, sent[1].DedupKey)
	}
	if !strings.Contains(sent[0].DedupKey, "2026-09-10") || !strings.Contains(sent[1].DedupKey, "2026-09-11") {
		t.Fatalf("dedup keys are not the local date: %q %q", sent[0].DedupKey, sent[1].DedupKey)
	}
}

// TestDigestIsSendable guards the one property that would silently break it: an
// INFO digest could never leave the outbox (migration 000060 CHECK).
func TestDigestIsSendable(t *testing.T) {
	loc := testLocation(t)
	store := newDigestStore()
	digest := newTestDigest(t, store, nil, loc)

	if err := digest.RunOnce(context.Background(), time.Date(2026, 9, 10, 8, 0, 0, 0, loc)); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	sent := store.sent()
	if len(sent) != 1 {
		t.Fatalf("want one digest, got %d", len(sent))
	}
	if !sent[0].Tier.Immediate() {
		t.Fatalf("digest tier %q is never sent", string(sent[0].Tier))
	}
	if _, err := sent[0].Normalize(time.Now()); err != nil {
		t.Fatalf("digest would be rejected by the outbox: %v", err)
	}
}

func TestDigestClosesBacklogOnlyAfterItIsEnqueued(t *testing.T) {
	loc := testLocation(t)
	store := newDigestStore()
	store.backlog = []Notification{
		{ID: "11111111-1111-1111-1111-111111111111", Tier: TierInfo, Subject: "ingestion lag", CreatedAt: time.Date(2026, 9, 10, 3, 12, 0, 0, loc)},
	}
	store.enqueueErr = errors.New("database down")
	digest := newTestDigest(t, store, nil, loc)

	if err := digest.RunOnce(context.Background(), time.Date(2026, 9, 10, 8, 0, 0, 0, loc)); err == nil {
		t.Fatal("a failed enqueue must be reported")
	}
	if got := store.closed(); len(got) != 0 {
		t.Fatalf("INFO rows were closed without a digest carrying them: %v", got)
	}

	store.mu.Lock()
	store.enqueueErr = nil
	store.mu.Unlock()
	if err := digest.RunOnce(context.Background(), time.Date(2026, 9, 10, 8, 5, 0, 0, loc)); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if got := store.closed(); len(got) != 1 || got[0] != store.backlog[0].ID {
		t.Fatalf("backlog not closed after delivery: %v", got)
	}
}

// TestDigestRendersUnavailableRatherThanOmitting is plan T2 item 4 verbatim:
// a missing source renders "unavailable"; an omitted line reads as "fine".
func TestDigestRendersUnavailableRatherThanOmitting(t *testing.T) {
	loc := testLocation(t)
	store := newDigestStore()
	store.backlogErr = errors.New("select failed")
	digest := newTestDigest(t, store, staticSummary{{Label: "Queue", Value: ""}}, loc)

	if err := digest.RunOnce(context.Background(), time.Date(2026, 9, 10, 8, 0, 0, 0, loc)); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	sent := store.sent()
	if len(sent) != 1 {
		t.Fatalf("an unreadable backlog must not cancel the digest, got %d", len(sent))
	}
	body := sent[0].Body
	if !strings.Contains(body, "Queue") || !strings.Contains(body, DigestUnavailable) {
		t.Fatalf("blank and failed sources are not marked unavailable:\n%s", body)
	}
	if strings.Count(body, DigestUnavailable) < 2 {
		t.Fatalf("want both the blank section and the backlog marked unavailable:\n%s", body)
	}
}

func TestDigestBodyListsBacklogAndCapsIt(t *testing.T) {
	loc := testLocation(t)
	backlog := make([]Notification, 0, digestBacklogCap+3)
	for i := 0; i < digestBacklogCap+3; i++ {
		backlog = append(backlog, Notification{
			ID:              "id",
			Subject:         "prediction coverage dipped",
			OccurrenceCount: int64(i%2 + 1),
			CreatedAt:       time.Date(2026, 9, 10, 1, i, 0, 0, loc),
		})
	}
	body := RenderDigestBody(nil, backlog, nil, time.Date(2026, 9, 9, 8, 0, 0, 0, loc), time.Date(2026, 9, 10, 8, 0, 0, 0, loc), loc)

	if !strings.Contains(body, "INFO notifications (13)") {
		t.Fatalf("backlog count missing:\n%s", body)
	}
	if !strings.Contains(body, "+3 more") {
		t.Fatalf("overflow is not counted:\n%s", body)
	}
	if got := strings.Count(body, "prediction coverage dipped"); got != digestBacklogCap {
		t.Fatalf("printed %d backlog lines, want the cap of %d", got, digestBacklogCap)
	}
	if !strings.Contains(body, "Summary") || !strings.Contains(body, DigestUnavailable) {
		t.Fatalf("a nil summary source must still render a line:\n%s", body)
	}
}

func TestDigestNextFireIsTomorrowOnceTodayHasPassed(t *testing.T) {
	loc := testLocation(t)
	digest := newTestDigest(t, newDigestStore(), nil, loc)

	before := time.Date(2026, 9, 10, 7, 59, 0, 0, loc)
	if got := digest.nextFire(before); !got.Equal(time.Date(2026, 9, 10, 8, 0, 0, 0, loc)) {
		t.Fatalf("nextFire(%s) = %s, want today at 08:00", before, got)
	}
	after := time.Date(2026, 9, 10, 8, 0, 1, 0, loc)
	if got := digest.nextFire(after); !got.Equal(time.Date(2026, 9, 11, 8, 0, 0, 0, loc)) {
		t.Fatalf("nextFire(%s) = %s, want tomorrow at 08:00", after, got)
	}
	// Exactly on the hour must not schedule a second send for the same day.
	onTime := time.Date(2026, 9, 10, 8, 0, 0, 0, loc)
	if got := digest.nextFire(onTime); !got.Equal(time.Date(2026, 9, 11, 8, 0, 0, 0, loc)) {
		t.Fatalf("nextFire(%s) = %s, want tomorrow at 08:00", onTime, got)
	}
}

// TestDigestCatchesUpAfterARestart covers the failure the operator actually
// reported: the controller was not running at 08:00, so nothing arrived. A
// restart at 09:00 still owes the day's digest.
func TestDigestCatchesUpAfterARestart(t *testing.T) {
	loc := testLocation(t)
	store := newDigestStore()
	digest, err := NewDigest(store, nil, nil, DigestConfig{
		Hour:     8,
		Location: loc,
		Now:      func() time.Time { return time.Date(2026, 9, 10, 9, 30, 0, 0, loc) },
	})
	if err != nil {
		t.Fatalf("NewDigest: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- digest.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for len(store.sent()) == 0 {
		select {
		case <-deadline:
			cancel()
			t.Fatal("a restart after the send time produced no catch-up digest")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop on cancellation")
	}
	if got := len(store.sent()); got != 1 {
		t.Fatalf("catch-up sent %d digests, want exactly 1", got)
	}
}

package notifications

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeStore records outcomes so tests assert on what was durably recorded
// rather than on logs.
type fakeStore struct {
	mu sync.Mutex

	queue []Notification

	delivered []deliveredRow
	retried   []string
	terminal  []terminalRow
	onceKeys  []string
}

type deliveredRow struct {
	ID        string
	Transport string
	Err       string
}

type terminalRow struct {
	ID     string
	Status string
	Err    string
}

func (f *fakeStore) Enqueue(context.Context, Notification) (EnqueueResult, error) {
	return EnqueueResult{ID: "n1", Created: true}, nil
}

func (f *fakeStore) EnqueueOnce(_ context.Context, n Notification) (EnqueueResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.onceKeys {
		if k == n.DedupKey {
			return EnqueueResult{Created: false}, nil
		}
	}
	f.onceKeys = append(f.onceKeys, n.DedupKey)
	return EnqueueResult{ID: n.DedupKey, Created: true}, nil
}

func (f *fakeStore) Touch(context.Context, string, time.Time) error { return nil }

func (f *fakeStore) Claim(_ context.Context, limit int, _ time.Duration, _ time.Time) ([]Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queue) == 0 {
		return nil, nil
	}
	if limit > len(f.queue) {
		limit = len(f.queue)
	}
	out := f.queue[:limit]
	f.queue = f.queue[limit:]
	return out, nil
}

func (f *fakeStore) MarkDelivered(_ context.Context, id, transport, deliveryErr string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delivered = append(f.delivered, deliveredRow{ID: id, Transport: transport, Err: deliveryErr})
	return nil
}

func (f *fakeStore) MarkRetry(_ context.Context, id, _ string, _, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retried = append(f.retried, id)
	return nil
}

func (f *fakeStore) MarkTerminal(_ context.Context, id, status, deliveryErr string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminal = append(f.terminal, terminalRow{ID: id, Status: status, Err: deliveryErr})
	return nil
}

func (f *fakeStore) DigestBacklog(context.Context, time.Time, time.Time, int) ([]Notification, error) {
	return nil, nil
}

func (f *fakeStore) MarkDigested(context.Context, []string, time.Time) error { return nil }

func (f *fakeStore) snapshot() ([]deliveredRow, []string, []terminalRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]deliveredRow(nil), f.delivered...),
		append([]string(nil), f.retried...),
		append([]terminalRow(nil), f.terminal...)
}

// fakeTransport records what it was asked to send and can be made to fail.
type fakeTransport struct {
	name string
	err  error

	mu   sync.Mutex
	sent []Notification
}

func (t *fakeTransport) Name() string { return t.name }

func (t *fakeTransport) Deliver(_ context.Context, n Notification) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return t.err
	}
	t.sent = append(t.sent, n)
	return nil
}

func (t *fakeTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.sent)
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testWorker(t *testing.T, store Store, primary, fallback Transport) *Worker {
	t.Helper()
	w, err := NewWorker(store, primary, fallback, nil, quietLogger(), WorkerConfig{
		Workers: 1, ClaimBatch: 64, QueueCapacity: 256,
		Now: func() time.Time { return time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	return w
}

// ---------------------------------------------------------------------------
// Tier routing, spec §12
// ---------------------------------------------------------------------------

// TestInfoIsNeverSentImmediately — INFO belongs to the daily digest. Sending it
// immediately is how an ops channel becomes noise nobody reads, which is worse
// than silence because it hides the messages that matter.
func TestInfoIsNeverSentImmediately(t *testing.T) {
	if TierInfo.Immediate() {
		t.Fatal("INFO is marked immediate")
	}
	if !TierImportant.Immediate() || !TierActionRequired.Immediate() {
		t.Fatal("IMPORTANT and ACTION REQUIRED must both be immediate")
	}
}

// TestActionRequiredIsPrefixed — the prefix is what makes an actionable
// message findable in a scrollback full of routine ones.
func TestActionRequiredIsPrefixed(t *testing.T) {
	body := RenderTelegram(Notification{Tier: TierActionRequired, Subject: "Backups failing"})
	if !strings.Contains(body, ActionRequiredPrefix) {
		t.Fatalf("ACTION REQUIRED was not prefixed:\n%s", body)
	}
	routine := RenderTelegram(Notification{Tier: TierImportant, Subject: "Latency high"})
	if strings.Contains(routine, ActionRequiredPrefix) {
		t.Fatalf("IMPORTANT wrongly carried the ACTION REQUIRED prefix:\n%s", routine)
	}
}

// TestTierMaxRaisesNeverLowers — when correlated observations merge onto one
// row, the row must end at the highest severity seen. Lowering it would hide
// an escalation behind an earlier, calmer message.
func TestTierMaxRaisesNeverLowers(t *testing.T) {
	cases := []struct{ a, b, want Tier }{
		{TierInfo, TierImportant, TierImportant},
		{TierImportant, TierInfo, TierImportant},
		{TierImportant, TierActionRequired, TierActionRequired},
		{TierActionRequired, TierInfo, TierActionRequired},
		{TierInfo, TierInfo, TierInfo},
	}
	for _, tc := range cases {
		if got := tc.a.Max(tc.b); got != tc.want {
			t.Fatalf("%s.Max(%s) = %s, want %s", tc.a, tc.b, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Delivery and fallback, spec §12
// ---------------------------------------------------------------------------

// TestTelegramFailureFallsBackToEmail is the guarantee the whole phase rests
// on: a Telegram send failure must never lose a notification. It is also why
// an email transport had to be built rather than deferred — without a second
// transport there is nothing to fall back to.
func TestTelegramFailureFallsBackToEmail(t *testing.T) {
	store := &fakeStore{queue: []Notification{
		{ID: "n1", Tier: TierActionRequired, Subject: "Disk full", Fingerprint: "fp1", Attempts: 1},
	}}
	telegramT := &fakeTransport{name: "telegram", err: errors.New("telegram unreachable")}
	emailT := &fakeTransport{name: "email"}

	w := testWorker(t, store, telegramT, emailT)
	if err := w.DrainOnce(context.Background()); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}

	if emailT.count() != 1 {
		t.Fatalf("email transport delivered %d, want 1", emailT.count())
	}
	delivered, retried, terminal := store.snapshot()
	if len(delivered) != 1 {
		t.Fatalf("delivered rows = %d, want 1 (retried=%v terminal=%v)", len(delivered), retried, terminal)
	}
	if delivered[0].Transport != "email" {
		t.Fatalf("recorded transport = %q, want email", delivered[0].Transport)
	}
	// The Telegram failure must survive on the row: a message delivered by
	// email after Telegram failed still has to show why.
	if delivered[0].Err == "" {
		t.Fatal("delivery_error was not recorded for the failed telegram attempt")
	}
}

// TestBothTransportsFailingRetainsTheNotification — when everything fails the
// notification is retried or durably failed, never silently dropped.
func TestBothTransportsFailingRetainsTheNotification(t *testing.T) {
	store := &fakeStore{queue: []Notification{
		{ID: "n1", Tier: TierImportant, Subject: "Everything is on fire", Fingerprint: "fp1", Attempts: 1},
	}}
	w := testWorker(t, store,
		&fakeTransport{name: "telegram", err: errors.New("down")},
		&fakeTransport{name: "email", err: errors.New("also down")})

	if err := w.DrainOnce(context.Background()); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}

	delivered, retried, terminal := store.snapshot()
	if len(delivered) != 0 {
		t.Fatalf("nothing should be marked delivered, got %+v", delivered)
	}
	if len(retried)+len(terminal) == 0 {
		t.Fatal("the notification was neither retried nor durably failed — it was lost")
	}
}

// TestSuccessfulTelegramNeverTouchesEmail — the fallback is a fallback, not a
// second copy. Sending both would double every alert.
func TestSuccessfulTelegramNeverTouchesEmail(t *testing.T) {
	store := &fakeStore{queue: []Notification{
		{ID: "n1", Tier: TierImportant, Subject: "Latency high", Fingerprint: "fp1", Attempts: 1},
	}}
	telegramT := &fakeTransport{name: "telegram"}
	emailT := &fakeTransport{name: "email"}
	w := testWorker(t, store, telegramT, emailT)

	if err := w.DrainOnce(context.Background()); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if telegramT.count() != 1 {
		t.Fatalf("telegram delivered %d, want 1", telegramT.count())
	}
	if emailT.count() != 0 {
		t.Fatalf("email delivered %d on a successful telegram send, want 0", emailT.count())
	}
}

// ---------------------------------------------------------------------------
// Suppression, spec §11.4
// ---------------------------------------------------------------------------

// TestSuppressionCollapsesTheRemainder — an alert storm must produce a queue
// and one notice, not hundreds of pushes.
func TestSuppressionCollapsesTheRemainder(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	current := now
	s := newSuppressor(20, time.Minute, func() time.Time { return current })

	admitted := 0
	for i := 0; i < 60; i++ {
		if s.Admit(Notification{Tier: TierImportant, Subject: "noise", Fingerprint: "fp"}) {
			admitted++
		}
	}
	if admitted > 20 {
		t.Fatalf("admitted %d IMPORTANT notifications, want at most the 20 cap", admitted)
	}
	// The notice is emitted when the window closes, not mid-window: the count
	// is not final until then. Advance past it.
	current = now.Add(2 * time.Minute)
	count, _, ok := s.takeNotice()
	if !ok || count == 0 {
		t.Fatalf("no suppression notice was produced (count=%d ok=%v)", count, ok)
	}
	if admitted+count != 60 {
		t.Fatalf("admitted %d + suppressed %d = %d, want 60 accounted for", admitted, count, admitted+count)
	}
}

// TestActionRequiredIsNeverSuppressed — the cap exists to protect against
// noise, and an action-required notification is by definition not noise.
func TestActionRequiredIsNeverSuppressed(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	s := newSuppressor(20, time.Minute, func() time.Time { return now })

	// Exhaust the window with routine traffic first.
	for i := 0; i < 100; i++ {
		s.Admit(Notification{Tier: TierImportant, Subject: "noise", Fingerprint: "fp"})
	}
	for i := 0; i < 10; i++ {
		if !s.Admit(Notification{Tier: TierActionRequired, Subject: "act now", Fingerprint: "urgent"}) {
			t.Fatal("an ACTION REQUIRED notification was suppressed")
		}
	}
}

// TestSuppressionWindowRolls — suppression is a rate limit, not a permanent
// mute. A new window must admit traffic again.
func TestSuppressionWindowRolls(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	current := now
	s := newSuppressor(20, time.Minute, func() time.Time { return current })

	for i := 0; i < 40; i++ {
		s.Admit(Notification{Tier: TierImportant, Subject: "noise", Fingerprint: "fp"})
	}
	current = now.Add(2 * time.Minute)
	if !s.Admit(Notification{Tier: TierImportant, Subject: "after the window", Fingerprint: "fp"}) {
		t.Fatal("the suppressor stayed closed after its window rolled")
	}
}

// TestSuppressionNoticeIsNotItselfSuppressed would otherwise be the bug where
// a storm silences the very message announcing the storm.
func TestSuppressionNoticeIsNotItselfSuppressed(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	s := newSuppressor(1, time.Minute, func() time.Time { return now })

	s.Admit(Notification{Tier: TierImportant, Subject: "first", Fingerprint: "fp"})
	notice := Notification{
		Tier:        TierImportant,
		Subject:     "further notifications suppressed",
		Fingerprint: suppressionKeyPrefix + "window",
		DedupKey:    suppressionKeyPrefix + "window",
	}
	if !isSuppressionNotice(notice) {
		t.Fatal("the suppression notice is not recognisable as one")
	}
	if !s.Admit(notice) {
		t.Fatal("the suppression notice was itself suppressed")
	}
}

// ---------------------------------------------------------------------------
// Bounds
// ---------------------------------------------------------------------------

// TestWorkerIsBounded — a fixed pool means an alert storm grows the queue, not
// memory. "Bounded everything" is a design principle, not an aspiration.
func TestWorkerIsBounded(t *testing.T) {
	queued := make([]Notification, 0, 200)
	for i := 0; i < 200; i++ {
		queued = append(queued, Notification{
			ID: "n", Tier: TierImportant, Subject: "burst", Fingerprint: "fp", Attempts: 1,
		})
	}
	store := &fakeStore{queue: queued}
	w := testWorker(t, store, &fakeTransport{name: "telegram"}, &fakeTransport{name: "email"})

	if err := w.DrainOnce(context.Background()); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if peak := w.PeakConcurrency(); peak > 1 {
		t.Fatalf("peak concurrency = %d, want at most 1 for a single worker", peak)
	}
}

// TestNormalizeBoundsUntrustedText — a notification body can come from an
// incident summary, which can be long and can carry control characters.
func TestNormalizeBoundsUntrustedText(t *testing.T) {
	now := time.Now()
	huge := strings.Repeat("x", 100_000)
	n, err := Notification{
		Tier: TierImportant, Subject: "a\n\rb\t" + huge, Body: huge, Fingerprint: "fp",
	}.Normalize(now)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(n.Subject) > MaxSubjectChars*4 {
		t.Fatalf("subject was not bounded: %d bytes", len(n.Subject))
	}
	if len(n.Body) > MaxBodyChars*4 {
		t.Fatalf("body was not bounded: %d bytes", len(n.Body))
	}
	if strings.ContainsAny(n.Subject, "\n\r") {
		t.Fatalf("control characters survived in the subject: %q", n.Subject)
	}
}

// TestNormalizeRejectsAMissingFingerprint — the fingerprint is the key the
// open-row dedup index is built on. Without it, correlated alerts stop
// collapsing and one incident becomes four messages at 3am.
func TestNormalizeRejectsAMissingFingerprint(t *testing.T) {
	if _, err := (Notification{Tier: TierImportant, Subject: "x"}).Normalize(time.Now()); err == nil {
		t.Fatal("a notification with no fingerprint was accepted")
	}
}

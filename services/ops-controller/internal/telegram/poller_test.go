package telegram

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

// --- fakes ------------------------------------------------------------------

type recordingSender struct {
	mu   sync.Mutex
	sent []string
}

func (s *recordingSender) Send(_ context.Context, _ int64, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, body)
	return nil
}

func (s *recordingSender) bodies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

type stubStore struct {
	mu        sync.Mutex
	offset    int64
	recorded  map[int64]bool
	completed map[int64]error
}

func newStubStore() *stubStore {
	return &stubStore{recorded: map[int64]bool{}, completed: map[int64]error{}}
}

func (s *stubStore) LoadOffset(context.Context) (int64, error) { return s.offset, nil }
func (s *stubStore) SaveOffset(_ context.Context, o int64) error {
	s.offset = o
	return nil
}

func (s *stubStore) RecordUpdate(_ context.Context, rec UpdateRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recorded[rec.UpdateID] {
		return ErrDuplicateUpdate
	}
	s.recorded[rec.UpdateID] = true
	return nil
}

func (s *stubStore) CompleteUpdate(_ context.Context, id int64, handlerErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed[id] = handlerErr
	return nil
}

func (s *stubStore) CountRecentUnauthorized(context.Context, time.Time) (int, error) { return 0, nil }
func (s *stubStore) AcquireLock(context.Context) (bool, error)                       { return true, nil }
func (s *stubStore) RenewLock(context.Context) error                                 { return nil }
func (s *stubStore) ReleaseLock(context.Context) error                               { return nil }

type allowAll struct{}

func (allowAll) Allowed(Update) bool { return true }

type dispatchFunc func(context.Context, Request) (Response, error)

func (f dispatchFunc) Dispatch(ctx context.Context, r Request) (Response, error) { return f(ctx, r) }

type nopClient struct{}

func (nopClient) GetUpdates(context.Context, int64, int, time.Duration) ([]Update, error) {
	return nil, nil
}
func (nopClient) SendMessage(context.Context, int64, string) error { return nil }
func (nopClient) DeleteWebhook(context.Context) error              { return nil }
func (nopClient) GetMe(context.Context) (User, error)              { return User{}, nil }

const testChatID = int64(-100123)

func newTestPoller(t *testing.T, store Store, sender Sender, dispatch dispatchFunc) *Poller {
	t.Helper()
	p, err := NewPoller(PollerOptions{
		Client:      nopClient{},
		Store:       store,
		Auth:        allowAll{},
		Dispatcher:  dispatch,
		Sender:      sender,
		Metrics:     discardMetrics{},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		ChatID:      testChatID,
		PollTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}
	return p
}

func commandUpdate(id int64, text string) Update {
	return Update{
		UpdateID: id,
		Message: &Message{
			MessageID: id,
			From:      &User{ID: 42, Username: "operator"},
			Chat:      Chat{ID: testChatID, Type: "supergroup"},
			Text:      text,
		},
	}
}

// --- tests ------------------------------------------------------------------

// TestFailingCommandStillAnswers is the regression for the bug that made
// /deploys look dead in production.
//
// Router.Dispatch returns an empty Response alongside an error when a handler
// errors or panics. The poller used to skip the send in that case, so a broken
// command produced total silence — indistinguishable from a dead bot, an
// unauthorized sender, or a message that never arrived. Those three call for
// completely different responses from the operator, so silence is the worst
// possible answer.
func TestFailingCommandStillAnswers(t *testing.T) {
	cases := map[string]error{
		"handler error": errors.New("deployment store exploded"),
		"handler panic": ErrHandlerPanic,
	}
	for name, handlerErr := range cases {
		t.Run(name, func(t *testing.T) {
			sender := &recordingSender{}
			store := newStubStore()
			p := newTestPoller(t, store, sender, func(context.Context, Request) (Response, error) {
				// Exactly what Router.Dispatch returns on failure.
				return Response{}, handlerErr
			})

			if err := p.handle(context.Background(), commandUpdate(1, "/deploys")); !errors.Is(err, handlerErr) {
				t.Fatalf("handle returned %v, want the handler error", err)
			}

			sent := sender.bodies()
			if len(sent) != 1 {
				t.Fatalf("sent %d replies, want exactly 1 — a failing command must not be silent", len(sent))
			}
			if !strings.Contains(sent[0], "deploys") {
				t.Fatalf("reply does not name the command: %q", sent[0])
			}
		})
	}
}

// TestFailureReplyLeaksNothing: an error string can carry a query, a
// connection string or a token. The reply names the command and stops there.
func TestFailureReplyLeaksNothing(t *testing.T) {
	secret := "postgres://ops:hunter2@db.internal:5432/sokalens"
	sender := &recordingSender{}
	p := newTestPoller(t, newStubStore(), sender, func(context.Context, Request) (Response, error) {
		return Response{}, errors.New("query failed: " + secret)
	})

	_ = p.handle(context.Background(), commandUpdate(2, "/deploys"))

	sent := sender.bodies()
	if len(sent) != 1 {
		t.Fatalf("sent %d replies, want 1", len(sent))
	}
	for _, leak := range []string{secret, "hunter2", "postgres://", "query failed"} {
		if strings.Contains(sent[0], leak) {
			t.Fatalf("failure reply leaked %q: %s", leak, sent[0])
		}
	}
}

// TestSuccessfulCommandRepliesOnce guards against the fix double-sending.
func TestSuccessfulCommandRepliesOnce(t *testing.T) {
	sender := &recordingSender{}
	p := newTestPoller(t, newStubStore(), sender, func(context.Context, Request) (Response, error) {
		return Response{Body: "all good"}, nil
	})
	if err := p.handle(context.Background(), commandUpdate(3, "/status")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if got := sender.bodies(); len(got) != 1 || got[0] != "all good" {
		t.Fatalf("sent %v, want exactly one \"all good\"", got)
	}
}

// TestSilentResponseStaysSilent: plain text in an ops channel must not draw a
// reply, and that must survive the failure-path change.
func TestSilentResponseStaysSilent(t *testing.T) {
	sender := &recordingSender{}
	p := newTestPoller(t, newStubStore(), sender, func(context.Context, Request) (Response, error) {
		return Response{Silent: true}, nil
	})
	if err := p.handle(context.Background(), commandUpdate(4, "just chatting")); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if got := sender.bodies(); len(got) != 0 {
		t.Fatalf("sent %v, want nothing for a non-command", got)
	}
}

// TestDuplicateUpdateStaysSilentAndDoesNotDispatch keeps the §7.4 replay
// defence intact: a redelivered update must not re-run the command, and must
// not answer twice either.
func TestDuplicateUpdateStaysSilentAndDoesNotDispatch(t *testing.T) {
	sender := &recordingSender{}
	store := newStubStore()
	var dispatched int
	p := newTestPoller(t, store, sender, func(context.Context, Request) (Response, error) {
		dispatched++
		return Response{Body: "ok"}, nil
	})

	update := commandUpdate(5, "/status")
	if err := p.handle(context.Background(), update); err != nil {
		t.Fatalf("first handle: %v", err)
	}
	if err := p.handle(context.Background(), update); err != nil {
		t.Fatalf("replay returned %v, want nil (a duplicate is a no-op)", err)
	}
	if dispatched != 1 {
		t.Fatalf("dispatched %d times, want 1", dispatched)
	}
	if got := sender.bodies(); len(got) != 1 {
		t.Fatalf("sent %d replies for a replayed update, want 1", len(got))
	}
}

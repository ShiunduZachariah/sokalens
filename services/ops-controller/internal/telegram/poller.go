package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Dispatcher executes one authorized update. The router satisfies this.
type Dispatcher interface {
	Dispatch(ctx context.Context, req Request) (Response, error)
}

// Authorizer decides whether an update may be acted on. Both the chat and the
// sender must match (spec §8).
type Authorizer interface {
	Allowed(u Update) bool
}

// Sender delivers a rendered reply. Chunking happens inside the implementation.
type Sender interface {
	Send(ctx context.Context, chatID int64, body string) error
}

// PollerOptions carries the poller's collaborators and tunables.
type PollerOptions struct {
	Client      Client
	Store       Store
	Auth        Authorizer
	Dispatcher  Dispatcher
	Sender      Sender
	Metrics     Metrics
	Logger      *slog.Logger
	ChatID      int64
	PollTimeout time.Duration

	// LockRenewInterval controls how often the advisory lock is refreshed
	// while polling. Zero selects a sane default.
	LockRenewInterval time.Duration
	// UnauthorizedWindow and UnauthorizedThreshold implement the one-shot
	// "5 attempts in 10 minutes" notification from spec §8.
	UnauthorizedWindow    time.Duration
	UnauthorizedThreshold int
}

// Poller owns the getUpdates loop and the offset lifecycle.
//
// The loop's ordering is the correctness-critical part of the whole bridge.
// See handle() for why the writes happen in the order they do.
type Poller struct {
	opts    PollerOptions
	backoff *Backoff

	// lastUnauthorizedNotice suppresses repeat notifications so an attacker
	// cannot turn the alert itself into the flood (spec §8: once, not per
	// attempt).
	lastUnauthorizedNotice time.Time
}

const (
	defaultLockRenewInterval     = 30 * time.Second
	defaultUnauthorizedWindow    = 10 * time.Minute
	defaultUnauthorizedThreshold = 5
	// getUpdates accepts 1..100.
	updateBatchLimit = 100
	// Bound what the forensic ledger stores from an untrusted message.
	maxStoredTextLen = 4096
)

// NewPoller validates the options and returns a ready poller.
func NewPoller(opts PollerOptions) (*Poller, error) {
	switch {
	case opts.Client == nil:
		return nil, errors.New("telegram: poller requires a client")
	case opts.Store == nil:
		return nil, errors.New("telegram: poller requires a store")
	case opts.Auth == nil:
		return nil, errors.New("telegram: poller requires an authorizer")
	case opts.Dispatcher == nil:
		return nil, errors.New("telegram: poller requires a dispatcher")
	case opts.Sender == nil:
		return nil, errors.New("telegram: poller requires a sender")
	case opts.Metrics == nil:
		return nil, errors.New("telegram: poller requires metrics")
	case opts.ChatID == 0:
		return nil, errors.New("telegram: poller requires a chat id")
	}
	if opts.PollTimeout <= 0 || opts.PollTimeout > 50*time.Second {
		return nil, errors.New("telegram: poll timeout must be between 1s and 50s")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.LockRenewInterval <= 0 {
		opts.LockRenewInterval = defaultLockRenewInterval
	}
	if opts.UnauthorizedWindow <= 0 {
		opts.UnauthorizedWindow = defaultUnauthorizedWindow
	}
	if opts.UnauthorizedThreshold <= 0 {
		opts.UnauthorizedThreshold = defaultUnauthorizedThreshold
	}
	return &Poller{opts: opts, backoff: NewBackoff(time.Second, 5*time.Minute)}, nil
}

// Run polls until the context is cancelled or a fatal condition occurs.
//
// Fatal conditions deliberately return rather than retry: a 409 means another
// consumer is polling the same token, and both consumers keep evicting each
// other so neither progresses. Retrying that in a loop is how the failure
// becomes invisible (spec §7.2).
func (p *Poller) Run(ctx context.Context) error {
	held, err := p.opts.Store.AcquireLock(ctx)
	if err != nil {
		return fmt.Errorf("acquire poller lock: %w", err)
	}
	if !held {
		// Not an error. Every instance may still send; only the lock holder
		// polls. This is what keeps a second instance from 409-ing the bridge.
		p.opts.Logger.Info("telegram poller standing by; lock held elsewhere")
		return ErrPollerLockHeld
	}
	defer func() {
		p.opts.Metrics.SetPollerUp(false)
		// Use a detached context: the parent is usually already cancelled by
		// the time we unwind, and the lock must still be released.
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := p.opts.Store.ReleaseLock(releaseCtx); err != nil {
			p.opts.Logger.Error("release poller lock", "error", err)
		}
	}()

	// Unconditional and strictly before the first poll. A registered webhook
	// makes getUpdates return 409, and overlapping calls against the same
	// token are themselves a source of spurious 409s (spec §7.3).
	if err := p.opts.Client.DeleteWebhook(ctx); err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}

	offset, err := p.opts.Store.LoadOffset(ctx)
	if err != nil {
		return fmt.Errorf("load offset: %w", err)
	}

	p.opts.Metrics.SetPollerUp(true)
	p.opts.Metrics.SetOffset(offset)
	p.opts.Logger.Info("telegram poller started", "offset", offset, "poll_timeout", p.opts.PollTimeout.String())
	p.reportStranded(ctx)

	lastRenew := time.Now()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		if time.Since(lastRenew) >= p.opts.LockRenewInterval {
			// A dropped connection loses the advisory lock server-side without
			// any error surfacing here, so treat a failed renewal as loss of
			// the right to poll rather than a transient blip.
			if err := p.opts.Store.RenewLock(ctx); err != nil {
				return fmt.Errorf("renew poller lock: %w", err)
			}
			lastRenew = time.Now()
		}

		updates, err := p.opts.Client.GetUpdates(ctx, offset, updateBatchLimit, p.opts.PollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if fatal, kind := classifyPollError(err); fatal {
				p.opts.Metrics.IncPollError(kind)
				return fmt.Errorf("fatal telegram poll error: %w", err)
			} else {
				p.opts.Metrics.IncPollError(kind)
			}
			if wait, ok := retryAfter(err); ok {
				// Honour retry_after exactly; do not layer our own backoff on
				// top of it (spec §11.3).
				if !sleepCtx(ctx, wait) {
					return ctx.Err()
				}
				continue
			}
			if !sleepCtx(ctx, p.backoff.Next()) {
				return ctx.Err()
			}
			continue
		}
		p.backoff.Reset()

		for _, u := range updates {
			if err := p.handle(ctx, u); err != nil {
				// Log and continue. A single bad update must not wedge the
				// loop forever — that is what the offset advance below is for.
				p.opts.Logger.Error("handle telegram update", "update_id", u.UpdateID, "error", err)
			}

			offset = u.UpdateID + 1
			if err := p.opts.Store.SaveOffset(ctx, offset); err != nil {
				// Storage is down. Stop rather than poll without being able to
				// persist offsets: continuing would replay this batch forever
				// on restart (spec §20).
				return fmt.Errorf("save offset: %w", err)
			}
			p.opts.Metrics.SetOffset(offset)
		}
	}
}

// handle processes one update.
//
// Ordering matters and is the reason this is not inlined:
//
//  1. RecordUpdate first. Its update_id primary key is the replay defence —
//     a redelivered update fails the insert and returns here as a no-op
//     before any work is dispatched (spec §7.4, defence 3).
//  2. Then dispatch, which writes any job durably to Postgres.
//  3. Only then does the caller advance the offset.
//
// A crash between 2 and 3 causes redelivery, which step 1 absorbs.
func (p *Poller) handle(ctx context.Context, u Update) error {
	if u.Message == nil {
		// allowed_updates is pinned to ["message"], so this is defensive only.
		return nil
	}
	p.opts.Metrics.IncUpdatesReceived()

	msg := u.Message
	authorized := p.opts.Auth.Allowed(u)
	command, args := PeekCommand(msg.Text)

	rec := UpdateRecord{
		UpdateID:   u.UpdateID,
		ChatID:     msg.Chat.ID,
		Username:   truncate(usernameOf(msg), 255),
		Command:    command,
		RawText:    truncate(msg.Text, maxStoredTextLen),
		Authorized: authorized,
		ReceivedAt: time.Now().UTC(),
	}
	if msg.From != nil {
		rec.UserID = msg.From.ID
	}

	if err := p.opts.Store.RecordUpdate(ctx, rec); err != nil {
		if errors.Is(err, ErrDuplicateUpdate) {
			// Telegram redelivered an update we already handled. Exactly the
			// case defence 3 exists for: do nothing, and let the offset
			// advance past it.
			p.opts.Metrics.IncDuplicate()
			p.opts.Logger.Warn("duplicate telegram update ignored", "update_id", u.UpdateID)
			return nil
		}
		return fmt.Errorf("record update: %w", err)
	}

	if !authorized {
		return p.handleUnauthorized(ctx, u)
	}

	req := Request{
		Command:  command,
		Args:     args,
		UserID:   rec.UserID,
		ChatID:   rec.ChatID,
		UpdateID: u.UpdateID,
	}
	if msg.ReplyTo != nil {
		req.ReplyToText = msg.ReplyTo.Text
	}

	resp, dispatchErr := p.opts.Dispatcher.Dispatch(ctx, req)

	// A command that fails must still answer.
	//
	// Dispatch returns an empty Response alongside an error when a handler
	// errors or panics. Sending nothing in that case makes a broken command
	// indistinguishable from a dead bot, an unauthorized sender, or a bridge
	// that never received the message — and the operator's next move differs
	// completely in each case. The reply names the command and nothing else:
	// an error string can carry a query, a connection string, or a token.
	body := resp.Body
	silent := resp.Silent
	if dispatchErr != nil {
		silent = false
		body = "⚠️ " + Bold("/"+Esc(req.Command)+" failed") +
			"\nThe controller logged the reason. Nothing was changed."
	}

	if !silent && body != "" {
		if err := p.opts.Sender.Send(ctx, rec.ChatID, body); err != nil {
			p.opts.Logger.Error("send telegram reply", "update_id", u.UpdateID, "error", err)
			p.opts.Metrics.IncMessageFailed("send")
		}
	}

	if err := p.opts.Store.CompleteUpdate(ctx, u.UpdateID, dispatchErr); err != nil {
		return fmt.Errorf("complete update: %w", err)
	}
	return dispatchErr
}

// handleUnauthorized records the rejection and stays silent.
//
// No reply is ever sent. A reply confirms the bot exists and is live; silence
// is the correct response to an unauthorized sender (spec §8).
func (p *Poller) handleUnauthorized(ctx context.Context, u Update) error {
	p.opts.Metrics.IncUnauthorized()
	p.opts.Logger.Warn("unauthorized telegram update rejected",
		"update_id", u.UpdateID,
		"chat_id", u.Message.Chat.ID,
		"user_id", senderID(u.Message),
	)

	since := time.Now().UTC().Add(-p.opts.UnauthorizedWindow)
	count, err := p.opts.Store.CountRecentUnauthorized(ctx, since)
	if err != nil {
		p.opts.Logger.Error("count recent unauthorized", "error", err)
		return p.opts.Store.CompleteUpdate(ctx, u.UpdateID, nil)
	}

	// One notification per window, to the authorized chat only — never a reply
	// to the sender.
	if count >= p.opts.UnauthorizedThreshold && time.Since(p.lastUnauthorizedNotice) >= p.opts.UnauthorizedWindow {
		p.lastUnauthorizedNotice = time.Now()
		body := fmt.Sprintf("⚠️ ACTION REQUIRED\n\n%d unauthorized command attempts in the last %s. No replies were sent.",
			count, p.opts.UnauthorizedWindow)
		if err := p.opts.Sender.Send(ctx, p.opts.ChatID, body); err != nil {
			p.opts.Logger.Error("send unauthorized notice", "error", err)
		}
	}

	return p.opts.Store.CompleteUpdate(ctx, u.UpdateID, nil)
}

// classifyPollError reports whether an error must stop the poller, and the
// metric label for it.
func classifyPollError(err error) (fatal bool, kind string) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.IsConflict():
			// Another consumer is polling this token, or a webhook is set.
			// Self-sustaining either way.
			return true, "conflict"
		case apiErr.IsUnauthorized():
			// Token revoked or wrong. Retrying cannot help.
			return true, "unauthorized"
		case apiErr.Code == 429:
			return false, "rate_limited"
		case apiErr.Code >= 500:
			return false, "server"
		}
		return false, "api"
	}
	return false, "network"
}

// retryAfter extracts a 429's retry_after, if present.
func retryAfter(err error) (time.Duration, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		return apiErr.RetryAfter, true
	}
	return 0, false
}

// sleepCtx waits for d, reporting false if the context ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func senderID(m *Message) int64 {
	if m == nil || m.From == nil {
		return 0
	}
	return m.From.ID
}

func usernameOf(m *Message) string {
	if m == nil || m.From == nil {
		return ""
	}
	return m.From.Username
}

func truncate(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	// Trim back to a rune boundary so the stored text stays valid UTF-8.
	cut := limit
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut]
}

// utf8Start reports whether b begins a UTF-8 rune.
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// strandedCounter is implemented by stores that can report accepted-but-never-
// completed updates. It is an optional interface so the Store contract and its
// test fakes stay unchanged.
type strandedCounter interface {
	CountStranded(ctx context.Context, before time.Time) (int64, error)
}

// strandedGrace is how long an update may sit unprocessed before it counts as
// stranded rather than merely in flight.
const strandedGrace = 5 * time.Minute

// reportStranded surfaces commands that were accepted and then silently
// dropped by a crash between RecordUpdate and dispatch.
//
// The at-most-once replay defence (§7.4) is deliberate — running a runbook
// twice is worse than not running it — but it means a dropped command leaves
// no trace in the chat. Reporting the count at startup is what makes the
// difference between "it never arrived" and "it arrived and we lost it"
// visible without reading the table by hand.
func (p *Poller) reportStranded(ctx context.Context) {
	counter, ok := p.opts.Store.(strandedCounter)
	if !ok {
		return
	}
	scoped, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	count, err := counter.CountStranded(scoped, time.Now().UTC().Add(-strandedGrace))
	if err != nil {
		p.opts.Logger.Warn("could not count stranded telegram updates", "error", err)
		return
	}
	if count > 0 {
		p.opts.Logger.Warn("telegram updates were accepted but never processed; these commands were dropped",
			"count", count, "older_than", strandedGrace.String())
	}
}

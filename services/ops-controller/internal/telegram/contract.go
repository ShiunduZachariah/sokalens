// Package telegram implements the outbound-only ops bridge described in
// docs/operations/telegram-agent-ops.md.
//
// The bridge initiates every connection via long polling. Nothing in this
// package listens on a port, and no file here may register an HTTP route.
//
// This file is the shared contract between the transport layer (client,
// poller), the dispatch layer (auth, router, format) and the command
// implementations. It is deliberately dependency-free so the pieces can be
// built independently.
package telegram

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// ErrDuplicateUpdate is returned by Store.RecordUpdate when the update_id
// primary key already exists. This is the replay defence from spec §7.4: a
// redelivered update becomes a no-op rather than a second execution.
var ErrDuplicateUpdate = errors.New("telegram: update already recorded")

// ErrPollerLockHeld is returned when another instance holds the poller lock.
var ErrPollerLockHeld = errors.New("telegram: poller lock held by another instance")

// ---------------------------------------------------------------------------
// Telegram API wire types. Only the fields the bridge actually reads are
// modelled; allowed_updates is pinned to ["message"] so nothing else arrives.
// ---------------------------------------------------------------------------

// Update is a single entry from getUpdates.
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

// Message is the only update kind the bridge subscribes to.
type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Date      int64  `json:"date"`
	Text      string `json:"text"`
	// ReplyTo carries the message a /issue with no arguments refers to.
	ReplyTo *Message `json:"reply_to_message"`
}

// User is a Telegram account. Authorization uses ID only: usernames are
// mutable and reassignable (spec §8).
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

// Chat is the conversation an update arrived in. Group IDs are negative.
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// ---------------------------------------------------------------------------
// Transport.
// ---------------------------------------------------------------------------

// Client is the Telegram HTTP surface the bridge uses. Implementations must be
// safe for concurrent use: the poller calls GetUpdates while the notification
// path calls SendMessage.
type Client interface {
	// GetUpdates long-polls. An update is only consumed once GetUpdates is
	// called with an offset strictly greater than its UpdateID.
	GetUpdates(ctx context.Context, offset int64, limit int, timeout time.Duration) ([]Update, error)
	// SendMessage delivers one already-chunked, already-escaped HTML part.
	SendMessage(ctx context.Context, chatID int64, html string) error
	// DeleteWebhook runs unconditionally at boot, strictly before the first
	// poll: a registered webhook makes getUpdates return 409 (spec §7.3).
	DeleteWebhook(ctx context.Context) error
	// GetMe validates the token at boot. Failure is a config error.
	GetMe(ctx context.Context) (User, error)
}

// APIError is returned for any non-2xx Telegram response. The poller
// distinguishes fatal conditions from retryable ones by Code.
type APIError struct {
	Code        int
	Description string
	// RetryAfter carries parameters.retry_after on a 429. Honour it exactly;
	// do not layer additional backoff on top (spec §11.3).
	RetryAfter time.Duration
}

// Error always names the status code. Telegram can return an empty
// description, which would otherwise produce a blank error string and lose the
// one field that tells you whether the condition is fatal.
func (e *APIError) Error() string {
	if e.Description == "" {
		return "telegram: api error " + strconv.Itoa(e.Code)
	}
	return "telegram: api error " + strconv.Itoa(e.Code) + ": " + e.Description
}

// IsConflict reports a 409. Both causes — an active webhook, and a second
// consumer polling the same token — are fatal to the poller goroutine. The
// second is self-sustaining: retrying in a loop makes the failure invisible
// (spec §7.2).
func (e *APIError) IsConflict() bool { return e.Code == 409 }

// IsUnauthorized reports a 401: token revoked or wrong. Fatal, never retried.
func (e *APIError) IsUnauthorized() bool { return e.Code == 401 }

// ---------------------------------------------------------------------------
// Durable state. Every method is Postgres-backed: the poller must not advance
// an offset it cannot persist (spec §7.4).
// ---------------------------------------------------------------------------

// Store is the bridge's persistence boundary.
type Store interface {
	// LoadOffset reads the single ops_telegram_state row.
	LoadOffset(ctx context.Context) (int64, error)
	// SaveOffset advances the persisted offset. Called only after the
	// update's own row is durably written.
	SaveOffset(ctx context.Context, offset int64) error

	// RecordUpdate inserts into ops_telegram_updates. The update_id primary
	// key is the replay defence: a duplicate insert must return
	// ErrDuplicateUpdate rather than an opaque driver error.
	RecordUpdate(ctx context.Context, rec UpdateRecord) error
	// CompleteUpdate stamps processed_at, and error when handling failed.
	CompleteUpdate(ctx context.Context, updateID int64, handlerErr error) error

	// CountRecentUnauthorized supports the "5 in 10 minutes" one-shot
	// notification in spec §8.
	CountRecentUnauthorized(ctx context.Context, since time.Time) (int, error)

	// AcquireLock takes the poller's Postgres advisory lock. Only the holder
	// polls; every instance may still send. Returns false when another
	// instance holds it. See plan "Deviations" — this replaces the spec's
	// Redis lock because ops-controller has no Redis dependency.
	AcquireLock(ctx context.Context) (bool, error)
	// RenewLock keeps the session's advisory lock observable as live.
	RenewLock(ctx context.Context) error
	// ReleaseLock drops the advisory lock on shutdown or a fatal poll error.
	ReleaseLock(ctx context.Context) error
}

// UpdateRecord is one row of the ops_telegram_updates forensic ledger. Both
// authorized and rejected updates are recorded.
type UpdateRecord struct {
	UpdateID   int64
	ChatID     int64
	UserID     int64
	Username   string
	Command    string
	RawText    string
	Authorized bool
	ReceivedAt time.Time
}

// ---------------------------------------------------------------------------
// Dispatch.
// ---------------------------------------------------------------------------

// Request is a parsed, authorized command ready to execute.
type Request struct {
	Command string
	Args    []string
	UserID  int64
	ChatID  int64
	// UpdateID sources the job idempotency key telegram:update:<update_id>.
	UpdateID int64
	// ReplyToText is the body of the message this command replied to, used by
	// /issue with no arguments.
	ReplyToText string
}

// Response is a rendered reply. Body is already HTML-escaped; the send path
// chunks it (spec §11.2).
type Response struct {
	Body string
	// Silent suppresses the reply entirely. Unauthorized senders get this:
	// any reply confirms the bot exists (spec §8).
	Silent bool
}

// Handler executes one command.
type Handler interface {
	Execute(ctx context.Context, req Request) (Response, error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, req Request) (Response, error)

// Execute implements Handler.
func (f HandlerFunc) Execute(ctx context.Context, req Request) (Response, error) {
	return f(ctx, req)
}

// Registration is one entry in the router's registry. /help renders itself
// from these so it cannot drift from what is actually dispatchable.
type Registration struct {
	Name string
	// Usage is the argument shape, e.g. "<id>". Empty when the command takes
	// no arguments.
	Usage string
	// Summary is the one-line description shown by /help.
	Summary string
	// Mutating marks commands that cause work. These must write their job to
	// Postgres before the offset advances (spec §7.2 "Postgres before
	// Telegram").
	Mutating bool
	Handler  Handler
}

// ---------------------------------------------------------------------------
// Audit. Recon A4: ops_audit_events has no "owner" actor_type, so the spec's
// actor `owner` maps to actor_type='admin' with actor_id='telegram:<user_id>'.
// ---------------------------------------------------------------------------

// Auditor records every command attempt, authorized or not.
type Auditor interface {
	RecordCommand(ctx context.Context, event AuditEvent) error
}

// AuditEvent is one ops_audit_events row.
type AuditEvent struct {
	EventType  string
	ActorID    string
	EntityID   string
	Details    map[string]any
	OccurredAt time.Time
}

// ActorID renders the audit actor for a Telegram user.
func ActorID(userID int64) string { return "telegram:" + strconv.FormatInt(userID, 10) }

// Metrics is the collector surface (spec §19). Implemented against the
// existing hand-rolled internal/telemetry style; no new dependency.
type Metrics interface {
	SetPollerUp(up bool)
	SetOffset(offset int64)
	IncUpdatesReceived()
	IncUnauthorized()
	IncDuplicate()
	IncPollError(kind string)
	ObserveCommand(command, status string, d time.Duration)
	IncMessageSent(tier string)
	IncMessageFailed(reason string)
	IncMessageSuppressed()
}

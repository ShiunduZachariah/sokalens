// Package notifications is the outbound notification path for the ops
// controller: an outbox table, a bounded worker, and two peer transports.
//
// The source spec (§12) describes this phase as "register Telegram as a
// transport with the existing notification worker". No such worker exists in
// this service. internal/alerts is an *inbound* HMAC alert validator with a
// Sink interface, there is no email transport here, and there was no
// ops_notifications table (recon A5). So this package is a build, not a
// registration, and the shape follows from that:
//
//   - Telegram and email are peers behind one Transport interface. Neither is
//     special-cased, because the guarantee that matters — a Telegram failure
//     must never lose a notification — is only true if the fallback is a real
//     transport rather than a TODO.
//   - Delivery state lives in Postgres (ops_notification_outbox, migration
//     000059), not in memory. A row leaves 'pending' only by being delivered
//     or by exhausting its attempts into a durable 'failed'.
//   - Correlation is not re-implemented. internal/incidents already
//     deduplicates by fingerprint and correlates alerts sharing a root cause;
//     this package consumes that identity (see Notifier.Publish) so four
//     correlated alerts produce one message by construction rather than by a
//     second, independently-drifting heuristic.
//
// Nothing here registers an HTTP route, and nothing here mutates production.
package notifications

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
)

// Tier is the routing class of a notification. It is deliberately not
// ops_incidents.severity: severity describes the world, tier describes what we
// do about it, and collapsing the two makes the routing rule unreadable.
//
// Values are the lowercase forms stored in ops_notification_outbox.tier and
// constrained by that table's CHECK.
type Tier string

const (
	// TierInfo is never sent. Spec §12: it appears in the daily digest only.
	TierInfo Tier = "info"
	// TierImportant is sent immediately.
	TierImportant Tier = "important"
	// TierActionRequired is sent immediately, prefixed, and is never dropped
	// by suppression.
	TierActionRequired Tier = "action_required"
)

// ActionRequiredPrefix leads every ACTION REQUIRED message. Spec §12.
const ActionRequiredPrefix = "⚠️ ACTION REQUIRED"

// SuppressionNotice is the collapse message from spec §11.4. The count is
// substituted for %d.
const suppressionNoticeFormat = "⚠️ %d further notifications suppressed. /incidents for detail."

var (
	// ErrInvalidNotification rejects a notification the outbox would refuse.
	ErrInvalidNotification = errors.New("notifications: invalid notification")
	// ErrStoreUnavailable mirrors incidents.ErrRepositoryUnavailable: a
	// missing store is a wiring fault, reported rather than panicked on.
	ErrStoreUnavailable = errors.New("notifications: store unavailable")
	// ErrNoTransport is returned when a worker is built with no primary
	// transport. There is nothing useful such a worker could do.
	ErrNoTransport = errors.New("notifications: at least a primary transport is required")
)

// Valid reports whether t is one of the three tiers.
func (t Tier) Valid() bool {
	switch t {
	case TierInfo, TierImportant, TierActionRequired:
		return true
	default:
		return false
	}
}

// Immediate reports whether the tier is sent at all. Only INFO is not.
func (t Tier) Immediate() bool { return t == TierImportant || t == TierActionRequired }

// Label is the human rendering used in message headers and email subjects.
func (t Tier) Label() string {
	switch t {
	case TierActionRequired:
		return "ACTION REQUIRED"
	case TierImportant:
		return "IMPORTANT"
	case TierInfo:
		return "INFO"
	default:
		return "UNKNOWN"
	}
}

// rank orders tiers by urgency, most urgent first. It drives both the claim
// ordering and the "raise, never lower" merge when correlated observations
// land on an open row.
func (t Tier) rank() int {
	switch t {
	case TierActionRequired:
		return 0
	case TierImportant:
		return 1
	default:
		return 2
	}
}

// Max returns the more urgent of two tiers.
func (t Tier) Max(other Tier) Tier {
	if other.rank() < t.rank() {
		return other
	}
	return t
}

// initialStatus is the status a freshly enqueued notification takes. INFO
// starts at 'digest', which the claim query does not select and the table's
// CHECK will not let it leave for 'pending'. That is the structural half of
// "INFO is not sent".
func (t Tier) initialStatus() string {
	if t == TierInfo {
		return StatusDigest
	}
	return StatusPending
}

// Outbox statuses, matching the CHECK in migration 000059.
const (
	StatusPending    = "pending"
	StatusDigest     = "digest"
	StatusDelivered  = "delivered"
	StatusFailed     = "failed"
	StatusSuppressed = "suppressed"
)

// Transport names, matching the CHECK in migration 000059.
const (
	TransportTelegram = "telegram"
	TransportEmail    = "email"
	TransportDigest   = "digest"
)

// Message body bounds. An alert storm must grow a queue, not memory, and a
// notification assembled from an upstream payload is not trusted to be small.
const (
	// MaxSubjectChars bounds the one-line summary.
	MaxSubjectChars = 200
	// MaxBodyChars bounds the detail block. Well under the 4096-character
	// Telegram ceiling even after escaping, so a single notification never
	// needs the chunker's truncation path.
	MaxBodyChars = 2000
	// MaxFingerprintChars bounds the correlation key.
	MaxFingerprintChars = 256
)

// Notification is one outbox row in flight.
type Notification struct {
	// ID is the ops_notification_outbox UUID. Empty before the row exists.
	ID string
	// Tier drives routing. See Tier.
	Tier Tier
	// Subject is the one-line headline.
	Subject string
	// Body is the detail block. May be empty.
	Body string
	// Fingerprint is the incident fingerprint, verbatim. Required: it is the
	// key the open-row dedup index is built on.
	Fingerprint string
	// DedupKey, when set, is unique for all time. Used by the daily digest and
	// the suppression notice, which must never repeat even across restarts.
	DedupKey string
	// IncidentID is the ops_incidents UUID this notification reports, if any.
	IncidentID string
	// IncidentReference is the short human reference (#143) added in 000059.
	// Zero when unknown; a UUID is unusable from a phone, which is the whole
	// reason that column exists.
	IncidentReference int64
	// OccurrenceCount is how many correlated observations this one message
	// stands for.
	OccurrenceCount int64
	// Attempts is the delivery attempt count *including* the current one, as
	// returned by Claim.
	Attempts int
	CreatedAt time.Time
}

// Normalize bounds and trims a notification, and fills the defaults the outbox
// requires. It is called by the store on the way in so no caller can smuggle
// an unbounded body past it.
func (n Notification) Normalize(now time.Time) (Notification, error) {
	n.Subject = telegram.TruncateField(collapseControl(strings.TrimSpace(n.Subject)), MaxSubjectChars)
	n.Body = telegram.TruncateField(strings.TrimSpace(n.Body), MaxBodyChars)
	n.Fingerprint = telegram.TruncateField(strings.TrimSpace(n.Fingerprint), MaxFingerprintChars)
	n.DedupKey = strings.TrimSpace(n.DedupKey)
	n.IncidentID = strings.TrimSpace(n.IncidentID)

	if !n.Tier.Valid() {
		return Notification{}, fmt.Errorf("%w: tier %q", ErrInvalidNotification, string(n.Tier))
	}
	if n.Subject == "" {
		return Notification{}, fmt.Errorf("%w: subject is required", ErrInvalidNotification)
	}
	if n.Fingerprint == "" {
		return Notification{}, fmt.Errorf("%w: fingerprint is required", ErrInvalidNotification)
	}
	if n.IncidentReference < 0 {
		return Notification{}, fmt.Errorf("%w: incident reference must not be negative", ErrInvalidNotification)
	}
	if n.OccurrenceCount < 1 {
		n.OccurrenceCount = 1
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = now
	}
	return n, nil
}

// collapseControl flattens control characters in a subject. A newline in a
// subject would break the email header and split the Telegram headline.
func collapseControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// reference renders the incident tail of a message: "#143 · x4". Both halves
// are optional, and an occurrence count of one is not worth the noise.
func (n Notification) reference() string {
	parts := make([]string, 0, 2)
	if n.IncidentReference > 0 {
		parts = append(parts, "#"+strconv.FormatInt(n.IncidentReference, 10))
	}
	if n.OccurrenceCount > 1 {
		parts = append(parts, "x"+strconv.FormatInt(n.OccurrenceCount, 10))
	}
	return strings.Join(parts, " · ")
}

// RenderTelegram assembles the HTML-parse-mode body for one notification.
//
// Every interpolated value passes through telegram.Esc; the body block goes
// through Pre so a multi-line detail keeps its shape on a phone. The result is
// still handed to telegram.Chunk by the sender, so an over-long body degrades
// rather than failing the send.
func RenderTelegram(n Notification) string {
	var b strings.Builder
	if n.Tier == TierActionRequired {
		b.WriteString(telegram.Esc(ActionRequiredPrefix))
		b.WriteString("\n")
	}
	b.WriteString(telegram.Bold(n.Subject))
	if ref := n.reference(); ref != "" {
		b.WriteString("\n")
		b.WriteString(telegram.Esc(ref))
	}
	if n.Body != "" {
		b.WriteString("\n\n")
		b.WriteString(telegram.Pre(n.Body))
	}
	return b.String()
}

// RenderEmailSubject is the fallback transport's subject line. The tier is in
// the subject because an email client shows nothing else in a list view, and
// the whole point of the fallback is that it is read when Telegram was not.
func RenderEmailSubject(n Notification) string {
	subject := "[SokaLens " + n.Tier.Label() + "] " + n.Subject
	return telegram.TruncateField(collapseControl(subject), MaxSubjectChars+40)
}

// RenderEmailBody is the plain-text body. Deliberately not HTML: this path
// runs when the primary transport is already failing, and a plain body has
// fewer ways to fail in turn.
func RenderEmailBody(n Notification) string {
	var b strings.Builder
	if n.Tier == TierActionRequired {
		b.WriteString(ActionRequiredPrefix)
		b.WriteString("\n\n")
	}
	b.WriteString(n.Subject)
	b.WriteString("\n")
	if ref := n.reference(); ref != "" {
		b.WriteString(ref)
		b.WriteString("\n")
	}
	if n.Body != "" {
		b.WriteString("\n")
		b.WriteString(n.Body)
		b.WriteString("\n")
	}
	b.WriteString("\nFingerprint: ")
	b.WriteString(n.Fingerprint)
	b.WriteString("\n")
	if !n.CreatedAt.IsZero() {
		b.WriteString("Raised: ")
		b.WriteString(n.CreatedAt.UTC().Format(time.RFC3339))
		b.WriteString("\n")
	}
	b.WriteString("\nDelivered by email because the Telegram ops bridge could not be reached.\n")
	return b.String()
}

// Transport is one delivery mechanism. Telegram and email are peers: the
// worker knows a primary and a fallback, not "the transport" and "a special
// case".
//
// Deliver must honour ctx and must return a non-nil error for any outcome it
// cannot prove succeeded. Returning nil on an unproven send would move the
// outbox row to 'delivered' and lose the notification, which is the exact
// failure this package exists to prevent.
type Transport interface {
	// Name is the value written to ops_notification_outbox.transport. It must
	// be one of the values that column's CHECK permits.
	Name() string
	Deliver(ctx context.Context, n Notification) error
}

// Metrics is the optional collector surface. It is deliberately NOT
// telegram.Metrics: that interface counts Telegram *parts* sent by
// RateLimitedSender, and reusing its counters here would double-count every
// notification. internal/telemetry is outside this phase's write scope, so
// this interface has no in-repo implementation yet; wiring may pass nil.
type Metrics interface {
	IncNotificationDelivered(tier, transport string)
	IncNotificationFailed(tier, transport string)
	IncNotificationSuppressed(tier string)
}

// nopMetrics keeps the worker free of nil checks at every call site.
type nopMetrics struct{}

func (nopMetrics) IncNotificationDelivered(string, string) {}
func (nopMetrics) IncNotificationFailed(string, string)    {}
func (nopMetrics) IncNotificationSuppressed(string)        {}

// truncateForColumn bounds a delivery error before it reaches the database.
// An error string can carry an entire upstream response body.
func truncateForColumn(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return telegram.TruncateField(s, telegram.MaxErrorChars)
}

// runeLen is used by the tests to assert the body bounds actually bind.
func runeLen(s string) int { return utf8.RuneCountInString(s) }

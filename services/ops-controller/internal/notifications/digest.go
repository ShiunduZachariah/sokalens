package notifications

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// The daily digest (plan T2 item 4: "Daily digest at 08:00 EAT, rendering
// `unavailable` for any missing source rather than omitting the line").
//
// Everything the digest needs already existed — Store.DigestBacklog,
// Store.MarkDigested, EnqueueOnce ("used by the digest"), the outbox's
// dedup_key column — but nothing ever called any of it, so the message was
// never assembled and never sent. This file is the missing caller.
//
// Two properties are load-bearing:
//
//  1. The digest is *enqueued*, not sent. It goes into the same outbox as
//     every other notification, so it inherits retries, the Telegram rate
//     limiter, and the email fallback. A Telegram outage at 08:00 delays the
//     digest; it does not lose it.
//  2. Once per calendar day, for all time. The dedup key is the local date, so
//     a restart loop at 08:05 produces one digest, not one per restart, and a
//     controller that was down at 08:00 still sends the day's digest when it
//     comes back rather than skipping the day in silence.

const (
	// DigestUnavailable is rendered for any summary line whose source could
	// not be read. An omitted line reads as "fine" and it is not.
	DigestUnavailable = "unavailable"

	// digestKeyPrefix namespaces both the dedup key and the fingerprint. The
	// fingerprint carries the date too: the open-fingerprint unique index
	// would otherwise collide with yesterday's digest if that one is still
	// undelivered.
	digestKeyPrefix = "ops-digest:"

	defaultDigestHour    = 8
	defaultDigestBacklog = 20
	// digestBacklogCap bounds the rendered list; the remainder is counted, not
	// printed, so an overnight storm cannot produce an unsendable message.
	digestBacklogCap = 10
)

// Section is one labelled line of the digest summary.
type Section struct {
	Label string
	Value string
}

// SummarySource supplies the health lines of the digest. It is deliberately
// tiny: this package must not learn how to query incidents, jobs, deployments
// or spend — those projections already exist behind the /status command, and
// the digest reuses them through this seam so the two can never disagree.
//
// An implementation must never return an error. A source it cannot read is a
// Section whose Value is DigestUnavailable.
type SummarySource interface {
	DigestSummary(ctx context.Context) []Section
}

// DigestConfig tunes the schedule. Every zero value has a default, so the zero
// DigestConfig sends at 08:00 UTC.
type DigestConfig struct {
	// Hour and Minute are the local send time, in Location.
	Hour   int
	Minute int
	// Location is the operator's timezone (Africa/Nairobi in production).
	Location *time.Location
	// Backlog bounds the INFO rows read for the window.
	Backlog int
	// Timeout bounds one digest assembly so a slow database cannot hold the
	// loop open until tomorrow.
	Timeout time.Duration
	// Now is injectable for tests.
	Now func() time.Time
}

func (c DigestConfig) withDefaults() DigestConfig {
	if c.Hour < 0 || c.Hour > 23 {
		c.Hour = defaultDigestHour
	}
	if c.Minute < 0 || c.Minute > 59 {
		c.Minute = 0
	}
	if c.Location == nil {
		c.Location = time.UTC
	}
	if c.Backlog < 1 {
		c.Backlog = defaultDigestBacklog
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Digest assembles and enqueues the once-a-day summary.
type Digest struct {
	store  Store
	source SummarySource
	cfg    DigestConfig
	logger *slog.Logger
}

// NewDigest builds the digest scheduler. source may be nil: a digest that can
// only report the notification backlog is worth far more than no digest, and
// the missing sections render as unavailable.
func NewDigest(store Store, source SummarySource, logger *slog.Logger, cfg DigestConfig) (*Digest, error) {
	if store == nil {
		return nil, ErrStoreUnavailable
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Digest{store: store, source: source, cfg: cfg.withDefaults(), logger: logger}, nil
}

// Run sends the digest once per day at the configured local time, and blocks
// until ctx is cancelled.
//
// It starts with a catch-up attempt: if the process comes up after today's
// send time, today's digest is owed. RunOnce is idempotent per day, so a
// restart loop cannot turn that into a flood.
func (d *Digest) Run(ctx context.Context) error {
	if now := d.cfg.Now(); !now.Before(d.fireTime(now)) {
		if err := d.RunOnce(ctx, now); err != nil {
			d.logger.Error("daily digest catch-up failed", "error", err)
		}
	}
	for {
		now := d.cfg.Now()
		next := d.nextFire(now)
		timer := time.NewTimer(next.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if err := d.RunOnce(ctx, d.cfg.Now()); err != nil {
			d.logger.Error("daily digest failed", "error", err)
		}
	}
}

// RunOnce assembles the digest for the day containing now and enqueues it. A
// second call on the same local day is a no-op.
//
// The backlog is closed only after the digest row exists: MarkDigested before
// a successful enqueue would drop the INFO notifications on the floor if the
// enqueue then failed.
func (d *Digest) RunOnce(ctx context.Context, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
	defer cancel()

	local := now.In(d.cfg.Location)
	until := now
	since := d.fireTime(now).AddDate(0, 0, -1)

	backlog, backlogErr := d.store.DigestBacklog(ctx, since, until, d.cfg.Backlog)
	if backlogErr != nil {
		// Not fatal. A digest that says the backlog is unavailable is still
		// the daily heartbeat that tells the operator the bridge is alive.
		d.logger.Error("digest backlog unavailable", "error", backlogErr)
	}

	var sections []Section
	if d.source != nil {
		sections = d.source.DigestSummary(ctx)
	}

	key := digestKeyPrefix + local.Format("2006-01-02")
	result, err := d.store.EnqueueOnce(ctx, Notification{
		// IMPORTANT, not INFO: an INFO row is structurally unsendable (see the
		// tier/status CHECK in migration 000060), and the digest is the one
		// message that must arrive every day whether or not anything happened.
		Tier:        TierImportant,
		Subject:     "SokaLens daily digest — " + local.Format("Mon 02 Jan 2006"),
		Body:        RenderDigestBody(sections, backlog, backlogErr, since, until, d.cfg.Location),
		Fingerprint: key,
		DedupKey:    key,
		CreatedAt:   now,
	})
	if err != nil {
		return fmt.Errorf("enqueue digest: %w", err)
	}
	if !result.Created {
		// Already sent (or queued) for this day.
		return nil
	}

	ids := make([]string, 0, len(backlog))
	for _, n := range backlog {
		if n.ID != "" {
			ids = append(ids, n.ID)
		}
	}
	if len(ids) > 0 {
		if err := d.store.MarkDigested(ctx, ids, now); err != nil {
			// The digest is already durable and will be delivered; the INFO
			// rows staying open means at worst they repeat tomorrow.
			d.logger.Error("digest backlog not closed", "error", err, "count", len(ids))
		}
	}
	d.logger.Info("daily digest enqueued", "dedup_key", key, "info_notifications", len(ids))
	return nil
}

// fireTime is the send instant on now's local day.
func (d *Digest) fireTime(now time.Time) time.Time {
	local := now.In(d.cfg.Location)
	return time.Date(local.Year(), local.Month(), local.Day(), d.cfg.Hour, d.cfg.Minute, 0, 0, d.cfg.Location)
}

// nextFire is the next send instant strictly after now.
func (d *Digest) nextFire(now time.Time) time.Time {
	fire := d.fireTime(now)
	if fire.After(now) {
		return fire
	}
	return d.fireTime(now.In(d.cfg.Location).AddDate(0, 0, 1))
}

// RenderDigestBody assembles the plain-text digest body.
//
// Plain text, not HTML: the body is wrapped in <pre> by RenderTelegram and
// sent verbatim by the email fallback, so it must read correctly in both.
func RenderDigestBody(sections []Section, backlog []Notification, backlogErr error, since, until time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	var b strings.Builder

	fmt.Fprintf(&b, "Window: %s → %s\n\n",
		since.In(loc).Format("Mon 02 Jan 15:04"), until.In(loc).Format("Mon 02 Jan 15:04 MST"))

	if len(sections) == 0 {
		sections = []Section{{Label: "Summary", Value: DigestUnavailable}}
	}
	width := 0
	for _, s := range sections {
		if n := len(s.Label); n > width {
			width = n
		}
	}
	for _, s := range sections {
		value := strings.TrimSpace(s.Value)
		if value == "" {
			value = DigestUnavailable
		}
		fmt.Fprintf(&b, "%-*s  %s\n", width, s.Label, value)
	}

	b.WriteString("\n")
	switch {
	case backlogErr != nil:
		b.WriteString("INFO notifications: " + DigestUnavailable + "\n")
	case len(backlog) == 0:
		b.WriteString("INFO notifications: none\n")
	default:
		fmt.Fprintf(&b, "INFO notifications (%d):\n", len(backlog))
		shown := backlog
		if len(shown) > digestBacklogCap {
			shown = shown[:digestBacklogCap]
		}
		for _, n := range shown {
			line := "· " + n.CreatedAt.In(loc).Format("15:04") + "  " + n.Subject
			if ref := n.reference(); ref != "" {
				line += " (" + ref + ")"
			}
			b.WriteString(line + "\n")
		}
		if remaining := len(backlog) - len(shown); remaining > 0 {
			fmt.Fprintf(&b, "+%d more — /incidents for detail\n", remaining)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

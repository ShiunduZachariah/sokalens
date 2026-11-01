package notifications

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"time"
)

// Worker drains the outbox and delivers.
//
// The shape follows internal/scheduler (recon A9): a fixed number of worker
// goroutines reading from a bounded channel, fed by exactly one claimer. There
// is no goroutine per notification and no unbounded buffer anywhere, so an
// alert storm grows the outbox table — which is on disk and queryable — rather
// than the process heap.
//
// Delivery order of concerns, per notification:
//
//	admission (suppression, spec §11.4)
//	  -> primary transport (Telegram)
//	  -> fallback transport (email) on primary failure
//	  -> retry with backoff, or a durable terminal state
//
// At no point is a row removed. See Store.
type Worker struct {
	store    Store
	primary  Transport
	fallback Transport
	metrics  Metrics
	logger   *slog.Logger
	cfg      WorkerConfig

	suppress *suppressor

	queue chan Notification

	// inflight counts notifications handed to a worker but not yet finalized.
	// The claimer never leases more than the queue can hold, so this is
	// bounded by Workers+QueueCapacity by construction; the counter exists so
	// the tests can prove it.
	mu       sync.Mutex
	inflight int
	peak     int
}

// WorkerConfig tunes the worker. Every zero value has a conservative default,
// so the zero WorkerConfig is usable.
type WorkerConfig struct {
	// Workers is the fixed worker count. Not "up to": exactly this many
	// goroutines run for the lifetime of Run.
	Workers int
	// QueueCapacity bounds the hand-off channel. Plan T2: 256.
	QueueCapacity int
	// ClaimBatch bounds one claim. Never larger than QueueCapacity.
	ClaimBatch int
	// PollInterval is how often the claimer looks for work when the last claim
	// came back empty.
	PollInterval time.Duration
	// ClaimLease is how long a claimed row is hidden from other claimers. A
	// crash mid-delivery costs at most this much delay, never the message.
	ClaimLease time.Duration
	// MaxAttempts is the number of delivery attempts before a notification
	// becomes durably 'failed'. It is never discarded.
	MaxAttempts int
	// BaseBackoff is the first retry delay; each attempt doubles it up to
	// MaxBackoff.
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	// RateLimit is the suppression cap: more than this many notifications in
	// one RateWindow collapse into a single notice. Spec §11.4 says 20/min.
	RateLimit  int
	RateWindow time.Duration
	// DeliverTimeout bounds one transport call so a hung SMTP server cannot
	// hold a worker forever.
	DeliverTimeout time.Duration
	// Now is injectable for tests.
	Now func() time.Time
}

const (
	defaultWorkers        = 4
	defaultQueueCapacity  = 256
	defaultClaimBatch     = 32
	defaultPollInterval   = 2 * time.Second
	defaultClaimLease     = 60 * time.Second
	defaultMaxAttempts    = 5
	defaultBaseBackoff    = 15 * time.Second
	defaultMaxBackoff     = 10 * time.Minute
	defaultRateLimit      = 20
	defaultRateWindow     = time.Minute
	defaultDeliverTimeout = 30 * time.Second
)

func (c WorkerConfig) withDefaults() WorkerConfig {
	if c.Workers < 1 {
		c.Workers = defaultWorkers
	}
	if c.QueueCapacity < 1 {
		c.QueueCapacity = defaultQueueCapacity
	}
	if c.ClaimBatch < 1 {
		c.ClaimBatch = defaultClaimBatch
	}
	if c.ClaimBatch > c.QueueCapacity {
		c.ClaimBatch = c.QueueCapacity
	}
	if c.PollInterval <= 0 {
		c.PollInterval = defaultPollInterval
	}
	if c.ClaimLease <= 0 {
		c.ClaimLease = defaultClaimLease
	}
	if c.MaxAttempts < 1 {
		c.MaxAttempts = defaultMaxAttempts
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = defaultBaseBackoff
	}
	if c.MaxBackoff < c.BaseBackoff {
		c.MaxBackoff = defaultMaxBackoff
	}
	if c.MaxBackoff < c.BaseBackoff {
		c.MaxBackoff = c.BaseBackoff
	}
	if c.RateLimit < 1 {
		c.RateLimit = defaultRateLimit
	}
	if c.RateWindow <= 0 {
		c.RateWindow = defaultRateWindow
	}
	if c.DeliverTimeout <= 0 {
		c.DeliverTimeout = defaultDeliverTimeout
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// NewWorker builds a worker. fallback may be nil — the service runs without
// email configured — but that is a degraded mode: with no fallback a Telegram
// outage means every notification retries until MaxAttempts and then sits in
// 'failed'. It is still not lost, but nobody is told. Wiring should log at
// warn when it passes nil here.
func NewWorker(store Store, primary, fallback Transport, metrics Metrics, logger *slog.Logger, cfg WorkerConfig) (*Worker, error) {
	if store == nil {
		return nil, ErrStoreUnavailable
	}
	if primary == nil {
		return nil, ErrNoTransport
	}
	cfg = cfg.withDefaults()
	if metrics == nil {
		metrics = nopMetrics{}
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Worker{
		store:    store,
		primary:  primary,
		fallback: fallback,
		metrics:  metrics,
		logger:   logger,
		cfg:      cfg,
		suppress: newSuppressor(cfg.RateLimit, cfg.RateWindow, cfg.Now),
		queue:    make(chan Notification, cfg.QueueCapacity),
	}, nil
}

// Run starts the fixed worker pool and the single claimer, and blocks until
// ctx is cancelled and every in-flight delivery has finished.
func (w *Worker) Run(ctx context.Context) error {
	var workers sync.WaitGroup
	workers.Add(w.cfg.Workers)
	for i := 0; i < w.cfg.Workers; i++ {
		go func() {
			defer workers.Done()
			w.consume(ctx)
		}()
	}

	w.claimLoop(ctx)

	// The claimer has stopped, so nothing more enters the queue. Closing it
	// lets the workers drain what is already buffered before returning; those
	// rows are leased and would otherwise wait out the lease for nothing.
	close(w.queue)
	workers.Wait()
	return ctx.Err()
}

// claimLoop is the only goroutine that talks to Claim. Keeping it single means
// the suppression window is evaluated in one place, in claim order, so
// "the first 20 in the window" is a well-defined set.
func (w *Worker) claimLoop(ctx context.Context) {
	timer := time.NewTimer(0)
	if !timer.Stop() {
		<-timer.C
	}
	for {
		claimed, err := w.tick(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			w.logger.WarnContext(ctx, "notification claim failed", "error", err)
		}

		// A full batch means there is probably more waiting; go straight back
		// round rather than sleeping through a storm.
		wait := w.cfg.PollInterval
		if claimed >= w.cfg.ClaimBatch && err == nil {
			wait = 0
		}
		if wait == 0 {
			select {
			case <-ctx.Done():
				return
			default:
				continue
			}
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// tick claims one batch, applies admission, and hands the survivors to the
// queue. It returns the number of rows claimed.
func (w *Worker) tick(ctx context.Context) (int, error) {
	w.flushSuppressionNotice(ctx)

	// Never lease more than the queue can accept. Rows left in the table cost
	// nothing; rows leased and unqueued are invisible for a whole lease.
	room := w.cfg.QueueCapacity - len(w.queue)
	if room > w.cfg.ClaimBatch {
		room = w.cfg.ClaimBatch
	}
	if room < 1 {
		return 0, nil
	}

	now := w.cfg.Now()
	batch, err := w.store.Claim(ctx, room, w.cfg.ClaimLease, now)
	if err != nil {
		return 0, err
	}
	for _, n := range batch {
		if !w.suppress.Admit(n) {
			w.metrics.IncNotificationSuppressed(string(n.Tier))
			if markErr := w.store.MarkTerminal(ctx, n.ID, StatusSuppressed,
				"suppressed: more than "+strconv.Itoa(w.cfg.RateLimit)+" notifications in one window", w.cfg.Now()); markErr != nil {
				w.logger.WarnContext(ctx, "recording suppression failed", "error", markErr, "notification_id", n.ID)
			}
			continue
		}
		select {
		case w.queue <- n:
		case <-ctx.Done():
			return len(batch), ctx.Err()
		}
	}
	return len(batch), nil
}

// flushSuppressionNotice emits the single §11.4 collapse message for a window
// that has closed.
//
// The notice is emitted when the window rolls, not when the cap is first
// exceeded, because the count has to be final to be true: "N further
// notifications suppressed" is only useful if N is the real total. Nothing
// urgent is delayed by this — ACTION REQUIRED is never suppressed, so it never
// waits behind the notice.
//
// It goes through the outbox like everything else (EnqueueOnce, keyed by the
// window) so the notice itself cannot be lost, and cannot be sent twice after
// a restart.
func (w *Worker) flushSuppressionNotice(ctx context.Context) {
	count, window, ok := w.suppress.takeNotice()
	if !ok {
		return
	}
	key := suppressionDedupKey(window)
	notice := Notification{
		Tier:        TierImportant,
		Subject:     fmt.Sprintf(suppressionNoticeFormat, count),
		Fingerprint: key,
		DedupKey:    key,
		CreatedAt:   w.cfg.Now(),
	}
	if _, err := w.store.EnqueueOnce(ctx, notice); err != nil {
		w.logger.WarnContext(ctx, "enqueueing suppression notice failed", "error", err, "suppressed", count)
	}
}

// suppressionDedupKey names one suppression window. The prefix is also what
// exempts the notice from being suppressed itself — see suppressor.Admit.
func suppressionDedupKey(window time.Time) string {
	return suppressionKeyPrefix + strconv.FormatInt(window.UTC().Unix(), 10)
}

const suppressionKeyPrefix = "ops:notification-suppression:"

// consume is one worker. There are exactly WorkerConfig.Workers of these.
func (w *Worker) consume(ctx context.Context) {
	for n := range w.queue {
		w.enter()
		w.deliver(ctx, n)
		w.leave()
	}
}

func (w *Worker) enter() {
	w.mu.Lock()
	w.inflight++
	if w.inflight > w.peak {
		w.peak = w.inflight
	}
	w.mu.Unlock()
}

func (w *Worker) leave() {
	w.mu.Lock()
	w.inflight--
	w.mu.Unlock()
}

// PeakConcurrency reports the largest number of simultaneous deliveries seen.
// Exported for the boundedness test and for wiring to log it at shutdown; it
// must never exceed WorkerConfig.Workers.
func (w *Worker) PeakConcurrency() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.peak
}

// deliver runs one notification through the transport chain.
//
// This function is where "a Telegram send failure must never lose a
// notification" is actually enforced, and it does so by never having a path
// that both fails and closes the row:
//
//   - primary succeeds        -> delivered via telegram
//   - primary fails, email ok -> delivered via email, delivery_error retains
//     the Telegram failure so the fallback is visible afterwards
//   - both fail, attempts left -> back to 'pending' with a backoff
//   - both fail, attempts gone -> 'failed', still a durable, queryable row
func (w *Worker) deliver(ctx context.Context, n Notification) {
	primaryErr := w.attempt(ctx, w.primary, n)
	if primaryErr == nil {
		w.metrics.IncNotificationDelivered(string(n.Tier), w.primary.Name())
		if err := w.store.MarkDelivered(ctx, n.ID, w.primary.Name(), "", w.cfg.Now()); err != nil {
			// The message went out; only the bookkeeping failed. The lease
			// will expire and it will be sent again — a duplicate ops message
			// is a far better outcome than a silent loss, so this is logged
			// and not otherwise compensated.
			w.logger.ErrorContext(ctx, "recording delivery failed; notification may resend",
				"error", err, "notification_id", n.ID)
		}
		return
	}
	w.metrics.IncNotificationFailed(string(n.Tier), w.primary.Name())
	w.logger.WarnContext(ctx, "primary notification transport failed",
		"error", primaryErr, "notification_id", n.ID, "tier", string(n.Tier))

	if w.fallback == nil {
		primaryErr = fmt.Errorf("%s: %w; no fallback transport configured", w.primary.Name(), primaryErr)
	} else if fallbackErr := w.attempt(ctx, w.fallback, n); fallbackErr == nil {
		w.metrics.IncNotificationDelivered(string(n.Tier), w.fallback.Name())
		// The Telegram error is written even though the row is delivered. A
		// fallback that hides the failure it worked around trains the operator
		// to believe Telegram is healthy.
		if err := w.store.MarkDelivered(ctx, n.ID, w.fallback.Name(), primaryErr.Error(), w.cfg.Now()); err != nil {
			w.logger.ErrorContext(ctx, "recording fallback delivery failed; notification may resend",
				"error", err, "notification_id", n.ID)
		}
		return
	} else {
		w.metrics.IncNotificationFailed(string(n.Tier), w.fallback.Name())
		primaryErr = fmt.Errorf("%s: %w; %s: %v", w.primary.Name(), primaryErr, w.fallback.Name(), fallbackErr)
	}

	if n.Attempts >= w.cfg.MaxAttempts {
		if err := w.store.MarkTerminal(ctx, n.ID, StatusFailed, primaryErr.Error(), w.cfg.Now()); err != nil {
			w.logger.ErrorContext(ctx, "recording notification failure failed", "error", err, "notification_id", n.ID)
		}
		w.logger.ErrorContext(ctx, "notification exhausted every transport",
			"notification_id", n.ID, "tier", string(n.Tier), "attempts", n.Attempts, "error", primaryErr)
		return
	}
	next := w.cfg.Now().Add(w.backoff(n.Attempts))
	if err := w.store.MarkRetry(ctx, n.ID, primaryErr.Error(), next, w.cfg.Now()); err != nil {
		w.logger.ErrorContext(ctx, "rescheduling notification failed", "error", err, "notification_id", n.ID)
	}
}

// attempt bounds one transport call and converts a panic-free failure into an
// error. A transport that returns nil without proving delivery would lose the
// notification; that contract is stated on Transport.
func (w *Worker) attempt(ctx context.Context, t Transport, n Notification) error {
	if t == nil {
		return ErrNoTransport
	}
	scoped, cancel := context.WithTimeout(ctx, w.cfg.DeliverTimeout)
	defer cancel()
	err := t.Deliver(scoped, n)
	if err == nil {
		return nil
	}
	return err
}

// backoff doubles from BaseBackoff, capped. attempts is the count including
// the one that just failed, so attempt 1 waits BaseBackoff.
func (w *Worker) backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := w.cfg.BaseBackoff
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= w.cfg.MaxBackoff {
			return w.cfg.MaxBackoff
		}
	}
	return d
}

// DrainOnce runs exactly one claim-and-deliver pass on the calling goroutine.
// It exists for tests and for a one-shot flush at shutdown; production uses
// Run. Deliveries are still bounded, because the batch is bounded.
func (w *Worker) DrainOnce(ctx context.Context) error {
	claimed, err := w.tick(ctx)
	if err != nil {
		return err
	}
	for i := 0; i < claimed; i++ {
		select {
		case n, ok := <-w.queue:
			if !ok {
				return nil
			}
			w.enter()
			w.deliver(ctx, n)
			w.leave()
		default:
			return nil
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Suppression, spec §11.4.
// ---------------------------------------------------------------------------

// suppressor implements the "more than 20 in one minute" rule.
//
// Three properties are required and all three are structural here:
//
//   - INFO is dropped first. It is dropped so completely that it never reaches
//     admission at all: an INFO notification is enqueued as 'digest' and the
//     claim query cannot return it (migration 000059 makes that a CHECK). So
//     the entire suppression budget belongs to IMPORTANT and ACTION REQUIRED.
//   - ACTION REQUIRED is never dropped. It consumes a slot, so a burst of them
//     can push the window past its nominal cap; that is the correct trade —
//     the cap protects against noise, and an action-required notification is
//     by definition not noise.
//   - the remainder collapses into ONE message. The window's suppressed count
//     accumulates and is emitted once, when the window closes.
type suppressor struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	now    func() time.Time

	windowStart time.Time
	sent        int
	suppressed  int

	// pending is the suppressed total of a *closed* window awaiting its
	// notice, and pendingWindow names that window so the notice can be keyed
	// idempotently.
	pending       int
	pendingWindow time.Time
}

func newSuppressor(limit int, window time.Duration, now func() time.Time) *suppressor {
	if limit < 1 {
		limit = defaultRateLimit
	}
	if window <= 0 {
		window = defaultRateWindow
	}
	if now == nil {
		now = time.Now
	}
	return &suppressor{limit: limit, window: window, now: now}
}

// Admit reports whether a notification may be sent now.
func (s *suppressor) Admit(n Notification) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollLocked(s.now())

	// The suppression notice itself is exempt. Suppressing the message that
	// explains the suppression is the one failure mode this rule must not
	// have.
	if isSuppressionNotice(n) {
		s.sent++
		return true
	}
	if n.Tier == TierActionRequired {
		s.sent++
		return true
	}
	if s.sent >= s.limit {
		s.suppressed++
		return false
	}
	s.sent++
	return true
}

// takeNotice returns the suppressed total of a closed window, once.
func (s *suppressor) takeNotice() (int, time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollLocked(s.now())
	if s.pending == 0 {
		return 0, time.Time{}, false
	}
	count, window := s.pending, s.pendingWindow
	s.pending, s.pendingWindow = 0, time.Time{}
	return count, window, true
}

// rollLocked advances the window if it has expired, carrying any suppressed
// count into pending so it is reported rather than forgotten.
func (s *suppressor) rollLocked(now time.Time) {
	if s.windowStart.IsZero() {
		s.windowStart = now
		return
	}
	if now.Sub(s.windowStart) < s.window {
		return
	}
	if s.suppressed > 0 {
		// A previous window's unreported count is added, not replaced: two
		// saturated windows in a row must not lose the first one's total.
		s.pending += s.suppressed
		if s.pendingWindow.IsZero() {
			s.pendingWindow = s.windowStart
		}
	}
	s.windowStart = now
	s.sent = 0
	s.suppressed = 0
}

func isSuppressionNotice(n Notification) bool {
	return len(n.DedupKey) >= len(suppressionKeyPrefix) && n.DedupKey[:len(suppressionKeyPrefix)] == suppressionKeyPrefix ||
		len(n.Fingerprint) >= len(suppressionKeyPrefix) && n.Fingerprint[:len(suppressionKeyPrefix)] == suppressionKeyPrefix
}

package notifications

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"
)

type WorkerOptions struct {
	Store            Store
	Transport        Transport
	Channel          string
	BatchSize        int
	MaxConcurrent    int
	LeaseDuration    time.Duration
	BaseRetryDelay   time.Duration
	MaxRetryDelay    time.Duration
	AccountGuard     AccountGuard
	PreferenceGuard  PreferenceGuard
	EntitlementGuard EntitlementGuard
	// AllowedCategories is an explicit delivery allow-list. A non-nil empty
	// map disables the channel. This is a final delivery-layer safety gate, so
	// dormant producers cannot accidentally contact users.
	AllowedCategories map[string]bool
	Now               func() time.Time
	Jitter            func(time.Duration) time.Duration
}

type Worker struct {
	opts WorkerOptions
}

func NewWorker(opts WorkerOptions) (Worker, error) {
	if opts.Store == nil {
		return Worker{}, errors.New("notification store is required")
	}
	if opts.Transport == nil {
		return Worker{}, errors.New("notification transport is required")
	}
	if opts.Channel == "" {
		return Worker{}, errors.New("notification channel is required")
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 50
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 4
	}
	if opts.MaxConcurrent > opts.BatchSize {
		opts.MaxConcurrent = opts.BatchSize
	}
	if opts.LeaseDuration <= 0 {
		opts.LeaseDuration = 2 * time.Minute
	}
	if opts.BaseRetryDelay <= 0 {
		opts.BaseRetryDelay = time.Minute
	}
	if opts.MaxRetryDelay <= 0 {
		opts.MaxRetryDelay = 30 * time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Jitter == nil {
		opts.Jitter = func(delay time.Duration) time.Duration {
			return time.Duration(rand.Int64N(max(int64(delay/5), 1)))
		}
	}
	return Worker{opts: opts}, nil
}

func (w Worker) RunOnce(ctx context.Context) (int, error) {
	processed := 0
	remaining := w.opts.BatchSize
	var runErrors []error
	for remaining > 0 {
		claimSize := min(remaining, w.opts.MaxConcurrent)
		messages, err := w.opts.Store.ClaimBatch(ctx, ClaimOptions{
			Channel:       w.opts.Channel,
			BatchSize:     claimSize,
			Now:           w.opts.Now().UTC(),
			LeaseDuration: w.opts.LeaseDuration,
		})
		if err != nil {
			runErrors = append(runErrors, err)
			break
		}
		if len(messages) == 0 {
			break
		}
		processed += len(messages)
		remaining -= len(messages)
		runErrors = append(runErrors, w.processBatch(ctx, messages))
		if len(messages) < claimSize {
			break
		}
	}
	return processed, errors.Join(runErrors...)
}

func (w Worker) processBatch(ctx context.Context, messages []Message) error {
	semaphore := make(chan struct{}, w.opts.MaxConcurrent)
	errorsByMessage := make([]error, len(messages))
	done := make(chan int, len(messages))
	for index, message := range messages {
		semaphore <- struct{}{}
		go func(index int, message Message) {
			defer func() {
				<-semaphore
				done <- index
			}()
			errorsByMessage[index] = w.process(ctx, message)
		}(index, message)
	}
	for range messages {
		<-done
	}
	return errors.Join(errorsByMessage...)
}

func (w Worker) process(ctx context.Context, message Message) error {
	now := w.opts.Now().UTC()
	if w.opts.AllowedCategories != nil && !w.opts.AllowedCategories[message.Category] {
		return w.opts.Store.Suppress(ctx, message, "notification category disabled by launch policy", now)
	}
	if strings.TrimSpace(message.DedupeKey) == "" {
		return w.opts.Store.Suppress(ctx, message, "notification idempotency key is missing", now)
	}
	if decision, err := w.checkGuards(ctx, message); err != nil {
		return w.retry(ctx, message, "eligibility_check_failed: "+err.Error(), "eligibility")
	} else if !decision.Allowed {
		reason := decision.Reason
		if reason == "" {
			reason = "notification suppressed by current account policy"
		}
		return w.opts.Store.Suppress(ctx, message, reason, now)
	} else if decision.DeferUntil.After(now) {
		reason := decision.Reason
		if reason == "" {
			reason = "delivery deferred by current notification policy"
		}
		return w.opts.Store.Reschedule(ctx, message, decision.DeferUntil.UTC(), reason, now)
	}

	delivery, err := w.opts.Transport.Send(ctx, DeliveryRequest{
		Message:        message,
		IdempotencyKey: message.DedupeKey,
	})
	if err != nil {
		if IsPermanentFault(err) {
			// The provider read the message and refused it -- an invalid
			// recipient, a rejected body. No provider will accept it and
			// attempt six fails exactly like attempt one, so burning the
			// retry budget only delays the truth by half an hour.
			return w.opts.Store.Suppress(ctx, message, "notification rejected as undeliverable: "+string(FaultOf(err)), w.opts.Now().UTC())
		}
		return w.retry(ctx, message, err.Error(), "transport")
	}
	return w.opts.Store.MarkSent(ctx, message, delivery, w.opts.Now().UTC())
}

func (w Worker) checkGuards(ctx context.Context, message Message) (GuardDecision, error) {
	combined := GuardDecision{Allowed: true}
	checks := []func(context.Context, Message) (GuardDecision, error){}
	if w.opts.AccountGuard != nil {
		checks = append(checks, w.opts.AccountGuard.CheckAccount)
	}
	if w.opts.PreferenceGuard != nil {
		checks = append(checks, w.opts.PreferenceGuard.CheckPreference)
	}
	if w.opts.EntitlementGuard != nil {
		checks = append(checks, w.opts.EntitlementGuard.CheckEntitlement)
	}
	for _, check := range checks {
		decision, err := check(ctx, message)
		if err != nil || !decision.Allowed {
			return decision, err
		}
		if decision.DeferUntil.After(combined.DeferUntil) {
			combined.DeferUntil = decision.DeferUntil
			combined.Reason = decision.Reason
		}
	}
	return combined, nil
}

func (w Worker) retry(ctx context.Context, message Message, reason, category string) error {
	delay := retryDelay(w.opts.BaseRetryDelay, w.opts.MaxRetryDelay, message.Attempts)
	delay += w.opts.Jitter(delay)
	status, err := w.opts.Store.MarkRetry(ctx, message, Retry{
		Reason:        reason,
		ErrorCategory: category,
		AvailableAt:   w.opts.Now().UTC().Add(delay),
	}, w.opts.Now().UTC())
	if err != nil {
		return err
	}
	if status == "dead_letter" {
		return fmt.Errorf("notification %d dead-lettered after %d attempts: %s", message.ID, message.Attempts, reason)
	}
	return nil
}

func retryDelay(base, maximum time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	exponent := min(attempt-1, 20)
	multiplier := math.Pow(2, float64(exponent))
	delay := time.Duration(float64(base) * multiplier)
	if delay > maximum || delay < 0 {
		return maximum
	}
	return delay
}

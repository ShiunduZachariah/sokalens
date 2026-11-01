package telegram

import (
	"context"
	"errors"
	"sync"
	"time"
)

// RateLimitedSender chunks a rendered reply and delivers the parts under a
// token bucket.
//
// Telegram enforces per-chat rate limits and answers a breach with 429 plus a
// retry_after. The limiter here sits well under any documented ceiling
// (1/second sustained, burst 5) so the 429 path stays exceptional; when one
// does occur the client surfaces retry_after and this sender honours it
// exactly rather than layering its own backoff on top (spec §11.3).
type RateLimitedSender struct {
	client  Client
	metrics Metrics

	mu     sync.Mutex
	tokens float64
	last   time.Time

	ratePerSec float64
	burst      float64
	now        func() time.Time
	sleep      func(context.Context, time.Duration) bool
}

var _ Sender = (*RateLimitedSender)(nil)

const (
	defaultSendRatePerSec = 1.0
	defaultSendBurst      = 5.0
	// maxRetryAfter bounds how long a single send will wait on a 429 before
	// giving up. Without it a hostile or broken retry_after could park the
	// update-handling path indefinitely.
	maxRetryAfter = 60 * time.Second
	// sendAttempts is the total attempts per part: the first, plus one retry
	// after honouring retry_after.
	sendAttempts = 2
)

// NewRateLimitedSender builds a sender. ratePerSec <= 0 selects the default.
func NewRateLimitedSender(client Client, metrics Metrics, ratePerSec float64) (*RateLimitedSender, error) {
	if client == nil {
		return nil, errors.New("telegram: sender requires a client")
	}
	if metrics == nil {
		return nil, errors.New("telegram: sender requires metrics")
	}
	if ratePerSec <= 0 {
		ratePerSec = defaultSendRatePerSec
	}
	burst := defaultSendBurst
	if burst < ratePerSec {
		burst = ratePerSec
	}
	return &RateLimitedSender{
		client:     client,
		metrics:    metrics,
		tokens:     burst,
		ratePerSec: ratePerSec,
		burst:      burst,
		now:        time.Now,
		sleep:      sleepCtx,
	}, nil
}

// Send chunks body and delivers each part in order.
//
// Parts are sent in order and a failed part aborts the rest: half a table is
// worse than a clear failure, and the caller logs the error.
func (s *RateLimitedSender) Send(ctx context.Context, chatID int64, body string) error {
	for _, part := range Chunk(body) {
		if err := s.sendOne(ctx, chatID, part); err != nil {
			return err
		}
	}
	return nil
}

func (s *RateLimitedSender) sendOne(ctx context.Context, chatID int64, part string) error {
	var lastErr error
	for attempt := 0; attempt < sendAttempts; attempt++ {
		if err := s.wait(ctx); err != nil {
			return err
		}
		err := s.client.SendMessage(ctx, chatID, part)
		if err == nil {
			s.metrics.IncMessageSent("info")
			return nil
		}
		lastErr = err

		var apiErr *APIError
		if errors.As(err, &apiErr) {
			if apiErr.Code == 429 && apiErr.RetryAfter > 0 && apiErr.RetryAfter <= maxRetryAfter {
				s.metrics.IncMessageFailed("rate_limited")
				if !s.sleep(ctx, apiErr.RetryAfter) {
					return ctx.Err()
				}
				continue
			}
			s.metrics.IncMessageFailed(reasonFor(apiErr))
			// 400/403 and friends will not succeed on a retry.
			return err
		}
		s.metrics.IncMessageFailed("network")
		return err
	}
	return lastErr
}

// wait blocks until a token is available.
func (s *RateLimitedSender) wait(ctx context.Context) error {
	for {
		s.mu.Lock()
		now := s.now()
		if s.last.IsZero() {
			s.last = now
		}
		s.tokens += now.Sub(s.last).Seconds() * s.ratePerSec
		if s.tokens > s.burst {
			s.tokens = s.burst
		}
		s.last = now
		if s.tokens >= 1 {
			s.tokens--
			s.mu.Unlock()
			return nil
		}
		deficit := time.Duration((1 - s.tokens) / s.ratePerSec * float64(time.Second))
		s.mu.Unlock()

		if deficit <= 0 {
			deficit = time.Millisecond
		}
		if !s.sleep(ctx, deficit) {
			return ctx.Err()
		}
	}
}

// reasonFor maps an API error to a bounded metric label. The vocabulary is
// closed in internal/telemetry; an unlisted value would collapse to "other".
func reasonFor(err *APIError) string {
	switch {
	case err.Code == 403:
		return "forbidden"
	case err.Code == 404:
		return "not_found"
	case err.Code == 429:
		return "rate_limited"
	case err.Code >= 500:
		return "server"
	default:
		return "send"
	}
}

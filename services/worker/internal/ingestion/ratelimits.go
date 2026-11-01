package ingestion

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type rateLimitDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type ProviderRateLimitRepository struct {
	db rateLimitDB
}

type ProviderRateLimitUpdate struct {
	Provider        string
	DailyLimit      int
	DailyRemaining  int
	MinuteLimit     int
	MinuteRemaining int
	ThrottleUntil   *time.Time
	UpdatedAt       time.Time
}

func NewProviderRateLimitRepository(db rateLimitDB) ProviderRateLimitRepository {
	return ProviderRateLimitRepository{db: db}
}

func (r ProviderRateLimitRepository) UpdateFromHeaders(ctx context.Context, update ProviderRateLimitUpdate) error {
	if r.db == nil {
		return errors.New("provider rate limit DB is nil")
	}
	if update.Provider == "" {
		return errors.New("provider is required")
	}
	if update.UpdatedAt.IsZero() {
		update.UpdatedAt = time.Now().UTC()
	}
	_, err := r.db.ExecContext(ctx, `
UPDATE provider_rate_limits
SET
  daily_limit = CASE WHEN $2 > 0 THEN $2 ELSE daily_limit END,
  daily_remaining = CASE WHEN $2 > 0 AND $3 >= 0 THEN $3 ELSE daily_remaining END,
  minute_limit = CASE WHEN $4 > 0 THEN $4 ELSE minute_limit END,
  minute_remaining = CASE WHEN $4 > 0 AND $5 >= 0 THEN $5 ELSE minute_remaining END,
  throttle_until = CASE WHEN CAST($6 AS TIMESTAMPTZ) IS NOT NULL THEN CAST($6 AS TIMESTAMPTZ) ELSE throttle_until END,
  -- Only advance the timestamp when the provider actually told us something.
  --
  -- This row is read back as the authoritative quota, and it is trusted on the
  -- strength of updated_at being recent. Bumping updated_at on a response that
  -- carried no rate-limit headers made a stale reading look fresh, and that is
  -- what deadlocked ingestion on 2026-09-22: yesterday's daily_remaining of 33
  -- kept being re-stamped with today's time, the worker seeded a budget that
  -- was already past its reserve, every provider call was refused, and a
  -- refused call fetches no headers -- so the value that caused the refusals
  -- could never be corrected. Live scores were fifteen hours stale while the
  -- provider dashboard showed the day's quota untouched at 0% used.
  updated_at = CASE WHEN $2 > 0 THEN $7 ELSE updated_at END
WHERE provider = $1`,
		update.Provider,
		update.DailyLimit,
		update.DailyRemaining,
		update.MinuteLimit,
		update.MinuteRemaining,
		update.ThrottleUntil,
		update.UpdatedAt,
	)
	return err
}

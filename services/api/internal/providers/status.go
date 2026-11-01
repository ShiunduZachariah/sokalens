package providers

import (
	"context"
	"database/sql"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/httpserver"
)

type StatusSource struct {
	db *sql.DB
}

func NewStatusSource(db *sql.DB) StatusSource {
	return StatusSource{db: db}
}

func (s StatusSource) ListProviderStatuses(ctx context.Context) ([]httpserver.ProviderStatus, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
  p.provider,
  p.display_name,
  p.enabled,
  CASE
    WHEN NOT p.enabled THEN 'disabled'
    WHEN pr.throttle_until IS NOT NULL AND pr.throttle_until > CURRENT_TIMESTAMP THEN 'throttled'
    ELSE 'ready'
  END AS status,
  pr.throttle_until,
  COALESCE(pr.daily_limit, 0),
  COALESCE(pr.daily_remaining, 0),
  COALESCE(pr.daily_reserve, 0),
  COALESCE(pr.minute_limit, 0),
  COALESCE(pr.minute_remaining, 0),
  COALESCE(pr.minute_reserve, 0),
  COALESCE(pr.updated_at, CURRENT_TIMESTAMP)
FROM providers p
LEFT JOIN provider_rate_limits pr ON pr.provider = p.provider
ORDER BY p.provider`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	statuses := []httpserver.ProviderStatus{}
	for rows.Next() {
		var status httpserver.ProviderStatus
		var throttleUntil sql.NullTime
		var updatedAt any
		if err := rows.Scan(
			&status.Provider,
			&status.DisplayName,
			&status.Enabled,
			&status.Status,
			&throttleUntil,
			&status.Quota.DailyLimit,
			&status.Quota.DailyRemaining,
			&status.Quota.DailyReserve,
			&status.Quota.MinuteLimit,
			&status.Quota.MinuteRemaining,
			&status.Quota.MinuteReserve,
			&updatedAt,
		); err != nil {
			return nil, err
		}
		if throttleUntil.Valid {
			value := throttleUntil.Time
			status.ThrottleUntil = &value
		}
		status.Quota.UpdatedAt = parseDBTime(updatedAt)
		if status.Quota.UpdatedAt.IsZero() {
			status.Quota.UpdatedAt = time.Now().UTC()
		}
		statuses = append(statuses, status)
	}
	return statuses, rows.Err()
}

func parseDBTime(value any) time.Time {
	switch v := value.(type) {
	case time.Time:
		return v
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05-07:00", "2006-01-02 15:04:05"} {
			parsed, err := time.Parse(layout, v)
			if err == nil {
				return parsed
			}
		}
	case []byte:
		return parseDBTime(string(v))
	}
	return time.Time{}
}

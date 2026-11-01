package ingestion

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
)

type requestLogDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type RequestLogRepository struct {
	db requestLogDB
}

type RequestLog struct {
	JobID         int64
	Provider      string
	Endpoint      string
	Method        string
	ParamsHash    string
	StatusCode    int
	QuotaCost     int
	Latency       time.Duration
	ResponseBytes int
	ErrorCategory string
	Reason        string
	Priority      int
	// PriorityClass and EndpointGroup are the request ledger's coordinates
	// (architecture v2 12.1). Both are optional: left empty they are derived
	// from Priority and Endpoint. Set them when the call site knows better than
	// the derivation can -- the numeric priority is a scheduling order, not a
	// quota class, and the two only line up by convention.
	PriorityClass string
	EndpointGroup string
	RateLimit     provider.RateLimitHeaders
	RequestedAt   time.Time
}

func NewRequestLogRepository(db requestLogDB) RequestLogRepository {
	return RequestLogRepository{db: db}
}

// locallyRefused reports that our own governor declined to call the provider,
// so no request was ever made.
//
// Status 0 with a rate_limit category is exactly that and nothing else: the
// daily-reserve and minute-bucket gates return before any HTTP request is
// built, while a rate limit the provider itself applied arrives as status 429.
func locallyRefused(log RequestLog) bool {
	return log.StatusCode == 0 && log.ErrorCategory == "rate_limit"
}

func (r RequestLogRepository) Insert(ctx context.Context, log RequestLog) error {
	if r.db == nil {
		return errors.New("request log DB is nil")
	}
	if log.Provider == "" {
		return errors.New("provider is required")
	}
	if log.Endpoint == "" {
		return errors.New("endpoint is required")
	}
	// A request we refused to make is not a request.
	//
	// Recording it here fed a runaway. Every refusal was written to
	// provider_request_logs and incremented provider_quota_minute.request_count
	// -- the same counter the daily reserve is measured against -- so each
	// refusal made the next one more likely. On 2026-09-22 the ledger showed
	// 1,103,638 api-football requests for the day against a 75,000 limit, of
	// which 1,103,517 were errors: roughly 121 real calls and a million
	// refusals counting themselves. Every provider job had been deferred since
	// midnight, live scores were fifteen hours stale, and 882 matches sat in
	// 'upcoming' with kickoff long past.
	//
	// It also accounted for essentially all of provider_request_logs' growth,
	// which is the larger half of a 13 GB database and the reason the deploy's
	// restore-verified backup takes over an hour.
	//
	// Returning nil rather than an error because nothing failed: the governor
	// did its job, and the caller still gets the deferral error from the client.
	if locallyRefused(log) {
		return nil
	}
	if log.Method == "" {
		log.Method = "GET"
	}
	if log.QuotaCost < 0 {
		return errors.New("quota cost must not be negative")
	}
	if log.RequestedAt.IsZero() {
		log.RequestedAt = time.Now().UTC()
	}
	rateLimitRaw, err := json.Marshal(log.RateLimit.Raw)
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO provider_request_logs (
  provider,
  endpoint,
  job_id,
  method,
  params_hash,
  status_code,
  quota_cost,
  latency_ms,
  response_bytes,
  daily_limit,
  daily_remaining,
  minute_limit,
  minute_remaining,
  rate_limit_headers,
  error_category,
  reason,
  priority,
  requested_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14::jsonb, $15, $16, $17, $18)`,
		log.Provider,
		log.Endpoint,
		nullableRequestLogJobID(log.JobID),
		log.Method,
		log.ParamsHash,
		log.StatusCode,
		log.QuotaCost,
		log.Latency.Milliseconds(),
		log.ResponseBytes,
		log.RateLimit.DailyLimit,
		log.RateLimit.DailyRemaining,
		log.RateLimit.MinuteLimit,
		log.RateLimit.MinuteRemaining,
		string(rateLimitRaw),
		log.ErrorCategory,
		log.Reason,
		log.Priority,
		log.RequestedAt,
	)
	if err != nil {
		return err
	}
	// The per-minute rollup is written on the same call rather than derived
	// later, so it survives the raw log's retention window. It is a second
	// statement, not a data-modifying CTE, because these tests run against
	// SQLite and production runs PostgreSQL; only one of those supports the CTE
	// form, and a ledger exercised in neither is worse than one that costs an
	// extra round trip.
	return r.recordRequest(ctx, log)
}

func nullableRequestLogJobID(jobID int64) any {
	if jobID <= 0 {
		return nil
	}
	return jobID
}

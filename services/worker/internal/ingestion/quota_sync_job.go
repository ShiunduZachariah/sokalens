package ingestion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
)

// QuotaSyncJobKey identifies the periodic provider quota reconciliation.
func QuotaSyncJobKey() string { return "provider.api-football.quota-sync" }

// QuotaSyncQueue is deliberately not the provider-fixtures queue.
//
// This job's whole purpose is to run when the fixtures queue is refusing to
// run anything, so sharing that queue's exclusive lock and capacity would make
// it unavailable exactly when it is needed.
const QuotaSyncQueue = "provider-reference"

// budgetObserver is the in-memory daily governor this job corrects.
type budgetObserver interface {
	Observe(limit, remaining int)
}

type QuotaSyncJobOptions struct {
	Client      ProviderClient
	RequestLogs requestLogger
	RateLimits  rateLimitWriter
	Budget      budgetObserver
	Now         func() time.Time
	Logger      *slog.Logger
}

// QuotaSyncJob reconciles what we believe about the provider's daily quota
// with what the provider says.
//
// WHY THIS EXISTS. Our view of the quota is only ever corrected as a side
// effect of a successful request, which makes it unrecoverable exactly when it
// is wrong in the blocking direction: believing the quota is spent stops the
// requests that would reveal it is not. On 2026-09-22 a stale `remaining` of
// 33 survived the 00:00 UTC reset, and ingestion stayed frozen for fifteen
// hours while the provider's own dashboard showed the day at 0% used.
//
// The daily budget now probes its way out of that within minutes, but a probe
// is a side effect of traffic the governor is refusing, and it does not
// survive a restart: a worker seeds from provider_rate_limits, so a wrong row
// there starts the next process wrong too.
//
// So this asks directly, on a schedule, and writes the answer down. /status
// reports requests.current and requests.limit_day for the account -- the same
// numbers the dashboard shows -- so it is the provider's own accounting rather
// than a reconstruction from ours.
type QuotaSyncJob struct {
	opts QuotaSyncJobOptions
}

func NewQuotaSyncJob(opts QuotaSyncJobOptions) QuotaSyncJob {
	return QuotaSyncJob{opts: opts}
}

func (j QuotaSyncJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Client == nil {
		return errors.New("quota sync client is required")
	}
	now := time.Now
	if j.opts.Now != nil {
		now = j.opts.Now
	}
	logger := j.opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	resp, err := j.opts.Client.Get(ctx, "status", url.Values{})
	if j.opts.RequestLogs != nil {
		log := RequestLog{
			JobID:         job.ID,
			Provider:      nonEmpty(resp.Provider, apifootball.ProviderName),
			Endpoint:      nonEmpty(resp.Endpoint, "status"),
			Method:        "GET",
			StatusCode:    resp.StatusCode,
			QuotaCost:     providerQuotaCost(resp),
			Latency:       resp.Latency,
			ResponseBytes: resp.ResponseBytes,
			RateLimit:     resp.RateLimit,
			Reason:        "quota_sync",
			RequestedAt:   now(),
		}
		if err != nil {
			log.ErrorCategory = categorizeProviderError(resp, err)
		}
		if logErr := j.opts.RequestLogs.Insert(ctx, log); logErr != nil {
			logger.WarnContext(ctx, "could not log quota sync request", "error", logErr)
		}
	}
	if err != nil {
		return fmt.Errorf("fetch provider status: %w", err)
	}

	status, err := apifootball.ParseStatus(resp.Body)
	if err != nil {
		return fmt.Errorf("parse provider status: %w", err)
	}

	limit := status.Requests.LimitDay
	used := status.Requests.Current
	if limit <= 0 {
		// Without a limit there is no remaining to compute, and writing a
		// guess here is how a wrong number becomes authoritative.
		logger.WarnContext(ctx, "provider status reported no daily limit; leaving quota state untouched",
			"plan", status.Subscription.Plan)
		return nil
	}
	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}

	if j.opts.Budget != nil {
		j.opts.Budget.Observe(limit, remaining)
	}
	if j.opts.RateLimits != nil {
		if err := j.opts.RateLimits.UpdateFromHeaders(ctx, ProviderRateLimitUpdate{
			Provider:   apifootball.ProviderName,
			DailyLimit: limit,
			// Persisted so the next worker start seeds from the provider's
			// accounting rather than from our own request log.
			DailyRemaining: remaining,
			UpdatedAt:      now().UTC(),
		}); err != nil {
			return fmt.Errorf("persist provider quota: %w", err)
		}
	}

	logger.InfoContext(ctx, "provider quota reconciled",
		"plan", status.Subscription.Plan,
		"daily_limit", limit,
		"used", used,
		"remaining", remaining,
		"active", status.Subscription.Active)
	return nil
}

// budgetObserverFor returns the client's in-memory budget when it has one.
//
// The adapter is handed a ProviderClient, which is only Get. A real
// *apifootball.Client also carries the daily governor, and correcting that
// governor is the difference between a wrong belief lasting an hour and a
// wrong belief lasting until someone restarts the worker. A client that cannot
// be corrected is not an error: the job still writes the provider's number
// down, and the next start reads it.
func budgetObserverFor(client ProviderClient) budgetObserver {
	if observer, ok := client.(budgetObserver); ok {
		return observer
	}
	return nil
}

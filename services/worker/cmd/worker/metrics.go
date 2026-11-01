package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/ingestion"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/metrics"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/quota"
)

// startMetricsServer serves the worker Prometheus text-format endpoint on
// address (defaults to 127.0.0.1:9091; never expose it publicly).
func startMetricsServer(address string, registry *metrics.Registry) *http.Server {
	server := &http.Server{
		Addr:              address,
		Handler:           registry.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		slog.Info("worker metrics endpoint listening", "address", address)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("worker metrics endpoint", "error", err)
		}
	}()
	return server
}

// runMetricsRefresh periodically refreshes DB-derived gauges so a scrape shows
// provider quota state, today's request accounting by reason, and provider
// queue depth — without needing in-process counters for every code path.
func runMetricsRefresh(ctx context.Context, db *sql.DB, registry *metrics.Registry, interval time.Duration, planLimit int) {
	if db == nil || registry == nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	refresh := func() {
		refreshCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		refreshProviderRateLimitGauges(refreshCtx, db, registry)
		refreshRequestAccountingGauges(refreshCtx, db, registry)
		refreshQueueDepthGauges(refreshCtx, db, registry)
		refreshQuotaBurnGauges(refreshCtx, db, registry, planLimit)
	}
	refresh()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refresh()
		}
	}
}

func refreshProviderRateLimitGauges(ctx context.Context, db *sql.DB, registry *metrics.Registry) {
	var dailyLimit, dailyRemaining, minuteLimit, minuteRemaining int
	var throttleUntil any
	err := db.QueryRowContext(ctx, `
SELECT COALESCE(daily_limit, 0), COALESCE(daily_remaining, 0),
       COALESCE(minute_limit, 0), COALESCE(minute_remaining, 0),
       throttle_until
FROM provider_rate_limits
WHERE provider = 'api-football'
ORDER BY updated_at DESC
LIMIT 1`).Scan(&dailyLimit, &dailyRemaining, &minuteLimit, &minuteRemaining, &throttleUntil)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("refresh provider rate limit gauges", "error", err)
		}
		return
	}
	registry.SetGauge("worker_provider_daily_limit", float64(dailyLimit))
	registry.SetGauge("worker_provider_daily_remaining", float64(dailyRemaining))
	registry.SetGauge("worker_provider_minute_limit", float64(minuteLimit))
	registry.SetGauge("worker_provider_minute_remaining", float64(minuteRemaining))
	throttleUnix := float64(0)
	if value, ok := throttleUntil.(time.Time); ok && !value.IsZero() {
		throttleUnix = float64(value.Unix())
	}
	registry.SetGauge("worker_provider_throttle_until_unix", throttleUnix)
}

func refreshRequestAccountingGauges(ctx context.Context, db *sql.DB, registry *metrics.Registry) {
	var total, quotaCost, errored, rateLimited int
	err := db.QueryRowContext(ctx, `
SELECT COUNT(*),
       COALESCE(SUM(quota_cost), 0),
       COUNT(*) FILTER (WHERE error_category IS NOT NULL OR status_code >= 400),
       COUNT(*) FILTER (WHERE status_code = 429)
FROM provider_request_logs
WHERE provider = 'api-football'
  AND requested_at >= date_trunc('day', now())`).Scan(&total, &quotaCost, &errored, &rateLimited)
	if err != nil {
		slog.Warn("refresh request accounting gauges", "error", err)
		return
	}
	registry.SetGauge("worker_requests_today_total", float64(total))
	registry.SetGauge("worker_requests_today_quota_cost", float64(quotaCost))
	registry.SetGauge("worker_requests_today_errors", float64(errored))
	registry.SetGauge("worker_requests_today_rate_limited", float64(rateLimited))

	rows, err := db.QueryContext(ctx, `
SELECT COALESCE(reason, 'unknown'), COUNT(*)
FROM provider_request_logs
WHERE provider = 'api-football'
  AND requested_at >= date_trunc('day', now())
GROUP BY reason`)
	if err != nil {
		slog.Warn("refresh request accounting by reason", "error", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var reason string
		var count int
		if err := rows.Scan(&reason, &count); err != nil {
			slog.Warn("scan request accounting by reason", "error", err)
			return
		}
		registry.SetGauge("worker_requests_today_by_reason_total", float64(count))
		// The registry has no label support; encode the reason in the metric
		// name so Prometheus can still aggregate with a single query.
		registry.SetGauge("worker_requests_today_by_reason_"+metrics.SafeMetricName(reason), float64(count))
	}
	if err := rows.Err(); err != nil {
		slog.Warn("iterate request accounting by reason", "error", err)
	}
}

// quotaBurnWarnFraction and quotaBurnCriticalFraction are where today's spend
// stops being information and becomes a warning.
//
// Sixty and eighty percent, not a hundred. An alert at the ceiling tells an
// operator that the thing they were meant to prevent has happened; there is no
// action left but to wait for midnight. Sixty percent of a 7,500 package is
// 4,500 requests, which on a normal day is reached in the evening and on a
// runaway day is reached before noon -- and the difference between those two
// is the entire signal.
const (
	quotaBurnWarnFraction     = 0.60
	quotaBurnCriticalFraction = 0.80
)

// refreshQuotaBurnGauges publishes today's provider spend per endpoint group
// against the daily package, and warns on the way up.
//
// Per endpoint group, because "we are at 80%" is not an actionable sentence.
// The actionable one is "we are at 80% and backfill is two thirds of it", and
// that distinction decides whether an operator pauses discretionary work or
// starts looking for a loop. The ledger already keys spend this way per
// minute; this is the daily rollup of it.
func refreshQuotaBurnGauges(ctx context.Context, db *sql.DB, registry *metrics.Registry, planLimit int) {
	rows, err := db.QueryContext(ctx, `
SELECT endpoint_group, COALESCE(SUM(request_count), 0)
FROM provider_quota_minute
WHERE provider = 'api-football'
  AND minute_bucket >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
GROUP BY endpoint_group`)
	if err != nil {
		slog.Warn("refresh quota burn gauges", "error", err)
		return
	}
	defer rows.Close()
	groups := map[string]int{}
	total := 0
	for rows.Next() {
		var group string
		var count int
		if err := rows.Scan(&group, &count); err != nil {
			slog.Warn("scan quota burn group", "error", err)
			return
		}
		groups[group] = count
		total += count
	}
	if err := rows.Err(); err != nil {
		slog.Warn("iterate quota burn groups", "error", err)
		return
	}

	// Publish a zero for every known group, not only the ones that appear.
	// A group absent from the result is a group that spent nothing, and
	// leaving its gauge at yesterday's value is how a quiet endpoint comes to
	// look like a busy one.
	for _, group := range []string{
		ingestion.EndpointGroupLive,
		ingestion.EndpointGroupFixtures,
		ingestion.EndpointGroupOdds,
		ingestion.EndpointGroupReference,
		ingestion.EndpointGroupBackfill,
		ingestion.EndpointGroupOther,
	} {
		registry.SetGauge("worker_quota_today_endpoint_"+metrics.SafeMetricName(group), float64(groups[group]))
	}
	registry.SetGauge("worker_quota_today_requests", float64(total))
	registry.SetGauge("worker_quota_plan_limit", float64(planLimit))
	if planLimit <= 0 {
		return
	}
	fraction := float64(total) / float64(planLimit)
	registry.SetGauge("worker_quota_today_fraction", fraction)

	attrs := []any{
		"requests_today", total,
		"plan_limit", planLimit,
		"fraction", fraction,
		"live", groups[ingestion.EndpointGroupLive],
		"fixtures", groups[ingestion.EndpointGroupFixtures],
		"odds", groups[ingestion.EndpointGroupOdds],
		"reference", groups[ingestion.EndpointGroupReference],
		"backfill", groups[ingestion.EndpointGroupBackfill],
		"other", groups[ingestion.EndpointGroupOther],
	}
	switch {
	case fraction >= quotaBurnCriticalFraction:
		slog.Error("provider daily quota burn critical", attrs...)
	case fraction >= quotaBurnWarnFraction:
		slog.Warn("provider daily quota burn elevated", attrs...)
	}
}

func refreshQueueDepthGauges(ctx context.Context, db *sql.DB, registry *metrics.Registry) {
	rows, err := db.QueryContext(ctx, `
SELECT queue, COUNT(*)
FROM jobs
WHERE status = 'queued'
GROUP BY queue`)
	if err != nil {
		slog.Warn("refresh queue depth gauges", "error", err)
		return
	}
	defer rows.Close()
	depths := make(map[string]int)
	for rows.Next() {
		var queue string
		var depth int
		if err := rows.Scan(&queue, &depth); err != nil {
			slog.Warn("scan queue depth", "error", err)
			return
		}
		depths[queue] = depth
	}
	if err := rows.Err(); err != nil {
		slog.Warn("iterate queue depth", "error", err)
		return
	}
	publishQueueDepthGauges(registry, depths)
}

func publishQueueDepthGauges(registry *metrics.Registry, depths map[string]int) {
	// An empty queue is absent from the grouped SQL result. Publish its zero
	// explicitly so alert recovery sees healthy telemetry after the last job
	// drains or the worker restarts. Only call this after a successful DB read.
	for queue, depth := range depths {
		registry.SetGauge("worker_jobs_queued_"+metrics.SafeMetricName(queue), float64(depth))
	}
	if _, present := depths["data-backfill"]; !present {
		registry.SetGauge("worker_jobs_queued_data_backfill", 0)
	}
}

// publishQuotaDecision exports the latest planner decision as gauges so
// operators can see projected demand vs remaining quota on every scrape.
func publishQuotaDecision(registry *metrics.Registry, decision quota.Decision) {
	if registry == nil {
		return
	}
	registry.SetGauge("worker_quota_daily_limit", float64(decision.DailyLimit))
	registry.SetGauge("worker_quota_daily_remaining", float64(decision.DailyRemaining))
	registry.SetGauge("worker_quota_projected_live", float64(decision.ProjectedLive))
	registry.SetGauge("worker_quota_projected_prematch", float64(decision.ProjectedPrematch))
	registry.SetGauge("worker_quota_projected_prediction", float64(decision.ProjectedPrediction))
	registry.SetGauge("worker_quota_projected_inplay_statistics", float64(decision.ProjectedInPlayStats))
	registry.SetGauge("worker_quota_projected_fixed", float64(decision.ProjectedFixed))
	registry.SetGauge("worker_quota_projected_reference", float64(decision.ProjectedReference))
	registry.SetGauge("worker_quota_emergency_reserve", float64(decision.EmergencyReserve))
	registry.SetGauge("worker_quota_safe_discretionary", float64(decision.SafeDiscretionary))
	// The pacer target. Published because "allowed" was already observable and
	// the platform still spent 12,000 of 75,000 a day -- what was missing from
	// the dashboards was any number saying how much we intended to spend.
	registry.SetGauge("worker_quota_ballast_per_minute", float64(decision.BallastPerMinute))
	registry.SetGauge("worker_quota_minutes_left_today", float64(decision.MinutesLeftToday))
	// Projected end-of-day spend if the current rate holds. The single number
	// that answers "will we waste quota tonight?".
	registry.SetGauge("worker_quota_projected_ballast_today", float64(decision.BallastPerMinute*decision.MinutesLeftToday))
	registry.SetGauge("worker_quota_pressure", float64(decision.Pressure))
	backfillPaused := 0.0
	if decision.BackfillPaused {
		backfillPaused = 1.0
	}
	registry.SetGauge("worker_quota_backfill_paused", backfillPaused)
	registry.SetGauge("worker_quota_live_poll_interval_seconds", decision.LivePollInterval.Seconds())
}

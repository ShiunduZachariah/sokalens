// Package alertevaluator turns a small fixed set of Prometheus conditions into
// durable incidents and notifications. It deliberately has no dynamic rule
// language: changing an operational threshold remains a reviewed code change.
package alertevaluator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/alerts"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/incidents"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/notifications"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/sysmetrics"
)

const source = "prometheus"

type Queryer interface {
	Query(context.Context, string) ([]sysmetrics.Sample, error)
}

type IncidentStore interface {
	Observe(context.Context, incidents.Observation) (incidents.Result, error)
}

type NotificationStore interface {
	Enqueue(context.Context, notifications.Notification) (notifications.EnqueueResult, error)
}

type RecoveryStore interface {
	ResolveHealthy(context.Context, string, time.Time, time.Time) (int, error)
}

const recoveryWindow = 15 * time.Minute

type Rule struct {
	Name          string
	Service       string
	Query         string
	RecoveryQuery string
	Summary       string
	Severity      alerts.Severity
	Tier          notifications.Tier
}

func DefaultRules() []Rule {
	return []Rule{
		{Name: "scrape_target_down", Service: "observability", Query: `max_over_time(up{job=~"node-exporter|cadvisor|worker|ops-controller"}[2m]) == 0`, Summary: "A production monitoring target has been unavailable for two minutes.", Severity: alerts.SeverityCritical, Tier: notifications.TierActionRequired},
		{Name: "root_disk_low", Service: "host", Query: `max_over_time(((node_filesystem_avail_bytes{mountpoint="/",fstype!~"tmpfs|overlay"} / node_filesystem_size_bytes{mountpoint="/",fstype!~"tmpfs|overlay"}) * 100)[10m:]) < 25`, Summary: "Root filesystem availability has stayed below 25% for ten minutes.", Severity: alerts.SeverityWarning, Tier: notifications.TierImportant},
		{Name: "root_disk_critical", Service: "host", Query: `(node_filesystem_avail_bytes{mountpoint="/",fstype!~"tmpfs|overlay"} / node_filesystem_size_bytes{mountpoint="/",fstype!~"tmpfs|overlay"}) * 100 < 8`, Summary: "Root filesystem availability is below 8%.", Severity: alerts.SeverityCritical, Tier: notifications.TierActionRequired},
		{Name: "host_memory_low", Service: "host", Query: `max_over_time(((node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes) * 100)[10m:]) < 10`, Summary: "Available host memory has stayed below 10% for ten minutes.", Severity: alerts.SeverityWarning, Tier: notifications.TierImportant},
		{Name: "backfill_queue_high", Service: "worker", Query: `min_over_time(worker_jobs_queued_data_backfill[15m]) > 1000`, RecoveryQuery: `(max_over_time(worker_jobs_queued_data_backfill[15m]) < 1000) and on(instance, job) (worker_jobs_queued_data_backfill offset 15m < 1000) and on(instance, job) (count_over_time(worker_jobs_queued_data_backfill[15m]) >= 56) and on(instance, job) (count_over_time(worker_jobs_queued_data_backfill[1m]) >= 2) and on(instance, job) (up{job="worker"} == 1)`, Summary: "Backfill queue has stayed above 1,000 jobs for fifteen minutes.", Severity: alerts.SeverityWarning, Tier: notifications.TierImportant},
		{Name: "backfill_queue_critical", Service: "worker", Query: `worker_jobs_queued_data_backfill > 5000`, RecoveryQuery: `(max_over_time(worker_jobs_queued_data_backfill[15m]) < 5000) and on(instance, job) (worker_jobs_queued_data_backfill offset 15m < 5000) and on(instance, job) (count_over_time(worker_jobs_queued_data_backfill[15m]) >= 56) and on(instance, job) (count_over_time(worker_jobs_queued_data_backfill[1m]) >= 2) and on(instance, job) (up{job="worker"} == 1)`, Summary: "Backfill queue is above 5,000 jobs.", Severity: alerts.SeverityCritical, Tier: notifications.TierActionRequired},
		{Name: "provider_quota_reserved", Service: "worker", Query: `worker_quota_daily_remaining <= worker_quota_emergency_reserve`, RecoveryQuery: `(min_over_time(worker_quota_daily_remaining[15m]) > max_over_time(worker_quota_emergency_reserve[15m])) and on(instance, job) ((worker_quota_daily_remaining offset 15m) > (worker_quota_emergency_reserve offset 15m)) and on(instance, job) (count_over_time(worker_quota_daily_remaining[15m]) >= 56) and on(instance, job) (count_over_time(worker_quota_emergency_reserve[15m]) >= 56) and on(instance, job) (count_over_time(worker_quota_daily_remaining[1m]) >= 2) and on(instance, job) (count_over_time(worker_quota_emergency_reserve[1m]) >= 2) and on(instance, job) (up{job="worker"} == 1)`, Summary: "Provider daily quota has reached the planner emergency reserve.", Severity: alerts.SeverityCritical, Tier: notifications.TierActionRequired},
	}
}

type Evaluator struct {
	queries       Queryer
	incidents     IncidentStore
	notifications NotificationStore
	recovery      RecoveryStore
	rules         []Rule
	logger        *slog.Logger
	now           func() time.Time
}

func New(queries Queryer, incidentStore IncidentStore, notificationStore NotificationStore, recovery RecoveryStore, rules []Rule, logger *slog.Logger, now func() time.Time) (*Evaluator, error) {
	if queries == nil || incidentStore == nil || notificationStore == nil || len(rules) == 0 {
		return nil, errors.New("alert evaluator dependencies are incomplete")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	for _, rule := range rules {
		if rule.RecoveryQuery != "" && recovery == nil {
			return nil, errors.New("recovery store is required for recoverable rules")
		}
	}
	return &Evaluator{queries: queries, incidents: incidentStore, notifications: notificationStore, recovery: recovery, rules: append([]Rule(nil), rules...), logger: logger, now: now}, nil
}

func (e *Evaluator) Evaluate(ctx context.Context) error {
	var failures []error
	for _, rule := range e.rules {
		evaluatedAt := e.now().UTC()
		samples, err := e.queries.Query(ctx, rule.Query)
		if err != nil && !errors.Is(err, sysmetrics.ErrNoData) {
			failures = append(failures, fmt.Errorf("%s: %w", rule.Name, err))
			continue
		}
		if err == nil && len(samples) > 0 {
			if err := e.observe(ctx, rule); err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", rule.Name, err))
			}
			continue
		}
		if rule.RecoveryQuery == "" {
			continue
		}
		healthy, err := e.queries.Query(ctx, rule.RecoveryQuery)
		if errors.Is(err, sysmetrics.ErrNoData) {
			continue // missing or insufficient telemetry is never recovery evidence
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("%s recovery: %w", rule.Name, err))
			continue
		}
		if len(healthy) == 0 {
			continue
		}
		// A fresh firing sample between the first query and the recovery query
		// takes precedence. The transaction also checks last_observed_at.
		refiring, err := e.queries.Query(ctx, rule.Query)
		if err != nil && !errors.Is(err, sysmetrics.ErrNoData) {
			failures = append(failures, fmt.Errorf("%s recheck: %w", rule.Name, err))
			continue
		}
		if err == nil && len(refiring) > 0 {
			if err := e.observe(ctx, rule); err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", rule.Name, err))
			}
			continue
		}
		_, err = e.recovery.ResolveHealthy(ctx, incidents.Fingerprint(rule.Name, rule.Service, "production"), evaluatedAt.Add(-recoveryWindow), evaluatedAt)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s recovery: %w", rule.Name, err))
		}
	}
	return errors.Join(failures...)
}

func (e *Evaluator) observe(ctx context.Context, rule Rule) error {
	now := e.now().UTC()
	eventID := fmt.Sprintf("%s:%d", rule.Name, now.Unix()/60)
	sum := sha256.Sum256([]byte(source + "\x00" + eventID))
	accepted := alerts.Accepted{IdempotencyKey: hex.EncodeToString(sum[:]), DeliveryID: eventID, AlertID: eventID, Source: source, Name: rule.Name, Service: rule.Service, Environment: "production", Severity: rule.Severity, StartsAt: now, Summary: rule.Summary, ReceivedAt: now}
	observation, err := incidents.Classify(accepted)
	if err != nil {
		return err
	}
	result, err := e.incidents.Observe(ctx, observation)
	if err != nil || !result.Created {
		return err
	}
	_, err = e.notifications.Enqueue(ctx, notifications.Notification{Tier: rule.Tier, Subject: observation.Title, Body: observation.Summary, Fingerprint: observation.Fingerprint, IncidentID: result.IncidentID, OccurrenceCount: result.OccurrenceCount, CreatedAt: now})
	return err
}

func (e *Evaluator) Run(ctx context.Context) error {
	if err := e.Evaluate(ctx); err != nil {
		e.logger.Error("alert evaluation failed", "error", err)
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := e.Evaluate(ctx); err != nil {
				e.logger.Error("alert evaluation failed", "error", err)
			}
		}
	}
}

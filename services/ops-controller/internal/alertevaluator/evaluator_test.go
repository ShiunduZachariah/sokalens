package alertevaluator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/alerts"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/incidents"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/notifications"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/sysmetrics"
)

type fakeQueries struct{ firing map[string]bool }

func (f fakeQueries) Query(_ context.Context, query string) ([]sysmetrics.Sample, error) {
	if f.firing[query] {
		return []sysmetrics.Sample{{Value: 1}}, nil
	}
	return nil, sysmetrics.ErrNoData
}

type queryResult struct {
	firing bool
	err    error
}

type sequencedQueries struct {
	results map[string][]queryResult
}

func (q *sequencedQueries) Query(_ context.Context, query string) ([]sysmetrics.Sample, error) {
	responses := q.results[query]
	if len(responses) == 0 {
		return nil, sysmetrics.ErrNoData
	}
	result := responses[0]
	q.results[query] = responses[1:]
	if result.err != nil {
		return nil, result.err
	}
	if result.firing {
		return []sysmetrics.Sample{{Value: 1}}, nil
	}
	return nil, sysmetrics.ErrNoData
}

type fakeRecovery struct {
	calls []struct {
		fingerprint string
		cutoff      time.Time
		at          time.Time
	}
}

func (r *fakeRecovery) ResolveHealthy(_ context.Context, fingerprint string, cutoff, at time.Time) (int, error) {
	r.calls = append(r.calls, struct {
		fingerprint string
		cutoff      time.Time
		at          time.Time
	}{fingerprint, cutoff, at})
	return 1, nil
}

func TestRecoveryRequiresPositiveSustainedSamplesAndRechecksFiring(t *testing.T) {
	now := time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC)
	rule := Rule{Name: "provider_quota_reserved", Service: "worker", Query: "firing", RecoveryQuery: "healthy", Summary: "quota", Severity: alerts.SeverityCritical, Tier: notifications.TierActionRequired}
	for _, test := range []struct {
		name       string
		firing     []queryResult
		healthy    []queryResult
		wantClose  int
		wantReport int
	}{
		{"healthy", []queryResult{{err: sysmetrics.ErrNoData}, {err: sysmetrics.ErrNoData}}, []queryResult{{firing: true}}, 1, 0},
		{"missing telemetry", []queryResult{{err: sysmetrics.ErrNoData}}, []queryResult{{err: sysmetrics.ErrNoData}}, 0, 0},
		{"refiring", []queryResult{{err: sysmetrics.ErrNoData}, {firing: true}}, []queryResult{{firing: true}}, 0, 1},
		{"already firing", []queryResult{{firing: true}}, nil, 0, 1},
		{"prometheus error", []queryResult{{err: errors.New("unavailable")}}, nil, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			queries := &sequencedQueries{results: map[string][]queryResult{"firing": test.firing, "healthy": test.healthy}}
			incidentStore := &fakeIncidents{}
			recovery := &fakeRecovery{}
			evaluator, err := New(queries, incidentStore, &fakeNotifications{}, recovery, []Rule{rule}, nil, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			gotErr := evaluator.Evaluate(context.Background())
			if test.name == "prometheus error" && gotErr == nil {
				t.Fatal("Prometheus error was ignored")
			}
			if test.name != "prometheus error" && gotErr != nil {
				t.Fatal(gotErr)
			}
			if len(recovery.calls) != test.wantClose || len(incidentStore.observations) != test.wantReport {
				t.Fatalf("recovery=%d reports=%d", len(recovery.calls), len(incidentStore.observations))
			}
			if test.wantClose == 1 {
				call := recovery.calls[0]
				if call.fingerprint != incidents.Fingerprint(rule.Name, rule.Service, "production") || !call.cutoff.Equal(now.Add(-15*time.Minute)) || !call.at.Equal(now) {
					t.Fatalf("recovery call=%+v", call)
				}
			}
		})
	}
}

func TestRecoveryRulesRequireFreshWorkerTelemetry(t *testing.T) {
	for _, rule := range DefaultRules() {
		if rule.Name != "backfill_queue_high" && rule.Name != "backfill_queue_critical" && rule.Name != "provider_quota_reserved" {
			continue
		}
		if !strings.Contains(rule.RecoveryQuery, "[15m]") || !strings.Contains(rule.RecoveryQuery, "offset 15m") || !strings.Contains(rule.RecoveryQuery, ">= 56") || !strings.Contains(rule.RecoveryQuery, "[1m]") || !strings.Contains(rule.RecoveryQuery, `up{job="worker"} == 1`) {
			t.Fatalf("%s recovery query lacks sustained healthy samples and a live worker: %q", rule.Name, rule.RecoveryQuery)
		}
	}
}

type fakeIncidents struct {
	observations []incidents.Observation
	created      bool
}

func (f *fakeIncidents) Observe(_ context.Context, observation incidents.Observation) (incidents.Result, error) {
	f.observations = append(f.observations, observation)
	return incidents.Result{IncidentID: "incident-1", OccurrenceCount: int64(len(f.observations)), Created: f.created}, nil
}

type fakeNotifications struct{ sent []notifications.Notification }

func (f *fakeNotifications) Enqueue(_ context.Context, notification notifications.Notification) (notifications.EnqueueResult, error) {
	f.sent = append(f.sent, notification)
	return notifications.EnqueueResult{ID: "notification-1", Created: true, Tier: notification.Tier}, nil
}

func TestEvaluatePersistsEveryFiringObservationAndNotifiesOnlyNewIncident(t *testing.T) {
	now := time.Date(2026, 9, 15, 20, 0, 0, 0, time.UTC)
	rule := Rule{Name: "backfill_queue_high", Service: "worker", Query: "queue-query", Summary: "Backfill queue is above 1,000 for 15 minutes.", Severity: alerts.SeverityWarning, Tier: notifications.TierImportant}
	incidentStore := &fakeIncidents{created: true}
	notificationStore := &fakeNotifications{}
	evaluator, err := New(fakeQueries{firing: map[string]bool{"queue-query": true}}, incidentStore, notificationStore, nil, []Rule{rule}, nil, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := evaluator.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(incidentStore.observations) != 1 || len(notificationStore.sent) != 1 {
		t.Fatalf("observations=%d notifications=%d, want 1/1", len(incidentStore.observations), len(notificationStore.sent))
	}
	got := notificationStore.sent[0]
	if got.Tier != notifications.TierImportant || got.IncidentID != "incident-1" || got.Fingerprint == "" {
		t.Fatalf("notification=%+v, want correlated important notification", got)
	}

	incidentStore.created = false
	now = now.Add(time.Minute)
	if err := evaluator.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(incidentStore.observations) != 2 || len(notificationStore.sent) != 1 {
		t.Fatalf("observations=%d notifications=%d, want 2/1 after correlated repeat", len(incidentStore.observations), len(notificationStore.sent))
	}
}

func TestDefaultRulesCoverServiceResourcesBackfillAndQuota(t *testing.T) {
	rules := DefaultRules()
	for _, name := range []string{"scrape_target_down", "root_disk_low", "host_memory_low", "backfill_queue_high", "provider_quota_reserved"} {
		found := false
		for _, rule := range rules {
			if rule.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("default rules missing %q", name)
		}
	}
	for _, rule := range rules {
		if rule.Name == "root_disk_low" {
			if !strings.Contains(rule.Query, "< 25") || !strings.Contains(rule.Summary, "below 25%") {
				t.Fatalf("root disk warning should fire below 25%% available: %+v", rule)
			}
			return
		}
	}
}

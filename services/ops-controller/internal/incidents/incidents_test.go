package incidents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/alerts"
)

func accepted(id, service, environment, name string, severity alerts.Severity, observed time.Time) alerts.Accepted {
	idempotency := sha256.Sum256([]byte(id))
	return alerts.Accepted{
		IdempotencyKey: hex.EncodeToString(idempotency[:]), DeliveryID: "delivery-" + id,
		AlertID: id, Source: "Prometheus", Name: name, Service: service,
		Environment: environment, Severity: severity, StartsAt: observed,
		Summary: "bounded evidence", ReceivedAt: observed.Add(time.Minute),
	}
}

func TestClassifyNormalizesAndSeparatesIndependentDimensions(t *testing.T) {
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	a, err := Classify(accepted("a", " API ", "PROD", "High_Latency", alerts.SeverityWarning, now))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Classify(accepted("b", "api", "prod", "high_latency", alerts.SeverityCritical, now.Add(time.Minute)))
	c, _ := Classify(accepted("c", "worker", "prod", "high_latency", alerts.SeverityCritical, now))
	d, _ := Classify(accepted("d", "api", "staging", "high_latency", alerts.SeverityCritical, now))
	if a.Fingerprint != b.Fingerprint || a.Service != "api" || a.Environment != "prod" {
		t.Fatalf("equivalent classifications differ: %+v %+v", a, b)
	}
	if a.Fingerprint == c.Fingerprint || a.Fingerprint == d.Fingerprint {
		t.Fatal("unrelated service/environment was correlated")
	}
}

type memoryIncident struct {
	id, fingerprint, source, event string
	severity                       alerts.Severity
	last                           time.Time
	count                          int64
}

type memoryRepository struct {
	mu            sync.Mutex
	window        time.Duration
	next          int
	items         []*memoryIncident
	sourceEvents  map[string]string
	idempotencies map[string]string
}

func (r *memoryRepository) Observe(_ context.Context, o Observation) (Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sourceEvents == nil {
		r.sourceEvents = make(map[string]string)
		r.idempotencies = make(map[string]string)
	}
	sourceKey := o.Source + "\x00" + o.SourceEventID
	incidentID, sourceExists := r.sourceEvents[sourceKey]
	if idempotencyIncident, exists := r.idempotencies[o.IdempotencyKey]; exists {
		incidentID, sourceExists = idempotencyIncident, true
	}
	if sourceExists {
		for _, item := range r.items {
			if item.id == incidentID {
				return Result{IncidentID: item.id, OccurrenceCount: item.count}, nil
			}
		}
	}
	for _, item := range r.items {
		if item.fingerprint == o.Fingerprint && !item.last.Before(o.ObservedAt.Add(-r.window)) && !item.last.After(o.ObservedAt.Add(r.window)) {
			item.count++
			if severityRank(o.Severity) > severityRank(item.severity) {
				item.severity = o.Severity
			}
			if o.ObservedAt.After(item.last) {
				item.last = o.ObservedAt
			}
			r.sourceEvents[sourceKey] = item.id
			r.idempotencies[o.IdempotencyKey] = item.id
			return Result{IncidentID: item.id, OccurrenceCount: item.count}, nil
		}
	}
	r.next++
	item := &memoryIncident{id: string(rune('A' + r.next - 1)), fingerprint: o.Fingerprint, source: o.Source, event: o.SourceEventID, severity: o.Severity, last: o.ObservedAt, count: 1}
	r.items = append(r.items, item)
	r.sourceEvents[sourceKey] = item.id
	r.idempotencies[o.IdempotencyKey] = item.id
	return Result{IncidentID: item.id, OccurrenceCount: 1, Created: true}, nil
}

func severityRank(severity alerts.Severity) int {
	switch severity {
	case alerts.SeverityCritical:
		return 3
	case alerts.SeverityWarning:
		return 2
	default:
		return 1
	}
}

func TestConcurrentDuplicateStormConvergesWithoutCountInflation(t *testing.T) {
	repository := &memoryRepository{window: time.Hour}
	sink := NewSink(repository)
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			if err := sink.Accept(context.Background(), accepted("source-event", "api", "production", "latency", alerts.SeverityWarning, now)); err != nil {
				t.Errorf("Accept: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if len(repository.items) != 1 || repository.items[0].count != 1 || repository.items[0].severity != alerts.SeverityWarning {
		t.Fatalf("duplicate storm inflated state: %+v", repository.items)
	}
}

func TestSourceEventRetryWithNewTransportIdentityDoesNotIncrement(t *testing.T) {
	repository := &memoryRepository{window: time.Hour}
	sink := NewSink(repository)
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	first := accepted("source-event", "api", "production", "latency", alerts.SeverityWarning, now)
	retry := first
	retry.DeliveryID = "replacement-delivery"
	digest := sha256.Sum256([]byte("replacement-idempotency"))
	retry.IdempotencyKey = hex.EncodeToString(digest[:])
	if err := sink.Accept(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := sink.Accept(context.Background(), retry); err != nil {
		t.Fatal(err)
	}
	if len(repository.items) != 1 || repository.items[0].count != 1 {
		t.Fatalf("source event retry inflated state: %+v", repository.items)
	}
}

func TestConcurrentDistinctObservationsConvergeAndEscalate(t *testing.T) {
	repository := &memoryRepository{window: time.Hour}
	sink := NewSink(repository)
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			severity := alerts.SeverityInfo
			if index == 99 {
				severity = alerts.SeverityCritical
			}
			id := fmt.Sprintf("source-event-%03d", index)
			if err := sink.Accept(context.Background(), accepted(id, "api", "production", "latency", severity, now.Add(time.Duration(index)*time.Second))); err != nil {
				t.Errorf("Accept: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if len(repository.items) != 1 || repository.items[0].count != 100 || repository.items[0].severity != alerts.SeverityCritical {
		t.Fatalf("distinct observations did not converge: %+v", repository.items)
	}
}

func TestDifferentSourceEventsCorrelateOnlyInsideWindow(t *testing.T) {
	repository := &memoryRepository{window: time.Hour}
	sink := NewSink(repository)
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	for _, alert := range []alerts.Accepted{
		accepted("first", "api", "prod", "latency", alerts.SeverityInfo, now),
		accepted("second", "api", "prod", "latency", alerts.SeverityWarning, now.Add(30*time.Minute)),
		accepted("third", "api", "prod", "latency", alerts.SeverityInfo, now.Add(2*time.Hour)),
		accepted("fourth", "worker", "prod", "latency", alerts.SeverityInfo, now),
	} {
		if err := sink.Accept(context.Background(), alert); err != nil {
			t.Fatal(err)
		}
	}
	if len(repository.items) != 3 || repository.items[0].count != 2 {
		t.Fatalf("unexpected correlation groups: %+v", repository.items)
	}
}

package snapshots

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

type recordingMetrics struct {
	counters map[string]int
	gauges   map[string]float64
}

func newRecordingMetrics() *recordingMetrics {
	return &recordingMetrics{counters: map[string]int{}, gauges: map[string]float64{}}
}

func (r *recordingMetrics) IncCounter(name string)              { r.counters[name]++ }
func (r *recordingMetrics) SetGauge(name string, value float64) { r.gauges[name] = value }

func TestPartitionJobRecordsItsLastSuccess(t *testing.T) {
	// The gauge is what an alert queries. Without it the only evidence of a
	// healthy sweep is a log line, which nothing can express a threshold
	// against.
	recorder := newRecordingMetrics()
	now := time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	job := PartitionJob{DB: &recordingExecer{}, Metrics: recorder, Now: func() time.Time { return now }}

	if err := job.Run(context.Background(), jobs.Job{}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := recorder.gauges[LastSuccessMetric]; got != float64(now.Unix()) {
		t.Fatalf("last success gauge = %v, want %v", got, float64(now.Unix()))
	}
	if recorder.counters[FailureMetric] != 0 {
		t.Fatalf("a successful sweep counted a failure")
	}
}

func TestPartitionJobCountsAndPropagatesAFailure(t *testing.T) {
	// A missing partition is every insert into that month failing, so the
	// error has to reach the job record as well as the counter.
	recorder := newRecordingMetrics()
	job := PartitionJob{DB: &recordingExecer{err: errors.New("permission denied")}, Metrics: recorder}

	if err := job.Run(context.Background(), jobs.Job{}); err == nil {
		t.Fatal("a failed sweep reported success")
	}
	if recorder.counters[FailureMetric] != 1 {
		t.Fatalf("failure counter = %d, want 1", recorder.counters[FailureMetric])
	}
	// The success gauge must not move on failure: an alert on time-since-last-
	// success is only meaningful if a failure leaves it stale.
	if _, moved := recorder.gauges[LastSuccessMetric]; moved {
		t.Fatal("the last-success gauge moved during a failure")
	}
}

func TestPartitionJobRunsWithoutMetrics(t *testing.T) {
	if err := (PartitionJob{DB: &recordingExecer{}}).Run(context.Background(), jobs.Job{}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

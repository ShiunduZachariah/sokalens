package snapshots

import (
	"context"
	"log/slog"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

// PartitionJobKey is the daily partition maintenance sweep.
const (
	PartitionJobKey = "snapshots.ensure-partitions"
	Queue           = "maintenance"
)

// PartitionJob keeps the snapshot partitions ahead of the clock.
//
// It runs whether or not snapshot writing is enabled. A partition that exists
// and is never written to costs an empty table; a partition that does not
// exist is an insert failure the moment the feature is switched on, and the
// switch is most likely to be flipped just before a fixture window rather than
// during a quiet Tuesday.
type PartitionJob struct {
	DB     Execer
	Tables []string
	Lead   int
	Logger *slog.Logger
	// Metrics makes the outcome queryable. The alert that this job's failure
	// mode deserves cannot be written against a log line, and there is no
	// Alertmanager in this stack to receive one anyway -- so the signal is a
	// scraped gauge, and the alert is an ops-configuration task with a named
	// query rather than an unwritten intention.
	Metrics MetricsRecorder
	Now     func() time.Time
}

// MetricsRecorder is the worker metrics registry, narrowed. Nil disables
// recording, which keeps the job usable in tests and one-shot commands.
type MetricsRecorder interface {
	IncCounter(name string)
	SetGauge(name string, value float64)
}

// Metric names. LastSuccessMetric is the one to alert on, and the choice
// matters: alerting on the failure counter only fires when the job runs and
// fails, so a job that stopped being scheduled at all -- the worse outcome --
// would be silent. Time since last success catches both.
const (
	LastSuccessMetric = "worker_snapshot_partitions_last_success_timestamp_seconds"
	FailureMetric     = "worker_snapshot_partition_failures_total"
)

func (j PartitionJob) Run(ctx context.Context, _ jobs.Job) error {
	now := time.Now
	if j.Now != nil {
		now = j.Now
	}
	_, err := Maintainer{DB: j.DB, Tables: j.Tables, Lead: j.Lead, Logger: j.Logger, Now: j.Now}.Ensure(ctx)
	if j.Metrics != nil {
		if err != nil {
			j.Metrics.IncCounter(FailureMetric)
		} else {
			j.Metrics.SetGauge(LastSuccessMetric, float64(now().UTC().Unix()))
		}
	}
	return err
}

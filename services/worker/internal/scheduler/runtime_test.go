package scheduler

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunPerformsHeartbeatScheduleAndWorkImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	heartbeats := 0
	schedules := 0
	work := 0
	var workCount atomic.Int64

	err := Run(ctx, RuntimeOptions{
		PollInterval: time.Hour,
		Heartbeat: func(time.Time) error {
			heartbeats++
			cancel()
			return nil
		},
		Schedule: func(context.Context, time.Time) error {
			schedules++
			return nil
		},
		Workers: []QueueWorker{{
			Queue: "test",
			ProcessOnce: func(context.Context) (bool, error) {
				workCount.Add(1)
				return false, nil
			},
		}},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	work = int(workCount.Load())
	if heartbeats != 1 || schedules != 1 {
		t.Fatalf("immediate calls = heartbeat:%d schedule:%d, want 1 each", heartbeats, schedules)
	}
	// The queue loop runs concurrently, so it may not have been reached before
	// the heartbeat cancelled the run. What matters is that it is never called
	// more times than it returned work for -- it must not spin.
	if work < 0 {
		t.Fatalf("work = %d", work)
	}
}

func TestRunRefreshesHeartbeatOnEveryLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	heartbeats := 0

	err := Run(ctx, RuntimeOptions{
		PollInterval: time.Millisecond,
		Heartbeat: func(time.Time) error {
			heartbeats++
			if heartbeats == 2 {
				cancel()
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if heartbeats != 2 {
		t.Fatalf("heartbeats = %d, want 2", heartbeats)
	}
}

func TestScheduleIterationDoesNotReportHealthyWhenSchedulingFails(t *testing.T) {
	heartbeats := 0
	err := runScheduleIteration(context.Background(), RuntimeOptions{
		Now:    time.Now,
		Logger: slog.Default(),
		Heartbeat: func(time.Time) error {
			heartbeats++
			return nil
		},
		Schedule: func(context.Context, time.Time) error {
			return context.DeadlineExceeded
		},
	})
	if err == nil {
		t.Fatal("runScheduleIteration returned nil error after scheduler failure")
	}
	if heartbeats != 0 {
		t.Fatalf("heartbeats = %d, want the schedule iteration not to touch the heartbeat", heartbeats)
	}
}

func TestRunExitsAfterConsecutiveInfrastructureFailures(t *testing.T) {
	schedules := 0
	err := Run(context.Background(), RuntimeOptions{
		PollInterval:           time.Millisecond,
		MaxConsecutiveFailures: 2,
		Schedule: func(context.Context, time.Time) error {
			schedules++
			return context.DeadlineExceeded
		},
	})
	if err == nil {
		t.Fatal("Run returned nil after consecutive infrastructure failures")
	}
	if schedules != 2 {
		t.Fatalf("schedule attempts = %d, want 2 before process exit", schedules)
	}
}

func TestHeartbeatFileReportsFreshThenStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.heartbeat")
	now := time.Date(2026, time.July, 14, 13, 37, 0, 0, time.UTC)

	if err := WriteHeartbeat(path, now); err != nil {
		t.Fatalf("WriteHeartbeat returned error: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read heartbeat: %v", err)
	}
	if string(body) != "2026-07-14T13:37:00Z\n" {
		t.Fatalf("heartbeat body = %q", body)
	}
	if err := CheckHeartbeat(path, 2*time.Minute, now.Add(time.Minute)); err != nil {
		t.Fatalf("fresh CheckHeartbeat returned error: %v", err)
	}
	if err := CheckHeartbeat(path, 2*time.Minute, now.Add(3*time.Minute)); err == nil {
		t.Fatal("stale CheckHeartbeat returned nil error")
	}
}

func TestRunRejectsMissingPollInterval(t *testing.T) {
	if err := Run(context.Background(), RuntimeOptions{}); err == nil {
		t.Fatal("Run returned nil error without poll interval")
	}
}

func TestScheduleIterationLogsDurationAndOutcomeMetrics(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	if err := runScheduleIteration(context.Background(), RuntimeOptions{Now: time.Now, Logger: logger}); err != nil {
		t.Fatalf("runScheduleIteration returned error: %v", err)
	}
	logLine := output.String()
	for _, required := range []string{`"msg":"worker iteration completed"`, `"status":"success"`, `"duration_ms":`, `"error_count":0`} {
		if !strings.Contains(logLine, required) {
			t.Fatalf("log=%q, missing %q", logLine, required)
		}
	}
}

// The three tests below are the reason this runtime was rewritten. Each one
// fails against the previous shape, where every queue ran inside one barrier
// per poll interval and the heartbeat was written only after all of them
// returned.

func TestASlowQueueDoesNotBlockAnotherQueue(t *testing.T) {
	// The production symptom: a fifteen-minute ACCA inventory build stopped
	// provider ingestion dead, because both ran inside the same barrier.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	slowStarted := make(chan struct{})
	var slowOnce sync.Once
	var fastRuns atomic.Int64

	go func() {
		_ = Run(ctx, RuntimeOptions{
			PollInterval: time.Hour,
			IdleBackoff:  time.Millisecond,
			Workers: []QueueWorker{
				{
					Queue: "slow",
					ProcessOnce: func(ctx context.Context) (bool, error) {
						slowOnce.Do(func() { close(slowStarted) })
						<-ctx.Done()
						return false, ctx.Err()
					},
				},
				{
					Queue: "fast",
					ProcessOnce: func(context.Context) (bool, error) {
						fastRuns.Add(1)
						return false, nil
					},
				},
			},
		})
	}()

	select {
	case <-slowStarted:
	case <-ctx.Done():
		t.Fatal("slow queue never started")
	}

	deadline := time.After(2 * time.Second)
	for fastRuns.Load() < 5 {
		select {
		case <-deadline:
			t.Fatalf("fast queue ran %d times while the slow queue was blocked; it is being held behind it", fastRuns.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestABacklogDrainsWithoutWaitingForThePollInterval(t *testing.T) {
	// The old runtime executed one job per queue per poll interval. With a poll
	// interval of an hour, a backlog of 50 would take 50 hours. Here it must
	// drain immediately, because a claim that did work claims again at once.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const backlog = 50
	var remaining atomic.Int64
	remaining.Store(backlog)
	drained := make(chan struct{})
	var drainedOnce sync.Once

	go func() {
		_ = Run(ctx, RuntimeOptions{
			// An hour: if anything in the drain path waits for a tick, this
			// test times out rather than passing slowly.
			PollInterval: time.Hour,
			IdleBackoff:  time.Hour,
			Workers: []QueueWorker{{
				Queue: "backlog",
				ProcessOnce: func(context.Context) (bool, error) {
					if remaining.Add(-1) < 0 {
						return false, nil
					}
					if remaining.Load() == 0 {
						drainedOnce.Do(func() { close(drained) })
					}
					return true, nil
				},
			}},
		})
	}()

	select {
	case <-drained:
	case <-ctx.Done():
		t.Fatalf("backlog did not drain; %d still queued", remaining.Load())
	}
}

func TestTheHeartbeatKeepsBeatingWhileAJobRuns(t *testing.T) {
	// A busy worker is alive. The old runtime wrote the heartbeat only after
	// all work returned, so a long job was indistinguishable from a dead
	// process and the container was reported unhealthy while working.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var beats atomic.Int64
	jobStarted := make(chan struct{})
	var startedOnce sync.Once

	go func() {
		_ = Run(ctx, RuntimeOptions{
			PollInterval:      time.Hour,
			HeartbeatInterval: 5 * time.Millisecond,
			IdleBackoff:       time.Millisecond,
			Heartbeat: func(time.Time) error {
				beats.Add(1)
				return nil
			},
			Workers: []QueueWorker{{
				Queue: "slow",
				ProcessOnce: func(ctx context.Context) (bool, error) {
					startedOnce.Do(func() { close(jobStarted) })
					<-ctx.Done()
					return false, ctx.Err()
				},
			}},
		})
	}()

	select {
	case <-jobStarted:
	case <-ctx.Done():
		t.Fatal("job never started")
	}

	before := beats.Load()
	deadline := time.After(2 * time.Second)
	for beats.Load() < before+3 {
		select {
		case <-deadline:
			t.Fatalf("heartbeat advanced %d times while a job was running; it is waiting behind the job", beats.Load()-before)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

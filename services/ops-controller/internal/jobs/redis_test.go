package jobs

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

type recordingRedis struct {
	command string
	args    []any
	err     error
}

func (client *recordingRedis) Do(_ context.Context, command string, args ...any) error {
	client.command = command
	client.args = append([]any(nil), args...)
	return client.err
}

func TestRedisAcceleratorPublishesDeterministicPayloadFreeWakeHint(t *testing.T) {
	client := &recordingRedis{}
	accelerator, err := NewRedisAccelerator(client, "sokalens:ops:wake")
	if err != nil {
		t.Fatal(err)
	}
	err = accelerator.NotifyAvailable(context.Background(), AvailabilityHint{Queue: "critical", AvailableAt: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if client.command != "PUBLISH" || len(client.args) != 2 || client.args[0] != "sokalens:ops:wake" {
		t.Fatalf("command=%q args=%v", client.command, client.args)
	}
	payload := client.args[1].(string)
	expected := `{"version":"ops-job-hint-v1","queue":"critical","available_at":"2026-08-14T15:00:00Z"}`
	if payload != expected {
		t.Fatalf("payload=%s", payload)
	}
	for _, forbidden := range []string{"payload", "lease", "authorization", jobID, leaseToken} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("wake hint contains authoritative field %q", forbidden)
		}
	}
}

func TestRedisAcceleratorFailureIsTypedAndRepositoryIgnoresItAfterCommit(t *testing.T) {
	client := &recordingRedis{err: errors.New("connection refused")}
	accelerator, _ := NewRedisAccelerator(client, "sokalens:ops:wake")
	if err := accelerator.NotifyAvailable(context.Background(), AvailabilityHint{Queue: "critical", AvailableAt: fixedNow}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("direct notification err=%v", err)
	}

	tx := &fakeTx{rows: []Row{fakeRow{err: errors.New("not found")}, fakeRow{values: values(StatusPending, 0)}}}
	// Use the database/sql sentinel expected by Enqueue's authoritative lookup.
	tx.rows[0] = fakeRow{err: sql.ErrNoRows}
	job, err := repository(t, tx, accelerator).Enqueue(context.Background(), enqueueRequest())
	if err != nil || job.ID != jobID || tx.commits != 1 {
		t.Fatalf("committed PostgreSQL job was lost because Redis failed: job=%+v err=%v commits=%d", job, err, tx.commits)
	}
}

func TestRedisAcceleratorRejectsInvalidConfigurationAndHints(t *testing.T) {
	client := &recordingRedis{}
	if _, err := NewRedisAccelerator(nil, "channel"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("nil client err=%v", err)
	}
	if _, err := NewRedisAccelerator(client, "bad channel"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("bad channel err=%v", err)
	}
	accelerator, _ := NewRedisAccelerator(client, "channel")
	if err := accelerator.NotifyAvailable(context.Background(), AvailabilityHint{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid hint err=%v", err)
	}
}

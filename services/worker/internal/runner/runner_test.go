package runner

import (
	"context"
	"testing"
	"time"
)

func TestRunStopsWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Run(ctx, Options{PollInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestRunRejectsNonPositivePollInterval(t *testing.T) {
	err := Run(context.Background(), Options{})
	if err == nil {
		t.Fatal("Run returned nil error for missing poll interval")
	}
}

func TestRunCallsTickUntilCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ticks := 0

	err := Run(ctx, Options{
		PollInterval: time.Millisecond,
		Tick: func(context.Context) error {
			ticks++
			cancel()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if ticks != 1 {
		t.Fatalf("ticks = %d, want 1", ticks)
	}
}

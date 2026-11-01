package runner

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

type Options struct {
	PollInterval time.Duration
	Tick         func(context.Context) error
}

func Run(ctx context.Context, opts Options) error {
	if opts.PollInterval <= 0 {
		return errors.New("poll interval must be positive")
	}

	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if opts.Tick == nil {
				slog.Debug("worker tick")
				continue
			}
			if err := opts.Tick(ctx); err != nil {
				slog.Error("worker tick failed", "error", err)
			}
		}
	}
}

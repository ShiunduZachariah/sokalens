package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// RedisCommander is intentionally smaller than any vendor client. An adapter
// may wrap go-redis, rueidis, or another client without making Redis a source
// of queue truth.
type RedisCommander interface {
	Do(context.Context, string, ...any) error
}

type RedisAccelerator struct {
	client  RedisCommander
	channel string
}

func NewRedisAccelerator(client RedisCommander, channel string) (*RedisAccelerator, error) {
	channel = strings.TrimSpace(channel)
	if client == nil || channel == "" || len(channel) > 128 || strings.ContainsAny(channel, " \t\r\n") {
		return nil, ErrInvalidRequest
	}
	return &RedisAccelerator{client: client, channel: channel}, nil
}

type redisHint struct {
	Version     string `json:"version"`
	Queue       string `json:"queue"`
	AvailableAt string `json:"available_at"`
}

// NotifyAvailable publishes only a wake-up hint. The message contains no job
// payload, lease token, or authorization. A subscriber must call Claim, whose
// atomic PostgreSQL statement remains the only way to obtain work.
func (a *RedisAccelerator) NotifyAvailable(ctx context.Context, hint AvailabilityHint) error {
	if a == nil || a.client == nil || !validName(hint.Queue) || hint.AvailableAt.IsZero() {
		return ErrInvalidRequest
	}
	payload, err := json.Marshal(redisHint{
		Version: "ops-job-hint-v1", Queue: hint.Queue,
		AvailableAt: hint.AvailableAt.UTC().Format(timeFormat),
	})
	if err != nil {
		return err
	}
	if err = a.client.Do(ctx, "PUBLISH", a.channel, string(payload)); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return errors.Join(ErrUnavailable, err)
	}
	return nil
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

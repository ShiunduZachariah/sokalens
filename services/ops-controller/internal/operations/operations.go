// Package operations defines the only boundary through which future runtime
// mutations may be requested. It deliberately contains no shell-command API.
package operations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrObserveOnly = errors.New("operation execution is disabled in observe mode")

type Kind string

const (
	RestartService Kind = "restart_service"
	Rollback       Kind = "rollback"
	ScaleWorker    Kind = "scale_worker"
	FlushSafeCache Kind = "flush_safe_cache"
	CheckBackup    Kind = "check_backup"
	// RetryJob returns a failed or dead-lettered durable job to the queue. It
	// touches only ops_jobs, which this service already owns, so it needs no
	// privilege beyond the database connection the controller already holds.
	RetryJob Kind = "retry_job"
)

// KnownKinds returns the complete, defensive-copy enumeration accepted by the
// typed operation boundary. Policy tests use this list so every reachable
// mutation shape remains denied in observe mode.
func KnownKinds() []Kind {
	return []Kind{RestartService, Rollback, ScaleWorker, FlushSafeCache, CheckBackup, RetryJob}
}

func (k Kind) Valid() bool {
	for _, known := range KnownKinds() {
		if k == known {
			return true
		}
	}
	return false
}

type Request struct {
	Kind           Kind
	Target         string
	Scope          string
	IdempotencyKey string
	Timeout        time.Duration
}

func (r Request) Validate() error {
	if !r.Kind.Valid() {
		return fmt.Errorf("unsupported operation kind %q", r.Kind)
	}
	if strings.TrimSpace(r.Target) == "" {
		return errors.New("operation target is required")
	}
	if strings.TrimSpace(r.Scope) == "" {
		return errors.New("operation scope is required")
	}
	if strings.TrimSpace(r.IdempotencyKey) == "" {
		return errors.New("operation idempotency key is required")
	}
	if r.Timeout <= 0 || r.Timeout > 15*time.Minute {
		return errors.New("operation timeout must be greater than zero and no more than 15m")
	}
	return nil
}

type Result struct {
	Accepted bool
	Reason   string
}

// Executor is intentionally typed. Implementations must never accept arbitrary
// command strings, scripts, or AI-produced shell fragments.
type Executor interface {
	Execute(context.Context, Request) (Result, error)
}

// ObserveOnly validates recommendations but cannot execute them.
type ObserveOnly struct{}

func (ObserveOnly) Execute(_ context.Context, request Request) (Result, error) {
	if err := request.Validate(); err != nil {
		return Result{}, err
	}
	return Result{Accepted: false, Reason: ErrObserveOnly.Error()}, ErrObserveOnly
}

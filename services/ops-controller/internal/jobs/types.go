package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalidRequest      = errors.New("invalid durable job request")
	ErrIdempotencyConflict = errors.New("durable job idempotency conflict")
	ErrNotFound            = errors.New("durable job not found")
	ErrLeaseLost           = errors.New("durable job lease lost")
	ErrUnavailable         = errors.New("durable job repository unavailable")
)

type Status string

const (
	StatusPending    Status = "pending"
	StatusLeased     Status = "leased"
	StatusRetry      Status = "retry"
	StatusSucceeded  Status = "succeeded"
	StatusFailed     Status = "failed"
	StatusDeadLetter Status = "dead_letter"
	StatusCancelled  Status = "cancelled"
)

// PriorityClass is intentionally distinct from the raw database integer.
// Scheduler P0 means critical even though PostgreSQL claims larger raw values
// first. PriorityValue is the required, non-reversing conversion boundary.
type PriorityClass int

const (
	PriorityP0 PriorityClass = iota
	PriorityP1
	PriorityP2
)

func PriorityValue(class PriorityClass) (int, error) {
	switch class {
	case PriorityP0:
		return 900, nil
	case PriorityP1:
		return 600, nil
	case PriorityP2:
		return 300, nil
	default:
		return 0, ErrInvalidRequest
	}
}

type FailureClass string

const (
	FailureRetryable FailureClass = "retryable"
	FailurePermanent FailureClass = "permanent"
)

type Job struct {
	ID               string
	IncidentID       string
	Queue            string
	Type             string
	IdempotencyKey   string
	Priority         int
	Payload          json.RawMessage
	Status           Status
	AvailableAt      time.Time
	Attempts         int
	MaxAttempts      int
	LeaseOwner       string
	LeaseToken       string
	LeaseExpiresAt   time.Time
	LastErrorClass   string
	LastErrorCode    string
	LastErrorMessage string
	CreatedAt        time.Time
	StartedAt        time.Time
	CompletedAt      time.Time
	DeadLetteredAt   time.Time
	UpdatedAt        time.Time
}

type EnqueueRequest struct {
	IncidentID     string
	Queue          string
	Type           string
	IdempotencyKey string
	Priority       int
	Payload        json.RawMessage
	AvailableAt    time.Time
	MaxAttempts    int
}

type ClaimRequest struct {
	Queue  string
	Worker string
	Lease  time.Duration
}

type LeaseRequest struct {
	JobID      string
	Worker     string
	LeaseToken string
}

type RenewRequest struct {
	LeaseRequest
	Lease time.Duration
}

type Failure struct {
	LeaseRequest
	Class   FailureClass
	Code    string
	Message string
}

// Accelerator can only announce that PostgreSQL may contain ready work. It
// cannot create, claim, renew, complete, fail, or authorize a job. Consumers
// must always claim through Repository after receiving a hint.
type Accelerator interface {
	NotifyAvailable(context.Context, AvailabilityHint) error
}

type AvailabilityHint struct {
	Queue       string
	AvailableAt time.Time
}

type Config struct {
	MinLease        time.Duration
	MaxLease        time.Duration
	InitialBackoff  time.Duration
	MaxBackoff      time.Duration
	MaxPayloadBytes int
	Now             func() time.Time
}

func DefaultConfig() Config {
	return Config{
		MinLease: time.Second, MaxLease: 5 * time.Minute,
		InitialBackoff: 5 * time.Second, MaxBackoff: 15 * time.Minute,
		MaxPayloadBytes: 256 << 10, Now: time.Now,
	}
}

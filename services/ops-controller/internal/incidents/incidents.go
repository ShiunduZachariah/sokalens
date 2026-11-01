// Package incidents deterministically classifies accepted alerts and persists
// observe-only incident state. Lifecycle transitions and immutable audit events
// remain the responsibility of OPS-005.
package incidents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/alerts"
)

const ClassificationVersion = "incident-classification-v1"

var ErrInvalidObservation = errors.New("invalid incident observation")

// Observation is the bounded, deterministic representation persisted by the
// repository. It deliberately contains no executable operation or arbitrary
// provider metadata.
type Observation struct {
	Source         string
	SourceEventID  string
	Fingerprint    string
	Title          string
	Summary        string
	Service        string
	Environment    string
	Severity       alerts.Severity
	ObservedAt     time.Time
	ReceivedAt     time.Time
	DeliveryID     string
	IdempotencyKey string
	Classification string
}

// Classify normalizes the dimensions that are allowed to correlate. Source
// and source event identity are intentionally not part of the fingerprint:
// independent transports reporting the same service/environment/condition
// should converge inside the repository's bounded correlation window.
func Classify(a alerts.Accepted) (Observation, error) {
	service := normalize(a.Service)
	environment := normalize(a.Environment)
	name := normalize(a.Name)
	source := normalize(a.Source)
	if service == "" || environment == "" || name == "" || source == "" || strings.TrimSpace(a.AlertID) == "" || !validDigest(a.IdempotencyKey) || a.StartsAt.IsZero() || a.ReceivedAt.IsZero() {
		return Observation{}, ErrInvalidObservation
	}
	return Observation{
		Source:         source,
		SourceEventID:  strings.TrimSpace(a.AlertID),
		Fingerprint:    Fingerprint(name, service, environment),
		Title:          humanTitle(name, service, environment),
		Summary:        strings.TrimSpace(a.Summary),
		Service:        service,
		Environment:    environment,
		Severity:       a.Severity,
		ObservedAt:     a.StartsAt.UTC(),
		ReceivedAt:     a.ReceivedAt.UTC(),
		DeliveryID:     strings.TrimSpace(a.DeliveryID),
		IdempotencyKey: a.IdempotencyKey,
		Classification: ClassificationVersion,
	}, nil
}

// Fingerprint uses the same identity for firing observations and their recovery.
func Fingerprint(name, service, environment string) string {
	parts := []string{ClassificationVersion, normalize(environment), normalize(service), normalize(name)}
	if parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func normalize(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func humanTitle(name, service, environment string) string {
	return fmt.Sprintf("%s on %s (%s)", strings.ReplaceAll(name, "_", " "), service, environment)
}

type Repository interface {
	Observe(context.Context, Observation) (Result, error)
}

type Result struct {
	IncidentID      string
	OccurrenceCount int64
	Created         bool
}

// Sink connects the authenticated alert boundary to durable incident state.
// It performs no production mutation and returns repository failures so the
// HTTP boundary can fail closed and permit a transport retry.
type Sink struct {
	repository Repository
}

func NewSink(repository Repository) *Sink { return &Sink{repository: repository} }

func (s *Sink) Accept(ctx context.Context, accepted alerts.Accepted) error {
	if s == nil || s.repository == nil {
		return alerts.ErrSinkDisabled
	}
	observation, err := Classify(accepted)
	if err != nil {
		return err
	}
	_, err = s.repository.Observe(ctx, observation)
	return err
}

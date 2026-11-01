// Package alerts authenticates and normalizes bounded operational alerts.
package alerts

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const ContractVersion = "v1"

var (
	ErrDisabled     = errors.New("alert ingestion is not configured")
	ErrInvalid      = errors.New("invalid alert")
	ErrSignature    = errors.New("invalid alert signature")
	ErrStale        = errors.New("alert timestamp is outside the accepted window")
	ErrReplay       = errors.New("alert delivery was already received")
	ErrCapacity     = errors.New("alert replay protection is at capacity")
	ErrSinkDisabled = errors.New("durable alert sink is not configured")
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Payload is the strict, provider-neutral wire contract. Labels and arbitrary
// annotations are deliberately excluded to keep cardinality and evidence bounded.
type Payload struct {
	Version     string   `json:"version"`
	AlertID     string   `json:"alert_id"`
	Source      string   `json:"source"`
	Name        string   `json:"name"`
	Service     string   `json:"service"`
	Environment string   `json:"environment"`
	Severity    Severity `json:"severity"`
	StartsAt    string   `json:"starts_at"`
	Summary     string   `json:"summary"`
}

// Accepted is the normalized seam consumed by OPS-004. Acceptance itself does
// not create incidents, jobs, actions, or any production mutation.
type Accepted struct {
	IdempotencyKey string
	DeliveryID     string
	AlertID        string
	Source         string
	Name           string
	Service        string
	Environment    string
	Severity       Severity
	StartsAt       time.Time
	Summary        string
	ReceivedAt     time.Time
}

type Sink interface {
	Accept(context.Context, Accepted) error
}
type SinkFunc func(context.Context, Accepted) error

func (f SinkFunc) Accept(ctx context.Context, a Accepted) error { return f(ctx, a) }

type NoopSink struct{}

// Accept fails closed until OPS-004 wires restart-durable PostgreSQL
// acceptance. A configured signing secret must never turn silent alert loss
// into an HTTP 202 response.
func (NoopSink) Accept(context.Context, Accepted) error { return ErrSinkDisabled }

type Validator struct {
	secret  []byte
	maxSkew time.Duration
	limit   int
	now     func() time.Time
	mu      sync.Mutex
	seen    map[string]time.Time
}

func NewValidator(secret string, maxSkew time.Duration, replayLimit int) *Validator {
	return &Validator{secret: []byte(secret), maxSkew: maxSkew, limit: replayLimit, now: time.Now, seen: make(map[string]time.Time)}
}

// Validate verifies timestamp, HMAC, delivery replay, and schema in that order.
// Signature is hex HMAC-SHA256 over "<RFC3339 timestamp>.<raw body>".
func (v *Validator) Validate(timestamp, deliveryID, signature string, body []byte) (Accepted, error) {
	if len(v.secret) == 0 {
		return Accepted{}, ErrDisabled
	}
	if !boundedToken(deliveryID, 1, 128) {
		return Accepted{}, ErrInvalid
	}
	provided, err := parseSignature(signature)
	if err != nil {
		return Accepted{}, ErrSignature
	}
	sentAt, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return Accepted{}, ErrInvalid
	}
	now := v.now().UTC()
	if delta := now.Sub(sentAt); delta > v.maxSkew || delta < -v.maxSkew {
		return Accepted{}, ErrStale
	}
	mac := hmac.New(sha256.New, v.secret)
	_, _ = io.WriteString(mac, timestamp+".")
	_, _ = mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), provided) {
		return Accepted{}, ErrSignature
	}

	var payload Payload
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return Accepted{}, ErrInvalid
	}
	if err := ensureEOF(decoder); err != nil {
		return Accepted{}, ErrInvalid
	}
	payload.AlertID = strings.TrimSpace(payload.AlertID)
	payload.Source = strings.TrimSpace(payload.Source)
	payload.Name = strings.TrimSpace(payload.Name)
	payload.Service = strings.TrimSpace(payload.Service)
	payload.Environment = strings.TrimSpace(payload.Environment)
	payload.Summary = strings.TrimSpace(payload.Summary)
	startsAt, err := validatePayload(payload)
	if err != nil {
		return Accepted{}, err
	}

	if err := v.reserve(deliveryID, sentAt.Add(v.maxSkew), now); err != nil {
		return Accepted{}, err
	}
	keyBytes := sha256.Sum256([]byte(strings.Join([]string{payload.Version, payload.Source, payload.AlertID, payload.Name, payload.Service, startsAt.Format(time.RFC3339Nano)}, "\x00")))
	return Accepted{IdempotencyKey: hex.EncodeToString(keyBytes[:]), DeliveryID: deliveryID, AlertID: payload.AlertID, Source: payload.Source, Name: payload.Name, Service: payload.Service, Environment: payload.Environment, Severity: payload.Severity, StartsAt: startsAt, Summary: payload.Summary, ReceivedAt: now}, nil
}

func parseSignature(raw string) ([]byte, error) {
	prefix := ContractVersion + "="
	if !strings.HasPrefix(raw, prefix) {
		return nil, ErrSignature
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(raw, prefix))
	if err != nil || len(decoded) != sha256.Size {
		return nil, ErrSignature
	}
	return decoded, nil
}

func validatePayload(p Payload) (time.Time, error) {
	if p.Version != ContractVersion || !boundedToken(p.AlertID, 1, 128) || !boundedToken(p.Source, 1, 64) || !boundedToken(p.Name, 1, 128) || !boundedToken(p.Service, 1, 64) || !boundedToken(p.Environment, 1, 32) || strings.TrimSpace(p.Summary) == "" || len(p.Summary) > 1024 {
		return time.Time{}, ErrInvalid
	}
	switch p.Severity {
	case SeverityInfo, SeverityWarning, SeverityCritical:
	default:
		return time.Time{}, ErrInvalid
	}
	startsAt, err := time.Parse(time.RFC3339, p.StartsAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: starts_at", ErrInvalid)
	}
	return startsAt.UTC(), nil
}

func boundedToken(value string, min, max int) bool {
	value = strings.TrimSpace(value)
	if len(value) < min || len(value) > max {
		return false
	}
	for _, r := range value {
		if !(r == '-' || r == '_' || r == '.' || r == ':' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}

func (v *Validator) reserve(id string, expiresAt, now time.Time) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	for key, expiry := range v.seen {
		if expiry.Before(now) {
			delete(v.seen, key)
		}
	}
	if _, exists := v.seen[id]; exists {
		return ErrReplay
	}
	if len(v.seen) >= v.limit {
		return ErrCapacity
	}
	v.seen[id] = expiresAt
	return nil
}

// Release permits a transport retry when the downstream typed sink fails.
func (v *Validator) Release(deliveryID string) {
	v.mu.Lock()
	delete(v.seen, deliveryID)
	v.mu.Unlock()
}

// Sign is exported for producers and deterministic contract tests.
func Sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = io.WriteString(mac, timestamp+".")
	_, _ = mac.Write(body)
	return ContractVersion + "=" + hex.EncodeToString(mac.Sum(nil))
}

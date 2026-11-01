package incidents

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const AuditHashVersion = "ops-audit-hash-v1"

var (
	ErrInvalidTransition   = errors.New("invalid incident transition")
	ErrTransitionConflict  = errors.New("incident transition idempotency conflict")
	ErrIncidentNotFound    = errors.New("incident not found")
	ErrObservationConflict = errors.New("incident observed after recovery cutoff")
)

type IncidentStatus string

const (
	StatusOpen          IncidentStatus = "open"
	StatusInvestigating IncidentStatus = "investigating"
	StatusMitigated     IncidentStatus = "mitigated"
	StatusResolved      IncidentStatus = "resolved"
	StatusSuppressed    IncidentStatus = "suppressed"
)

type AuditActor struct {
	Type string
	ID   string
}

type TransitionRequest struct {
	IncidentID     string
	IdempotencyKey string
	To             IncidentStatus
	Reason         string
	Actor          AuditActor
	OccurredAt     time.Time
	// ObservedBefore is an optional recovery guard. A fresh firing observation
	// must prevent an otherwise healthy transition from closing the incident.
	ObservedBefore time.Time
}

type TransitionResult struct {
	IncidentID   string
	From         IncidentStatus
	To           IncidentStatus
	AuditEventID string
	EventHash    string
	Applied      bool
}

// AllowedTransition is deliberately explicit. Resolved incidents are terminal;
// a later alert creates or correlates to another active incident. Suppression
// can be reversed, but it must return to open before investigation continues.
func AllowedTransition(from, to IncidentStatus) bool {
	switch from {
	case StatusOpen:
		return to == StatusInvestigating || to == StatusSuppressed
	case StatusInvestigating:
		return to == StatusMitigated || to == StatusSuppressed
	case StatusMitigated:
		return to == StatusInvestigating || to == StatusResolved
	case StatusSuppressed:
		return to == StatusOpen
	default:
		return false
	}
}

func validateTransitionRequest(request TransitionRequest) error {
	if strings.TrimSpace(request.IncidentID) == "" || !validDigest(request.IdempotencyKey) || strings.TrimSpace(request.Reason) == "" || strings.TrimSpace(request.Actor.ID) == "" || request.OccurredAt.IsZero() {
		return ErrInvalidTransition
	}
	switch request.Actor.Type {
	case "system", "user", "admin", "worker", "ai":
	default:
		return ErrInvalidTransition
	}
	switch request.To {
	case StatusOpen, StatusInvestigating, StatusMitigated, StatusResolved, StatusSuppressed:
		return nil
	default:
		return ErrInvalidTransition
	}
}

type auditDetails struct {
	HashVersion    string         `json:"audit_hash_version"`
	IdempotencyKey string         `json:"idempotency_key"`
	From           IncidentStatus `json:"from_status"`
	To             IncidentStatus `json:"to_status"`
	Reason         string         `json:"reason"`
}

type canonicalAuditEvent struct {
	HashVersion  string          `json:"hash_version"`
	PreviousHash string          `json:"previous_event_hash"`
	EventID      string          `json:"event_id"`
	EventType    string          `json:"event_type"`
	ActorType    string          `json:"actor_type"`
	ActorID      string          `json:"actor_id"`
	EntityType   string          `json:"entity_type"`
	EntityID     string          `json:"entity_id"`
	OccurredAt   string          `json:"occurred_at"`
	Details      json.RawMessage `json:"details"`
}

func buildAuditEvent(request TransitionRequest, from IncidentStatus, previousHash string) (eventID string, details []byte, eventHash string, err error) {
	eventID, err = randomUUID()
	if err != nil {
		return "", nil, "", err
	}
	details, err = json.Marshal(auditDetails{
		HashVersion: AuditHashVersion, IdempotencyKey: request.IdempotencyKey,
		From: from, To: request.To, Reason: strings.TrimSpace(request.Reason),
	})
	if err != nil {
		return "", nil, "", err
	}
	canonical, err := json.Marshal(canonicalAuditEvent{
		HashVersion: AuditHashVersion, PreviousHash: previousHash, EventID: eventID,
		EventType: "incident.status_transition", ActorType: request.Actor.Type,
		ActorID: strings.TrimSpace(request.Actor.ID), EntityType: "incident",
		EntityID: strings.TrimSpace(request.IncidentID), OccurredAt: request.OccurredAt.UTC().Format(time.RFC3339Nano),
		Details: details,
	})
	if err != nil {
		return "", nil, "", err
	}
	sum := sha256.Sum256(canonical)
	return eventID, details, hex.EncodeToString(sum[:]), nil
}

func randomUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate audit event id: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

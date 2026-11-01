// Package policy defines the reachability boundary between operational
// recommendations and typed mutation executors.
package policy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/operations"
)

const (
	RecommendationSchemaVersion = "ops-recommendation/v1"
	AuditHashVersion            = "ops-recommendation-audit/v1"
)

var (
	ErrInvalidRecommendation = errors.New("invalid operation recommendation")
	ErrAuditUnavailable      = errors.New("recommendation audit unavailable")
)

// Recommendation is the bounded schema accepted by the reachability policy.
// It contains a typed operation request and has no shell, script, deployment,
// job, or arbitrary command field.
type Recommendation struct {
	Version          string
	RecommendationID string
	IncidentID       string
	Operation        operations.Request
	Reason           string
	EvidenceRefs     []string
	RecommendedAt    time.Time
}

func (r Recommendation) Validate() error {
	if r.Version != RecommendationSchemaVersion || !boundedToken(r.RecommendationID, 1, 128) || !boundedToken(r.IncidentID, 1, 128) {
		return ErrInvalidRecommendation
	}
	if err := r.Operation.Validate(); err != nil {
		return errors.Join(ErrInvalidRecommendation, err)
	}
	if !boundedToken(r.Operation.Target, 1, 128) || !boundedToken(r.Operation.Scope, 1, 64) || !boundedToken(r.Operation.IdempotencyKey, 1, 128) {
		return ErrInvalidRecommendation
	}
	reason := strings.TrimSpace(r.Reason)
	if reason == "" || len(reason) > 1024 || r.RecommendedAt.IsZero() || len(r.EvidenceRefs) > 32 {
		return ErrInvalidRecommendation
	}
	seenRefs := make(map[string]struct{}, len(r.EvidenceRefs))
	for _, ref := range r.EvidenceRefs {
		if !boundedReference(ref, 1, 256) {
			return ErrInvalidRecommendation
		}
		ref = strings.TrimSpace(ref)
		if _, duplicate := seenRefs[ref]; duplicate {
			return ErrInvalidRecommendation
		}
		seenRefs[ref] = struct{}{}
	}
	return nil
}

// Decision is always denied in this release. Recorded proves the immutable
// append boundary accepted the recommendation before it was returned.
type Decision struct {
	Allowed   bool
	Recorded  bool
	Reason    string
	AuditHash string
}

// AuditRecord is immutable outside this package: its state is private and
// accessors return values or defensive copies. Its digest covers the complete
// normalized recommendation and the observe-only decision.
type AuditRecord struct {
	recommendation Recommendation
	decision       Decision
	digest         string
}

func (r AuditRecord) Recommendation() Recommendation { return cloneRecommendation(r.recommendation) }
func (r AuditRecord) Decision() Decision             { return r.decision }
func (r AuditRecord) Digest() string                 { return r.digest }

// ImmutableAuditSink has append-only semantics. It intentionally exposes no
// update or delete method.
type ImmutableAuditSink interface {
	AppendRecommendation(context.Context, AuditRecord) error
}

// ObserveOnlyReachability deliberately holds only an audit capability. It has
// no mutation capability that Submit could reach.
type ObserveOnlyReachability struct {
	audit ImmutableAuditSink
}

// The executor parameter is intentionally discarded. Observe-only policy does
// not retain a mutation capability, making execution unreachable by
// construction while letting boundary tests provide a tripwire executor.
func NewObserveOnlyReachability(audit ImmutableAuditSink, _ operations.Executor) *ObserveOnlyReachability {
	return &ObserveOnlyReachability{audit: audit}
}

// Submit validates and records a recommendation, then denies execution. Audit
// unavailability fails closed before any executor could become reachable.
func (p *ObserveOnlyReachability) Submit(ctx context.Context, recommendation Recommendation) (Decision, error) {
	record, err := newAuditRecord(recommendation)
	if err != nil {
		return Decision{Reason: ErrInvalidRecommendation.Error()}, err
	}
	if err := ctx.Err(); err != nil {
		return Decision{Reason: ErrAuditUnavailable.Error()}, ErrAuditUnavailable
	}
	if p == nil || p.audit == nil {
		return Decision{Reason: ErrAuditUnavailable.Error()}, ErrAuditUnavailable
	}
	if err := p.audit.AppendRecommendation(ctx, record); err != nil {
		return Decision{Reason: ErrAuditUnavailable.Error()}, ErrAuditUnavailable
	}
	decision := record.Decision()
	decision.Recorded = true
	return decision, operations.ErrObserveOnly
}

func newAuditRecord(recommendation Recommendation) (AuditRecord, error) {
	if err := recommendation.Validate(); err != nil {
		return AuditRecord{}, err
	}
	recommendation = cloneRecommendation(recommendation)
	recommendation.RecommendationID = strings.TrimSpace(recommendation.RecommendationID)
	recommendation.IncidentID = strings.TrimSpace(recommendation.IncidentID)
	recommendation.Reason = strings.TrimSpace(recommendation.Reason)
	recommendation.RecommendedAt = recommendation.RecommendedAt.UTC()
	recommendation.Operation.Target = strings.TrimSpace(recommendation.Operation.Target)
	recommendation.Operation.Scope = strings.TrimSpace(recommendation.Operation.Scope)
	recommendation.Operation.IdempotencyKey = strings.TrimSpace(recommendation.Operation.IdempotencyKey)
	for index := range recommendation.EvidenceRefs {
		recommendation.EvidenceRefs[index] = strings.TrimSpace(recommendation.EvidenceRefs[index])
	}
	decision := Decision{Allowed: false, Reason: operations.ErrObserveOnly.Error()}
	canonical := struct {
		AuditHashVersion string             `json:"audit_hash_version"`
		Version          string             `json:"version"`
		RecommendationID string             `json:"recommendation_id"`
		IncidentID       string             `json:"incident_id"`
		Operation        operations.Request `json:"operation"`
		Reason           string             `json:"reason"`
		EvidenceRefs     []string           `json:"evidence_refs"`
		RecommendedAt    time.Time          `json:"recommended_at"`
		Allowed          bool               `json:"allowed"`
		DecisionReason   string             `json:"decision_reason"`
	}{
		AuditHashVersion: AuditHashVersion,
		Version:          recommendation.Version,
		RecommendationID: recommendation.RecommendationID,
		IncidentID:       recommendation.IncidentID,
		Operation:        recommendation.Operation,
		Reason:           recommendation.Reason,
		EvidenceRefs:     recommendation.EvidenceRefs,
		RecommendedAt:    recommendation.RecommendedAt,
		Allowed:          decision.Allowed,
		DecisionReason:   decision.Reason,
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return AuditRecord{}, ErrInvalidRecommendation
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	decision.AuditHash = digest
	return AuditRecord{recommendation: recommendation, decision: decision, digest: digest}, nil
}

func cloneRecommendation(r Recommendation) Recommendation {
	r.EvidenceRefs = append([]string(nil), r.EvidenceRefs...)
	return r
}

func boundedToken(value string, minimum, maximum int) bool {
	return boundedValue(value, minimum, maximum, false)
}

func boundedReference(value string, minimum, maximum int) bool {
	return boundedValue(value, minimum, maximum, true)
}

func boundedValue(value string, minimum, maximum int, allowSlash bool) bool {
	value = strings.TrimSpace(value)
	if len(value) < minimum || len(value) > maximum {
		return false
	}
	for _, char := range value {
		if !(char == '-' || char == '_' || char == '.' || char == ':' || allowSlash && char == '/' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9') {
			return false
		}
	}
	return true
}

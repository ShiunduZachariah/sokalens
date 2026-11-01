package policy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/operations"
)

func TestObserveModeRecordsAndDeniesEveryOperationKindWithoutExecutorReachability(t *testing.T) {
	executor := &countingExecutor{}
	for _, kind := range operations.KnownKinds() {
		t.Run(string(kind), func(t *testing.T) {
			audit := &recordingAudit{}
			policy := NewObserveOnlyReachability(audit, executor)
			decision, err := policy.Submit(context.Background(), validRecommendation(kind))
			if !errors.Is(err, operations.ErrObserveOnly) {
				t.Fatalf("error = %v, want observe-only denial", err)
			}
			if decision.Allowed || !decision.Recorded || len(decision.AuditHash) != 64 {
				t.Fatalf("unexpected decision: %+v", decision)
			}
			if len(audit.records) != 1 {
				t.Fatalf("audit records = %d, want 1", len(audit.records))
			}
			recorded := audit.records[0]
			if recorded.Recommendation().Operation.Kind != kind || recorded.Decision().Allowed || recorded.Digest() != decision.AuditHash {
				t.Fatalf("unexpected audit record: recommendation=%+v decision=%+v digest=%q", recorded.Recommendation(), recorded.Decision(), recorded.Digest())
			}
		})
	}
	if executor.calls != 0 {
		t.Fatalf("mutation executor invoked %d times in observe mode", executor.calls)
	}
}

func TestAuditFailureFailsClosedBeforeExecutor(t *testing.T) {
	executor := &countingExecutor{}
	audit := &recordingAudit{err: errors.New("storage unavailable")}
	decision, err := NewObserveOnlyReachability(audit, executor).Submit(context.Background(), validRecommendation(operations.Rollback))
	if !errors.Is(err, ErrAuditUnavailable) || decision.Allowed || decision.Recorded {
		t.Fatalf("decision=%+v err=%v, want closed audit failure", decision, err)
	}
	if executor.calls != 0 {
		t.Fatalf("mutation executor invoked %d times after audit failure", executor.calls)
	}
	if err.Error() == audit.err.Error() {
		t.Fatal("policy exposed audit backend error")
	}
}

func TestMissingAuditSinkAndCancelledContextFailClosed(t *testing.T) {
	executor := &countingExecutor{}
	policy := NewObserveOnlyReachability(nil, executor)
	if decision, err := policy.Submit(context.Background(), validRecommendation(operations.CheckBackup)); !errors.Is(err, ErrAuditUnavailable) || decision.Allowed {
		t.Fatalf("nil audit decision=%+v err=%v", decision, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	audit := &recordingAudit{}
	if decision, err := NewObserveOnlyReachability(audit, executor).Submit(ctx, validRecommendation(operations.CheckBackup)); !errors.Is(err, ErrAuditUnavailable) || decision.Allowed {
		t.Fatalf("cancelled decision=%+v err=%v", decision, err)
	}
	if executor.calls != 0 || len(audit.records) != 0 {
		t.Fatalf("cancelled path executor calls=%d audit records=%d", executor.calls, len(audit.records))
	}
}

func TestInvalidRecommendationIsNeitherAuditedNorExecuted(t *testing.T) {
	valid := validRecommendation(operations.RestartService)
	tests := map[string]Recommendation{
		"schema":           func() Recommendation { r := valid; r.Version = "v2"; return r }(),
		"unknown kind":     func() Recommendation { r := valid; r.Operation.Kind = "shell"; return r }(),
		"unsafe target":    func() Recommendation { r := valid; r.Operation.Target = "api;restart"; return r }(),
		"long idempotency": func() Recommendation { r := valid; r.Operation.IdempotencyKey = string(make([]byte, 129)); return r }(),
		"missing id":       func() Recommendation { r := valid; r.RecommendationID = ""; return r }(),
		"missing incident": func() Recommendation { r := valid; r.IncidentID = ""; return r }(),
		"reason":           func() Recommendation { r := valid; r.Reason = " "; return r }(),
		"time":             func() Recommendation { r := valid; r.RecommendedAt = time.Time{}; return r }(),
		"evidence token":   func() Recommendation { r := valid; r.EvidenceRefs = []string{"https://bad?query=unbounded"}; return r }(),
		"duplicate evidence": func() Recommendation {
			r := valid
			r.EvidenceRefs = []string{"metric/api-latency", "metric/api-latency"}
			return r
		}(),
		"evidence count": func() Recommendation {
			r := valid
			r.EvidenceRefs = make([]string, 33)
			for index := range r.EvidenceRefs {
				r.EvidenceRefs[index] = "evidence"
			}
			return r
		}(),
	}
	for name, recommendation := range tests {
		t.Run(name, func(t *testing.T) {
			audit := &recordingAudit{}
			executor := &countingExecutor{}
			decision, err := NewObserveOnlyReachability(audit, executor).Submit(context.Background(), recommendation)
			if !errors.Is(err, ErrInvalidRecommendation) || decision.Allowed {
				t.Fatalf("decision=%+v err=%v", decision, err)
			}
			if len(audit.records) != 0 || executor.calls != 0 {
				t.Fatalf("invalid recommendation reached audit/executor: records=%d calls=%d", len(audit.records), executor.calls)
			}
		})
	}
}

func TestAuditRecordIsNormalizedDeterministicAndDefensivelyCopied(t *testing.T) {
	recommendation := validRecommendation(operations.ScaleWorker)
	recommendation.RecommendationID = " recommendation-1 "
	recommendation.Reason = " capacity threshold exceeded "
	recommendation.EvidenceRefs = []string{" metric/api-latency "}
	first, err := newAuditRecord(recommendation)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newAuditRecord(recommendation)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest() != second.Digest() || first.Recommendation().RecommendationID != "recommendation-1" {
		t.Fatalf("records are not normalized/deterministic: %q %q", first.Digest(), second.Digest())
	}

	copy := first.Recommendation()
	copy.EvidenceRefs[0] = "mutated"
	if first.Recommendation().EvidenceRefs[0] != "metric/api-latency" {
		t.Fatal("audit record exposed mutable evidence state")
	}
}

func validRecommendation(kind operations.Kind) Recommendation {
	return Recommendation{
		Version:          RecommendationSchemaVersion,
		RecommendationID: "recommendation-1",
		IncidentID:       "incident-1",
		Operation: operations.Request{
			Kind:           kind,
			Target:         "api",
			Scope:          "production",
			IdempotencyKey: "recommendation-1",
			Timeout:        time.Minute,
		},
		Reason:        "bounded operational recommendation",
		EvidenceRefs:  []string{"metric/api-latency"},
		RecommendedAt: time.Date(2026, 8, 14, 12, 0, 0, 0, time.FixedZone("EAT", 3*60*60)),
	}
}

type recordingAudit struct {
	records []AuditRecord
	err     error
}

func (a *recordingAudit) AppendRecommendation(_ context.Context, record AuditRecord) error {
	if a.err != nil {
		return a.err
	}
	a.records = append(a.records, record)
	return nil
}

type countingExecutor struct{ calls int }

func (e *countingExecutor) Execute(context.Context, operations.Request) (operations.Result, error) {
	e.calls++
	return operations.Result{Accepted: true}, nil
}

var _ ImmutableAuditSink = (*recordingAudit)(nil)
var _ operations.Executor = (*countingExecutor)(nil)

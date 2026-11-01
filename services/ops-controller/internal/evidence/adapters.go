package evidence

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrFixtureUnavailable = errors.New("diagnostic fixture unavailable")

type FixtureMode string

const (
	FixtureSuccess FixtureMode = "success"
	FixtureError   FixtureMode = "error"
)

type CollectRequest struct {
	IncidentID  string
	CollectedAt time.Time
	WindowStart time.Time
	WindowEnd   time.Time
}

func (r CollectRequest) validate() error {
	if err := requiredIdentifier("incident_id", r.IncidentID); err != nil {
		return err
	}
	if r.CollectedAt.IsZero() || r.WindowStart.IsZero() || r.WindowEnd.IsZero() {
		return errors.New("collection timestamps are required")
	}
	if r.WindowEnd.Before(r.WindowStart) || r.CollectedAt.Before(r.WindowStart) {
		return errors.New("invalid collection time window")
	}
	return nil
}

// DiagnosticAdapter returns validated, redacted evidence only. Production
// adapters should preserve this boundary and must not expose raw response bodies.
type DiagnosticAdapter interface {
	Source() Source
	Collect(context.Context, CollectRequest) (PreparedBundle, error)
}

// MockAdapter is deterministic and has no transport, URL, or credential
// fields, making accidental network access impossible.
type MockAdapter struct {
	source Source
	mode   FixtureMode
}

func NewMockPrometheus(mode FixtureMode) *MockAdapter { return newMock(SourcePrometheus, mode) }
func NewMockLoki(mode FixtureMode) *MockAdapter       { return newMock(SourceLoki, mode) }
func NewMockTempo(mode FixtureMode) *MockAdapter      { return newMock(SourceTempo, mode) }
func NewMockPostgreSQL(mode FixtureMode) *MockAdapter { return newMock(SourcePostgreSQL, mode) }
func NewMockRedis(mode FixtureMode) *MockAdapter      { return newMock(SourceRedis, mode) }
func NewMockDeployment(mode FixtureMode) *MockAdapter { return newMock(SourceDeployment, mode) }

func newMock(source Source, mode FixtureMode) *MockAdapter {
	return &MockAdapter{source: source, mode: mode}
}

func (m *MockAdapter) Source() Source { return m.source }

func (m *MockAdapter) Collect(ctx context.Context, request CollectRequest) (PreparedBundle, error) {
	if err := ctx.Err(); err != nil {
		return PreparedBundle{}, err
	}
	if err := request.validate(); err != nil {
		return PreparedBundle{}, err
	}
	if m.mode != FixtureSuccess && m.mode != FixtureError {
		return PreparedBundle{}, errors.New("unsupported diagnostic fixture mode")
	}
	if m.mode == FixtureError {
		return PreparedBundle{}, fmt.Errorf("%s: %w", m.source, ErrFixtureUnavailable)
	}
	record, err := fixtureRecord(m.source, request.CollectedAt)
	if err != nil {
		return PreparedBundle{}, err
	}
	return Prepare(Bundle{
		Version: SchemaVersion, IncidentID: request.IncidentID,
		CollectedAt: request.CollectedAt, WindowStart: request.WindowStart, WindowEnd: request.WindowEnd,
		Records: []Reference{record},
	})
}

func fixtureRecord(source Source, observedAt time.Time) (Reference, error) {
	base := Reference{ID: "fixture-" + string(source), Source: source, ObservedAt: observedAt}
	switch source {
	case SourcePrometheus:
		base.Kind = KindMetric
		base.Metric = &MetricReference{Name: "http_request_duration_p95", Value: 1.25, Unit: "seconds", Comparator: "gte", Labels: []Label{{Name: "service", Value: "api"}, {Name: "route", Value: "predictions_today"}}}
	case SourceLoki:
		base.Kind = KindLog
		base.Log = &LogReference{Stream: "api", QueryFingerprint: "sha256-log-query-001", MatchCount: 12, Levels: []LevelCount{{Level: "error", Count: 4}, {Level: "warn", Count: 8}}, SampleHashes: []string{"sha256-log-sample-001", "sha256-log-sample-002"}}
	case SourceTempo:
		base.Kind = KindTrace
		base.Trace = &TraceReference{TraceIDHash: "sha256-trace-001", Service: "api", Operation: "predictions_today", DurationMS: 1250, Status: "error", ErrorClass: "deadline_exceeded"}
	case SourcePostgreSQL:
		base.Kind = KindPostgreSQL
		base.PostgreSQL = &PostgreSQLReference{DatabaseAlias: "primary", QueryFingerprint: "sha256-query-001", DurationMS: 820, Rows: 40, WaitEvent: "ClientRead", RelationAliases: []string{"engine_predictions", "matches"}}
	case SourceRedis:
		base.Kind = KindRedis
		base.Redis = &RedisReference{KeyspaceAlias: "public_cache", CommandFamily: "get", DurationMS: 9, Hits: 910, Misses: 90}
	case SourceDeployment:
		base.Kind = KindDeployment
		base.Deployment = &DeploymentReference{Service: "api", Environment: "staging", ReleaseID: "release-20260814-001", CommitSHA: "0123456789abcdef0123456789abcdef01234567", ImageDigest: "sha256:0123456789abcdef", Status: "healthy", DeployedAt: observedAt.Add(-10 * time.Minute)}
	default:
		return Reference{}, errors.New("unsupported diagnostic fixture source")
	}
	return base, nil
}

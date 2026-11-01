// Package evidence defines the bounded, redacted diagnostic contract used by
// the operations controller. The contract intentionally contains references
// and aggregates only; raw log bodies, SQL, Redis keys, HTTP payloads, and
// arbitrary attribute maps are not representable.
package evidence

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

const (
	SchemaVersion       = "diagnostic-evidence.v1"
	MaxRecords          = 64
	MaxLabelsPerMetric  = 12
	MaxLevelCounts      = 8
	MaxSampleHashes     = 8
	MaxRelationAliases  = 12
	MaxStringLength     = 256
	MaxIdentifierLength = 128
)

type Source string

const (
	SourcePrometheus Source = "prometheus"
	SourceLoki       Source = "loki"
	SourceTempo      Source = "tempo"
	SourcePostgreSQL Source = "postgresql"
	SourceRedis      Source = "redis"
	SourceDeployment Source = "deployment"
)

type Kind string

const (
	KindMetric     Kind = "metric"
	KindLog        Kind = "log"
	KindTrace      Kind = "trace"
	KindPostgreSQL Kind = "postgresql"
	KindRedis      Kind = "redis"
	KindDeployment Kind = "deployment"
)

// Bundle is the persistence and AI-dispatch unit. Call Prepare before either
// use; PreparedBundle cannot be constructed outside this package.
type Bundle struct {
	Version     string      `json:"version"`
	IncidentID  string      `json:"incident_id"`
	CollectedAt time.Time   `json:"collected_at"`
	WindowStart time.Time   `json:"window_start"`
	WindowEnd   time.Time   `json:"window_end"`
	Records     []Reference `json:"records"`
}

// PreparedBundle proves that schema validation and deterministic redaction
// have completed. Value returns a defensive copy suitable for persistence or
// dispatch.
type PreparedBundle struct{ bundle Bundle }

func (p PreparedBundle) Value() Bundle { return cloneBundle(p.bundle) }

type Reference struct {
	ID         string               `json:"id"`
	Kind       Kind                 `json:"kind"`
	Source     Source               `json:"source"`
	ObservedAt time.Time            `json:"observed_at"`
	Metric     *MetricReference     `json:"metric,omitempty"`
	Log        *LogReference        `json:"log,omitempty"`
	Trace      *TraceReference      `json:"trace,omitempty"`
	PostgreSQL *PostgreSQLReference `json:"postgresql,omitempty"`
	Redis      *RedisReference      `json:"redis,omitempty"`
	Deployment *DeploymentReference `json:"deployment,omitempty"`
}

type Label struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type MetricReference struct {
	Name       string  `json:"name"`
	Value      float64 `json:"value"`
	Unit       string  `json:"unit"`
	Comparator string  `json:"comparator"`
	Labels     []Label `json:"labels,omitempty"`
}

type LevelCount struct {
	Level string `json:"level"`
	Count int64  `json:"count"`
}

type LogReference struct {
	Stream           string       `json:"stream"`
	QueryFingerprint string       `json:"query_fingerprint"`
	MatchCount       int64        `json:"match_count"`
	Levels           []LevelCount `json:"levels,omitempty"`
	SampleHashes     []string     `json:"sample_hashes,omitempty"`
}

type TraceReference struct {
	TraceIDHash string `json:"trace_id_hash"`
	Service     string `json:"service"`
	Operation   string `json:"operation"`
	DurationMS  int64  `json:"duration_ms"`
	Status      string `json:"status"`
	ErrorClass  string `json:"error_class,omitempty"`
}

type PostgreSQLReference struct {
	DatabaseAlias    string   `json:"database_alias"`
	QueryFingerprint string   `json:"query_fingerprint"`
	DurationMS       int64    `json:"duration_ms"`
	Rows             int64    `json:"rows"`
	WaitEvent        string   `json:"wait_event,omitempty"`
	RelationAliases  []string `json:"relation_aliases,omitempty"`
}

type RedisReference struct {
	KeyspaceAlias string `json:"keyspace_alias"`
	CommandFamily string `json:"command_family"`
	DurationMS    int64  `json:"duration_ms"`
	Hits          int64  `json:"hits"`
	Misses        int64  `json:"misses"`
	ErrorClass    string `json:"error_class,omitempty"`
}

type DeploymentReference struct {
	Service     string    `json:"service"`
	Environment string    `json:"environment"`
	ReleaseID   string    `json:"release_id"`
	CommitSHA   string    `json:"commit_sha"`
	ImageDigest string    `json:"image_digest"`
	Status      string    `json:"status"`
	DeployedAt  time.Time `json:"deployed_at"`
}

var safeToken = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:/@+\-=]*$`)

func (b Bundle) Validate() error {
	if b.Version != SchemaVersion {
		return errors.New("unsupported evidence version")
	}
	if err := requiredIdentifier("incident_id", b.IncidentID); err != nil {
		return err
	}
	if b.CollectedAt.IsZero() || b.WindowStart.IsZero() || b.WindowEnd.IsZero() {
		return errors.New("collection and window timestamps are required")
	}
	if b.WindowEnd.Before(b.WindowStart) || b.CollectedAt.Before(b.WindowStart) {
		return errors.New("invalid evidence time window")
	}
	if len(b.Records) == 0 || len(b.Records) > MaxRecords {
		return fmt.Errorf("records must contain 1..%d entries", MaxRecords)
	}
	seen := make(map[string]struct{}, len(b.Records))
	for i := range b.Records {
		if err := b.Records[i].validate(); err != nil {
			return fmt.Errorf("record %d: %w", i, err)
		}
		if _, ok := seen[b.Records[i].ID]; ok {
			return fmt.Errorf("record %d: duplicate id", i)
		}
		seen[b.Records[i].ID] = struct{}{}
	}
	return nil
}

func (r Reference) validate() error {
	if err := requiredIdentifier("id", r.ID); err != nil {
		return err
	}
	if r.ObservedAt.IsZero() {
		return errors.New("observed_at is required")
	}
	present := 0
	for _, v := range []bool{r.Metric != nil, r.Log != nil, r.Trace != nil, r.PostgreSQL != nil, r.Redis != nil, r.Deployment != nil} {
		if v {
			present++
		}
	}
	if present != 1 {
		return errors.New("exactly one typed evidence reference is required")
	}
	switch r.Kind {
	case KindMetric:
		if r.Source != SourcePrometheus || r.Metric == nil {
			return errors.New("metric evidence requires prometheus source and metric reference")
		}
		return r.Metric.validate()
	case KindLog:
		if r.Source != SourceLoki || r.Log == nil {
			return errors.New("log evidence requires loki source and log reference")
		}
		return r.Log.validate()
	case KindTrace:
		if r.Source != SourceTempo || r.Trace == nil {
			return errors.New("trace evidence requires tempo source and trace reference")
		}
		return r.Trace.validate()
	case KindPostgreSQL:
		if r.Source != SourcePostgreSQL || r.PostgreSQL == nil {
			return errors.New("postgresql evidence requires postgresql source and reference")
		}
		return r.PostgreSQL.validate()
	case KindRedis:
		if r.Source != SourceRedis || r.Redis == nil {
			return errors.New("redis evidence requires redis source and reference")
		}
		return r.Redis.validate()
	case KindDeployment:
		if r.Source != SourceDeployment || r.Deployment == nil {
			return errors.New("deployment evidence requires deployment source and reference")
		}
		return r.Deployment.validate()
	default:
		return errors.New("unsupported evidence kind")
	}
}

func (m MetricReference) validate() error {
	if err := requiredToken("metric name", m.Name); err != nil {
		return err
	}
	if err := requiredToken("metric unit", m.Unit); err != nil {
		return err
	}
	if m.Comparator != "eq" && m.Comparator != "gt" && m.Comparator != "gte" && m.Comparator != "lt" && m.Comparator != "lte" {
		return errors.New("invalid metric comparator")
	}
	if math.IsNaN(m.Value) || math.IsInf(m.Value, 0) {
		return errors.New("metric value must be finite")
	}
	if len(m.Labels) > MaxLabelsPerMetric {
		return fmt.Errorf("metric labels exceed %d", MaxLabelsPerMetric)
	}
	for _, l := range m.Labels {
		if err := requiredToken("label name", l.Name); err != nil {
			return err
		}
		if err := bounded("label value", l.Value); err != nil {
			return err
		}
	}
	return nil
}

func (l LogReference) validate() error {
	if err := requiredToken("log stream", l.Stream); err != nil {
		return err
	}
	if err := requiredIdentifier("query fingerprint", l.QueryFingerprint); err != nil {
		return err
	}
	if l.MatchCount < 0 {
		return errors.New("log match count cannot be negative")
	}
	if len(l.Levels) > MaxLevelCounts || len(l.SampleHashes) > MaxSampleHashes {
		return errors.New("log aggregates exceed bounds")
	}
	for _, level := range l.Levels {
		if err := requiredToken("log level", level.Level); err != nil {
			return err
		}
		if level.Count < 0 {
			return errors.New("log level count cannot be negative")
		}
	}
	for _, hash := range l.SampleHashes {
		if err := requiredIdentifier("sample hash", hash); err != nil {
			return err
		}
	}
	return nil
}

func (t TraceReference) validate() error {
	for n, v := range map[string]string{"trace hash": t.TraceIDHash, "trace service": t.Service, "trace operation": t.Operation, "trace status": t.Status} {
		if err := requiredToken(n, v); err != nil {
			return err
		}
	}
	if t.DurationMS < 0 {
		return errors.New("trace duration cannot be negative")
	}
	return optionalBounded("trace error class", t.ErrorClass)
}

func (p PostgreSQLReference) validate() error {
	if err := requiredToken("database alias", p.DatabaseAlias); err != nil {
		return err
	}
	if err := requiredIdentifier("query fingerprint", p.QueryFingerprint); err != nil {
		return err
	}
	if p.DurationMS < 0 || p.Rows < 0 {
		return errors.New("postgresql duration and rows cannot be negative")
	}
	if err := optionalBounded("wait event", p.WaitEvent); err != nil {
		return err
	}
	if len(p.RelationAliases) > MaxRelationAliases {
		return fmt.Errorf("relation aliases exceed %d", MaxRelationAliases)
	}
	for _, alias := range p.RelationAliases {
		if err := requiredToken("relation alias", alias); err != nil {
			return err
		}
	}
	return nil
}

func (r RedisReference) validate() error {
	if err := requiredToken("redis keyspace alias", r.KeyspaceAlias); err != nil {
		return err
	}
	if err := requiredToken("redis command family", r.CommandFamily); err != nil {
		return err
	}
	if r.DurationMS < 0 || r.Hits < 0 || r.Misses < 0 {
		return errors.New("redis counters cannot be negative")
	}
	return optionalBounded("redis error class", r.ErrorClass)
}

func (d DeploymentReference) validate() error {
	for n, v := range map[string]string{"deployment service": d.Service, "deployment environment": d.Environment, "release id": d.ReleaseID, "commit sha": d.CommitSHA, "image digest": d.ImageDigest, "deployment status": d.Status} {
		if err := requiredToken(n, v); err != nil {
			return err
		}
	}
	if d.DeployedAt.IsZero() {
		return errors.New("deployment timestamp is required")
	}
	return nil
}

func requiredIdentifier(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > MaxIdentifierLength {
		return fmt.Errorf("%s exceeds %d bytes", name, MaxIdentifierLength)
	}
	if !safeToken.MatchString(value) {
		return fmt.Errorf("%s contains unsupported characters", name)
	}
	return nil
}

func requiredToken(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > MaxStringLength {
		return fmt.Errorf("%s exceeds %d bytes", name, MaxStringLength)
	}
	if !safeToken.MatchString(value) {
		return fmt.Errorf("%s contains unsupported characters", name)
	}
	return nil
}

func bounded(name, value string) error {
	if len(value) > MaxStringLength {
		return fmt.Errorf("%s exceeds %d bytes", name, MaxStringLength)
	}
	return nil
}
func optionalBounded(name, value string) error {
	if value == "" {
		return nil
	}
	return bounded(name, value)
}

func cloneBundle(in Bundle) Bundle {
	out := in
	out.Records = make([]Reference, len(in.Records))
	for i, record := range in.Records {
		out.Records[i] = record
		if record.Metric != nil {
			v := *record.Metric
			v.Labels = append([]Label(nil), v.Labels...)
			out.Records[i].Metric = &v
		}
		if record.Log != nil {
			v := *record.Log
			v.Levels = append([]LevelCount(nil), v.Levels...)
			v.SampleHashes = append([]string(nil), v.SampleHashes...)
			out.Records[i].Log = &v
		}
		if record.Trace != nil {
			v := *record.Trace
			out.Records[i].Trace = &v
		}
		if record.PostgreSQL != nil {
			v := *record.PostgreSQL
			v.RelationAliases = append([]string(nil), v.RelationAliases...)
			out.Records[i].PostgreSQL = &v
		}
		if record.Redis != nil {
			v := *record.Redis
			out.Records[i].Redis = &v
		}
		if record.Deployment != nil {
			v := *record.Deployment
			out.Records[i].Deployment = &v
		}
	}
	return out
}

package evidence

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func testTime() time.Time { return time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC) }

func validBundle() Bundle {
	now := testTime()
	records := make([]Reference, 0, 6)
	for _, source := range []Source{SourcePrometheus, SourceLoki, SourceTempo, SourcePostgreSQL, SourceRedis, SourceDeployment} {
		record, err := fixtureRecord(source, now)
		if err != nil {
			panic(err)
		}
		records = append(records, record)
	}
	return Bundle{Version: SchemaVersion, IncidentID: "incident-001", CollectedAt: now, WindowStart: now.Add(-5 * time.Minute), WindowEnd: now, Records: records}
}

func TestBundleValidateAcceptsEveryTypedReference(t *testing.T) {
	if err := validBundle().Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestBundleValidateRejectsUnsafeShapes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Bundle)
	}{
		{"version", func(b *Bundle) { b.Version = "v2" }},
		{"missing incident", func(b *Bundle) { b.IncidentID = "" }},
		{"bad window", func(b *Bundle) { b.WindowEnd = b.WindowStart.Add(-time.Second) }},
		{"empty records", func(b *Bundle) { b.Records = nil }},
		{"too many records", func(b *Bundle) { b.Records = make([]Reference, MaxRecords+1) }},
		{"duplicate ids", func(b *Bundle) { b.Records[1].ID = b.Records[0].ID }},
		{"two payloads", func(b *Bundle) { b.Records[0].Log = b.Records[1].Log }},
		{"kind source mismatch", func(b *Bundle) { b.Records[0].Source = SourceLoki }},
		{"unsafe arbitrary field", func(b *Bundle) { b.Records[0].Metric.Name = "raw metric body with spaces" }},
		{"nonfinite metric", func(b *Bundle) { b.Records[0].Metric.Value = math.Inf(1) }},
		{"too many labels", func(b *Bundle) { b.Records[0].Metric.Labels = make([]Label, MaxLabelsPerMetric+1) }},
		{"too many log hashes", func(b *Bundle) { b.Records[1].Log.SampleHashes = make([]string, MaxSampleHashes+1) }},
		{"negative trace duration", func(b *Bundle) { b.Records[2].Trace.DurationMS = -1 }},
		{"too many relations", func(b *Bundle) { b.Records[3].PostgreSQL.RelationAliases = make([]string, MaxRelationAliases+1) }},
		{"negative redis hits", func(b *Bundle) { b.Records[4].Redis.Hits = -1 }},
		{"missing deploy timestamp", func(b *Bundle) { b.Records[5].Deployment.DeployedAt = time.Time{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := validBundle()
			tt.mutate(&b)
			if err := b.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
		})
	}
}

func TestPreparedBundleValueIsDefensiveCopy(t *testing.T) {
	prepared, err := Prepare(validBundle())
	if err != nil {
		t.Fatal(err)
	}
	first := prepared.Value()
	first.Records[0].Metric.Labels[0].Value = "mutated"
	first.Records[1].Log.SampleHashes[0] = "mutated"
	second := prepared.Value()
	if second.Records[0].Metric.Labels[0].Value == "mutated" || second.Records[1].Log.SampleHashes[0] == "mutated" {
		t.Fatal("prepared evidence exposed mutable backing storage")
	}
}

func TestSchemaDoesNotRepresentRawDiagnosticBodies(t *testing.T) {
	encoded, err := json.Marshal(validBundle())
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"raw_body", "log_body", "sql_text", "redis_key", "http_payload", "attributes"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("schema exposed forbidden field %q", forbidden)
		}
	}
}

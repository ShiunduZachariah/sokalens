package telemetry

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMetricsExposeReadinessAndBoundedHTTPLabels(t *testing.T) {
	metrics := NewMetrics()
	metrics.SetReady(true)
	metrics.ObserveHTTP("GET", "/healthz", 200)
	metrics.ObserveHTTP("GET", "/healthz", 200)
	metrics.ObserveAlert("accepted")

	var output bytes.Buffer
	metrics.WritePrometheus(&output)
	text := output.String()
	for _, want := range []string{
		"ops_controller_up 1",
		"ops_controller_ready 1",
		`ops_controller_http_requests_total{method="GET",route="/healthz",status="200"} 2`,
		`ops_controller_alert_ingest_total{outcome="accepted"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q:\n%s", want, text)
		}
	}
}

// telegramMetrics mirrors the Metrics interface in
// internal/telegram/contract.go. It is restated here rather than imported so
// this package keeps no dependency on the bridge; if contract.go changes, this
// assertion must be updated with it.
type telegramMetrics interface {
	SetPollerUp(up bool)
	SetOffset(offset int64)
	IncUpdatesReceived()
	IncUnauthorized()
	IncDuplicate()
	IncPollError(kind string)
	ObserveCommand(command, status string, d time.Duration)
	IncMessageSent(tier string)
	IncMessageFailed(reason string)
	IncMessageSuppressed()
}

var _ telegramMetrics = (*Metrics)(nil)

var (
	metricNamePattern = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	labelNamePattern  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
	samplePattern     = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{(.*)\})? (\S+)$`)
)

type sample struct {
	name   string
	labels map[string]string
	value  string
}

// parseExposition is a strict-enough Prometheus text format parser: it rejects
// undeclared families, duplicate series, malformed label sets and non-numeric
// values.
func parseExposition(t *testing.T, text string) []sample {
	t.Helper()
	types := map[string]string{}
	helps := map[string]bool{}
	seen := map[string]bool{}
	var samples []sample

	for i, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		lineNo := i + 1
		if strings.HasPrefix(line, "#") {
			fields := strings.SplitN(strings.TrimSpace(strings.TrimPrefix(line, "#")), " ", 3)
			switch fields[0] {
			case "HELP":
				if len(fields) < 3 || !metricNamePattern.MatchString(fields[1]) {
					t.Fatalf("line %d: malformed HELP: %q", lineNo, line)
				}
				if helps[fields[1]] {
					t.Fatalf("line %d: duplicate HELP for %q", lineNo, fields[1])
				}
				helps[fields[1]] = true
			case "TYPE":
				if len(fields) < 3 || !metricNamePattern.MatchString(fields[1]) {
					t.Fatalf("line %d: malformed TYPE: %q", lineNo, line)
				}
				switch fields[2] {
				case "counter", "gauge", "histogram", "summary", "untyped":
				default:
					t.Fatalf("line %d: unknown metric type %q", lineNo, fields[2])
				}
				if _, exists := types[fields[1]]; exists {
					t.Fatalf("line %d: duplicate TYPE for %q", lineNo, fields[1])
				}
				types[fields[1]] = fields[2]
			default:
				t.Fatalf("line %d: unknown comment directive: %q", lineNo, line)
			}
			continue
		}

		match := samplePattern.FindStringSubmatch(line)
		if match == nil {
			t.Fatalf("line %d: malformed sample: %q", lineNo, line)
		}
		parsed := sample{name: match[1], labels: map[string]string{}, value: match[4]}
		if match[3] != "" {
			for _, pair := range splitLabels(t, lineNo, match[3]) {
				key, val, ok := strings.Cut(pair, "=")
				if !ok || !labelNamePattern.MatchString(key) {
					t.Fatalf("line %d: malformed label %q", lineNo, pair)
				}
				if len(val) < 2 || !strings.HasPrefix(val, `"`) || !strings.HasSuffix(val, `"`) {
					t.Fatalf("line %d: label value not quoted: %q", lineNo, pair)
				}
				if _, dup := parsed.labels[key]; dup {
					t.Fatalf("line %d: duplicate label %q", lineNo, key)
				}
				parsed.labels[key] = strings.Trim(val, `"`)
			}
		}
		if _, err := strconv.ParseFloat(parsed.value, 64); err != nil && parsed.value != "+Inf" && parsed.value != "NaN" {
			t.Fatalf("line %d: value %q is not a float", lineNo, parsed.value)
		}
		if _, declared := types[familyOf(parsed.name, types)]; !declared {
			t.Fatalf("line %d: sample %q has no declared TYPE", lineNo, parsed.name)
		}
		series := parsed.name + "\x00" + canonicalLabels(parsed.labels)
		if seen[series] {
			t.Fatalf("line %d: duplicate series %q", lineNo, line)
		}
		seen[series] = true
		samples = append(samples, parsed)
	}
	return samples
}

// familyOf resolves a histogram child series back to its declared family.
func familyOf(name string, types map[string]string) string {
	if _, ok := types[name]; ok {
		return name
	}
	for _, suffix := range []string{"_bucket", "_sum", "_count"} {
		base := strings.TrimSuffix(name, suffix)
		if base == name {
			continue
		}
		if kind, ok := types[base]; ok && (kind == "histogram" || kind == "summary") {
			return base
		}
	}
	return name
}

func splitLabels(t *testing.T, lineNo int, raw string) []string {
	t.Helper()
	var pairs []string
	var current strings.Builder
	inQuotes := false
	for _, r := range raw {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			current.WriteRune(r)
		case r == ',' && !inQuotes:
			pairs = append(pairs, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	if inQuotes {
		t.Fatalf("line %d: unterminated label value in %q", lineNo, raw)
	}
	pairs = append(pairs, current.String())
	return pairs
}

func canonicalLabels(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}
	return strings.Join(parts, ",")
}

func render(t *testing.T, metrics *Metrics) string {
	t.Helper()
	var output bytes.Buffer
	metrics.WritePrometheus(&output)
	return output.String()
}

func TestWritePrometheusEmitsValidExpositionWithTelegramCollectors(t *testing.T) {
	metrics := NewMetrics()
	// Pre-existing collectors must keep rendering alongside the new ones.
	metrics.SetReady(true)
	metrics.ObserveHTTP("GET", "/healthz", 200)
	metrics.ObserveAlert("accepted")

	metrics.SetPollerUp(true)
	metrics.SetOffset(4711)
	metrics.IncUpdatesReceived()
	metrics.IncUpdatesReceived()
	metrics.IncUnauthorized()
	metrics.IncDuplicate()
	metrics.IncPollError("conflict")
	metrics.ObserveCommand("/status", "ok", 120*time.Millisecond)
	metrics.ObserveCommand("status", "error", 3*time.Second)
	metrics.IncMessageSent("important")
	metrics.IncMessageFailed("rate_limited")
	metrics.IncMessageSuppressed()

	text := render(t, metrics)
	parseExposition(t, text)

	for _, want := range []string{
		"ops_controller_up 1",
		"ops_controller_ready 1",
		`ops_controller_http_requests_total{method="GET",route="/healthz",status="200"} 1`,
		`ops_controller_alert_ingest_total{outcome="accepted"} 1`,
		"ops_telegram_poller_up 1",
		"ops_telegram_updates_received_total 2",
		"ops_telegram_updates_unauthorized_total 1",
		"ops_telegram_updates_duplicate_total 1",
		"ops_telegram_messages_suppressed_total 1",
		"ops_telegram_offset 4711",
		`ops_telegram_commands_total{command="status",status="ok"} 1`,
		`ops_telegram_commands_total{command="status",status="error"} 1`,
		`ops_telegram_messages_sent_total{tier="important"} 1`,
		`ops_telegram_messages_failed_total{reason="rate_limited"} 1`,
		`ops_telegram_poll_errors_total{kind="conflict"} 1`,
		`ops_telegram_command_duration_seconds_count{command="status"} 2`,
		`ops_telegram_command_duration_seconds_bucket{command="status",le="0.25"} 1`,
		`ops_telegram_command_duration_seconds_bucket{command="status",le="+Inf"} 2`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics missing %q:\n%s", want, text)
		}
	}
}

// Every §19 collector must be declared even before it records anything, so a
// dashboard never depends on traffic having happened first.
func TestWritePrometheusDeclaresEveryTelegramCollectorWhenIdle(t *testing.T) {
	text := render(t, NewMetrics())
	parseExposition(t, text)
	for _, name := range []string{
		"ops_telegram_poller_up",
		"ops_telegram_updates_received_total",
		"ops_telegram_updates_unauthorized_total",
		"ops_telegram_updates_duplicate_total",
		"ops_telegram_commands_total",
		"ops_telegram_command_duration_seconds",
		"ops_telegram_messages_sent_total",
		"ops_telegram_messages_failed_total",
		"ops_telegram_messages_suppressed_total",
		"ops_telegram_poll_errors_total",
		"ops_telegram_offset",
	} {
		if !strings.Contains(text, "# TYPE "+name+" ") {
			t.Fatalf("collector %q not declared:\n%s", name, text)
		}
	}
	if !strings.Contains(text, "ops_telegram_poller_up 0") {
		t.Fatalf("idle poller must render 0:\n%s", text)
	}
}

// Label values are attacker-influenced: a crafted command must collapse into
// the bounded vocabulary instead of minting a time series.
func TestTelegramLabelsAreBounded(t *testing.T) {
	metrics := NewMetrics()
	hostile := []string{
		`"; DROP TABLE ops_incidents; --`,
		strings.Repeat("a", 4096),
		"status\nops_telegram_offset 999",
		`quoted"label`,
		"",
		"   ",
		"UNKNOWN-COMMAND-42",
	}
	for _, raw := range hostile {
		metrics.ObserveCommand(raw, raw, time.Millisecond)
		metrics.IncMessageSent(raw)
		metrics.IncMessageFailed(raw)
		metrics.IncPollError(raw)
	}

	text := render(t, metrics)
	samples := parseExposition(t, text)

	for _, s := range samples {
		for key, val := range s.labels {
			switch key {
			case "command", "status", "tier", "reason", "kind":
				if val != "other" && !isKnownTelegramLabel(val) {
					t.Fatalf("unbounded %s label %q in %s", key, val, s.name)
				}
			}
		}
	}
	if !strings.Contains(text, `ops_telegram_commands_total{command="other",status="other"} 7`) {
		t.Fatalf("hostile commands not collapsed into a single series:\n%s", text)
	}
	if !strings.Contains(text, `ops_telegram_poll_errors_total{kind="other"} 7`) {
		t.Fatalf("hostile poll error kinds not collapsed:\n%s", text)
	}
	// A crafted value must not be able to inject a line of its own.
	if strings.Contains(text, "\nops_telegram_offset 999") {
		t.Fatalf("label value escaped into the exposition:\n%s", text)
	}
}

func isKnownTelegramLabel(value string) bool {
	for _, set := range []map[string]struct{}{
		telegramCommandLabels, telegramCommandStatusLabels,
		telegramPollErrorKindLabels, telegramMessageTierLabels, telegramMessageReasonLabels,
	} {
		if _, ok := set[value]; ok {
			return true
		}
	}
	return false
}

func TestTelegramCollectorsAreConcurrencySafe(t *testing.T) {
	metrics := NewMetrics()
	const workers, iterations = 8, 200
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				metrics.IncUpdatesReceived()
				metrics.IncUnauthorized()
				metrics.IncDuplicate()
				metrics.IncMessageSuppressed()
				metrics.ObserveCommand("status", "ok", time.Duration(i)*time.Millisecond)
				metrics.IncMessageSent("info")
				metrics.IncMessageFailed("timeout")
				metrics.IncPollError("network")
				metrics.SetOffset(int64(worker*iterations + i))
				metrics.SetPollerUp(i%2 == 0)
				metrics.WritePrometheus(io.Discard)
			}
		}(w)
	}
	wg.Wait()

	text := render(t, metrics)
	parseExposition(t, text)
	total := workers * iterations
	for _, want := range []string{
		fmt.Sprintf("ops_telegram_updates_received_total %d", total),
		fmt.Sprintf("ops_telegram_messages_suppressed_total %d", total),
		fmt.Sprintf(`ops_telegram_commands_total{command="status",status="ok"} %d`, total),
		fmt.Sprintf(`ops_telegram_command_duration_seconds_count{command="status"} %d`, total),
		fmt.Sprintf(`ops_telegram_poll_errors_total{kind="network"} %d`, total),
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("lost counts under concurrency, missing %q", want)
		}
	}
}

// Buckets are cumulative and must be non-decreasing, ending at _count.
func TestCommandDurationHistogramIsCumulative(t *testing.T) {
	metrics := NewMetrics()
	for _, d := range []time.Duration{10 * time.Millisecond, 300 * time.Millisecond, 90 * time.Second} {
		metrics.ObserveCommand("incidents", "ok", d)
	}
	text := render(t, metrics)
	samples := parseExposition(t, text)

	var previous, infValue, count float64
	for _, s := range samples {
		switch s.name {
		case "ops_telegram_command_duration_seconds_bucket":
			value, err := strconv.ParseFloat(s.value, 64)
			if err != nil {
				t.Fatal(err)
			}
			if value < previous {
				t.Fatalf("bucket le=%s value %v decreased from %v", s.labels["le"], value, previous)
			}
			previous = value
			if s.labels["le"] == "+Inf" {
				infValue = value
			}
		case "ops_telegram_command_duration_seconds_count":
			count, _ = strconv.ParseFloat(s.value, 64)
		}
	}
	if count != 3 || infValue != count {
		t.Fatalf("+Inf bucket %v must equal count %v (want 3)", infValue, count)
	}
	// 90s exceeds the largest finite bucket, so it lands only in +Inf.
	if !strings.Contains(text, `ops_telegram_command_duration_seconds_bucket{command="incidents",le="30"} 2`) {
		t.Fatalf("out-of-range observation counted in a finite bucket:\n%s", text)
	}
}

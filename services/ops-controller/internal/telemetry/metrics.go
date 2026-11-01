package telemetry

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// labelOther is the sink every unrecognised label value collapses into.
// Telegram command text is attacker-influenced free text, so no label value
// may ever originate from user input: an unknown value becomes "other" rather
// than a new time series.
const labelOther = "other"

// Bounded label vocabularies for the Telegram ops bridge collectors
// (spec §19). Each set is closed; adding a value is a code change with a
// cardinality review, not a runtime accident.
var (
	telegramCommandLabels = newLabelSet(
		"help", "status", "incidents", "incident", "queues", "deploys", "budget",
		"run", "confirm", "issue", "release", "hold", "unknown",
	)
	telegramCommandStatusLabels = newLabelSet("ok", "error", "denied", "invalid", "timeout")
	// "api" is an API error the poller could not classify further, and
	// "oversize" is the client rejecting an over-large response body. Both are
	// emitted by poller.go and client.go; without them they would silently
	// collapse into "other" and a real fault would look like noise.
	telegramPollErrorKindLabels = newLabelSet(
		"conflict", "unauthorized", "rate_limited", "timeout", "network", "decode",
		"server", "store", "api", "oversize",
	)
	telegramMessageTierLabels = newLabelSet("info", "important", "action_required")
	// "send" covers a reply that failed for an unclassified reason.
	telegramMessageReasonLabels = newLabelSet(
		"rate_limited", "forbidden", "not_found", "timeout", "network", "server",
		"too_long", "send",
	)
	telegramDurationSecondsUpper = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}
)

func newLabelSet(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values)+1)
	for _, value := range values {
		set[value] = struct{}{}
	}
	set[labelOther] = struct{}{}
	return set
}

// boundedLabel maps a caller-supplied label to the closed vocabulary.
func boundedLabel(set map[string]struct{}, raw string) string {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	normalized = strings.TrimPrefix(normalized, "/")
	if _, ok := set[normalized]; ok {
		return normalized
	}
	return labelOther
}

// telegramHistogram is a fixed-bucket histogram over telegramDurationSecondsUpper.
// Guarded by Metrics.telegramMu.
type telegramHistogram struct {
	buckets []uint64
	sum     float64
	count   uint64
}

func newTelegramHistogram() *telegramHistogram {
	return &telegramHistogram{buckets: make([]uint64, len(telegramDurationSecondsUpper))}
}

func (h *telegramHistogram) observe(seconds float64) {
	if seconds < 0 {
		seconds = 0
	}
	for i, upper := range telegramDurationSecondsUpper {
		if seconds <= upper {
			h.buckets[i]++
		}
	}
	h.sum += seconds
	h.count++
}

type Metrics struct {
	ready  atomic.Bool
	mu     sync.Mutex
	http   map[string]uint64
	alerts map[string]uint64

	// Telegram ops bridge. Scalars are atomics; the labelled families share
	// telegramMu so the bridge's send path never contends with HTTP serving.
	telegramPollerUp        atomic.Bool
	telegramOffset          atomic.Int64
	telegramUpdatesReceived atomic.Uint64
	telegramUnauthorized    atomic.Uint64
	telegramDuplicate       atomic.Uint64
	telegramSuppressed      atomic.Uint64

	telegramMu         sync.Mutex
	telegramCommands   map[string]uint64             // command\x00status
	telegramDurations  map[string]*telegramHistogram // command
	telegramSent       map[string]uint64             // tier
	telegramFailed     map[string]uint64             // reason
	telegramPollErrors map[string]uint64             // kind
}

func NewMetrics() *Metrics {
	return &Metrics{
		http:               make(map[string]uint64),
		alerts:             make(map[string]uint64),
		telegramCommands:   make(map[string]uint64),
		telegramDurations:  make(map[string]*telegramHistogram),
		telegramSent:       make(map[string]uint64),
		telegramFailed:     make(map[string]uint64),
		telegramPollErrors: make(map[string]uint64),
	}
}

func (m *Metrics) ObserveAlert(outcome string) {
	m.mu.Lock()
	m.alerts[outcome]++
	m.mu.Unlock()
}

func (m *Metrics) SetReady(ready bool) { m.ready.Store(ready) }
func (m *Metrics) Ready() bool         { return m.ready.Load() }

func (m *Metrics) ObserveHTTP(method, route string, status int) {
	key := fmt.Sprintf("%s\x00%s\x00%d", method, route, status)
	m.mu.Lock()
	m.http[key]++
	m.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Telegram ops bridge collectors (spec §19). These methods together satisfy
// the Metrics interface in internal/telegram/contract.go.
// ---------------------------------------------------------------------------

// SetPollerUp records whether this instance holds the poller lock and is
// actively long-polling.
func (m *Metrics) SetPollerUp(up bool) { m.telegramPollerUp.Store(up) }

// SetOffset records the last durably persisted getUpdates offset.
func (m *Metrics) SetOffset(offset int64) { m.telegramOffset.Store(offset) }

// IncUpdatesReceived counts every update returned by getUpdates.
func (m *Metrics) IncUpdatesReceived() { m.telegramUpdatesReceived.Add(1) }

// IncUnauthorized counts updates rejected by the chat/user allowlist.
func (m *Metrics) IncUnauthorized() { m.telegramUnauthorized.Add(1) }

// IncDuplicate counts redelivered updates caught by the update_id key.
func (m *Metrics) IncDuplicate() { m.telegramDuplicate.Add(1) }

// IncPollError counts poll failures by bounded kind.
func (m *Metrics) IncPollError(kind string) {
	label := boundedLabel(telegramPollErrorKindLabels, kind)
	m.telegramMu.Lock()
	m.telegramPollErrors[label]++
	m.telegramMu.Unlock()
}

// ObserveCommand records one command execution: its bounded outcome and its
// latency. Both labels are clamped to the closed vocabularies above, so a
// crafted command name cannot mint a new time series.
func (m *Metrics) ObserveCommand(command, status string, d time.Duration) {
	commandLabel := boundedLabel(telegramCommandLabels, command)
	statusLabel := boundedLabel(telegramCommandStatusLabels, status)
	key := commandLabel + "\x00" + statusLabel
	m.telegramMu.Lock()
	m.telegramCommands[key]++
	histogram, ok := m.telegramDurations[commandLabel]
	if !ok {
		histogram = newTelegramHistogram()
		m.telegramDurations[commandLabel] = histogram
	}
	histogram.observe(d.Seconds())
	m.telegramMu.Unlock()
}

// IncMessageSent counts delivered messages by notification tier.
func (m *Metrics) IncMessageSent(tier string) {
	label := boundedLabel(telegramMessageTierLabels, tier)
	m.telegramMu.Lock()
	m.telegramSent[label]++
	m.telegramMu.Unlock()
}

// IncMessageFailed counts send failures by bounded reason.
func (m *Metrics) IncMessageFailed(reason string) {
	label := boundedLabel(telegramMessageReasonLabels, reason)
	m.telegramMu.Lock()
	m.telegramFailed[label]++
	m.telegramMu.Unlock()
}

// IncMessageSuppressed counts replies deliberately not sent, including every
// reply withheld from an unauthorized sender.
func (m *Metrics) IncMessageSuppressed() { m.telegramSuppressed.Add(1) }

func (m *Metrics) WritePrometheus(w io.Writer) {
	ready := 0
	if m.Ready() {
		ready = 1
	}
	fmt.Fprintln(w, "# HELP ops_controller_up Whether the process is running.")
	fmt.Fprintln(w, "# TYPE ops_controller_up gauge")
	fmt.Fprintln(w, "ops_controller_up 1")
	fmt.Fprintln(w, "# HELP ops_controller_ready Whether the controller is ready to serve.")
	fmt.Fprintln(w, "# TYPE ops_controller_ready gauge")
	fmt.Fprintf(w, "ops_controller_ready %d\n", ready)
	fmt.Fprintln(w, "# HELP ops_controller_http_requests_total HTTP requests by bounded route and status.")
	fmt.Fprintln(w, "# TYPE ops_controller_http_requests_total counter")

	m.mu.Lock()
	snapshot := make(map[string]uint64, len(m.http))
	keys := make([]string, 0, len(m.http))
	for key, count := range m.http {
		keys = append(keys, key)
		snapshot[key] = count
	}
	m.mu.Unlock()
	sort.Strings(keys)
	for _, key := range keys {
		parts := strings.Split(key, "\x00")
		if len(parts) != 3 {
			continue
		}
		fmt.Fprintf(w, "ops_controller_http_requests_total{method=%q,route=%q,status=%q} %d\n", parts[0], parts[1], parts[2], snapshot[key])
	}
	fmt.Fprintln(w, "# HELP ops_controller_alert_ingest_total Alert ingestion attempts by bounded outcome.")
	fmt.Fprintln(w, "# TYPE ops_controller_alert_ingest_total counter")
	m.mu.Lock()
	alertSnapshot := make(map[string]uint64, len(m.alerts))
	alertKeys := make([]string, 0, len(m.alerts))
	for key, count := range m.alerts {
		alertKeys = append(alertKeys, key)
		alertSnapshot[key] = count
	}
	m.mu.Unlock()
	sort.Strings(alertKeys)
	for _, key := range alertKeys {
		fmt.Fprintf(w, "ops_controller_alert_ingest_total{outcome=%q} %d\n", key, alertSnapshot[key])
	}
	m.writeTelegramPrometheus(w)
}

// writeTelegramPrometheus renders the bridge collectors. It takes one
// consistent snapshot under telegramMu and formats outside the lock.
func (m *Metrics) writeTelegramPrometheus(w io.Writer) {
	pollerUp := 0
	if m.telegramPollerUp.Load() {
		pollerUp = 1
	}

	m.telegramMu.Lock()
	commands := copyCounters(m.telegramCommands)
	sent := copyCounters(m.telegramSent)
	failed := copyCounters(m.telegramFailed)
	pollErrors := copyCounters(m.telegramPollErrors)
	durations := make(map[string]telegramHistogram, len(m.telegramDurations))
	for command, histogram := range m.telegramDurations {
		snapshot := telegramHistogram{
			buckets: append([]uint64(nil), histogram.buckets...),
			sum:     histogram.sum,
			count:   histogram.count,
		}
		durations[command] = snapshot
	}
	m.telegramMu.Unlock()

	writeGauge(w, "ops_telegram_poller_up", "Whether this instance holds the poller lock and is polling.", pollerUp)
	writeCounter(w, "ops_telegram_updates_received_total", "Telegram updates returned by getUpdates.", m.telegramUpdatesReceived.Load())
	writeCounter(w, "ops_telegram_updates_unauthorized_total", "Updates rejected by the chat and user allowlist.", m.telegramUnauthorized.Load())
	writeCounter(w, "ops_telegram_updates_duplicate_total", "Redelivered updates rejected by the update_id key.", m.telegramDuplicate.Load())

	fmt.Fprintln(w, "# HELP ops_telegram_commands_total Command executions by bounded command and status.")
	fmt.Fprintln(w, "# TYPE ops_telegram_commands_total counter")
	for _, key := range sortedKeys(commands) {
		command, status, ok := strings.Cut(key, "\x00")
		if !ok {
			continue
		}
		fmt.Fprintf(w, "ops_telegram_commands_total{command=%q,status=%q} %d\n", command, status, commands[key])
	}

	fmt.Fprintln(w, "# HELP ops_telegram_command_duration_seconds Command execution latency by bounded command.")
	fmt.Fprintln(w, "# TYPE ops_telegram_command_duration_seconds histogram")
	for _, command := range sortedHistogramKeys(durations) {
		histogram := durations[command]
		for i, upper := range telegramDurationSecondsUpper {
			fmt.Fprintf(w, "ops_telegram_command_duration_seconds_bucket{command=%q,le=%q} %d\n", command, formatFloat(upper), histogram.buckets[i])
		}
		fmt.Fprintf(w, "ops_telegram_command_duration_seconds_bucket{command=%q,le=\"+Inf\"} %d\n", command, histogram.count)
		fmt.Fprintf(w, "ops_telegram_command_duration_seconds_sum{command=%q} %s\n", command, formatFloat(histogram.sum))
		fmt.Fprintf(w, "ops_telegram_command_duration_seconds_count{command=%q} %d\n", command, histogram.count)
	}

	writeLabelledCounter(w, "ops_telegram_messages_sent_total", "Messages delivered to Telegram by notification tier.", "tier", sent)
	writeLabelledCounter(w, "ops_telegram_messages_failed_total", "Message sends that failed, by bounded reason.", "reason", failed)
	writeCounter(w, "ops_telegram_messages_suppressed_total", "Replies deliberately withheld, including every reply to an unauthorized sender.", m.telegramSuppressed.Load())
	writeLabelledCounter(w, "ops_telegram_poll_errors_total", "getUpdates failures by bounded kind.", "kind", pollErrors)

	fmt.Fprintln(w, "# HELP ops_telegram_offset Last durably persisted getUpdates offset.")
	fmt.Fprintln(w, "# TYPE ops_telegram_offset gauge")
	fmt.Fprintf(w, "ops_telegram_offset %d\n", m.telegramOffset.Load())
}

func writeGauge(w io.Writer, name, help string, value int) {
	fmt.Fprintf(w, "# HELP %s %s\n", name, help)
	fmt.Fprintf(w, "# TYPE %s gauge\n", name)
	fmt.Fprintf(w, "%s %d\n", name, value)
}

func writeCounter(w io.Writer, name, help string, value uint64) {
	fmt.Fprintf(w, "# HELP %s %s\n", name, help)
	fmt.Fprintf(w, "# TYPE %s counter\n", name)
	fmt.Fprintf(w, "%s %d\n", name, value)
}

func writeLabelledCounter(w io.Writer, name, help, label string, counters map[string]uint64) {
	fmt.Fprintf(w, "# HELP %s %s\n", name, help)
	fmt.Fprintf(w, "# TYPE %s counter\n", name)
	for _, key := range sortedKeys(counters) {
		fmt.Fprintf(w, "%s{%s=%q} %d\n", name, label, key, counters[key])
	}
}

func copyCounters(source map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(source))
	for key, count := range source {
		out[key] = count
	}
	return out
}

func sortedKeys(counters map[string]uint64) []string {
	keys := make([]string, 0, len(counters))
	for key := range counters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedHistogramKeys(histograms map[string]telegramHistogram) []string {
	keys := make([]string, 0, len(histograms))
	for key := range histograms {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// formatFloat renders a bucket boundary or sum in Prometheus' plain decimal
// form, never in Go's exponent notation.
func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

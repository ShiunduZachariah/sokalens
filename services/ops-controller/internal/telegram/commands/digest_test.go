package commands

import (
	"context"
	"strings"
	"testing"
)

// TestDigestSummaryDegradesWithoutOmitting is the /status property applied to
// the message nobody asked for: with no projections wired every line must
// still be present and marked unavailable. A digest that quietly drops the
// queue line reads as "the queue is fine".
func TestDigestSummaryDegradesWithoutOmitting(t *testing.T) {
	sections := testDeps().DigestSummary(context.Background()) // every projection nil

	want := []string{"Health", "Controller", "Incidents", "Queue", "Production", "AI spend"}
	if len(sections) != len(want) {
		t.Fatalf("digest rendered %d sections, want %d", len(sections), len(want))
	}
	for i, label := range want {
		if sections[i].Label != label {
			t.Fatalf("section %d is %q, want %q", i, sections[i].Label, label)
		}
		if strings.TrimSpace(sections[i].Value) == "" {
			t.Fatalf("section %q is blank; it must say %q instead", label, unavailable)
		}
	}
	for _, s := range sections[2:] {
		if !strings.Contains(s.Value, unavailable) {
			t.Fatalf("section %q reported %q with no source wired", s.Label, s.Value)
		}
	}
	if strings.Contains(sections[0].Value, "🟢") {
		t.Fatalf("digest reported healthy with an unreadable incident store: %q", sections[0].Value)
	}
}

// TestDigestHealthMatchesStatusHeadline guards the one way these two can
// drift: the digest and /status must derive their marker from the same switch.
func TestDigestHealthMatchesStatusHeadline(t *testing.T) {
	for _, counts := range []incidentSeverity{{}, {Warning: 2}, {Critical: 1, Warning: 4}} {
		marker, word := healthLabel(counts, nil)
		if got := headline(counts, nil); !strings.Contains(got, marker) || !strings.Contains(got, word) {
			t.Fatalf("headline(%+v) = %q, want the digest's %q / %q", counts, got, marker, word)
		}
	}
}

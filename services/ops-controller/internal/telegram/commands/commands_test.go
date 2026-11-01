package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
)

// fixedNow keeps rendered ages deterministic.
func testDeps() Deps {
	return Deps{
		Version:     "v0.3.8",
		Environment: "production",
		Location:    time.UTC,
		Now:         func() time.Time { return time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC) },
	}
}

// TestCommandsDegradeWhenSourceMissing is the property that matters most for a
// 3am read: a projection that is not wired must render "unavailable" rather
// than erroring or, worse, omitting the line. An omitted line reads as "fine".
func TestCommandsDegradeWhenSourceMissing(t *testing.T) {
	deps := testDeps() // every projection nil
	ctx := context.Background()

	cases := map[string]func(context.Context, telegram.Request) (telegram.Response, error){
		"status":    deps.status,
		"incidents": deps.incidents,
		"queues":    deps.queues,
		"deploys":   deps.deploys,
		"budget":    deps.budget,
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			resp, err := handler(ctx, telegram.Request{Command: name})
			if err != nil {
				t.Fatalf("handler returned an error instead of degrading: %v", err)
			}
			if resp.Silent {
				t.Fatal("handler went silent; an ops read must always answer")
			}
			if !strings.Contains(resp.Body, unavailable) {
				t.Fatalf("missing %q marker, body was:\n%s", unavailable, resp.Body)
			}
		})
	}
}

// TestStatusHeadlineIsAmberWhenIncidentsUnreadable guards against the most
// dangerous rendering bug in the whole bridge: reporting green while the
// incident store is unreadable. Unknown is not healthy.
func TestStatusHeadlineIsAmberWhenIncidentsUnreadable(t *testing.T) {
	resp, err := testDeps().status(context.Background(), telegram.Request{})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if strings.Contains(resp.Body, "🟢") {
		t.Fatalf("status reported healthy with an unreadable incident store:\n%s", resp.Body)
	}
	if !strings.Contains(resp.Body, "🟡") {
		t.Fatalf("expected the degraded marker, got:\n%s", resp.Body)
	}
}

func TestHeadlineSeverityPrecedence(t *testing.T) {
	cases := []struct {
		name   string
		counts incidentSeverity
		want   string
	}{
		{"critical wins", incidentSeverity{Critical: 1, Warning: 5, Info: 9}, "🔴"},
		{"warning next", incidentSeverity{Warning: 1, Info: 9}, "🟡"},
		{"info alone is green", incidentSeverity{Info: 3}, "🟢"},
		{"quiet is green", incidentSeverity{}, "🟢"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := headline(tc.counts, nil); !strings.Contains(got, tc.want) {
				t.Fatalf("headline(%+v) = %q, want marker %q", tc.counts, got, tc.want)
			}
		})
	}
}

// TestIncidentRejectsBadReference covers the argument-validation paths without
// touching a database.
func TestIncidentRejectsBadReference(t *testing.T) {
	deps := testDeps()
	cases := map[string]struct {
		args []string
		want string
	}{
		"no argument":  {nil, "Usage:"},
		"not a number": {[]string{"abc"}, "must be a number"},
		"zero":         {[]string{"0"}, "must be a number"},
		"negative":     {[]string{"-4"}, "must be a number"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resp, err := deps.incident(context.Background(), telegram.Request{Args: tc.args})
			if err != nil {
				t.Fatalf("incident: %v", err)
			}
			if !strings.Contains(resp.Body, tc.want) {
				t.Fatalf("got %q, want it to contain %q", resp.Body, tc.want)
			}
		})
	}
}

// TestIncidentAcceptsHashPrefix: operators copy "#143" straight out of the
// /incidents listing, so the leading hash must not be a parse error.
func TestIncidentAcceptsHashPrefix(t *testing.T) {
	deps := testDeps() // Incidents nil, so this reaches the unavailable branch
	resp, err := deps.incident(context.Background(), telegram.Request{Args: []string{"#143"}})
	if err != nil {
		t.Fatalf("incident: %v", err)
	}
	if strings.Contains(resp.Body, "must be a number") {
		t.Fatalf("a #-prefixed reference was rejected: %q", resp.Body)
	}
}

// TestRegisterBuildsHelpFromRegistry proves /help cannot drift from what is
// actually dispatchable.
func TestRegisterBuildsHelpFromRegistry(t *testing.T) {
	registry := telegram.NewRegistry()
	if err := Register(registry, testDeps()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	reg, ok := registry.Lookup("help")
	if !ok {
		t.Fatal("/help was not registered")
	}
	resp, err := reg.Handler.Execute(context.Background(), telegram.Request{Command: "help"})
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	for _, name := range []string{"status", "incidents", "incident", "queues", "deploys", "budget", "help"} {
		if !strings.Contains(resp.Body, name) {
			t.Fatalf("/help omitted %q; it must render the registry:\n%s", name, resp.Body)
		}
	}
}

// TestRegisteredCommandsAreNonMutating: every command in this package reads.
// If one is ever marked mutating, it needs the durable-write ordering from
// spec §7.4 and this test should fail until that is deliberate.
func TestRegisteredCommandsAreNonMutating(t *testing.T) {
	registry := telegram.NewRegistry()
	if err := Register(registry, testDeps()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, reg := range registry.List() {
		if reg.Mutating {
			t.Fatalf("/%s is marked mutating but lives in the read-only command package", reg.Name)
		}
	}
}

// TestSeverityLabelUsesRealSeverities: the schema constrains severity to
// info|warning|critical. Rendering a P-scale here would make /status disagree
// with the database.
func TestSeverityLabelUsesRealSeverities(t *testing.T) {
	cases := map[string]string{
		"critical": "crit",
		"warning":  "warn",
		"info":     "info",
		"CRITICAL": "crit",
		"":         "?",
	}
	for in, want := range cases {
		if got := severityLabel(in); got != want {
			t.Fatalf("severityLabel(%q) = %q, want %q", in, got, want)
		}
	}
	for _, forbidden := range []string{"P0", "P1", "P2", "P3"} {
		if got := severityLabel("critical"); strings.Contains(got, forbidden) {
			t.Fatalf("severity rendering invented a P-scale: %q", got)
		}
	}
}

func TestAgeRendering(t *testing.T) {
	cases := map[time.Duration]string{
		14 * time.Second:       "14s",
		4 * time.Minute:        "4m",
		2 * time.Hour:          "2h",
		73 * time.Hour:         "3d",
		-5 * time.Second:       "0s",
		999 * time.Millisecond: "0s",
	}
	for in, want := range cases {
		if got := age(in); got != want {
			t.Fatalf("age(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestShortSHAAndMicros(t *testing.T) {
	if got := shortSHA("af72c11deadbeef"); got != "af72c11" {
		t.Fatalf("shortSHA = %q", got)
	}
	if got := shortSHA("abc"); got != "abc" {
		t.Fatalf("shortSHA on a short value = %q", got)
	}
	if got := micros(23_410_000); got != "$23.41" {
		t.Fatalf("micros = %q", got)
	}
	if got := micros(0); got != "$0.00" {
		t.Fatalf("micros(0) = %q", got)
	}
}

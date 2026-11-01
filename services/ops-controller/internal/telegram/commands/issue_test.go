package commands

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/alerts"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/deployments"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/github"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/incidents"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
)

// issueTestToken is the fake GitHub credential. No reply and no error this
// package produces may contain it.
var issueTestToken = "ghp" + "_" + "fakeTokenPlantedByTheTestSuite0123456789"

// plantedSecrets are credential-shaped strings deliberately planted in incident
// text and in the owner's note. Every one of them must be gone from anything
// that reaches GitHub or Telegram.
var (
	plantedInSummary = "sk" + "_live_" + "abcdefgh12345678"
	plantedInNote    = "hunter2SuperSecret"
)

// ---------------------------------------------------------------------------
// Fakes.
// ---------------------------------------------------------------------------

type fakeIncidents struct {
	detail      incidents.Detail
	getErr      error
	correlation incidents.DeploymentCorrelation
	corrErr     error

	gotReference int64
	getCalls     int
}

func (f *fakeIncidents) GetByReference(_ context.Context, reference int64) (incidents.Detail, error) {
	f.getCalls++
	f.gotReference = reference
	if f.getErr != nil {
		return incidents.Detail{}, f.getErr
	}
	return f.detail, nil
}

func (f *fakeIncidents) CorrelateDeployment(context.Context, string) (incidents.DeploymentCorrelation, error) {
	if f.corrErr != nil {
		return incidents.DeploymentCorrelation{}, f.corrErr
	}
	return f.correlation, nil
}

type fakeDeployments struct {
	listing deployments.Listing
	err     error
}

func (f *fakeDeployments) Recent(context.Context, int, string) (deployments.Listing, error) {
	if f.err != nil {
		return deployments.Listing{}, f.err
	}
	return f.listing, nil
}

// githubStub is an httptest-backed GitHub. Using the real client against it
// keeps the command and the client honest about each other's contract.
type githubStub struct {
	t *testing.T

	// searchItems is what /search/issues returns; searchStatus overrides it.
	searchItems  []github.Issue
	searchStatus int
	// createStatus defaults to 201.
	createStatus int
	// mirrorAuth makes an error response echo the Authorization header, the way
	// a badly written proxy would.
	mirrorAuth bool

	createCalls  int
	searchCalls  int
	commentCalls int
	createBody   github.IssueRequest
	commentText  string
	lastQuery    string
}

func (g *githubStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/search/issues":
			g.searchCalls++
			g.lastQuery = r.URL.Query().Get("q")
			if g.searchStatus != 0 {
				g.fail(w, r, g.searchStatus)
				return
			}
			payload, _ := json.Marshal(struct {
				TotalCount int            `json:"total_count"`
				Items      []github.Issue `json:"items"`
			}{TotalCount: len(g.searchItems), Items: g.searchItems})
			_, _ = w.Write(payload)

		case strings.HasSuffix(r.URL.Path, "/comments"):
			g.commentCalls++
			raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			var payload struct {
				Body string `json:"body"`
			}
			_ = json.Unmarshal(raw, &payload)
			g.commentText = payload.Body
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":1,"html_url":"https://github.com/acme/platform/issues/77#issuecomment-1"}`))

		case strings.HasSuffix(r.URL.Path, "/issues"):
			g.createCalls++
			raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err := json.Unmarshal(raw, &g.createBody); err != nil {
				g.t.Errorf("create payload was not JSON: %v", err)
			}
			if g.createStatus != 0 && g.createStatus != http.StatusCreated {
				g.fail(w, r, g.createStatus)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"number":412,"state":"open","html_url":"https://github.com/acme/platform/issues/412"}`))

		default:
			g.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func (g *githubStub) fail(w http.ResponseWriter, r *http.Request, status int) {
	message := "upstream failure"
	if g.mirrorAuth {
		message = "rejected credential " + r.Header.Get("Authorization")
	}
	w.WriteHeader(status)
	payload, _ := json.Marshal(map[string]string{"message": message})
	_, _ = w.Write(payload)
}

// ---------------------------------------------------------------------------
// Fixtures.
// ---------------------------------------------------------------------------

func issueNow() time.Time { return time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC) }

// sampleIncident carries a credential in its summary on purpose: an incident
// summary is assembled from log fragments and this is the realistic shape.
func sampleIncident() incidents.Detail {
	return incidents.Detail{
		ID:              "3f7c0a2e-0000-4000-8000-000000000001",
		Reference:       143,
		Title:           "Prediction API latency above threshold",
		Summary:         "upstream auth failed: api_key=" + plantedInSummary + " retrying",
		Service:         "api",
		Environment:     "production",
		Classification:  "latency",
		Severity:        alerts.Severity("critical"),
		Status:          incidents.IncidentStatus("open"),
		Source:          "alertmanager",
		SourceEventID:   "evt-9931",
		Fingerprint:     "a1b2c3d4e5f6",
		FirstObservedAt: issueNow().Add(-3 * time.Hour),
		LastObservedAt:  issueNow().Add(-4 * time.Minute),
		UpdatedAt:       issueNow().Add(-2 * time.Minute),
		Evidence:        json.RawMessage(`{"service":"api","p99_ms":1840}`),
		OccurrenceCount: 12,
	}
}

func sampleCorrelation() incidents.DeploymentCorrelation {
	return incidents.DeploymentCorrelation{
		IncidentID:  "3f7c0a2e-0000-4000-8000-000000000001",
		OnsetAt:     issueNow().Add(-3 * time.Hour),
		Service:     "api",
		Environment: "production",
		Current: incidents.DeploymentRef{
			ID:           "dep-current",
			Service:      "api",
			Environment:  "production",
			SourceCommit: "abcdef1234567890",
			Status:       "succeeded",
			StartedAt:    issueNow().Add(-3*time.Hour - 12*time.Minute),
		},
		Previous: incidents.DeploymentRef{
			ID:           "dep-previous",
			SourceCommit: "0987654321fedcba",
		},
		HasCurrent:     true,
		HasPrevious:    true,
		ServiceMatched: true,
		SinceDeploy:    12 * time.Minute,
		Strength:       incidents.CorrelationStrong,
		Lookback:       incidents.DeploymentLookback,
	}
}

// newIssueDeps wires the command against an httptest GitHub.
func newIssueDeps(t *testing.T, stub *githubStub, source IncidentSource, deploys DeploymentSource) IssueDeps {
	t.Helper()
	server := httptest.NewServer(stub.handler())
	t.Cleanup(server.Close)
	client, err := github.New("acme", "platform", issueTestToken,
		github.WithBaseURL(server.URL), github.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("github.New: %v", err)
	}
	return IssueDeps{
		Incidents:   source,
		Deployments: deploys,
		GitHub:      client,
		Repository:  "acme/platform",
		Environment: "production",
		Location:    time.UTC,
		Now:         issueNow,
	}
}

func defaultSource() *fakeIncidents {
	return &fakeIncidents{detail: sampleIncident(), correlation: sampleCorrelation()}
}

func immediateDeployments() *fakeDeployments {
	return &fakeDeployments{listing: deployments.Listing{Deployments: []deployments.Deployment{
		{ID: "dep-current", DeployClass: "IMMEDIATE"},
	}}}
}

// ---------------------------------------------------------------------------
// Syntax disambiguation (spec §15).
// ---------------------------------------------------------------------------

func TestParseIssueTargetCoversAllFourSyntaxes(t *testing.T) {
	cases := []struct {
		name      string
		req       telegram.Request
		wantRef   int64
		wantHas   bool
		wantNote  string
		wantReply bool
	}{
		{
			name:    "bare command in reply to an incident message",
			req:     telegram.Request{ReplyToText: "Incident #143 — crit — OPEN\nService  api"},
			wantRef: 143, wantHas: true, wantReply: true,
		},
		{
			name:    "explicit incident",
			req:     telegram.Request{Args: []string{"143"}},
			wantRef: 143, wantHas: true,
		},
		{
			name:    "hash-prefixed incident",
			req:     telegram.Request{Args: []string{"#143"}},
			wantRef: 143, wantHas: true,
		},
		{
			name:    "explicit incident plus a note",
			req:     telegram.Request{Args: []string{"143", "check", "the", "pool"}},
			wantRef: 143, wantHas: true, wantNote: "check the pool",
		},
		{
			name:     "standalone note",
			req:      telegram.Request{Args: []string{"check", "the", "pool"}},
			wantNote: "check the pool",
		},
		{
			name: "explicit argument beats the replied-to message",
			req: telegram.Request{
				Args:        []string{"200"},
				ReplyToText: "Incident #143 — crit — OPEN",
			},
			wantRef: 200, wantHas: true,
		},
		{
			name: "a note wins over the reply when arguments are present",
			req: telegram.Request{
				Args:        []string{"pool", "exhausted"},
				ReplyToText: "Incident #143 — crit — OPEN",
			},
			wantNote: "pool exhausted",
		},
		{
			name: "bare command with no usable reply",
			req:  telegram.Request{ReplyToText: "no open incidents"},
		},
		{
			name:     "zero is not a reference",
			req:      telegram.Request{Args: []string{"0"}},
			wantNote: "0",
		},
		{
			name:     "a negative number is not a reference",
			req:      telegram.Request{Args: []string{"-4", "queues"}},
			wantNote: "-4 queues",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseIssueTarget(tc.req)
			if got.Reference != tc.wantRef || got.HasReference != tc.wantHas {
				t.Fatalf("reference = (%d, %v), want (%d, %v)", got.Reference, got.HasReference, tc.wantRef, tc.wantHas)
			}
			if got.Note != tc.wantNote {
				t.Fatalf("note = %q, want %q", got.Note, tc.wantNote)
			}
			if got.FromReply != tc.wantReply {
				t.Fatalf("FromReply = %v, want %v", got.FromReply, tc.wantReply)
			}
		})
	}
}

// TestReplyFormResolvesTheIncidentFromTheRepliedToMessage is the bare-/issue
// path end to end: the reference must come out of ReplyToText and reach the
// incident store.
func TestReplyFormResolvesTheIncidentFromTheRepliedToMessage(t *testing.T) {
	stub := &githubStub{t: t}
	source := defaultSource()
	deps := newIssueDeps(t, stub, source, nil)

	resp, err := deps.issue(context.Background(), telegram.Request{
		Command:     "issue",
		ReplyToText: "Incident #143 — crit — OPEN\nStarted 09:00 05 Sep",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if source.gotReference != 143 {
		t.Fatalf("resolved reference = %d, want 143", source.gotReference)
	}
	if stub.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", stub.createCalls)
	}
	if !strings.Contains(resp.Body, "📋 Issue #412 created.") {
		t.Fatalf("reply did not confirm creation:\n%s", resp.Body)
	}
	if !strings.Contains(resp.Body, "https://github.com/acme/platform/issues/412") {
		t.Fatalf("reply is missing the issue URL:\n%s", resp.Body)
	}
}

func TestStandaloneNoteNeverTouchesTheIncidentStore(t *testing.T) {
	stub := &githubStub{t: t}
	source := defaultSource()
	deps := newIssueDeps(t, stub, source, nil)

	resp, err := deps.issue(context.Background(), telegram.Request{
		Command: "issue",
		Args:    []string{"connection", "pool", "looks", "wrong"},
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if source.getCalls != 0 {
		t.Fatalf("standalone form read the incident store %d times", source.getCalls)
	}
	if stub.searchCalls != 0 {
		t.Fatal("standalone form ran a dedup search; there is no fingerprint to dedup on")
	}
	if stub.createBody.Title != "[ops] connection pool looks wrong" {
		t.Fatalf("title = %q", stub.createBody.Title)
	}
	if !strings.Contains(stub.createBody.Body, issueFooter) {
		t.Fatalf("body is missing the provenance footer:\n%s", stub.createBody.Body)
	}
	if !strings.Contains(resp.Body, "Issue #412 created.") {
		t.Fatalf("reply = %s", resp.Body)
	}
}

// TestStandaloneLabelsOmitIncident guards a small but real correctness point:
// a note with no incident behind it must not carry the "incident" label, or
// every incident query in the repository becomes wrong.
func TestStandaloneLabelsOmitIncident(t *testing.T) {
	stub := &githubStub{t: t}
	deps := newIssueDeps(t, stub, defaultSource(), nil)
	if _, err := deps.issue(context.Background(), telegram.Request{Args: []string{"pool", "looks", "wrong"}}); err != nil {
		t.Fatal(err)
	}
	for _, label := range stub.createBody.Labels {
		if label == "incident" {
			t.Fatalf("standalone note was labelled as an incident: %v", stub.createBody.Labels)
		}
	}
	if len(stub.createBody.Labels) != 1 || stub.createBody.Labels[0] != "ops" {
		t.Fatalf("labels = %v, want [ops]", stub.createBody.Labels)
	}
}

func TestBareIssueWithNothingToGoOnExplainsItself(t *testing.T) {
	stub := &githubStub{t: t}
	deps := newIssueDeps(t, stub, defaultSource(), nil)
	resp, err := deps.issue(context.Background(), telegram.Request{Command: "issue"})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !strings.Contains(resp.Body, "Usage:") {
		t.Fatalf("expected usage guidance, got:\n%s", resp.Body)
	}
	if stub.createCalls != 0 {
		t.Fatal("an empty /issue filed something")
	}
}

// ---------------------------------------------------------------------------
// Body, labels, dedup.
// ---------------------------------------------------------------------------

func TestIncidentBodyCarriesTheTemplateSections(t *testing.T) {
	stub := &githubStub{t: t}
	deps := newIssueDeps(t, stub, defaultSource(), immediateDeployments())

	if _, err := deps.issue(context.Background(), telegram.Request{
		Args: []string{"143", "check", "the", "pool"},
	}); err != nil {
		t.Fatalf("issue: %v", err)
	}

	body := stub.createBody.Body
	for _, want := range []string{
		"## Incident #143 — critical — OPEN",
		"| Service | api |",
		"| Environment | production |",
		"Occurrences", "12 (at filing)",
		"| Status at filing | open, unacknowledged |",
		"### Deployment correlation",
		"| Strength | strong |",
		"| Current SHA | `abcdef1` |",
		"| Previous SHA | `0987654` |",
		"| Deploy class | IMMEDIATE |",
		"### Impact",
		"### Diagnostics",
		"| Fingerprint | `a1b2c3d4e5f6` |",
		"### Evidence",
		"### Owner note",
		"check the pool",
		referenceMarker + "143",
		fingerprintMarker + "a1b2c3d4e5f6",
		issueFooter,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body is missing %q:\n%s", want, body)
		}
	}
	if stub.createBody.Title != "[ops] Incident #143 — Prediction API latency above threshold" {
		t.Fatalf("title = %q", stub.createBody.Title)
	}
}

// TestLabelConstruction pins the label set: ops, incident, the REAL severity
// (info|warning|critical, never a P-scale), and deploy-class when known.
func TestLabelConstruction(t *testing.T) {
	cases := []struct {
		name        string
		severity    string
		deployClass string
		want        []string
	}{
		{"critical with class", "critical", "IMMEDIATE", []string{"ops", "incident", "critical", "deploy-class:immediate"}},
		{"warning no class", "warning", "", []string{"ops", "incident", "warning"}},
		{"info", "info", "batched", []string{"ops", "incident", "info", "deploy-class:batched"}},
		{"unknown severity is dropped, not invented", "", "", []string{"ops", "incident"}},
		{"a hostile class cannot inject a qualifier", "critical", "a b/c?d", []string{"ops", "incident", "critical", "deploy-class:a-bcd"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := issueLabels(tc.severity, tc.deployClass)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("issueLabels(%q, %q) = %v, want %v", tc.severity, tc.deployClass, got, tc.want)
			}
		})
	}
}

func TestLabelsReachGitHubForAnIncident(t *testing.T) {
	stub := &githubStub{t: t}
	deps := newIssueDeps(t, stub, defaultSource(), immediateDeployments())
	if _, err := deps.issue(context.Background(), telegram.Request{Args: []string{"143"}}); err != nil {
		t.Fatal(err)
	}
	want := "ops,incident,critical,deploy-class:immediate"
	if got := strings.Join(stub.createBody.Labels, ","); got != want {
		t.Fatalf("labels = %q, want %q", got, want)
	}
}

// TestDedupCommentsInsteadOfCreatingASecondIssue is the point of the whole
// dedup path: three issues for one recurring incident is how a tracker dies.
func TestDedupCommentsInsteadOfCreatingASecondIssue(t *testing.T) {
	stub := &githubStub{
		t: t,
		searchItems: []github.Issue{{
			Number:  77,
			State:   "open",
			HTMLURL: "https://github.com/acme/platform/issues/77",
		}},
	}
	deps := newIssueDeps(t, stub, defaultSource(), nil)

	resp, err := deps.issue(context.Background(), telegram.Request{Args: []string{"143", "again"}})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if stub.createCalls != 0 {
		t.Fatalf("created %d issues despite an open match", stub.createCalls)
	}
	if stub.commentCalls != 1 {
		t.Fatalf("commentCalls = %d, want 1", stub.commentCalls)
	}
	if !strings.Contains(resp.Body, "Added to existing issue #77.") {
		t.Fatalf("reply = %s", resp.Body)
	}
	if !strings.Contains(stub.commentText, "Recurrence reported from Telegram") {
		t.Fatalf("comment body = %s", stub.commentText)
	}
	if !strings.Contains(stub.commentText, "again") {
		t.Fatalf("comment dropped the owner note: %s", stub.commentText)
	}
	if !strings.Contains(stub.lastQuery, fingerprintMarker+"a1b2c3d4e5f6") {
		t.Fatalf("dedup query did not search the fingerprint: %q", stub.lastQuery)
	}
}

// TestDedupFallsBackToTheReferenceWithoutAFingerprint keeps the dedup key
// honest for incidents whose fingerprint is empty.
func TestDedupFallsBackToTheReferenceWithoutAFingerprint(t *testing.T) {
	source := defaultSource()
	source.detail.Fingerprint = ""
	stub := &githubStub{t: t}
	deps := newIssueDeps(t, stub, source, nil)

	if _, err := deps.issue(context.Background(), telegram.Request{Args: []string{"143"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stub.lastQuery, referenceMarker+"143") {
		t.Fatalf("dedup query = %q, want the reference marker", stub.lastQuery)
	}
}

// TestDedupSearchFailureStillFilesAndSaysSo: a search outage must not block the
// filing, but the operator has to know the duplicate check did not run.
func TestDedupSearchFailureStillFilesAndSaysSo(t *testing.T) {
	stub := &githubStub{t: t, searchStatus: http.StatusInternalServerError}
	deps := newIssueDeps(t, stub, defaultSource(), nil)

	resp, err := deps.issue(context.Background(), telegram.Request{Args: []string{"143"}})
	if err != nil {
		t.Fatal(err)
	}
	if stub.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", stub.createCalls)
	}
	if !strings.Contains(resp.Body, "Deduplication check failed") {
		t.Fatalf("reply hid the failed dedup check:\n%s", resp.Body)
	}
}

// ---------------------------------------------------------------------------
// Failure handling (spec §20).
// ---------------------------------------------------------------------------

// TestGitHubFailureReturnsTheAssembledBody is the "never silently lose it"
// rule: a 5xx must hand the operator the whole body to paste by hand.
func TestGitHubFailureReturnsTheAssembledBody(t *testing.T) {
	stub := &githubStub{t: t, createStatus: http.StatusBadGateway}
	deps := newIssueDeps(t, stub, defaultSource(), nil)

	resp, err := deps.issue(context.Background(), telegram.Request{Args: []string{"143", "pool", "exhausted"}})
	if err != nil {
		t.Fatalf("issue returned an error instead of degrading: %v", err)
	}
	if resp.Silent {
		t.Fatal("the reply was suppressed; the body would have been lost")
	}
	for _, want := range []string{
		"Issue not filed",
		"GitHub returned 502",
		"Paste this into GitHub by hand",
		"Incident #143",
		"ops, incident, critical",
		issueFooter,
		"pool exhausted",
	} {
		if !strings.Contains(resp.Body, want) {
			t.Fatalf("failure reply is missing %q:\n%s", want, resp.Body)
		}
	}
}

func TestMissingGitHubClientStillReturnsTheBody(t *testing.T) {
	deps := IssueDeps{
		Incidents: defaultSource(),
		Location:  time.UTC,
		Now:       issueNow,
	}
	resp, err := deps.issue(context.Background(), telegram.Request{Args: []string{"143"}})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !strings.Contains(resp.Body, unavailable) || !strings.Contains(resp.Body, issueFooter) {
		t.Fatalf("expected the degraded reply to carry the body:\n%s", resp.Body)
	}
}

func TestCommentFailureReturnsTheBodyToo(t *testing.T) {
	stub := &githubStub{
		t:           t,
		searchItems: []github.Issue{{Number: 77, State: "open"}},
	}
	// Make the comment fail by pointing the stub's comment branch at a 500.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/comments") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
			return
		}
		stub.handler().ServeHTTP(w, r)
	}))
	defer server.Close()

	client, err := github.New("acme", "platform", issueTestToken,
		github.WithBaseURL(server.URL), github.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	deps := IssueDeps{Incidents: defaultSource(), GitHub: client, Location: time.UTC, Now: issueNow}

	resp, err := deps.issue(context.Background(), telegram.Request{Args: []string{"143"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Body, "Issue #77 already covers this") {
		t.Fatalf("reply = %s", resp.Body)
	}
	if !strings.Contains(resp.Body, issueFooter) {
		t.Fatalf("the assembled body was lost:\n%s", resp.Body)
	}
}

func TestNotFoundIncidentReferenceFilesNothing(t *testing.T) {
	stub := &githubStub{t: t}
	source := &fakeIncidents{getErr: incidents.ErrIncidentNotFound}
	deps := newIssueDeps(t, stub, source, nil)

	resp, err := deps.issue(context.Background(), telegram.Request{Args: []string{"999"}})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if resp.Body != "Incident #999 not found." {
		t.Fatalf("reply = %q", resp.Body)
	}
	if stub.createCalls != 0 || stub.searchCalls != 0 {
		t.Fatal("a missing incident still reached GitHub")
	}
}

func TestMissingIncidentStoreDegrades(t *testing.T) {
	stub := &githubStub{t: t}
	deps := newIssueDeps(t, stub, nil, nil)
	resp, err := deps.issue(context.Background(), telegram.Request{Args: []string{"143"}})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !strings.Contains(resp.Body, unavailable) {
		t.Fatalf("expected the %q marker, got:\n%s", unavailable, resp.Body)
	}
	if stub.createCalls != 0 {
		t.Fatal("filed an incident issue with no incident data")
	}
}

// ---------------------------------------------------------------------------
// Redaction (spec §18) — mandatory, on every free-text field.
// ---------------------------------------------------------------------------

// TestRedactionOfIncidentSummaryAndOwnerNote is the test this command exists to
// pass. A credential in an incident summary or in the owner's own note must
// never reach GitHub, where it becomes a durable, widely readable artefact.
func TestRedactionOfIncidentSummaryAndOwnerNote(t *testing.T) {
	stub := &githubStub{t: t}
	source := defaultSource()
	deps := newIssueDeps(t, stub, source, nil)

	resp, err := deps.issue(context.Background(), telegram.Request{
		Args: []string{"143", "rotate", "password=" + plantedInNote},
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	for _, secret := range []string{plantedInSummary, plantedInNote} {
		if strings.Contains(stub.createBody.Body, secret) {
			t.Fatalf("secret %q reached the GitHub body:\n%s", secret, stub.createBody.Body)
		}
		if strings.Contains(stub.createBody.Title, secret) {
			t.Fatalf("secret %q reached the GitHub title: %s", secret, stub.createBody.Title)
		}
		if strings.Contains(resp.Body, secret) {
			t.Fatalf("secret %q reached the Telegram reply:\n%s", secret, resp.Body)
		}
	}
	if !strings.Contains(stub.createBody.Body, "redacted") {
		t.Fatalf("nothing was redacted; the patterns did not run:\n%s", stub.createBody.Body)
	}
}

// TestRedactionAppliesToTheStandaloneNoteAndTheFailureFallback closes the two
// paths that bypass the incident template.
func TestRedactionAppliesToTheStandaloneNoteAndTheFailureFallback(t *testing.T) {
	t.Run("standalone note", func(t *testing.T) {
		stub := &githubStub{t: t}
		deps := newIssueDeps(t, stub, nil, nil)
		resp, err := deps.issue(context.Background(), telegram.Request{
			Args: []string{"rotate", "api_key=" + plantedInSummary},
		})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(stub.createBody.Body, plantedInSummary) ||
			strings.Contains(stub.createBody.Title, plantedInSummary) ||
			strings.Contains(resp.Body, plantedInSummary) {
			t.Fatalf("the standalone note leaked a credential:\ntitle %s\nbody %s\nreply %s",
				stub.createBody.Title, stub.createBody.Body, resp.Body)
		}
	})

	t.Run("failure fallback", func(t *testing.T) {
		stub := &githubStub{t: t, createStatus: http.StatusServiceUnavailable}
		deps := newIssueDeps(t, stub, defaultSource(), nil)
		resp, err := deps.issue(context.Background(), telegram.Request{
			Args: []string{"143", "password=" + plantedInNote},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{plantedInSummary, plantedInNote} {
			if strings.Contains(resp.Body, secret) {
				t.Fatalf("the pasted-back body leaked %q:\n%s", secret, resp.Body)
			}
		}
	})

	t.Run("a note that is nothing but a secret is refused", func(t *testing.T) {
		stub := &githubStub{t: t}
		deps := newIssueDeps(t, stub, nil, nil)
		resp, err := deps.issue(context.Background(), telegram.Request{
			Args: []string{"password=" + plantedInNote},
		})
		if err != nil {
			t.Fatal(err)
		}
		if stub.createCalls != 0 {
			t.Fatal("filed an issue whose entire body was a redacted credential")
		}
		if !strings.Contains(resp.Body, "redaction") {
			t.Fatalf("reply = %s", resp.Body)
		}
	})
}

// TestGitHubTokenNeverReachesAReply is the §18 invariant at the command layer:
// a hostile or misconfigured upstream that mirrors the Authorization header
// must not turn a failure reply into a credential disclosure.
func TestGitHubTokenNeverReachesAReply(t *testing.T) {
	for _, status := range []int{
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusUnprocessableEntity,
		http.StatusInternalServerError,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			stub := &githubStub{t: t, createStatus: status, mirrorAuth: true}
			deps := newIssueDeps(t, stub, defaultSource(), nil)
			resp, err := deps.issue(context.Background(), telegram.Request{Args: []string{"143"}})
			if err != nil {
				t.Fatalf("issue: %v", err)
			}
			if strings.Contains(resp.Body, issueTestToken) {
				t.Fatalf("the GitHub token leaked into the reply:\n%s", resp.Body)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Registration.
// ---------------------------------------------------------------------------

func TestRegisterIssue(t *testing.T) {
	registry := telegram.NewRegistry()
	if err := RegisterIssue(registry, IssueDeps{}); err != nil {
		t.Fatalf("RegisterIssue: %v", err)
	}
	reg, ok := registry.Lookup("issue")
	if !ok {
		t.Fatal("/issue was not registered")
	}
	if !reg.Mutating {
		t.Fatal("/issue must be marked mutating: it creates an external artefact")
	}
	if err := RegisterIssue(nil, IssueDeps{}); err == nil {
		t.Fatal("a nil registry must be rejected")
	}
}

// TestRegisterIssueCoexistsWithTheReadCommands proves the two registration
// entry points do not collide, which is the whole reason /issue has its own.
func TestRegisterIssueCoexistsWithTheReadCommands(t *testing.T) {
	registry := telegram.NewRegistry()
	if err := Register(registry, testDeps()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := RegisterIssue(registry, IssueDeps{}); err != nil {
		t.Fatalf("RegisterIssue after Register: %v", err)
	}
	if _, ok := registry.Lookup("issue"); !ok {
		t.Fatal("/issue is missing from the registry")
	}
	if _, ok := registry.Lookup("incidents"); !ok {
		t.Fatal("the read commands were lost")
	}
}

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

func TestSearchableFingerprintRejectsQueryMetacharacters(t *testing.T) {
	cases := map[string]string{
		"a1b2c3":         "a1b2c3",
		"sha256.abc-def": "sha256.abc-def",
		`bad" in:title`:  "",
		"has space":      "",
	}
	for input, want := range cases {
		if got := searchableFingerprint(input); got != want {
			t.Fatalf("searchableFingerprint(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRedactEvidenceHandlesEmptyAndInvalidJSON(t *testing.T) {
	if got := redactEvidence(nil); got != "" {
		t.Fatalf("nil evidence = %q", got)
	}
	if got := redactEvidence(json.RawMessage(`{}`)); got != "" {
		t.Fatalf("empty object = %q", got)
	}
	if got := redactEvidence(json.RawMessage(`{"a":1}`)); got != `{"a":1}` {
		t.Fatalf("compacted = %q", got)
	}
	if got := redactEvidence(json.RawMessage(`not json`)); got == "" {
		t.Fatal("invalid JSON must still be carried through, redacted")
	}
}

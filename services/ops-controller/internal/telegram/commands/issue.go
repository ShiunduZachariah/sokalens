package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/deployments"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/evidence"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/github"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/incidents"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
)

// /issue — file a GitHub issue from a phone (spec §15).
//
// This is the one command in the package that changes something outside this
// service, so it lives in its own file with its own dependency struct and its
// own registration. Deps in commands.go is read-only by construction; widening
// it to carry a credentialed GitHub client would erase that property.
//
// Two rules govern everything below.
//
// Redaction is unconditional. Every free-text field that reaches GitHub —
// title, summary, evidence, the correlation strings and the owner's own note —
// passes evidence.RedactText first. An incident summary is assembled from log
// fragments, and a GitHub issue is a durable, widely readable artefact; a
// credential that lands in one has to be rotated, not deleted.
//
// Nothing is ever silently lost. If GitHub is unreachable or refuses the
// request, the reply carries the failure *and* the assembled body, so the
// operator can paste it into the browser by hand (spec §20).

// IncidentSource is the read-only incident projection /issue needs. It is an
// interface rather than *incidents.Queries so a test can supply a fixture
// without standing up a fake database seam.
type IncidentSource interface {
	GetByReference(ctx context.Context, reference int64) (incidents.Detail, error)
	CorrelateDeployment(ctx context.Context, incidentID string) (incidents.DeploymentCorrelation, error)
}

// DeploymentSource supplies the deploy class for the correlated deployment.
// DeploymentCorrelation does not carry deploy_class, so the class is recovered
// by matching the correlated deployment's ID against the recent listing. It is
// entirely optional: a miss drops the deploy-class label and nothing else.
type DeploymentSource interface {
	Recent(ctx context.Context, limit int, environment string) (deployments.Listing, error)
}

// IssueFiler is the outbound GitHub surface. *github.Client implements it.
type IssueFiler interface {
	CreateIssue(ctx context.Context, req github.IssueRequest) (github.Issue, error)
	SearchOpenIssues(ctx context.Context, query string) ([]github.Issue, error)
	CommentOnIssue(ctx context.Context, number int, body string) (github.Comment, error)
}

// The production types must satisfy the seams; a signature drift in either
// storage package should fail the build here rather than at wiring time.
var (
	_ IncidentSource   = (*incidents.Queries)(nil)
	_ DeploymentSource = (*deployments.Queries)(nil)
	_ IssueFiler       = (*github.Client)(nil)
)

// IssueDeps are /issue's dependencies. Every source may be nil: a missing one
// degrades the reply rather than failing it, in the same spirit as Deps.
type IssueDeps struct {
	// Incidents resolves an incident reference. Nil means /issue can only file
	// a standalone note.
	Incidents IncidentSource
	// Deployments is optional and supplies deploy_class only.
	Deployments DeploymentSource
	// GitHub is the outbound client. Nil means every filing degrades to
	// "here is the body, paste it yourself".
	GitHub IssueFiler

	// Repository is rendered in replies so the operator knows where the issue
	// went. Cosmetic; the client owns the real target.
	Repository string
	// Environment labels the assembled body when the incident carries none.
	Environment string
	// Location renders timestamps in the operator's zone.
	Location *time.Location
	// Now is injectable for tests.
	Now func() time.Time
	// QueryTimeout bounds each database read.
	QueryTimeout time.Duration
	// GitHubTimeout bounds each outbound GitHub call. It is separate from
	// QueryTimeout: a 5s bound that is right for a local index scan is wrong
	// for a cross-internet round trip, and a create that timed out client-side
	// may still have filed the issue.
	GitHubTimeout time.Duration
}

const (
	// defaultGitHubTimeout bounds one GitHub call.
	defaultGitHubTimeout = 15 * time.Second

	// issueFooter is the provenance line spec §15 requires. It is also what
	// makes an issue filed this way recognisable in a repository that receives
	// issues from several sources.
	issueFooter = "Filed from Telegram via ops-controller"

	// fingerprintMarker and referenceMarker are the machine-readable dedup
	// anchors written into every body. They are plain text on their own lines:
	// wrapping them in backticks would change how GitHub's search tokenizes
	// them and quietly break the dedup query.
	fingerprintMarker = "ops-fingerprint: "
	referenceMarker   = "ops-incident: #"

	// Bounds on the free text that reaches GitHub. An issue body has no
	// practical size limit, but an unbounded evidence blob pasted into a chat
	// reply on failure does.
	maxIssueTitleChars = 120
	maxSummaryChars    = 1200
	maxEvidenceChars   = 800
	maxNoteChars       = 400
	maxFingerprintLen  = 64
	maxLabelChars      = 48

	// deployLookupLimit bounds the recent-deployments scan used to recover
	// deploy_class.
	deployLookupLimit = 25
)

func (d IssueDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d IssueDeps) location() *time.Location {
	if d.Location != nil {
		return d.Location
	}
	return time.UTC
}

func (d IssueDeps) queryScope(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := d.QueryTimeout
	if timeout <= 0 {
		timeout = defaultQueryTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

func (d IssueDeps) githubScope(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := d.GitHubTimeout
	if timeout <= 0 {
		timeout = defaultGitHubTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// RegisterIssue adds /issue to the registry.
//
// It is separate from Register in commands.go on purpose: that function owns
// the read commands and is edited by a different workstream, and /issue is the
// only mutating command in the package. Main wires the two independently.
func RegisterIssue(registry *telegram.Registry, deps IssueDeps) error {
	if registry == nil {
		return errors.New("commands: registry is required")
	}
	return registry.Register(telegram.Registration{
		Name:    "issue",
		Usage:   "[id] [note]",
		Summary: "File a GitHub issue, from an incident or a note.",
		// Mutating: this reaches outside the service and creates a durable,
		// externally visible artefact.
		Mutating: true,
		Handler:  telegram.HandlerFunc(deps.issue),
	})
}

// ---------------------------------------------------------------------------
// Argument disambiguation (spec §15).
// ---------------------------------------------------------------------------

// issueTarget is the parsed intent behind one /issue invocation.
type issueTarget struct {
	Reference    int64
	HasReference bool
	// Note is the operator's free text, empty when none was given.
	Note string
	// FromReply records that the reference came out of the replied-to message
	// rather than the arguments, which the reply text mentions so a
	// mis-targeted reply is visible before the issue is filed.
	FromReply bool
}

// parseIssueTarget resolves the four accepted syntaxes:
//
//	/issue                    reply to an incident message; use that incident
//	/issue 143                explicit incident
//	/issue 143 check the pool explicit incident plus a note
//	/issue check the pool     standalone issue, no incident context
//
// The whole disambiguation is one rule: if the first argument parses as a
// positive integer, optionally "#"-prefixed, it is an incident reference;
// otherwise the entire argument text is a note. Nothing about the rest of the
// arguments changes that decision, so "/issue 143 check the pool" and
// "/issue check the pool" cannot be confused, and a note that happens to start
// with a number ("/issue 500s on checkout") is the one case an operator has to
// write differently — which is why the reply always names what was filed.
func parseIssueTarget(req telegram.Request) issueTarget {
	if len(req.Args) > 0 {
		if reference, ok := parseIncidentReference(req.Args[0]); ok {
			return issueTarget{
				Reference:    reference,
				HasReference: true,
				Note:         strings.TrimSpace(strings.Join(req.Args[1:], " ")),
			}
		}
		return issueTarget{Note: strings.TrimSpace(strings.Join(req.Args, " "))}
	}

	// Bare /issue: the incident comes from the message being replied to.
	if reference, ok := referenceFromReply(req.ReplyToText); ok {
		return issueTarget{Reference: reference, HasReference: true, FromReply: true}
	}
	return issueTarget{}
}

// parseIncidentReference accepts "143" and "#143". Anything else — a negative
// number, a float, a word — is not a reference.
func parseIncidentReference(arg string) (int64, bool) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(arg), "#")
	if trimmed == "" {
		return 0, false
	}
	// ParseInt would accept a leading "+" or "-"; a reference is a bare
	// sequence of digits and nothing else.
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	reference, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || reference <= 0 {
		return 0, false
	}
	return reference, true
}

// referenceFromReply pulls an incident reference out of a replied-to message.
//
// The messages this bridge sends lead with "Incident #143", so the scan looks
// for a "#" followed by digits and takes the first one. It deliberately does
// not accept a bare number anywhere in the text: an incident body is full of
// them — occurrence counts, ages, severities — and picking one at random would
// file an issue against the wrong incident.
func referenceFromReply(text string) (int64, bool) {
	for i := 0; i < len(text); i++ {
		if text[i] != '#' {
			continue
		}
		j := i + 1
		for j < len(text) && text[j] >= '0' && text[j] <= '9' {
			j++
		}
		if j == i+1 {
			continue
		}
		if reference, ok := parseIncidentReference(text[i:j]); ok {
			return reference, true
		}
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// Handler.
// ---------------------------------------------------------------------------

const issueUsage = "Usage: /issue &lt;id&gt;, /issue &lt;note&gt;, or reply to an incident with /issue."

// filing is one assembled, fully redacted issue ready to send.
type filing struct {
	Title  string
	Body   string
	Labels []string
	// SearchQuery is the dedup query, empty when there is nothing stable to
	// deduplicate on (a standalone note).
	SearchQuery string
	// CommentBody is what gets appended to an existing issue instead of
	// creating a second one.
	CommentBody string
	// Reference is the incident reference, zero for a standalone note.
	Reference int64
}

func (d IssueDeps) issue(ctx context.Context, req telegram.Request) (telegram.Response, error) {
	target := parseIssueTarget(req)

	switch {
	case target.HasReference:
		return d.fileForIncident(ctx, target)
	case target.Note != "":
		return d.fileStandalone(ctx, target)
	default:
		return telegram.Response{Body: issueUsage}, nil
	}
}

// fileForIncident assembles and files an issue for a real incident.
func (d IssueDeps) fileForIncident(ctx context.Context, target issueTarget) (telegram.Response, error) {
	if d.Incidents == nil {
		// Without the projection there is no body to assemble, so there is
		// nothing to hand back for manual filing either. Say so plainly.
		return telegram.Response{Body: "Incident store " + unavailable + ". File the issue from the dashboard."}, nil
	}

	scoped, cancel := d.queryScope(ctx)
	detail, err := d.Incidents.GetByReference(scoped, target.Reference)
	cancel()
	if err != nil {
		if errors.Is(err, incidents.ErrIncidentNotFound) {
			return telegram.Response{Body: fmt.Sprintf("Incident #%d not found.", target.Reference)}, nil
		}
		return telegram.Response{Body: "Incident store " + unavailable + "."}, nil
	}

	correlation, hasCorrelation := d.correlate(ctx, detail.ID)
	deployClass := d.deployClass(ctx, correlation, hasCorrelation)

	assembled := d.assembleIncidentFiling(detail, correlation, hasCorrelation, deployClass, target)
	return d.send(ctx, assembled, target), nil
}

// fileStandalone files a note with no incident behind it. Spec §15's fourth
// form: the operator saw something the incident pipeline did not.
func (d IssueDeps) fileStandalone(ctx context.Context, target issueTarget) (telegram.Response, error) {
	note := redactBounded(target.Note, maxNoteChars)
	if isOnlyRedaction(note) {
		// Redaction consumed the whole note — it was nothing but a credential.
		// An issue titled "[ops] redacted" is noise with a rotation incident
		// attached; refusing is better than filing it.
		return telegram.Response{Body: "Nothing left to file after redaction. Rewrite the note without secrets."}, nil
	}

	var body strings.Builder
	body.WriteString("## Ops note\n\n")
	body.WriteString(note)
	body.WriteString("\n\n")
	body.WriteString("### Context\n\n")
	body.WriteString("| Field | Value |\n| --- | --- |\n")
	writeRow(&body, "Environment", valueOr(d.Environment, "unknown"))
	writeRow(&body, "Filed at", d.now().In(d.location()).Format("2006-01-02 15:04 MST"))
	writeRow(&body, "Incident", "none — filed directly from Telegram")
	body.WriteString("\n---\n")
	body.WriteString(issueFooter)
	body.WriteString("\n")

	assembled := filing{
		Title: "[ops] " + telegram.TruncateField(firstLine(note), maxIssueTitleChars),
		Body:  body.String(),
		// No "incident" label: this issue is not backed by one, and a label
		// that lies makes every incident query in the repository wrong.
		Labels: []string{"ops"},
	}
	return d.send(ctx, assembled, target), nil
}

// correlate reads the deployment correlation, best-effort. A failure is not
// fatal to the filing: an issue with no correlation section is still useful.
func (d IssueDeps) correlate(ctx context.Context, incidentID string) (incidents.DeploymentCorrelation, bool) {
	if d.Incidents == nil || incidentID == "" {
		return incidents.DeploymentCorrelation{}, false
	}
	scoped, cancel := d.queryScope(ctx)
	defer cancel()
	correlation, err := d.Incidents.CorrelateDeployment(scoped, incidentID)
	if err != nil {
		return incidents.DeploymentCorrelation{}, false
	}
	return correlation, true
}

// deployClass recovers ops_deployments.deploy_class for the correlated
// deployment. DeploymentCorrelation does not carry the column, so the value is
// matched out of the recent listing by deployment ID. A miss is silent.
func (d IssueDeps) deployClass(ctx context.Context, correlation incidents.DeploymentCorrelation, hasCorrelation bool) string {
	if d.Deployments == nil || !hasCorrelation || !correlation.HasCurrent {
		return ""
	}
	scoped, cancel := d.queryScope(ctx)
	defer cancel()
	listing, err := d.Deployments.Recent(scoped, deployLookupLimit, correlation.Environment)
	if err != nil {
		return ""
	}
	for _, deployment := range listing.Deployments {
		if deployment.ID == correlation.Current.ID {
			return deployment.DeployClass
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Body assembly (spec §15's template, adapted to the real schema).
// ---------------------------------------------------------------------------

// assembleIncidentFiling renders the issue. Every field is sourced from
// internal/incidents, so the template follows the columns that exist rather
// than the ones the spec assumed:
//
//   - Severity is the stored info|warning|critical, not a P0-P3 scale.
//   - Occurrences is COUNT(*) over ops_incident_observations; there is no
//     occurrence-count column.
//   - The incident is identified by its reference, with the UUID recorded in
//     the diagnostics table as the join key.
//   - There is no impact field anywhere in ops_incidents. Rather than leave the
//     section out — an absent line reads as "no impact" — it is rendered with
//     the incident summary and an explicit note about where it came from.
func (d IssueDeps) assembleIncidentFiling(
	detail incidents.Detail,
	correlation incidents.DeploymentCorrelation,
	hasCorrelation bool,
	deployClass string,
	target issueTarget,
) filing {
	now := d.now()
	loc := d.location()
	severity := strings.ToLower(strings.TrimSpace(string(detail.Severity)))
	status := strings.ToLower(strings.TrimSpace(string(detail.Status)))
	title := redactBounded(detail.Title, maxIssueTitleChars)
	if title == "" {
		title = "(untitled incident)"
	}
	summary := redactBounded(detail.Summary, maxSummaryChars)
	note := redactBounded(target.Note, maxNoteChars)
	environment := valueOr(evidence.RedactText(detail.Environment), d.Environment)

	var body strings.Builder
	fmt.Fprintf(&body, "## Incident #%d — %s — %s\n\n",
		detail.Reference, valueOr(severity, "unknown"), strings.ToUpper(valueOr(status, "unknown")))

	body.WriteString("| Field | Value |\n| --- | --- |\n")
	writeRow(&body, "Service", valueOr(evidence.RedactText(detail.Service), "unknown"))
	writeRow(&body, "Environment", valueOr(environment, "unknown"))
	if class := evidence.RedactText(detail.Classification); class != "" {
		writeRow(&body, "Classification", class)
	}
	writeRow(&body, "Started", fmt.Sprintf("%s (%s ago)",
		detail.FirstObservedAt.In(loc).Format("2006-01-02 15:04 MST"),
		age(now.Sub(detail.FirstObservedAt))))
	writeRow(&body, "Last observed", fmt.Sprintf("%s (%s ago)",
		detail.LastObservedAt.In(loc).Format("2006-01-02 15:04 MST"),
		age(now.Sub(detail.LastObservedAt))))
	// Occurrences is computed, not stored: it is COUNT(*) over
	// ops_incident_observations at the moment of filing, so it is a snapshot
	// and will drift as the incident continues.
	writeRow(&body, "Occurrences", strconv.FormatInt(detail.OccurrenceCount, 10)+" (at filing)")
	writeRow(&body, "Status at filing", statusAtFiling(detail, loc))
	writeRow(&body, "Filed at", now.In(loc).Format("2006-01-02 15:04 MST"))

	body.WriteString("\n### Deployment correlation\n\n")
	body.WriteString(renderCorrelationTable(correlation, hasCorrelation, deployClass, loc))

	body.WriteString("\n### Impact\n\n")
	if summary != "" {
		body.WriteString(summary)
		body.WriteString("\n\n")
	}
	body.WriteString("_ops_incidents has no impact column; the text above is the incident summary. " +
		"Blast radius has to be added by hand._\n")

	body.WriteString("\n### Diagnostics\n\n")
	body.WriteString("| Field | Value |\n| --- | --- |\n")
	writeRow(&body, "Incident ID", "`"+evidence.RedactText(detail.ID)+"`")
	writeRow(&body, "Reference", "#"+strconv.FormatInt(detail.Reference, 10))
	writeRow(&body, "Source", valueOr(evidence.RedactText(detail.Source), "unknown"))
	if eventID := evidence.RedactText(detail.SourceEventID); eventID != "" {
		writeRow(&body, "Source event", "`"+eventID+"`")
	}
	writeRow(&body, "Fingerprint", "`"+valueOr(evidence.RedactText(detail.Fingerprint), "none")+"`")
	writeRow(&body, "Updated", detail.UpdatedAt.In(loc).Format("2006-01-02 15:04 MST"))

	body.WriteString("\n### Evidence\n\n")
	if evidenceText := redactEvidence(detail.Evidence); evidenceText != "" {
		body.WriteString("```json\n")
		body.WriteString(evidenceText)
		body.WriteString("\n```\n")
	} else {
		body.WriteString("None recorded on the incident.\n")
	}

	body.WriteString("\n### Owner note\n\n")
	if note != "" {
		body.WriteString(note)
		body.WriteString("\n")
	} else {
		body.WriteString("_None._\n")
	}
	if target.FromReply {
		body.WriteString("\n_Filed by replying to the incident message in Telegram._\n")
	}

	body.WriteString("\n---\n")
	body.WriteString(referenceMarker)
	body.WriteString(strconv.FormatInt(detail.Reference, 10))
	body.WriteString("\n")
	fingerprint := searchableFingerprint(detail.Fingerprint)
	if fingerprint != "" {
		body.WriteString(fingerprintMarker)
		body.WriteString(fingerprint)
		body.WriteString("\n")
	}
	body.WriteString(issueFooter)
	body.WriteString("\n")

	return filing{
		Title:       fmt.Sprintf("[ops] Incident #%d — %s", detail.Reference, title),
		Body:        body.String(),
		Labels:      issueLabels(severity, deployClass),
		SearchQuery: dedupQuery(detail.Reference, fingerprint),
		CommentBody: renderRecurrenceComment(detail, note, now, loc),
		Reference:   detail.Reference,
	}
}

// statusAtFiling records the acknowledgement and resolution state at the moment
// the issue was filed, so a reader months later is not misled by the incident's
// current state.
func statusAtFiling(detail incidents.Detail, loc *time.Location) string {
	status := strings.ToLower(valueOr(strings.TrimSpace(string(detail.Status)), "unknown"))
	switch {
	case detail.Resolved:
		return status + ", resolved " + detail.ResolvedAt.In(loc).Format("2006-01-02 15:04 MST")
	case detail.Acknowledged:
		return status + ", acknowledged " + detail.AcknowledgedAt.In(loc).Format("2006-01-02 15:04 MST")
	default:
		return status + ", unacknowledged"
	}
}

// renderCorrelationTable renders the current/previous SHA table §15 asks for.
// "No deployment in the window" is a real answer and is stated, not omitted.
func renderCorrelationTable(correlation incidents.DeploymentCorrelation, has bool, deployClass string, loc *time.Location) string {
	if !has {
		return "Correlation unavailable — the deployment projection could not be read.\n"
	}
	if !correlation.HasCurrent {
		return fmt.Sprintf("No deployment found in the %s before onset.\n", age(correlation.Lookback))
	}

	var table strings.Builder
	table.WriteString("| Field | Value |\n| --- | --- |\n")
	writeRow(&table, "Strength", string(correlation.Strength))
	writeRow(&table, "Current SHA", "`"+shortSHA(evidence.RedactText(correlation.Current.SourceCommit))+"`")
	if correlation.HasPrevious && correlation.Previous.SourceCommit != "" {
		writeRow(&table, "Previous SHA", "`"+shortSHA(evidence.RedactText(correlation.Previous.SourceCommit))+"`")
	} else {
		writeRow(&table, "Previous SHA", "none recorded")
	}
	writeRow(&table, "Deployed", correlation.Current.StartedAt.In(loc).Format("2006-01-02 15:04 MST"))
	writeRow(&table, "Onset after deploy", age(correlation.SinceDeploy))
	writeRow(&table, "Deploy service", valueOr(evidence.RedactText(correlation.Current.Service), "unknown"))
	if !correlation.ServiceMatched {
		// The strength rule caps an unmatched service at weak; say why, so
		// nobody reads a coincidence as a cause.
		writeRow(&table, "Service match", "no — correlation is by time only")
	}
	if deployClass != "" {
		writeRow(&table, "Deploy class", evidence.RedactText(deployClass))
	}
	return table.String()
}

// renderRecurrenceComment is what gets appended to an existing issue when the
// dedup search finds one. It is short by design: the issue already carries the
// full context, and what a reader needs on a recurrence is that it happened
// again and how much.
func renderRecurrenceComment(detail incidents.Detail, note string, now time.Time, loc *time.Location) string {
	var comment strings.Builder
	fmt.Fprintf(&comment, "Recurrence reported from Telegram at %s.\n\n",
		now.In(loc).Format("2006-01-02 15:04 MST"))
	comment.WriteString("| Field | Value |\n| --- | --- |\n")
	writeRow(&comment, "Incident", "#"+strconv.FormatInt(detail.Reference, 10))
	writeRow(&comment, "Status", statusAtFiling(detail, loc))
	writeRow(&comment, "Occurrences", strconv.FormatInt(detail.OccurrenceCount, 10))
	writeRow(&comment, "Last observed", fmt.Sprintf("%s (%s ago)",
		detail.LastObservedAt.In(loc).Format("2006-01-02 15:04 MST"),
		age(now.Sub(detail.LastObservedAt))))
	if note != "" {
		comment.WriteString("\n**Owner note:** ")
		comment.WriteString(note)
		comment.WriteString("\n")
	}
	comment.WriteString("\n")
	comment.WriteString(issueFooter)
	comment.WriteString("\n")
	return comment.String()
}

// issueLabels builds the label set: always "ops" and "incident", the real
// stored severity, and deploy-class when the deployment carried one. The
// severity is the database's own value — inventing a P-scale here would make
// the repository's labels disagree with the incident store.
func issueLabels(severity, deployClass string) []string {
	labels := []string{"ops", "incident"}
	if clean := sanitizeLabel(severity); clean != "" {
		labels = append(labels, clean)
	}
	if clean := sanitizeLabel(deployClass); clean != "" {
		labels = append(labels, "deploy-class:"+clean)
	}
	return labels
}

// sanitizeLabel bounds a label to characters GitHub accepts unambiguously.
// deploy_class and severity both originate in the database, but a label is
// part of a URL in the GitHub UI and a stray space or slash there is a support
// ticket nobody wants to debug from a phone.
func sanitizeLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
		if b.Len() >= maxLabelChars {
			break
		}
	}
	return strings.Trim(b.String(), "-.")
}

// dedupQuery builds the GitHub search that decides create-versus-comment. The
// fingerprint is preferred: it is what the incident pipeline uses to decide two
// observations are the same problem, so it is exactly the right dedup key. The
// reference is the fallback for an incident with no fingerprint.
func dedupQuery(reference int64, fingerprint string) string {
	if fingerprint != "" {
		return fmt.Sprintf("%q in:body", fingerprintMarker+fingerprint)
	}
	if reference > 0 {
		return fmt.Sprintf("%q in:body", referenceMarker+strconv.FormatInt(reference, 10))
	}
	return ""
}

// searchableFingerprint bounds a fingerprint to characters that survive a
// GitHub search query intact. A fingerprint carrying a quote or a qualifier
// character would change the meaning of the query it is embedded in.
func searchableFingerprint(fingerprint string) string {
	fingerprint = strings.TrimSpace(fingerprint)
	var b strings.Builder
	for _, r := range fingerprint {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			return "" // anything unexpected: fall back to the reference marker
		}
		if b.Len() >= maxFingerprintLen {
			break
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Sending, deduplication and failure handling.
// ---------------------------------------------------------------------------

// send performs the dedup search, then creates or comments, and renders the
// reply. Every failure path carries the assembled body back to the operator.
func (d IssueDeps) send(ctx context.Context, assembled filing, target issueTarget) telegram.Response {
	if d.GitHub == nil {
		return d.fallback(assembled, "GitHub client "+unavailable+".")
	}

	// Deduplicate before creating. Filing a third issue for the same recurring
	// incident is how an issue tracker stops being read.
	existing, searchFailed := d.findExisting(ctx, assembled)
	if existing != nil {
		return d.comment(ctx, assembled, *existing)
	}

	scoped, cancel := d.githubScope(ctx)
	issue, err := d.GitHub.CreateIssue(scoped, github.IssueRequest{
		Title:  assembled.Title,
		Body:   assembled.Body,
		Labels: assembled.Labels,
	})
	cancel()
	if err != nil {
		return d.fallback(assembled, "GitHub rejected the issue: "+describeGitHubError(err))
	}

	body := fmt.Sprintf("📋 Issue #%d created.", issue.Number)
	if issue.HTMLURL != "" {
		body += "\n" + telegram.Esc(issue.HTMLURL)
	} else if d.Repository != "" {
		body += "\n" + telegram.Esc(d.Repository)
	}
	if target.FromReply {
		body += fmt.Sprintf("\nFrom the incident in the replied-to message (#%d).", assembled.Reference)
	}
	if searchFailed {
		// Do not pretend the dedup ran. A duplicate the operator knows about is
		// recoverable; one they do not is how three issues appear.
		body += "\nDeduplication check failed, so this may duplicate an open issue."
	}
	return telegram.Response{Body: body}
}

// findExisting runs the dedup search. The bool reports that the search itself
// failed, which is different from "found nothing" and is surfaced in the reply.
func (d IssueDeps) findExisting(ctx context.Context, assembled filing) (*github.Issue, bool) {
	if assembled.SearchQuery == "" {
		return nil, false
	}
	scoped, cancel := d.githubScope(ctx)
	defer cancel()
	matches, err := d.GitHub.SearchOpenIssues(scoped, assembled.SearchQuery)
	if err != nil {
		return nil, true
	}
	for i := range matches {
		if matches[i].Number > 0 {
			return &matches[i], false
		}
	}
	return nil, false
}

// comment appends to an existing open issue instead of creating a second one.
func (d IssueDeps) comment(ctx context.Context, assembled filing, existing github.Issue) telegram.Response {
	body := assembled.CommentBody
	if strings.TrimSpace(body) == "" {
		body = assembled.Body
	}
	scoped, cancel := d.githubScope(ctx)
	_, err := d.GitHub.CommentOnIssue(scoped, existing.Number, body)
	cancel()
	if err != nil {
		return d.fallback(assembled, fmt.Sprintf("Issue #%d already covers this, but the comment failed: %s",
			existing.Number, describeGitHubError(err)))
	}

	reply := fmt.Sprintf("Added to existing issue #%d.", existing.Number)
	if existing.HTMLURL != "" {
		reply += "\n" + telegram.Esc(existing.HTMLURL)
	}
	return telegram.Response{Body: reply}
}

// fallback is spec §20's rule made concrete: when GitHub cannot take the issue,
// the operator gets the failure *and* the assembled body, so the work of
// writing it is not lost and the filing can be finished by hand.
func (d IssueDeps) fallback(assembled filing, reason string) telegram.Response {
	var reply strings.Builder
	reply.WriteString(telegram.Bold("Issue not filed"))
	reply.WriteString("\n")
	reply.WriteString(telegram.Esc(telegram.TruncateError(reason)))
	reply.WriteString("\nPaste this into GitHub by hand:\n")
	reply.WriteString(telegram.Bold("Title"))
	reply.WriteString("\n")
	reply.WriteString(telegram.Pre(assembled.Title))
	reply.WriteString("\n")
	reply.WriteString(telegram.Bold("Labels"))
	reply.WriteString("\n")
	reply.WriteString(telegram.Pre(strings.Join(assembled.Labels, ", ")))
	reply.WriteString("\n")
	reply.WriteString(telegram.Bold("Body"))
	reply.WriteString("\n")
	reply.WriteString(telegram.Pre(assembled.Body))
	return telegram.Response{Body: reply.String()}
}

// describeGitHubError renders a GitHub failure for an operator.
//
// The client already guarantees the token is absent from the error string; this
// adds the "what do I do about it" half, because "422" alone at 3am is not
// actionable. The message is bounded and escaped by the caller.
func describeGitHubError(err error) string {
	var apiErr *github.APIError
	if !errors.As(err, &apiErr) {
		return telegram.TruncateError(err.Error())
	}
	switch {
	case apiErr.IsUnauthorized():
		return "the GitHub token was rejected (401). It needs rotating."
	case apiErr.IsRateLimited():
		if apiErr.RetryAfter > 0 {
			return "rate limited by GitHub. Retry in " + age(apiErr.RetryAfter) + "."
		}
		return "rate limited by GitHub."
	case apiErr.IsForbidden():
		return "the token lacks permission on this repository (403)."
	case apiErr.IsNotFound():
		return "the repository was not found, or the token cannot see it (404)."
	case apiErr.IsValidation():
		return "GitHub rejected the request (422): " + telegram.TruncateError(apiErr.Message)
	case apiErr.IsServer():
		return fmt.Sprintf("GitHub returned %d. It is not the request.", apiErr.StatusCode)
	default:
		return telegram.TruncateError(apiErr.Error())
	}
}

// ---------------------------------------------------------------------------
// Small helpers.
// ---------------------------------------------------------------------------

// redactBounded is the mandatory path for every free-text field. Redaction runs
// first and truncation second: truncating first could cut a credential in half
// and leave a fragment the patterns no longer match.
func redactBounded(value string, max int) string {
	return strings.TrimSpace(telegram.TruncateField(evidence.RedactText(value), max))
}

// isOnlyRedaction reports that a string has no content left once the redaction
// markers are removed. A standalone note that redacts to nothing but markers
// carried no information in the first place.
func isOnlyRedaction(value string) bool {
	stripped := strings.ReplaceAll(value, evidence.Redacted, "")
	return strings.TrimSpace(strings.Trim(stripped, " \t.,:;-_")) == ""
}

// redactEvidence renders the incident's evidence JSONB as compact, redacted
// text. Invalid or absent JSON yields an empty string rather than an error: the
// evidence section is a convenience, not the reason the issue exists.
func redactEvidence(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		// Not valid JSON: redact and bound the raw text rather than dropping
		// evidence entirely.
		return redactBounded(string(raw), maxEvidenceChars)
	}
	text := compact.String()
	if text == "" || text == "null" || text == "{}" {
		return ""
	}
	return redactBounded(text, maxEvidenceChars)
}

// writeRow emits one Markdown table row, escaping the pipe that would
// otherwise split a cell. Values reaching here are already redacted.
func writeRow(b *strings.Builder, field, value string) {
	b.WriteString("| ")
	b.WriteString(field)
	b.WriteString(" | ")
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(value, "|", "\\|"), "\n", " "))
	b.WriteString(" |\n")
}

// valueOr returns value, or fallback when value is blank.
func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// firstLine keeps a multi-line note from becoming a multi-line issue title.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

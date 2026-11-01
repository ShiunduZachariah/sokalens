package commands

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/evidence"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
)

// errNoSource marks a projection whose data source was not wired.
var errNoSource = errors.New("commands: data source unavailable")

// incidents lists open incidents, most severe first then most recent, capped
// with a "+N more" tail. Spec §10.2.
func (d Deps) incidents(ctx context.Context, _ telegram.Request) (telegram.Response, error) {
	if d.Incidents == nil {
		return telegram.Response{Body: "Incident store " + unavailable + "."}, nil
	}
	scoped, cancel := d.scope(ctx)
	defer cancel()

	// Ask for one past the cap so "+N more" is honest without a second query.
	open, err := d.Incidents.ListOpen(scoped, listCap+1)
	if err != nil {
		return telegram.Response{Body: "Incident store " + unavailable + "."}, nil
	}
	if len(open) == 0 {
		return telegram.Response{Body: "No open incidents."}, nil
	}

	shown := open
	extra := 0
	if len(shown) > listCap {
		extra = len(shown) - listCap
		shown = shown[:listCap]
	}

	now := d.now()
	rows := make([][]string, 0, len(shown))
	for _, inc := range shown {
		rows = append(rows, []string{
			"#" + strconv.FormatInt(inc.Reference, 10),
			severityLabel(string(inc.Severity)),
			truncateCell(inc.Service, 10),
			truncateCell(titleOf(inc.Title), 22),
			age(now.Sub(inc.FirstObservedAt)),
			"×" + strconv.FormatInt(inc.OccurrenceCount, 10),
		})
	}

	body := telegram.Bold(fmt.Sprintf("Open incidents (%d)", len(open))) + "\n\n"
	body += telegram.PreTable(rows)
	if extra > 0 {
		body += "\n+" + strconv.Itoa(extra) + " more"
	}
	body += "\n\n/incident &lt;id&gt; for detail"
	return telegram.Response{Body: body}, nil
}

// incident renders full detail for one incident. Spec §10.3.
//
// All free text passes evidence.RedactText before it leaves the process:
// incident titles and summaries can carry fragments of log lines, and Telegram
// cloud chats are not end-to-end encrypted (spec §18).
func (d Deps) incident(ctx context.Context, req telegram.Request) (telegram.Response, error) {
	if len(req.Args) == 0 {
		return telegram.Response{Body: "Usage: /incident &lt;id&gt;"}, nil
	}
	reference, err := strconv.ParseInt(strings.TrimPrefix(req.Args[0], "#"), 10, 64)
	if err != nil || reference <= 0 {
		return telegram.Response{Body: "Incident id must be a number."}, nil
	}
	if d.Incidents == nil {
		return telegram.Response{Body: "Incident store " + unavailable + "."}, nil
	}

	scoped, cancel := d.scope(ctx)
	defer cancel()
	detail, err := d.Incidents.GetByReference(scoped, reference)
	if err != nil {
		// Do not distinguish "never existed" from "purged": that distinction
		// is only useful to someone probing (spec §10.3).
		return telegram.Response{Body: fmt.Sprintf("Incident #%d not found.", reference)}, nil
	}

	now := d.now()
	header := fmt.Sprintf("Incident #%d — %s — %s",
		detail.Reference, severityLabel(string(detail.Severity)), strings.ToUpper(string(detail.Status)))

	rows := [][]string{
		{"Title", redactCell(detail.Title, 60)},
		{"Service", redactCell(detail.Service, 24)},
		{"Environment", redactCell(detail.Environment, 24)},
		{"Started", fmt.Sprintf("%s (%s ago)",
			detail.FirstObservedAt.In(d.location()).Format("15:04 02 Jan"),
			age(now.Sub(detail.FirstObservedAt)))},
		{"Last seen", age(now.Sub(detail.LastObservedAt)) + " ago"},
		{"Occurrences", strconv.FormatInt(detail.OccurrenceCount, 10)},
	}
	if detail.Classification != "" {
		rows = append(rows, []string{"Class", redactCell(detail.Classification, 24)})
	}

	body := telegram.Bold(header) + "\n\n" + telegram.PreTable(rows)

	if summary := evidence.RedactText(detail.Summary); strings.TrimSpace(summary) != "" {
		body += "\n" + telegram.Bold("Summary") + "\n" + telegram.Pre(telegram.TruncateField(summary, 600))
	}

	body += "\n" + d.renderCorrelation(ctx, detail.ID)
	body += fmt.Sprintf("\n/issue %d to file this on GitHub", detail.Reference)
	return telegram.Response{Body: body}, nil
}

// renderCorrelation reports the deployment in effect at onset.
func (d Deps) renderCorrelation(ctx context.Context, incidentID string) string {
	scoped, cancel := d.scope(ctx)
	defer cancel()
	corr, err := d.Incidents.CorrelateDeployment(scoped, incidentID)
	if err != nil {
		return telegram.Bold("Deployment correlation") + "\n" + telegram.Pre(unavailable)
	}
	if corr.Strength == "none" || corr.Current.SourceCommit == "" {
		return telegram.Bold("Deployment correlation") + "\n" + telegram.Pre("none")
	}
	rows := [][]string{
		{"strength", string(corr.Strength)},
		{"current", shortSHA(corr.Current.SourceCommit)},
	}
	if corr.Previous.SourceCommit != "" {
		rows = append(rows, []string{"previous", shortSHA(corr.Previous.SourceCommit)})
	}
	return telegram.Bold("Deployment correlation") + "\n" + telegram.PreTable(rows)
}

// titleOf keeps a title renderable in a narrow mobile column.
func titleOf(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(untitled)"
	}
	return s
}

// truncateCell bounds one table cell. Redaction is applied by redactCell where
// the value can carry free text.
func truncateCell(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	return telegram.TruncateField(s, max)
}

// redactCell redacts then bounds a free-text cell.
func redactCell(s string, max int) string {
	return truncateCell(evidence.RedactText(s), max)
}

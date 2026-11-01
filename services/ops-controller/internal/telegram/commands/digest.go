package commands

import (
	"context"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/notifications"
)

// DigestSummary renders the health lines of the daily digest.
//
// It is deliberately the same four projections /status renders, through the
// same helpers: the digest is the message that arrives when nobody typed
// /status, so the two must never tell different stories. A projection that is
// missing or unreadable renders "unavailable" here exactly as it does there —
// never an omitted line, which would read as "fine".
//
// This method never returns an error. The digest goes out every day whatever
// the database is doing.
func (d Deps) DigestSummary(ctx context.Context) []notifications.Section {
	severity, sevErr := d.severityCounts(ctx)
	marker, word := healthLabel(severity, sevErr)

	controller := d.Version
	if controller == "" {
		controller = "dev"
	}
	if d.Environment != "" {
		controller += " · " + d.Environment
	}

	return []notifications.Section{
		{Label: "Health", Value: marker + " " + word},
		{Label: "Controller", Value: controller},
		{Label: "Incidents", Value: d.renderSeverity(severity, sevErr)},
		{Label: "Queue", Value: d.renderQueueLine(ctx)},
		{Label: "Production", Value: d.renderDeployLine(ctx)},
		{Label: "AI spend", Value: d.renderSpendLine(ctx)},
	}
}

var _ notifications.SummarySource = Deps{}

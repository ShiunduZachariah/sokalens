package commands

import (
	"context"
	"fmt"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
)

// status renders the one screen that answers "is everything fine" without
// opening a dashboard. Spec §10.1.
//
// Every section degrades independently: a failed projection renders
// "unavailable" rather than failing the whole command or, worse, silently
// omitting the line. An omitted line reads as "fine" and it is not.
func (d Deps) status(ctx context.Context, _ telegram.Request) (telegram.Response, error) {
	rows := make([][]string, 0, 5)

	// Incidents drive the headline emoji, so read them first.
	severity, sevErr := d.severityCounts(ctx)
	rows = append(rows, []string{"Incidents", d.renderSeverity(severity, sevErr)})
	rows = append(rows, []string{"Queue", d.renderQueueLine(ctx)})
	rows = append(rows, []string{"Production", d.renderDeployLine(ctx)})
	rows = append(rows, []string{"AI spend", d.renderSpendLine(ctx)})

	controller := d.Version
	if controller == "" {
		controller = "dev"
	}
	if d.Environment != "" {
		controller += " · " + d.Environment
	}
	rows = append([][]string{{"Controller", controller}}, rows...)

	var body string
	body += telegram.Bold(headline(severity, sevErr)) + "\n\n"
	body += telegram.PreTable(rows)
	return telegram.Response{Body: body}, nil
}

// headline derives the leading emoji deterministically: red when any critical
// incident is open, amber when any warning is, green otherwise. An unreadable
// incident store is amber, not green — unknown is not healthy.
func headline(counts incidentSeverity, err error) string {
	marker, word := healthLabel(counts, err)
	return marker + " SokaLens — " + word
}

// healthLabel is the marker and word behind both the /status headline and the
// digest's Health line. One switch, so the daily digest can never disagree
// with what /status says thirty seconds later.
func healthLabel(counts incidentSeverity, err error) (marker, word string) {
	switch {
	case err != nil:
		return "🟡", "status degraded (incident store " + unavailable + ")"
	case counts.Critical > 0:
		return "🔴", "Critical"
	case counts.Warning > 0:
		return "🟡", "Degraded"
	default:
		return "🟢", "Healthy"
	}
}

// incidentSeverity mirrors incidents.SeverityCounts so status can hold a zero
// value when the projection is unavailable.
type incidentSeverity struct {
	Critical int64
	Warning  int64
	Info     int64
}

func (d Deps) severityCounts(ctx context.Context) (incidentSeverity, error) {
	if d.Incidents == nil {
		return incidentSeverity{}, errNoSource
	}
	scoped, cancel := d.scope(ctx)
	defer cancel()
	counts, err := d.Incidents.CountOpenBySeverity(scoped)
	if err != nil {
		return incidentSeverity{}, err
	}
	return incidentSeverity{Critical: counts.Critical, Warning: counts.Warning, Info: counts.Info}, nil
}

func (d Deps) renderSeverity(c incidentSeverity, err error) string {
	if err != nil {
		return unavailable
	}
	return fmt.Sprintf("crit %d · warn %d · info %d", c.Critical, c.Warning, c.Info)
}

func (d Deps) renderQueueLine(ctx context.Context) string {
	if d.Jobs == nil {
		return unavailable
	}
	scoped, cancel := d.scope(ctx)
	defer cancel()
	snap, err := d.Jobs.QueueDepth(scoped, 0)
	if err != nil {
		return unavailable
	}
	line := fmt.Sprintf("waiting %d · leased %d", snap.TotalWaiting, snap.TotalLeased)
	if snap.TotalWaiting > 0 {
		line += fmt.Sprintf(" (oldest %s)", age(snap.OldestWaitingAge))
	}
	if snap.TotalDeadLettered > 0 {
		line += fmt.Sprintf(" · DLQ %d", snap.TotalDeadLettered)
	}
	return line
}

func (d Deps) renderDeployLine(ctx context.Context) string {
	if d.Deployments == nil {
		return unavailable
	}
	scoped, cancel := d.scope(ctx)
	defer cancel()
	listing, err := d.Deployments.Recent(scoped, 1, d.Environment)
	if err != nil {
		return unavailable
	}
	if len(listing.Deployments) == 0 {
		return "no deployments recorded"
	}
	dep := listing.Deployments[0]
	line := fmt.Sprintf("%s · %s ago", shortSHA(dep.SourceCommit), age(dep.Age))
	if dep.RollbackState == "rolled_back" {
		line += " · rolled back"
	}
	return line
}

func (d Deps) renderSpendLine(ctx context.Context) string {
	if d.Budget == nil {
		return unavailable
	}
	scoped, cancel := d.scope(ctx)
	defer cancel()
	snap, err := d.Budget.Spend(scoped)
	if err != nil {
		return unavailable
	}
	return fmt.Sprintf("%s today · %s month · %s",
		micros(snap.TodayMicros), micros(snap.MonthToDateMicros), snap.Tier)
}

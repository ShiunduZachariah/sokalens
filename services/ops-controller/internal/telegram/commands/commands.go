// Package commands implements the read-only Telegram ops commands.
//
// It imports internal/telegram for the Request/Response/Registration contract;
// telegram never imports this package. Wiring happens in main via Register, so
// the dependency stays one-directional and there is no import cycle.
//
// Every command here is read-only. Nothing in this package writes to the
// database, dispatches a job, or mutates production in any way.
package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/aispend"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/deployments"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/incidents"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
)

// Deps are the read-only projections the commands render. Any of them may be
// nil: a command whose data source is missing replies "unavailable" rather
// than failing, because a missing line reads as "fine" and it is not.
type Deps struct {
	Incidents   *incidents.Queries
	Jobs        *jobs.Queries
	Budget      *aispend.Queries
	Deployments *deployments.Queries

	// Version and Environment label the /status header.
	Version     string
	Environment string
	// Location renders timestamps in the operator's zone (EAT in production).
	Location *time.Location
	// Now is injectable for tests.
	Now func() time.Time
	// QueryTimeout bounds every projection read so one slow query cannot hold
	// the poller's update-handling path open.
	QueryTimeout time.Duration
}

const (
	defaultQueryTimeout = 5 * time.Second
	// unavailable is rendered for any section whose source is missing or
	// failed. Spec §13: never omit the line.
	unavailable = "unavailable"
	// listCap is the /incidents display cap before "+N more".
	listCap = 10
	// deployCap is the /deploys display cap.
	deployCap = 5
)

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Deps) location() *time.Location {
	if d.Location != nil {
		return d.Location
	}
	return time.UTC
}

func (d Deps) timeout() time.Duration {
	if d.QueryTimeout > 0 {
		return d.QueryTimeout
	}
	return defaultQueryTimeout
}

// scope bounds one projection read.
func (d Deps) scope(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d.timeout())
}

// Register adds every read command to the registry. /help is generated from
// the registry itself, so it cannot drift from what is actually dispatchable.
func Register(registry *telegram.Registry, deps Deps) error {
	if registry == nil {
		return errors.New("commands: registry is required")
	}
	all := []telegram.Registration{
		{
			Name:     "status",
			Summary:  "One screen: health, incidents, queues, deploy, spend.",
			Handler:  telegram.HandlerFunc(deps.status),
			Mutating: false,
		},
		{
			Name:     "incidents",
			Summary:  "Open incidents, most severe first.",
			Handler:  telegram.HandlerFunc(deps.incidents),
			Mutating: false,
		},
		{
			Name:     "incident",
			Usage:    "<id>",
			Summary:  "Full detail for one incident.",
			Handler:  telegram.HandlerFunc(deps.incident),
			Mutating: false,
		},
		{
			Name:     "queues",
			Summary:  "Queue depth, throughput and dead letters.",
			Handler:  telegram.HandlerFunc(deps.queues),
			Mutating: false,
		},
		{
			Name:     "deploys",
			Summary:  "Recent deployments and rollback state.",
			Handler:  telegram.HandlerFunc(deps.deploys),
			Mutating: false,
		},
		{
			Name:     "budget",
			Summary:  "AI spend today, month to date, and projection.",
			Handler:  telegram.HandlerFunc(deps.budget),
			Mutating: false,
		},
	}
	for _, reg := range all {
		if err := registry.Register(reg); err != nil {
			return fmt.Errorf("register /%s: %w", reg.Name, err)
		}
	}

	// /help renders the registry, so it must be registered last and must read
	// the registry it belongs to.
	return registry.Register(telegram.Registration{
		Name:    "help",
		Summary: "This list.",
		Handler: telegram.HandlerFunc(func(context.Context, telegram.Request) (telegram.Response, error) {
			return telegram.Response{Body: registry.Help()}, nil
		}),
	})
}

// ---------------------------------------------------------------------------
// Rendering helpers.
// ---------------------------------------------------------------------------

// severityLabel renders the real stored severity. The database constrains
// severity to info|warning|critical; there is no P0-P3 scale in this schema,
// and inventing one here would make /status disagree with the database.
func severityLabel(s string) string {
	switch strings.ToLower(s) {
	case "critical":
		return "crit"
	case "warning":
		return "warn"
	case "info":
		return "info"
	case "":
		return "?"
	default:
		return strings.ToLower(s)
	}
}

// age renders a duration compactly: 14s, 4m, 2h, 3d.
func age(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// shortSHA trims a commit to the 7 characters an operator actually reads.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// micros renders the ledger's micro-unit amounts as dollars.
func micros(v int64) string {
	return fmt.Sprintf("$%.2f", float64(v)/1_000_000)
}

// pct renders a percentage to one decimal place.
func pct(v float64) string { return fmt.Sprintf("%.1f%%", v) }

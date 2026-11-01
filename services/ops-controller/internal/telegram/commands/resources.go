package commands

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/sysmetrics"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
)

// ---------------------------------------------------------------------------
// /resources — host disk, memory and CPU, read from the Prometheus that
// already runs in the compose network.
//
// Disk is the reading this command exists for: the question behind it is "how
// close to full is the box", so disk goes first, gets a row per filesystem,
// and anything at or above the warning threshold is flagged rather than left
// to be spotted in a column of numbers.
//
// This command is read-only and outbound-only. It registers no route: it is a
// client of Prometheus, not a server.
//
// Like release.go and run.go, this file declares its own deps struct and its
// own registration function rather than extending Deps in commands.go, so main
// can wire it independently and a deployment without Prometheus simply does
// not call RegisterResources.
// ---------------------------------------------------------------------------

// ResourceDeps are the /resources collaborators. Metrics may be nil, and a nil
// source degrades to "unavailable" exactly like every other read command.
type ResourceDeps struct {
	// Metrics is the Prometheus instant-query client.
	Metrics *sysmetrics.Client

	// QueryTimeout bounds the whole command. The client applies its own
	// per-query bound underneath this; both exist because five sequential
	// queries each inside a 5s bound is not a 5s command.
	QueryTimeout time.Duration
}

const (
	// defaultResourceTimeout bounds all five queries together.
	defaultResourceTimeout = 12 * time.Second

	// resourceDiskCap is the most filesystems rendered. They are sorted
	// fullest-first, so the cap can only ever hide the least interesting
	// rows, and the count of what was hidden is still shown.
	resourceDiskCap = 6

	// resourceWarnGlyph marks a filesystem at or above the threshold.
	resourceWarnGlyph = "⚠️"

	// resourceMountCell bounds a mountpoint cell. PreTable bounds cells too;
	// this keeps the table narrow enough for a phone before it gets there.
	resourceMountCell = 18
)

func (d ResourceDeps) scope(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := d.QueryTimeout
	if timeout <= 0 {
		timeout = defaultResourceTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// RegisterResources adds /resources to the registry.
func RegisterResources(registry *telegram.Registry, deps ResourceDeps) error {
	if registry == nil {
		return errors.New("commands: registry is required")
	}
	if err := registry.Register(telegram.Registration{
		Name:    "resources",
		Summary: "Host disk, memory and CPU. Disk first, pressure flagged.",
		// Read-only: this issues GET requests to Prometheus and nothing else.
		Mutating: false,
		Handler:  telegram.HandlerFunc(deps.resources),
	}); err != nil {
		return fmt.Errorf("register /resources: %w", err)
	}
	return nil
}

// resources renders the snapshot. It never returns an error: an unreachable
// Prometheus is an "unavailable" line, not a failure that surfaces in the
// poller as a dropped command.
func (d ResourceDeps) resources(ctx context.Context, _ telegram.Request) (telegram.Response, error) {
	scoped, cancel := d.scope(ctx)
	defer cancel()

	// A nil client yields a fully-unavailable snapshot rather than a panic, so
	// the reply is still the full skeleton with every section marked
	// unavailable. A silent or truncated reply here would read as "fine".
	snap := d.Metrics.Snapshot(scoped)
	return telegram.Response{Body: renderResources(snap)}, nil
}

// renderResources is the whole reply. Split out from the handler so the
// rendering rules can be tested against a constructed snapshot without a
// server.
func renderResources(snap sysmetrics.Snapshot) string {
	body := telegram.Bold("Resources") + "\n\n"

	// Disk first, and never omitted.
	body += telegram.Bold("Disk") + "\n"
	body += renderDiskSection(snap)

	// Memory and CPU share one table: two label/value rows, the same shape
	// /status uses.
	body += "\n\n" + telegram.PreTable([][]string{
		{"Memory", renderMemoryLine(snap)},
		{"CPU", renderCPULine(snap)},
	})
	return body
}

// renderDiskSection renders a row per filesystem, fullest first.
func renderDiskSection(snap sysmetrics.Snapshot) string {
	if snap.DiskErr != nil {
		// The marker stays in the pre block, which does not wrap on a phone;
		// the diagnosis goes underneath as ordinary text, which does.
		return telegram.Pre("disk "+unavailable) + diskErrorNote(snap.DiskErr)
	}
	if len(snap.Filesystems) == 0 {
		return telegram.Pre("no real filesystems reported")
	}

	shown := snap.Filesystems
	hidden := 0
	if len(shown) > resourceDiskCap {
		hidden = len(shown) - resourceDiskCap
		shown = shown[:resourceDiskCap]
	}

	rows := make([][]string, 0, len(shown))
	for _, fs := range shown {
		mount := truncateCell(fs.Mountpoint, resourceMountCell)
		used := pct(fs.UsedPercent)
		if fs.Pressured() {
			// The glyph leads the used column so the eye finds it without
			// reading the numbers. A 91% disk must be obvious at a glance.
			used = resourceWarnGlyph + " " + used
		}
		rows = append(rows, []string{mount, used, resourceBytes(fs.AvailBytes) + " free"})
	}
	out := telegram.PreTable(rows)
	if snap.DiskFallback {
		// The reader has to know which view this is. On a containerised host
		// the overlay root is the real capacity figure; on a normal host this
		// note appearing at all is itself the thing to investigate.
		out += "\n" + telegram.Esc("(no non-container filesystem is reported on this host; showing every mount that carries capacity)")
	}
	if hidden > 0 {
		out += "\n(" + strconv.Itoa(hidden) + " quieter filesystem(s) not shown)"
	}

	// A summary line after the table, because the table is where a reader
	// stops looking once the first row seems fine.
	if pressured := snap.Pressured(); len(pressured) > 0 {
		noun := "filesystems"
		if len(pressured) == 1 {
			noun = "filesystem"
		}
		out += "\n" + resourceWarnGlyph + " " + telegram.Esc(fmt.Sprintf(
			"%d %s at or above %s used", len(pressured), noun, pct(sysmetrics.WarnUsedPercent)))
	}
	return out
}

// diskErrorNote says which failure this was. "unavailable" alone cannot be
// acted on: a dead Prometheus, a rejected query and a host reporting no
// filesystem metrics need three different responses, and the reply is read on
// a phone with no dashboard to hand.
func diskErrorNote(err error) string {
	var note string
	switch {
	case errors.Is(err, sysmetrics.ErrNoData):
		note = "Prometheus answered but reported no filesystem series; check the node-exporter filesystem collector."
	case errors.Is(err, sysmetrics.ErrUnreachable):
		note = "Prometheus is unreachable from the ops controller."
	default:
		var apiErr *sysmetrics.APIError
		if errors.As(err, &apiErr) {
			note = "Prometheus refused the query."
		}
	}
	if note == "" {
		return ""
	}
	return "\n" + telegram.Esc(note)
}

func renderMemoryLine(snap sysmetrics.Snapshot) string {
	if snap.MemoryErr != nil || snap.Memory == nil {
		return sectionErrorNote(snap.MemoryErr)
	}
	return fmt.Sprintf("%s used of %s", pct(snap.Memory.UsedPercent), resourceBytes(snap.Memory.TotalBytes))
}

func renderCPULine(snap sysmetrics.Snapshot) string {
	if snap.CPUErr != nil || snap.CPU == nil {
		return sectionErrorNote(snap.CPUErr)
	}
	window := snap.CPU.Window
	if window == "" {
		window = sysmetrics.CPUWindow
	}
	return fmt.Sprintf("%s used (%s avg)", pct(snap.CPU.UsedPercent), window)
}

// sectionErrorNote is the one-cell version of diskErrorLine for the memory and
// CPU rows, which live in a narrow table.
func sectionErrorNote(err error) string {
	switch {
	case errors.Is(err, sysmetrics.ErrNoData):
		return unavailable + " (not scraped)"
	case errors.Is(err, sysmetrics.ErrUnreachable):
		return unavailable + " (prometheus down)"
	}
	return unavailable
}

// resourceBytes renders a byte count in the unit an operator reads it in.
// Named with a resource prefix so it cannot collide with another command's
// helper in this package.
func resourceBytes(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return "?"
	}
	const unit = 1024.0
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

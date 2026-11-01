package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/provider"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
)

// ---------------------------------------------------------------------------
// /quota — how close the platform is to its provider ceilings.
//
// The API-Football daily allowance is the hardest external limit the platform
// has: exhausting it stops ingestion until the window rolls over, and nothing
// in the product degrades gracefully when that happens. This command exists so
// that state is one message away instead of a dashboard away.
//
// It is read-only, and it declares its own deps and its own Register rather
// than extending the Deps struct in commands.go, matching RunDeps/RegisterRun
// and ReleaseDeps/RegisterRelease: main wires each command group separately.
// ---------------------------------------------------------------------------

// QuotaDeps are the /quota command's collaborators. Quota may be nil, in which
// case the command degrades to "unavailable" rather than failing — a missing
// line reads as "fine", and a quota this command cannot read is not fine.
type QuotaDeps struct {
	Quota *provider.Queries

	// Location renders the throttle deadline in the operator's zone (EAT in
	// production).
	Location *time.Location
	// Now is injectable for tests.
	Now func() time.Time
	// QueryTimeout bounds each read so one slow query cannot hold the poller's
	// update-handling path open.
	QueryTimeout time.Duration
}

const (
	// quotaCap is the number of providers rendered. The projection is already
	// bounded; this bounds the message.
	quotaCap = 8
	// quotaCategoryCap is the number of error categories named per provider.
	quotaCategoryCap = 4
	// quotaNameCap keeps the provider column from pushing the table wider than
	// a phone screen.
	quotaNameCap = 17
	// quotaUnknown is what a counter with no reported ceiling renders as. It
	// is deliberately not "0%": zero used and unknown-how-much-used are
	// opposite readings, and the second must never be shown as the first.
	quotaUnknown = "unknown"
)

func (d QuotaDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d QuotaDeps) location() *time.Location {
	if d.Location != nil {
		return d.Location
	}
	return time.UTC
}

func (d QuotaDeps) scope(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := d.QueryTimeout
	if timeout <= 0 {
		timeout = defaultQueryTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// RegisterQuota adds /quota to the registry.
//
// It is separate from Register in commands.go so main can wire the provider
// projection independently of the other read commands.
func RegisterQuota(registry *telegram.Registry, deps QuotaDeps) error {
	if registry == nil {
		return errors.New("commands: registry is required")
	}
	if err := registry.Register(telegram.Registration{
		Name:     "quota",
		Summary:  "Provider API quota: daily headroom, throttles, today's errors.",
		Handler:  telegram.HandlerFunc(deps.quota),
		Mutating: false,
	}); err != nil {
		return fmt.Errorf("register /quota: %w", err)
	}
	return nil
}

// quota renders provider headroom. Both sections degrade independently: a
// section whose source is missing or unreadable renders "unavailable" and is
// never omitted.
func (d QuotaDeps) quota(ctx context.Context, _ telegram.Request) (telegram.Response, error) {
	usage, usageErr := d.usage(ctx)
	today, todayErr := d.today(ctx)

	var b strings.Builder
	b.WriteString(telegram.Bold(quotaHeadline(usage, usageErr)) + "\n\n")
	b.WriteString(d.renderUsage(usage, usageErr))
	b.WriteString(d.renderAlerts(usage, usageErr))
	b.WriteString("\n" + telegram.Bold("Today") + "\n")
	b.WriteString(d.renderToday(today, todayErr))
	return telegram.Response{Body: b.String()}, nil
}

func (d QuotaDeps) usage(ctx context.Context) (provider.Usage, error) {
	if d.Quota == nil {
		return provider.Usage{}, errNoSource
	}
	scoped, cancel := d.scope(ctx)
	defer cancel()
	return d.Quota.Usage(scoped)
}

func (d QuotaDeps) today(ctx context.Context) (provider.Today, error) {
	if d.Quota == nil {
		return provider.Today{}, errNoSource
	}
	scoped, cancel := d.scope(ctx)
	defer cancel()
	return d.Quota.Today(scoped)
}

// quotaHeadline derives the leading glyph deterministically. A live throttle or
// a provider at the warning threshold is red: both mean ingestion is about to
// stop or already has. Unreadable or stale counters are amber, never green —
// unknown is not healthy.
func quotaHeadline(usage provider.Usage, err error) string {
	if err != nil {
		return "🟡 Provider quota — counters " + unavailable
	}
	if throttled := usage.Throttled(); len(throttled) > 0 {
		return "🔴 Provider quota — " + quotaName(throttled[0]) + " throttled"
	}
	if pressured := usage.Pressured(); len(pressured) > 0 {
		worst := pressured[0]
		for _, p := range pressured[1:] {
			if p.DailyPercent > worst.DailyPercent {
				worst = p
			}
		}
		return fmt.Sprintf("🔴 Provider quota — %s at %s of its daily limit",
			quotaName(worst), pct(worst.DailyPercent))
	}
	if len(usage.Providers) == 0 {
		return "🟡 Provider quota — no counters recorded"
	}
	for _, p := range usage.Providers {
		if p.Stale {
			return "🟡 Provider quota — counters not live"
		}
	}
	return "🟢 Provider quota — headroom"
}

// renderUsage is the per-provider table. The daily column carries both the
// derived usage and its percentage, because either alone is easy to misread.
func (d QuotaDeps) renderUsage(usage provider.Usage, err error) string {
	if err != nil {
		return telegram.PreTable([][]string{{"Provider quota", unavailable}}) + "\n"
	}
	if len(usage.Providers) == 0 {
		return "No provider counters recorded.\n"
	}

	rows := make([][]string, 0, len(usage.Providers)+1)
	rows = append(rows, []string{"provider", "daily", "minute", "age", ""})
	for i, p := range usage.Providers {
		if i >= quotaCap {
			break
		}
		rows = append(rows, []string{
			truncateCell(quotaName(p), quotaNameCap),
			quotaDaily(p),
			quotaWindow(p.MinuteUsed, p.MinuteLimit, p.MinuteKnown),
			age(p.Age),
			quotaMark(p),
		})
	}
	body := telegram.PreTable(rows) + "\n"
	if usage.Truncated || len(usage.Providers) > quotaCap {
		body += "(more providers not shown)\n"
	}
	return body
}

// quotaDaily renders the daily window. Used is derived — the provider reports
// what is left, not what was spent — so when the ceiling is unknown there is no
// honest number to print and the cell says so.
func quotaDaily(p provider.Quota) string {
	if !p.DailyKnown {
		return quotaUnknown
	}
	return fmt.Sprintf("%d/%d %s", p.DailyUsed, p.DailyLimit, pct(p.DailyPercent))
}

func quotaWindow(used, limit int64, known bool) string {
	if !known {
		return quotaUnknown
	}
	return fmt.Sprintf("%d/%d", used, limit)
}

// quotaMark is the last column, so its emoji cannot skew the table's alignment.
func quotaMark(p provider.Quota) string {
	switch {
	case p.Throttled:
		return "⛔ throttled"
	case p.UnderPressure():
		return "⚠️ low"
	case !p.DailyKnown:
		return "? no limit"
	case p.Stale:
		return "🕒 stale"
	case !p.Enabled:
		return "disabled"
	default:
		return "ok"
	}
}

// renderAlerts spells out the conditions the table can only hint at. A throttle
// and a near-exhausted allowance each get their own line: this is the thing the
// command exists to make impossible to miss.
func (d QuotaDeps) renderAlerts(usage provider.Usage, err error) string {
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range usage.Throttled() {
		b.WriteString(fmt.Sprintf("\n⛔ %s throttled for %s (until %s)",
			telegram.Esc(quotaName(p)), telegram.Esc(age(p.ThrottleRemaining)),
			telegram.Esc(p.ThrottledUntil.In(d.location()).Format("15:04 MST"))))
	}
	for _, p := range usage.Pressured() {
		b.WriteString(fmt.Sprintf("\n⚠️ %s at %s of its daily limit — %d call(s) left, reserve %d",
			telegram.Esc(quotaName(p)), telegram.Esc(pct(p.DailyPercent)),
			p.DailyRemaining, p.DailyReserve))
	}
	// Staleness is reported after the pressure lines and never instead of
	// them: counters that are both stale and near the ceiling are the worst
	// case, not a reason to soften either statement.
	for _, p := range usage.Providers {
		if p.Stale {
			b.WriteString(fmt.Sprintf("\n🕒 %s counters are %s old — not live",
				telegram.Esc(quotaName(p)), telegram.Esc(age(p.Age))))
		}
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	return b.String()
}

// renderToday is the day's request history. Its source is separate from the
// counters, so it degrades separately.
func (d QuotaDeps) renderToday(today provider.Today, err error) string {
	if err != nil {
		return telegram.PreTable([][]string{{"Request log", unavailable}})
	}
	if len(today.Providers) == 0 {
		return "No provider requests recorded since midnight UTC."
	}

	rows := make([][]string, 0, len(today.Providers)+1)
	rows = append(rows, []string{"provider", "req", "err", "cost"})
	for i, p := range today.Providers {
		if i >= quotaCap {
			break
		}
		rows = append(rows, []string{
			truncateCell(p.Provider, quotaNameCap),
			fmt.Sprintf("%d", p.Requests),
			fmt.Sprintf("%d", p.Errors),
			fmt.Sprintf("%d", p.QuotaCost),
		})
	}
	body := telegram.PreTable(rows)

	for i, p := range today.Providers {
		if i >= quotaCap {
			break
		}
		if line := quotaCategories(p); line != "" {
			body += "\n" + telegram.Esc(truncateCell(p.Provider, quotaNameCap)) + " errors: " + line
		}
	}
	if today.CategoriesTruncated {
		body += "\n(error breakdown truncated)"
	}
	return body
}

// quotaCategories names the day's error buckets for one provider. An empty
// result means no errors were categorised, which the table's err column already
// reports; it is not a missing source and must not render as "unavailable".
func quotaCategories(p provider.ProviderDay) string {
	if len(p.Categories) == 0 {
		return ""
	}
	parts := make([]string, 0, quotaCategoryCap)
	for i, c := range p.Categories {
		if i >= quotaCategoryCap {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, fmt.Sprintf("%s %d", telegram.Esc(c.Category), c.Count))
	}
	return strings.Join(parts, " · ")
}

// quotaName prefers the human display name and falls back to the provider key.
func quotaName(p provider.Quota) string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.Provider
}

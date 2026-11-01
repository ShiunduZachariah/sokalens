package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/useraccounts"
)

// ---------------------------------------------------------------------------
// /users — how many people are logged in, how many signed up today, how many
// there are in total.
//
// # Counts only. Never identities.
//
// This command reports aggregates and nothing else. It must never render an
// email address, a name, a public_id, an IP hash, a device name or a user
// agent, and internal/useraccounts is built so those columns are never even
// selected. Telegram cloud chats are not end-to-end encrypted, spec §18
// forbids user PII in any message, and internal/evidence exists in this
// service because output from here can leak.
//
// The tempting next feature is "show me the last five signups". Do not add it.
// The operational question — is the product growing, is anybody using it right
// now — is fully answered by counts; a list answers the same question while
// exporting real people's identities into a third party's cloud, permanently.
// users_test.go asserts the rendered body contains no "@" as a standing guard.
//
// This file declares its own UserDeps and RegisterUsers rather than extending
// the Deps struct in commands.go, matching RunDeps/RegisterRun and
// ReleaseDeps/RegisterRelease: main wires each group separately, so a
// deployment can ship the rest of the read commands without granting this one
// a route into the product's account tables.
// ---------------------------------------------------------------------------

// UserDeps are the /users collaborators. Users may be nil: a missing source
// renders "unavailable" rather than failing or, worse, silently omitting the
// line. An omitted line reads as "fine" and it is not (§13).
type UserDeps struct {
	Users *useraccounts.Queries

	// Location renders the as-of stamp in the operator's zone.
	Location *time.Location
	// Now is injectable for tests.
	Now func() time.Time
	// QueryTimeout bounds the read so one slow aggregate cannot hold the
	// poller's update-handling path open.
	QueryTimeout time.Duration
}

// usersStatusCap bounds the rendered status breakdown. The package returns at
// most useraccounts.MaxStatusRows; a phone screen wants fewer.
const usersStatusCap = 6

func (d UserDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d UserDeps) location() *time.Location {
	if d.Location != nil {
		return d.Location
	}
	return time.UTC
}

func (d UserDeps) scope(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := d.QueryTimeout
	if timeout <= 0 {
		timeout = defaultQueryTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// RegisterUsers adds /users to the registry.
//
// It is separate from Register in commands.go so main can wire the account
// projection independently of the ops projections.
func RegisterUsers(registry *telegram.Registry, deps UserDeps) error {
	if registry == nil {
		return errors.New("commands: registry is required")
	}
	if err := registry.Register(telegram.Registration{
		Name:    "users",
		Summary: "Users logged in now, new in 24h, and the account total.",
		// Read-only: this issues SELECTs and nothing else.
		Mutating: false,
		Handler:  telegram.HandlerFunc(deps.users),
	}); err != nil {
		return fmt.Errorf("register /users: %w", err)
	}
	return nil
}

// users renders the three sections the owner asked for. Each degrades on its
// own: a section whose source is missing or failed renders "unavailable", and
// is never dropped.
func (d UserDeps) users(ctx context.Context, _ telegram.Request) (telegram.Response, error) {
	var stats useraccounts.Stats
	if d.Users != nil {
		scoped, cancel := d.scope(ctx)
		defer cancel()
		// The error is deliberately not propagated: Stats reports partial
		// failure through the per-section Available flags, and a section that
		// failed renders "unavailable" beside the sections that worked.
		stats, _ = d.Users.Stats(scoped)
	}

	rows := make([][]string, 0, 6)
	rows = append(rows, d.presenceRows(stats.Sessions)...)
	rows = append(rows, d.accountRows(stats.Accounts)...)

	var b strings.Builder
	b.WriteString(telegram.Bold("Users") + "\n\n")
	b.WriteString(telegram.PreTable(rows))
	b.WriteString(d.statusSection(stats.Statuses))

	asOf := stats.AsOf
	if asOf.IsZero() {
		asOf = d.now()
	}
	b.WriteString("\nas of " + telegram.Esc(asOf.In(d.location()).Format("15:04 MST")))
	return telegram.Response{Body: b.String()}, nil
}

// presenceRows renders the two different things "online" can mean, labelled
// distinctly. Reporting only one of them, under the word "online", would be a
// lie in whichever direction that one was wrong: a valid 30-day session is not
// somebody at the keyboard, and a person who stepped away for twenty minutes
// has not logged out.
func (d UserDeps) presenceRows(s useraccounts.Sessions) [][]string {
	if !s.Available {
		// The labels are identical to the available case, so a degraded
		// screen reads as the same two questions with no answer rather than
		// as a different, shorter report.
		return [][]string{
			{"logged in", unavailable},
			{"active <" + usersWindowLabel(useraccounts.PresenceWindow), unavailable},
		}
	}
	loggedIn := usersCount(s.LiveSessionUsers, "user")
	// Sessions are labelled as sessions. One person with a phone and a laptop
	// is one user; reporting session rows as people inflates the figure.
	loggedIn += fmt.Sprintf(" · %d sessions", s.LiveSessions)
	return [][]string{
		{"logged in", loggedIn},
		{"active <" + usersWindowLabel(s.PresenceWindow), usersCount(s.PresentUsers, "user")},
	}
}

// accountRows renders the population. The soft-deleted count is shown rather
// than silently dropped from the total, so a purge cannot be mistaken for
// churn or hidden entirely.
func (d UserDeps) accountRows(a useraccounts.Accounts) [][]string {
	if !a.Available {
		return [][]string{
			{"new 24h", unavailable},
			{"total", unavailable},
		}
	}
	rows := [][]string{
		{"new " + usersWindowLabel(a.NewWindow), fmt.Sprintf("%d", a.New)},
		{"total", fmt.Sprintf("%d", a.Total)},
	}
	if a.SoftDeleted > 0 {
		rows = append(rows, []string{"deleted", fmt.Sprintf("%d (excluded)", a.SoftDeleted)})
	}
	return rows
}

// statusSection breaks the total down by account_status, so a pile of
// unverified signups is visible as something other than growth.
func (d UserDeps) statusSection(b useraccounts.Breakdown) string {
	head := "\n" + telegram.Bold("By account status") + "\n\n"
	if !b.Available {
		return head + telegram.PreTable([][]string{{"breakdown", unavailable}})
	}
	if len(b.Counts) == 0 {
		return head + telegram.PreTable([][]string{{"no accounts", "0"}})
	}

	counts := b.Counts
	hidden := 0
	if len(counts) > usersStatusCap {
		hidden = len(counts) - usersStatusCap
		counts = counts[:usersStatusCap]
	}

	total := int64(0)
	for _, c := range b.Counts {
		total += c.Count
	}

	rows := make([][]string, 0, len(counts))
	for _, c := range counts {
		share := ""
		if total > 0 {
			share = pct(float64(c.Count) / float64(total) * 100)
		}
		rows = append(rows, []string{usersSafeLabel(c.Status), fmt.Sprintf("%d", c.Count), share})
	}
	out := head + telegram.PreTable(rows)
	if hidden > 0 || b.Truncated {
		out += "\n(further statuses not shown)"
	}
	return out
}

// usersSafeLabel bounds a database-sourced status label and strips "@".
//
// account_status is a lifecycle enum, so this should never do anything. It
// exists because this is the only value on this screen that comes out of the
// database as text rather than as a number, which makes it the only way an
// address could ever reach a chat from here — whether through a schema change,
// a mis-joined column, or someone widening the query later. Removing the one
// character that makes a string an address costs nothing and makes the no-PII
// guarantee structural rather than a matter of everyone remembering.
func usersSafeLabel(s string) string {
	s = strings.ReplaceAll(s, "@", "")
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	return truncateCell(s, 24)
}

// usersWindowLabel renders a reporting window. age() collapses 24h to "1d";
// the owner asked for the past 24 hours and the line should say what was
// measured.
func usersWindowLabel(d time.Duration) string {
	if d >= time.Hour && d < 48*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return age(d)
}

// usersCount renders a count with a correctly pluralised unit.
func usersCount(n int64, unit string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

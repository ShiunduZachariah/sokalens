package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/useraccounts"
)

// The command is exercised through a fake useraccounts.Queryer so the render
// path runs end to end without a database.

var usersClock = time.Date(2026, 9, 5, 14, 32, 0, 0, time.UTC)

type usersRows struct {
	values [][]any
	index  int
}

func (r *usersRows) Next() bool {
	if r.index >= len(r.values) {
		return false
	}
	r.index++
	return true
}

func (r *usersRows) Scan(dest ...any) error {
	values := r.values[r.index-1]
	if len(dest) != len(values) {
		return fmt.Errorf("scan arity: got %d dest for %d values", len(dest), len(values))
	}
	for i, value := range values {
		switch target := dest[i].(type) {
		case *string:
			*target = value.(string)
		case *int64:
			*target = value.(int64)
		default:
			return fmt.Errorf("unsupported scan dest %T", dest[i])
		}
	}
	return nil
}

func (r *usersRows) Err() error   { return nil }
func (r *usersRows) Close() error { return nil }

// usersDB answers by statement shape: the aggregate reads are told apart from
// the grouped breakdown by the GROUP BY, so the fake needs no copy of the SQL.
type usersDB struct {
	sessions []any
	accounts []any
	statuses [][]any
	failAll  bool
}

func (d *usersDB) QueryContext(_ context.Context, query string, _ ...any) (useraccounts.Rows, error) {
	if d.failAll {
		return nil, errors.New("connection reset")
	}
	switch {
	case strings.Contains(query, "GROUP BY"):
		return &usersRows{values: d.statuses}, nil
	case strings.Contains(query, "user_sessions"):
		if d.sessions == nil {
			return &usersRows{}, nil
		}
		return &usersRows{values: [][]any{d.sessions}}, nil
	default:
		if d.accounts == nil {
			return &usersRows{}, nil
		}
		return &usersRows{values: [][]any{d.accounts}}, nil
	}
}

func populatedUsersDB() *usersDB {
	return &usersDB{
		// 7 people logged in across 11 sessions, 3 of them seen just now.
		sessions: []any{int64(7), int64(11), int64(3)},
		// 1204 live accounts, 9 soft-deleted, 23 new in the last day.
		accounts: []any{int64(1204), int64(9), int64(23)},
		statuses: [][]any{
			{"active", int64(1100)},
			{"pending_verification", int64(96)},
			{"deletion_pending", int64(8)},
		},
	}
}

func testUserDeps(t *testing.T, db useraccounts.Queryer) UserDeps {
	t.Helper()
	deps := UserDeps{Now: func() time.Time { return usersClock }, Location: time.UTC}
	if db != nil {
		queries, err := useraccounts.NewQueries(db, deps.Now)
		if err != nil {
			t.Fatalf("NewQueries: %v", err)
		}
		deps.Users = queries
	}
	return deps
}

func renderUsers(t *testing.T, deps UserDeps) string {
	t.Helper()
	resp, err := deps.users(context.Background(), telegram.Request{})
	if err != nil {
		t.Fatalf("users: %v", err)
	}
	if resp.Silent {
		t.Fatal("/users must never reply silently")
	}
	if strings.TrimSpace(resp.Body) == "" {
		t.Fatal("/users must never reply with an empty body")
	}
	return resp.Body
}

// TestUsersRendersNoAtSignEverIsTheStandingGuard.
//
// An email address is the one thing this command must never emit, and every
// email contains "@". The assertion is on the rendered body rather than on the
// query, so it keeps holding if someone later adds a field, a footer or a
// label that carries user text. The database here is hostile on purpose: the
// status column, the only free-text value on the screen, arrives holding an
// address.
func TestUsersRendersNoAtSignEverIsTheStandingGuard(t *testing.T) {
	db := populatedUsersDB()
	db.statuses = append(db.statuses, []any{"owner@example.com", int64(1)})

	for name, deps := range map[string]UserDeps{
		"populated":  testUserDeps(t, db),
		"unwired":    testUserDeps(t, nil),
		"failing":    testUserDeps(t, &usersDB{failAll: true}),
		"emptyStore": testUserDeps(t, &usersDB{sessions: []any{int64(0), int64(0), int64(0)}, accounts: []any{int64(0), int64(0), int64(0)}}),
		"statusOnly": testUserDeps(t, &usersDB{statuses: [][]any{{"a@b.c", int64(2)}}}),
		"noSoftDeletes": testUserDeps(t, &usersDB{
			sessions: []any{int64(1), int64(1), int64(1)},
			accounts: []any{int64(5), int64(0), int64(5)},
		}),
	} {
		t.Run(name, func(t *testing.T) {
			body := renderUsers(t, deps)
			if strings.Contains(body, "@") {
				t.Fatalf("an email must never reach a chat; body contained @:\n%s", body)
			}
		})
	}
}

// TestUsersDistinguishesLoggedInFromActiveNow. Conflating a valid session with
// a person at the keyboard is the easy way to make this number a lie, so both
// figures must appear with distinct labels.
func TestUsersDistinguishesLoggedInFromActiveNow(t *testing.T) {
	body := renderUsers(t, testUserDeps(t, populatedUsersDB()))

	if !strings.Contains(body, "logged in") {
		t.Fatalf("the logged-in figure is missing:\n%s", body)
	}
	if !strings.Contains(body, "active &lt;15m") {
		t.Fatalf("the presence figure must be labelled with its window:\n%s", body)
	}
	if !strings.Contains(body, "7 users") {
		t.Fatalf("logged-in users not rendered:\n%s", body)
	}
	if !strings.Contains(body, "3 users") {
		t.Fatalf("present users not rendered:\n%s", body)
	}
	// Sessions appear, but only ever labelled as sessions.
	if !strings.Contains(body, "11 sessions") {
		t.Fatalf("session count must be visible next to the user count:\n%s", body)
	}
	if strings.Contains(body, "11 users") {
		t.Fatalf("sessions must never be reported as users:\n%s", body)
	}
}

// TestUsersReportsNewTotalAndTheSoftDeletedExclusion.
func TestUsersReportsNewTotalAndTheSoftDeletedExclusion(t *testing.T) {
	body := renderUsers(t, testUserDeps(t, populatedUsersDB()))

	for _, want := range []string{"new 24h", "23", "total", "1204"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q:\n%s", want, body)
		}
	}
	// The accounts left out of the total are reported, not silently dropped.
	if !strings.Contains(body, "9 (excluded)") {
		t.Fatalf("soft-deleted accounts must be reported separately:\n%s", body)
	}
	// The status breakdown makes unverified signups visible as something
	// other than growth.
	if !strings.Contains(body, "pending_verification") || !strings.Contains(body, "96") {
		t.Fatalf("account status breakdown missing:\n%s", body)
	}
}

// TestUsersRepliesWhenDepsAreNil: a missing source must say so. A command that
// answers nothing reads as "fine", and a nil dependency must never panic into
// the poller.
func TestUsersRepliesWhenDepsAreNil(t *testing.T) {
	body := renderUsers(t, testUserDeps(t, nil))
	if strings.Count(body, unavailable) < 3 {
		t.Fatalf("every section must degrade to %q, not be omitted:\n%s", unavailable, body)
	}
	for _, want := range []string{"logged in", "active &lt;", "new 24h", "total"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the %q line must still be present when unavailable:\n%s", want, body)
		}
	}
	// A zero must never be printed for a section that was never read.
	if strings.Contains(body, "0 users") {
		t.Fatalf("an unread section must not render as zero users:\n%s", body)
	}

	// A zero-value UserDeps has no clock and no location either.
	resp, err := UserDeps{}.users(context.Background(), telegram.Request{})
	if err != nil || resp.Silent || strings.TrimSpace(resp.Body) == "" {
		t.Fatalf("a zero-value UserDeps must still reply: resp=%+v err=%v", resp, err)
	}
}

// TestUsersDegradesPerSectionWhenTheReadFails.
func TestUsersDegradesPerSectionWhenTheReadFails(t *testing.T) {
	body := renderUsers(t, testUserDeps(t, &usersDB{failAll: true}))
	if !strings.Contains(body, unavailable) {
		t.Fatalf("a failed read must render %q:\n%s", unavailable, body)
	}
	if strings.Contains(body, "0 users") || strings.Contains(body, "\ntotal  0") {
		t.Fatalf("a failed read must not be rendered as zero:\n%s", body)
	}
}

// TestUsersOnAnEmptyDatabaseRendersZeros. Zero users is a real answer and must
// not be confused with "unavailable".
func TestUsersOnAnEmptyDatabaseRendersZeros(t *testing.T) {
	deps := testUserDeps(t, &usersDB{
		sessions: []any{int64(0), int64(0), int64(0)},
		accounts: []any{int64(0), int64(0), int64(0)},
	})
	body := renderUsers(t, deps)
	if strings.Contains(body, unavailable) {
		t.Fatalf("an empty database is zeros, not unavailable:\n%s", body)
	}
	if !strings.Contains(body, "0 users") {
		t.Fatalf("zero users must be rendered:\n%s", body)
	}
	if !strings.Contains(body, "no accounts") {
		t.Fatalf("an empty status breakdown must say so:\n%s", body)
	}
	// Nothing was excluded, so no exclusion line.
	if strings.Contains(body, "excluded") {
		t.Fatalf("nothing was excluded:\n%s", body)
	}
}

// TestUsersRendersHouseStyle: bold heading and monospace tables, as the other
// read commands do, plus the as-of stamp in the operator's zone.
func TestUsersRendersHouseStyle(t *testing.T) {
	body := renderUsers(t, testUserDeps(t, populatedUsersDB()))
	if !strings.HasPrefix(body, telegram.Bold("Users")) {
		t.Fatalf("body must open with the bold heading:\n%s", body)
	}
	if !strings.Contains(body, "<pre>") {
		t.Fatalf("figures must be rendered in a monospace table:\n%s", body)
	}
	if !strings.Contains(body, "as of 14:32 UTC") {
		t.Fatalf("a point-in-time count must carry its as-of stamp:\n%s", body)
	}
	// Every part must be independently sendable.
	if parts := telegram.Chunk(body); len(parts) != 1 {
		t.Fatalf("the reply should fit one message, got %d parts", len(parts))
	}
}

func TestRegisterUsersAddsTheCommand(t *testing.T) {
	if err := RegisterUsers(nil, UserDeps{}); err == nil {
		t.Fatal("a nil registry must be rejected")
	}
	registry := telegram.NewRegistry()
	if err := RegisterUsers(registry, testUserDeps(t, populatedUsersDB())); err != nil {
		t.Fatalf("RegisterUsers: %v", err)
	}
	reg, ok := registry.Lookup("users")
	if !ok {
		t.Fatal("/users was not registered")
	}
	if reg.Mutating {
		t.Fatal("/users is read-only and must not be marked mutating")
	}
	if err := RegisterUsers(registry, UserDeps{}); err == nil {
		t.Fatal("registering /users twice must fail")
	}
}

// TestUsersSafeLabelStripsAddressShapedText documents the last line of defence
// on the one free-text value this screen renders.
func TestUsersSafeLabelStripsAddressShapedText(t *testing.T) {
	for input, want := range map[string]string{
		"active":            "active",
		"owner@example.com": "ownerexample.com",
		"@":                 "unknown",
		"":                  "unknown",
		"   ":               "unknown",
	} {
		if got := usersSafeLabel(input); got != want {
			t.Fatalf("usersSafeLabel(%q)=%q want %q", input, got, want)
		}
	}
	if got := usersSafeLabel(strings.Repeat("x", 64)); len([]rune(got)) > 24 {
		t.Fatalf("a long label must be bounded, got %d runes", len([]rune(got)))
	}
}

package fixtureevents

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
	_ "modernc.org/sqlite"
)

// The column list mirrors migration 000070 as applied. It is SQLite rather than
// PostgreSQL because what is under test here is the writer's idempotency, not
// partitioning -- that is covered against a real PostgreSQL by
// snapshots.TestSnapshotsAgainstPostgres.
func eventDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE fixture_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		fixture_id INTEGER NOT NULL,
		provider TEXT NOT NULL,
		observed_at TIMESTAMP NOT NULL,
		elapsed_minute INTEGER NOT NULL,
		elapsed_extra INTEGER,
		event_type TEXT NOT NULL,
		event_detail TEXT NOT NULL,
		team_id INTEGER,
		player_id INTEGER,
		player_name TEXT,
		assist_id INTEGER,
		assist_name TEXT,
		comments TEXT,
		identity_key TEXT NOT NULL,
		is_retracted BOOLEAN NOT NULL DEFAULT false,
		retracted_at TIMESTAMP
	)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func ptr[T any](value T) *T { return &value }

func eventRowCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM fixture_events`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	return count
}

func TestReconcileScoringEventsRetiresSupersededOwnGoal(t *testing.T) {
	db := eventDB(t)
	repository := NewRepository(db)
	at := time.Date(2026, 9, 23, 10, 6, 0, 0, time.UTC)
	// The Lesotho-Angola feed briefly added an own goal at 6' beside the
	// actual 6' Angola goal. The next complete list had only four goals and
	// the authoritative score remained 0-4.
	current := []apifootball.FixtureEvent{
		{ElapsedMinute: 6, Type: "Goal", Detail: "Normal Goal", Kind: apifootball.EventKindGoal, TeamID: ptr[int64](202), IdentityKey: "jairo"},
		{ElapsedMinute: 13, Type: "Goal", Detail: "Normal Goal", Kind: apifootball.EventKindGoal, TeamID: ptr[int64](202), IdentityKey: "panda-1"},
		{ElapsedMinute: 19, Type: "Goal", Detail: "Penalty", Kind: apifootball.EventKindPenaltyGoal, TeamID: ptr[int64](202), IdentityKey: "panda-2"},
		{ElapsedMinute: 37, Type: "Goal", Detail: "Normal Goal", Kind: apifootball.EventKindGoal, TeamID: ptr[int64](202), IdentityKey: "romano"},
	}
	stale := apifootball.FixtureEvent{ElapsedMinute: 6, Type: "Goal", Detail: "Own Goal", Kind: apifootball.EventKindOwnGoal, IdentityKey: "ntsenko"}
	for _, item := range append(append([]apifootball.FixtureEvent{}, current...), stale) {
		if _, err := repository.Append(context.Background(), 4509918, "api-football", at, item); err != nil {
			t.Fatal(err)
		}
	}
	home, away := 0, 4
	changed, err := repository.ReconcileScoringEvents(context.Background(), 4509918, "api-football", at.Add(4*time.Minute), current, 101, 202, &home, &away)
	if err != nil || changed != 1 {
		t.Fatalf("changed=%d err=%v, want one retired own goal", changed, err)
	}
	var retracted bool
	if err := db.QueryRow(`SELECT is_retracted FROM fixture_events WHERE identity_key='ntsenko'`).Scan(&retracted); err != nil || !retracted {
		t.Fatalf("stale own goal retracted=%v err=%v", retracted, err)
	}
	if got := eventRowCount(t, db); got != 5 {
		t.Fatalf("reconciliation deleted audit history: %d rows", got)
	}
	// A thin response cannot remove any other goal, even if one is absent.
	changed, err = repository.ReconcileScoringEvents(context.Background(), 4509918, "api-football", at.Add(5*time.Minute), current[:3], 101, 202, &home, &away)
	if err != nil || changed != 0 {
		t.Fatalf("incomplete list changed=%d err=%v", changed, err)
	}
	var romanoRetracted bool
	if err := db.QueryRow(`SELECT is_retracted FROM fixture_events WHERE identity_key='romano'`).Scan(&romanoRetracted); err != nil || romanoRetracted {
		t.Fatalf("valid goal retracted=%v err=%v", romanoRetracted, err)
	}
}

func TestReconcileScoringEventsKeepsRealSameMinuteGoalsAndRestoresReappearingGoal(t *testing.T) {
	db := eventDB(t)
	repository := NewRepository(db)
	at := time.Date(2026, 9, 23, 10, 6, 0, 0, time.UTC)
	one := apifootball.FixtureEvent{ElapsedMinute: 6, Type: "Goal", Detail: "Normal Goal", Kind: apifootball.EventKindGoal, TeamID: ptr[int64](101), IdentityKey: "first"}
	two := apifootball.FixtureEvent{ElapsedMinute: 6, Type: "Goal", Detail: "Normal Goal", Kind: apifootball.EventKindGoal, TeamID: ptr[int64](202), IdentityKey: "second"}
	for _, item := range []apifootball.FixtureEvent{one, two} {
		if _, err := repository.Append(context.Background(), 1, "api-football", at, item); err != nil {
			t.Fatal(err)
		}
	}
	home, away := 1, 1
	changed, err := repository.ReconcileScoringEvents(context.Background(), 1, "api-football", at.Add(time.Minute), []apifootball.FixtureEvent{one, two}, 101, 202, &home, &away)
	if err != nil || changed != 0 {
		t.Fatalf("same-minute goals changed=%d err=%v", changed, err)
	}
	away = 0
	if _, err := repository.ReconcileScoringEvents(context.Background(), 1, "api-football", at.Add(2*time.Minute), []apifootball.FixtureEvent{one}, 101, 202, &home, &away); err != nil {
		t.Fatal(err)
	}
	away = 1
	changed, err = repository.ReconcileScoringEvents(context.Background(), 1, "api-football", at.Add(3*time.Minute), []apifootball.FixtureEvent{one, two}, 101, 202, &home, &away)
	if err != nil || changed != 1 {
		t.Fatalf("reappearing goal changed=%d err=%v", changed, err)
	}
	var retracted bool
	if err := db.QueryRow(`SELECT is_retracted FROM fixture_events WHERE identity_key='second'`).Scan(&retracted); err != nil || retracted {
		t.Fatalf("reappearing goal retracted=%v err=%v", retracted, err)
	}
}

func TestReconcileScoringEventsRequiresGoalsForEachOfficialSide(t *testing.T) {
	db := eventDB(t)
	repository := NewRepository(db)
	at := time.Date(2026, 9, 23, 10, 6, 0, 0, time.UTC)
	home := apifootball.FixtureEvent{ElapsedMinute: 6, Type: "Goal", Detail: "Normal Goal", Kind: apifootball.EventKindGoal, TeamID: ptr[int64](101), IdentityKey: "home"}
	away := apifootball.FixtureEvent{ElapsedMinute: 20, Type: "Goal", Detail: "Normal Goal", Kind: apifootball.EventKindGoal, TeamID: ptr[int64](202), IdentityKey: "away"}
	wrong := apifootball.FixtureEvent{ElapsedMinute: 20, Type: "Goal", Detail: "Normal Goal", Kind: apifootball.EventKindGoal, TeamID: ptr[int64](101), IdentityKey: "wrong-home"}
	for _, item := range []apifootball.FixtureEvent{home, away, wrong} {
		if _, err := repository.Append(context.Background(), 1, "api-football", at, item); err != nil {
			t.Fatal(err)
		}
	}
	one := 1
	changed, err := repository.ReconcileScoringEvents(context.Background(), 1, "api-football", at.Add(time.Minute), []apifootball.FixtureEvent{home, wrong}, 101, 202, &one, &one)
	if err != nil || changed != 0 {
		t.Fatalf("wrong-side feed changed=%d err=%v", changed, err)
	}
	changed, err = repository.ReconcileScoringEvents(context.Background(), 1, "api-football", at.Add(time.Minute), nil, 101, 202, &one, &one)
	if err != nil || changed != 0 {
		t.Fatalf("missing event list changed=%d err=%v", changed, err)
	}
	var retracted bool
	if err := db.QueryRow(`SELECT is_retracted FROM fixture_events WHERE identity_key='away'`).Scan(&retracted); err != nil || retracted {
		t.Fatalf("valid away goal retracted=%v err=%v", retracted, err)
	}
}

func TestAppendPersistsTheProviderObservation(t *testing.T) {
	db := eventDB(t)
	at := time.Date(2026, 9, 17, 19, 45, 0, 0, time.UTC)
	event := apifootball.FixtureEvent{
		FixtureID: 999, ElapsedMinute: 90, ElapsedExtra: ptr(3),
		Type: "Goal", Detail: "Penalty", Kind: apifootball.EventKindPenaltyGoal,
		TeamID: ptr[int64](42), PlayerID: ptr[int64](7), PlayerName: "Scorer",
		AssistID: ptr[int64](8), AssistName: "Assist", Comments: "confirmed",
		IdentityKey: "stable-provider-identity",
	}

	written, err := NewRepository(db).Append(context.Background(), 1234, "api-football", at, event)
	if err != nil || !written {
		t.Fatalf("Append written=%v err=%v", written, err)
	}

	var (
		fixtureID, extra, teamID, playerID, assistID          int64
		provider, eventType, detail, player, assist, comments string
		retracted                                             bool
	)
	if err := db.QueryRow(`SELECT fixture_id, provider, elapsed_extra, event_type, event_detail,
		team_id, player_id, player_name, assist_id, assist_name, comments, is_retracted
		FROM fixture_events`).Scan(&fixtureID, &provider, &extra, &eventType, &detail,
		&teamID, &playerID, &player, &assistID, &assist, &comments, &retracted); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if fixtureID != 1234 || provider != "api-football" || extra != 3 {
		t.Fatalf("identity/clock = %d %q +%d", fixtureID, provider, extra)
	}
	if eventType != "Goal" || detail != "Penalty" {
		t.Fatalf("event meaning = %q/%q", eventType, detail)
	}
	// The provider's team id, unchanged. The read path resolves the side from
	// it through provider_entity_maps; a canonical id here matches nothing and
	// costs every event its side, silently.
	if teamID != 42 {
		t.Fatalf("team_id = %d, want the provider's 42", teamID)
	}
	if playerID != 7 || player != "Scorer" || assistID != 8 || assist != "Assist" || comments != "confirmed" {
		t.Fatalf("participants = player=%d/%q assist=%d/%q comments=%q", playerID, player, assistID, assist, comments)
	}
	if retracted {
		t.Fatal("ordinary goal was marked retracted")
	}
}

func TestAppendSuppressesARepeatedIdentity(t *testing.T) {
	db := eventDB(t)
	repository := NewRepository(db)
	at := time.Date(2026, 9, 17, 19, 45, 0, 0, time.UTC)
	event := apifootball.FixtureEvent{FixtureID: 999, ElapsedMinute: 12, Type: "Goal", Detail: "Normal Goal", Kind: apifootball.EventKindGoal, IdentityKey: "same"}

	written, err := repository.Append(context.Background(), 1234, "api-football", at, event)
	if err != nil || !written {
		t.Fatalf("first append written=%v err=%v", written, err)
	}
	// Assist is deliberately excluded from identity. Filling it in later must
	// not turn one goal into two timeline entries -- and it must actually
	// arrive, which is the half this test used to miss.
	//
	// The provider publishes an event the moment it happens and completes it
	// afterwards: a goal appears with no scorer and gains the name and assist
	// seconds later. Discarding that second observation as a duplicate would
	// leave the goal permanently unattributed, which is the mirror of showing
	// it twice and just as wrong. So a repeat that carries new detail is a
	// write, and `written` says so.
	event.AssistID = ptr[int64](88)
	event.AssistName = "Late assist"
	written, err = repository.Append(context.Background(), 1234, "api-football", at.Add(time.Minute), event)
	if err != nil || !written {
		t.Fatalf("enriching repeat written=%v err=%v; the assist was dropped", written, err)
	}
	if got := eventRowCount(t, db); got != 1 {
		t.Fatalf("rows=%d, want 1 -- enrichment created a second timeline entry", got)
	}
	var storedAssist string
	if err := db.QueryRow(`SELECT COALESCE(assist_name, '') FROM fixture_events WHERE identity_key = 'same'`).Scan(&storedAssist); err != nil {
		t.Fatalf("read merged row: %v", err)
	}
	if storedAssist != "Late assist" {
		t.Fatalf("assist_name = %q, want the enriched value merged in", storedAssist)
	}

	// A third poll with nothing new must not count as a write. The provider
	// returns the whole event list every time, so most polls are repeats and
	// treating them as writes would make the metric meaningless.
	written, err = repository.Append(context.Background(), 1234, "api-football", at.Add(2*time.Minute), event)
	if err != nil || written {
		t.Fatalf("unchanged repeat written=%v err=%v", written, err)
	}
	if got := eventRowCount(t, db); got != 1 {
		t.Fatalf("rows=%d after an unchanged repeat, want 1", got)
	}
}

func TestAppendKeepsDistinctEventsAndRetractions(t *testing.T) {
	db := eventDB(t)
	repository := NewRepository(db)
	at := time.Date(2026, 9, 17, 19, 45, 0, 0, time.UTC)
	first := apifootball.FixtureEvent{ElapsedMinute: 25, Type: "Goal", Detail: "Normal Goal", Kind: apifootball.EventKindGoal, IdentityKey: "goal"}
	second := apifootball.FixtureEvent{ElapsedMinute: 26, Type: "Var", Detail: "Goal cancelled", Kind: apifootball.EventKindVarRetract, IdentityKey: "var", IsRetraction: true}
	for _, event := range []apifootball.FixtureEvent{first, second} {
		written, err := repository.Append(context.Background(), 1234, "api-football", at, event)
		if err != nil || !written {
			t.Fatalf("append %s written=%v err=%v", event.IdentityKey, written, err)
		}
	}
	if got := eventRowCount(t, db); got != 2 {
		t.Fatalf("rows=%d, want 2", got)
	}
	var retracted bool
	var retractedAt sql.NullTime
	if err := db.QueryRow(`SELECT is_retracted, retracted_at FROM fixture_events WHERE identity_key='var'`).Scan(&retracted, &retractedAt); err != nil {
		t.Fatalf("read retraction: %v", err)
	}
	if !retracted || !retractedAt.Valid || !retractedAt.Time.Equal(at) {
		t.Fatalf("retraction=%v at=%v", retracted, retractedAt)
	}
	// The goal it withdrew keeps its own row unflagged: nothing in the provider
	// payload says which earlier event a Var entry cancels, so the cancellation
	// is carried by the Var entry rather than guessed onto a goal.
	var goalRetracted bool
	if err := db.QueryRow(`SELECT is_retracted FROM fixture_events WHERE identity_key='goal'`).Scan(&goalRetracted); err != nil {
		t.Fatalf("read goal: %v", err)
	}
	if goalRetracted {
		t.Fatal("a Var event flagged an earlier goal it was never linked to")
	}
}

func TestAppendValidatesRequiredIdentity(t *testing.T) {
	db := eventDB(t)
	at := time.Date(2026, 9, 17, 19, 45, 0, 0, time.UTC)
	valid := apifootball.FixtureEvent{ElapsedMinute: 1, IdentityKey: "key"}
	tests := []struct {
		name       string
		repository Repository
		fixtureID  int64
		provider   string
		at         time.Time
		event      apifootball.FixtureEvent
	}{
		{"nil DB", Repository{}, 1, "p", at, valid},
		{"no fixture", NewRepository(db), 0, "p", at, valid},
		{"no provider", NewRepository(db), 1, "", at, valid},
		{"no time", NewRepository(db), 1, "p", time.Time{}, valid},
		{"negative minute", NewRepository(db), 1, "p", at, apifootball.FixtureEvent{ElapsedMinute: -1, IdentityKey: "key"}},
		{"no identity", NewRepository(db), 1, "p", at, apifootball.FixtureEvent{ElapsedMinute: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.repository.Append(context.Background(), tc.fixtureID, tc.provider, tc.at, tc.event); err == nil {
				t.Fatal("invalid event accepted")
			}
		})
	}
}

func TestIdentityLookupIsBoundedToLiveMatchWindow(t *testing.T) {
	db := eventDB(t)
	repository := NewRepository(db)
	at := time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC)
	event := apifootball.FixtureEvent{ElapsedMinute: 1, IdentityKey: "same"}
	if _, err := repository.Append(context.Background(), 1234, "api-football", at, event); err != nil {
		t.Fatalf("first append: %v", err)
	}
	written, err := repository.Append(context.Background(), 1234, "api-football", at.Add(25*time.Hour), event)
	if err != nil || !written {
		t.Fatalf("outside window written=%v err=%v", written, err)
	}
	if got := eventRowCount(t, db); got != 2 {
		t.Fatalf("rows=%d, want 2", got)
	}
}

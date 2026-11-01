package matches

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func disciplineDB(t *testing.T, extra ...string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	stmts := append([]string{
		"CREATE TABLE matches (id INTEGER PRIMARY KEY, home_team_id INTEGER, away_team_id INTEGER, kickoff_at TEXT NOT NULL, status TEXT NOT NULL);",
		"CREATE TABLE fixture_statistics (fixture_id INTEGER NOT NULL, team_id INTEGER NOT NULL, yellow_cards INTEGER, red_cards INTEGER, fouls INTEGER, shots_total INTEGER, corners INTEGER, card_points INTEGER, PRIMARY KEY (fixture_id, team_id));",
		// The fixture being previewed: team 1 hosts team 2 on 24 September.
		"INSERT INTO matches VALUES (100, 1, 2, '2026-09-24T18:00:00Z', 'scheduled');",
	}, extra...)
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	return db
}

func TestMatchDisciplineCountsCardsBeforeKickoff(t *testing.T) {
	db := disciplineDB(t,
		"INSERT INTO matches VALUES "+
			"(1, 1, 9, '2026-09-01T15:00:00Z', 'finished'),"+ // counted
			"(2, 8, 1, '2026-09-08T15:00:00Z', 'FT'),"+ // counted, team 1 away
			"(3, 1, 7, '2026-09-15T15:00:00Z', 'finished'),"+ // clean match reported as nulls: counted as zero
			"(4, 1, 6, '2026-09-18T15:00:00Z', 'finished'),"+ // opponent row missing: skipped
			"(5, 1, 5, '2026-09-20T15:00:00Z', 'finished'),"+ // own row captured nothing: skipped
			"(6, 1, 4, '2026-09-22T15:00:00Z', 'live'),"+ // not finished: skipped
			"(7, 1, 3, '2026-09-27T15:00:00Z', 'finished');", // after kickoff: skipped
		"INSERT INTO fixture_statistics (fixture_id, team_id, yellow_cards, red_cards, fouls, card_points) VALUES "+
			"(1, 1, 3, 1, 12, 50), (1, 9, 2, NULL, 10, 20),"+
			"(2, 1, 1, 0, 9, 10), (2, 8, 4, 0, 14, 40),"+
			"(3, 1, NULL, NULL, 8, NULL), (3, 7, NULL, NULL, 11, NULL),"+
			"(4, 1, 5, 0, 9, 50),"+
			"(5, 1, NULL, NULL, NULL, NULL), (5, 5, 2, 0, 10, 20),"+
			"(6, 1, 6, 1, 15, 85), (6, 4, 1, 0, 9, 10),"+
			"(7, 1, 9, 2, 20, 140), (7, 3, 9, 2, 20, 140);",
	)

	discipline, err := NewSource(db).matchDiscipline(context.Background(), 100)
	if err != nil {
		t.Fatalf("matchDiscipline: %v", err)
	}
	if discipline == nil {
		t.Fatal("discipline = nil, want team 1's record")
	}
	home := discipline.Home
	if home.Matches != 3 || home.YellowCards != 4 || home.RedCards != 1 || home.MatchCards != 11 {
		t.Fatalf("home = %+v, want 3 matches, 4 yellow, 1 red, 11 match cards", home)
	}
	if discipline.Away.Matches != 0 {
		t.Fatalf("away = %+v, want no record", discipline.Away)
	}
}

// card_points is stored as yellow*10+red*25 by one writer and yellow+2*red by
// another; the figures must come from raw counts so the scale cannot leak in.
func TestMatchDisciplineIgnoresCardPoints(t *testing.T) {
	db := disciplineDB(t,
		"INSERT INTO matches VALUES (1, 1, 9, '2026-09-01T15:00:00Z', 'finished');",
		"INSERT INTO fixture_statistics (fixture_id, team_id, yellow_cards, red_cards, card_points) VALUES (1, 1, 2, 0, 20), (1, 9, 1, 0, 1);",
	)
	discipline, err := NewSource(db).matchDiscipline(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := discipline.Home.MatchCards; got != 3 {
		t.Fatalf("match cards = %d, want 3 from raw counts", got)
	}
}

func TestMatchDisciplineCoversTheLastTenOnly(t *testing.T) {
	var matches, stats []string
	for i := 1; i <= 14; i++ {
		matches = append(matches, fmt.Sprintf("(%d, 1, 9, '2026-08-%02dT15:00:00Z', 'finished')", i, i))
		// The four oldest matches carry many more cards; they must fall
		// outside the window.
		yellow := 1
		if i <= 4 {
			yellow = 9
		}
		stats = append(stats, fmt.Sprintf("(%d, 1, %d, 0), (%d, 9, 0, 0)", i, yellow, i))
	}
	db := disciplineDB(t,
		"INSERT INTO matches VALUES "+strings.Join(matches, ",")+";",
		"INSERT INTO fixture_statistics (fixture_id, team_id, yellow_cards, red_cards) VALUES "+strings.Join(stats, ",")+";",
	)
	discipline, err := NewSource(db).matchDiscipline(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if discipline.Home.Matches != disciplineWindow || discipline.Home.YellowCards != disciplineWindow {
		t.Fatalf("home = %+v, want the %d most recent matches with one yellow each", discipline.Home, disciplineWindow)
	}
}

func TestMatchDisciplineIsNilWithoutRecordsOrFixture(t *testing.T) {
	db := disciplineDB(t)
	source := NewSource(db)
	if discipline, err := source.matchDiscipline(context.Background(), 100); err != nil || discipline != nil {
		t.Fatalf("no records: discipline = %+v, err = %v; want nil, nil", discipline, err)
	}
	if discipline, err := source.matchDiscipline(context.Background(), 404); err != nil || discipline != nil {
		t.Fatalf("unknown fixture: discipline = %+v, err = %v; want nil, nil", discipline, err)
	}
}

func TestMatchDisciplineErrorsWhenStatisticsTableIsMissing(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE matches (id INTEGER PRIMARY KEY, home_team_id INTEGER, away_team_id INTEGER, kickoff_at TEXT NOT NULL, status TEXT NOT NULL); INSERT INTO matches VALUES (100, 1, 2, '2026-09-24T18:00:00Z', 'scheduled');"); err != nil {
		t.Fatal(err)
	}
	// The error is reported so GetMatch can log it and serve the match
	// without card figures; it must not be swallowed here.
	if _, err := NewSource(db).matchDiscipline(context.Background(), 100); err == nil {
		t.Fatal("matchDiscipline with no statistics table returned nil error")
	}
}

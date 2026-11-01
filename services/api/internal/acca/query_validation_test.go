package acca

import (
	"database/sql"
	_ "modernc.org/sqlite"
	"strings"
	"testing"
)

func TestCursorValidationAndRoundTrip(t *testing.T) {
	sortValue := strings.Repeat("0123456789abcdef", 2)
	encoded := encodeCursor(sortValue)
	if got, err := decodeCursor(encoded); err != nil || got != sortValue {
		t.Fatalf("cursor roundtrip=%q err=%v", got, err)
	}
	if err := ValidateCursor(""); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"!", encodeCursor("short"), encodeCursor(strings.Repeat("g", 32)), strings.Repeat("A", 10000)} {
		if err := ValidateCursor(invalid); err == nil {
			t.Error("accepted malformed cursor")
		}
	}
}

func TestParseOddsAcceptsBoundaries(t *testing.T) {
	for _, input := range []string{"0", "1.5", "1000"} {
		if value, err := ParseOdds(input, 1000); err != nil || value == nil {
			t.Errorf("rejected %s: %v", input, err)
		}
	}
}

func TestParseOddsRejectsNonfinite(t *testing.T) {
	for _, input := range []string{"NaN", "+Inf", "-Inf"} {
		if _, err := ParseOdds(input, 1000); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
}

func TestGeographicPredicateRequiresCountryAndLeagueOnSameLeg(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE acca_slips(id INTEGER); CREATE TABLE acca_slip_legs(slip_id INTEGER, match_id INTEGER); CREATE TABLE matches(id INTEGER, league_id INTEGER); CREATE TABLE leagues(id INTEGER, name TEXT, country_code TEXT);
INSERT INTO acca_slips VALUES(1),(2); INSERT INTO leagues VALUES(10,'Premier League','GB'),(20,'La Liga','ES'); INSERT INTO matches VALUES(100,10),(200,20); INSERT INTO acca_slip_legs VALUES(1,100),(1,200),(2,200);`)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		country, league string
		want            int
	}{{"GB", "La Liga", 0}, {"ES", "La Liga", 2}, {"GB", "10", 1}, {"all", "La Liga", 2}, {"GB", "all", 1}} {
		predicate, args := geographicPredicate("main", test.country, test.league, 1)
		var got int
		if err := db.QueryRow("SELECT COUNT(*) FROM acca_slips s WHERE "+predicate, args...).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Errorf("country=%s league=%s count=%d want=%d", test.country, test.league, got, test.want)
		}
	}
}

func TestSelectionPredicateRequiresEveryLegToMatch(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE acca_slips(id INTEGER); CREATE TABLE acca_slip_legs(slip_id INTEGER, selection TEXT); INSERT INTO acca_slips VALUES(1),(2),(3); INSERT INTO acca_slip_legs VALUES(1,'over_2_5'),(1,'under_2_5'),(2,'over_2_5'),(2,'over_2_5'),(3,NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	var got int
	if err := db.QueryRow("SELECT COUNT(*) FROM acca_slips s WHERE "+selectionPredicate("main", 1), "over_2_5").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("matching slips=%d want 1", got)
	}
}

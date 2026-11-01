package httpserver

import "testing"

// intPtr already exists in server_test.go; these are this file's own.
func statInt(v int) *int           { return &v }
func statFloat(v float64) *float64 { return &v }

// Absent and zero are different answers, and the database says so: of 11,076
// rows in fixture_statistics, red_cards is explicitly 0 in 4,371 and NULL in
// 5,710. A provider that reported no red cards and a provider that reported
// nothing are not the same fixture, and rendering both as "0" invents a figure
// for the second.
//
// So absent fields are omitted from the response rather than sent as null or
// coerced to zero. An omitted key is unambiguous; a null has to be told apart
// from 0 by every consumer separately, and one of them eventually gets it wrong.
func TestAbsentStatisticsAreOmittedRatherThanZeroed(t *testing.T) {
	stats := TeamMatchStats{
		PossessionPct: statFloat(39),
		ShotsTotal:    statInt(6),
		YellowCards:   statInt(0), // reported, and genuinely none
		// RedCards, Corners, Fouls, Offsides, ShotsOnTarget: not reported.
	}
	out := teamMatchStatsResponse(stats)

	if _, present := out["red_cards"]; present {
		t.Error("red_cards was not reported but appears in the response; the page will print a count nobody measured")
	}
	for _, key := range []string{"corners", "fouls", "offsides", "shots_on_target"} {
		if _, present := out[key]; present {
			t.Errorf("%s was not reported but appears in the response", key)
		}
	}

	yellow, present := out["yellow_cards"]
	if !present {
		t.Fatal("yellow_cards was reported as 0 and is missing; a reported zero is a real measurement")
	}
	if yellow != 0 {
		t.Errorf("yellow_cards = %v, want 0", yellow)
	}
	if out["possession_pct"] != float64(39) {
		t.Errorf("possession_pct = %v, want 39", out["possession_pct"])
	}
	if out["shots_total"] != 6 {
		t.Errorf("shots_total = %v, want 6", out["shots_total"])
	}
}

// An upcoming match has no statistics, and sixteen absent fields is not a
// useful thing to send on every fixture that has not kicked off.
func TestMatchWithoutStatisticsCarriesNoStatisticsBlock(t *testing.T) {
	match := MatchSummary{ID: 1, HomeTeam: "Home", AwayTeam: "Away"}
	if _, present := matchSummaryResponse(match)["statistics"]; present {
		t.Error("a match with no statistics still carries a statistics block")
	}

	match.Statistics = &MatchStatistics{Home: TeamMatchStats{ShotsTotal: statInt(6)}}
	response, ok := matchSummaryResponse(match)["statistics"].(map[string]any)
	if !ok {
		t.Fatal("a match with statistics does not serve them")
	}
	if _, present := response["home"]; !present {
		t.Error("the statistics block has no home side")
	}
	if _, present := response["away"]; !present {
		t.Error("the statistics block has no away side; the design draws both columns always")
	}
}

package apifootball

import (
	"os"
	"path/filepath"
	"testing"
)

// These tests read a REAL capture, taken 2026-10-02 against ten fixtures that
// were live at the time (services/worker/cmd/capturefixtureids). It replaced a
// synthetic fixture written from documentation, and being replaced is what the
// synthetic one existed for: the shape is now evidence rather than assumption.
// Re-run that command to refresh it after a provider change.
func loadEmbedded(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "fixtures-ids-embedded.captured.json"))
	if err != nil {
		t.Fatalf("the fixtures?ids= capture is missing: %v", err)
	}
	return body
}

func TestParseFixturesWithStatisticsReadsEveryFixtureInTheResponse(t *testing.T) {
	// Ten ids were requested and ten came back, which is the claim the whole
	// Tier 2 costing rests on: one batched call covers up to twenty fixtures, so
	// live statistics for 200 concurrent matches cost ~3,000 requests a day
	// rather than ~72,000.
	got, err := ParseFixturesWithStatistics(loadEmbedded(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Fatalf("parsed %d fixtures, want 10", len(got))
	}
	if got[0].Fixture.Fixture.ID != 1493161 {
		t.Fatalf("fixture id = %d", got[0].Fixture.Fixture.ID)
	}
	// The fixture itself must still parse exactly as the bulk poll parses it --
	// the embedded objects are additive, not a different fixture shape.
	if got[0].Fixture.Fixture.Status.Short != "1H" {
		t.Fatalf("base fixture status did not survive: %+v", got[0].Fixture)
	}
	// Goals are pointers because the provider sends null before kickoff, and
	// "no goals yet" is not "nil".
	if got[0].Fixture.Goals.Home == nil || *got[0].Fixture.Goals.Home != 1 {
		t.Fatalf("base fixture goals did not survive: %+v", got[0].Fixture.Goals)
	}
}

func TestEmbeddedStatisticsCarryTheValuesTheLadderNeeds(t *testing.T) {
	got, err := ParseFixturesWithStatistics(loadEmbedded(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0].Statistics) != 2 {
		t.Fatalf("got %d teams of statistics, want 2", len(got[0].Statistics))
	}
	home := got[0].Statistics[0]
	if home.TeamID != 453 {
		t.Fatalf("team id = %d, want the PROVIDER id -- resolution happens later", home.TeamID)
	}
	values := map[string]string{}
	for _, stat := range home.Statistics {
		values[stat.Type] = stat.Value
	}
	// Every market Tier 2 unlocks needs its source present.
	for _, want := range []string{"Corner Kicks", "Total Shots", "Shots on Goal", "Fouls", "Ball Possession"} {
		if values[want] == "" {
			t.Errorf("missing %q, which a market depends on: %v", want, values)
		}
	}
	if values["Corner Kicks"] != "1" {
		t.Errorf("corners = %q, want 1", values["Corner Kicks"])
	}
	// Possession keeps its percent sign, matching the standalone parser, so one
	// normaliser downstream handles both endpoints.
	if values["Ball Possession"] != "46%" {
		t.Errorf("possession = %q, want 46%%", values["Ball Possession"])
	}
	// xG is NOT here, and the synthetic fixture was wrong to imply it would be.
	// The capture carries fifteen statistics and expected_goals is not among
	// them, so a market built on xG would be absent exactly where it looked
	// free. Pinned so that the day the provider starts sending it, this test
	// says so rather than the market quietly appearing.
	if _, present := values["expected_goals"]; present {
		t.Errorf("expected_goals is now present: %v", values)
	}
}

func TestANullStatisticCollapsesToEmptyNotZero(t *testing.T) {
	// "Not observed" and "zero" are different claims. A null red-card count that
	// became 0 would assert nobody was sent off, which the provider did not say --
	// the same reason fixture_live_snapshots keeps those columns nullable.
	//
	// Constructed rather than read off the capture, deliberately: the capture
	// carries no null at all across ten fixtures, which is worth knowing and is
	// exactly why this property cannot depend on one being there. A parser rule
	// that only holds while the provider happens to send complete data is not a
	// rule.
	got, err := ParseFixturesWithStatistics([]byte(`{"get":"fixtures","errors":[],"results":1,
	  "response":[{"fixture":{"id":7},"statistics":[{"team":{"id":453},"statistics":[
	    {"type":"Red Cards","value":null}]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, stat := range got[0].Statistics[0].Statistics {
		if stat.Type == "Red Cards" {
			if stat.Value != "" {
				t.Fatalf("null Red Cards became %q, want empty", stat.Value)
			}
			return
		}
	}
	t.Fatal("Red Cards not present in the parsed statistics")
}

func TestEveryCapturedFixtureCarriedStatistics(t *testing.T) {
	// Coverage was assumed to be patchy -- "leagues without statistics are the
	// norm" -- and on this capture it was not: all ten live fixtures carried two
	// teams of statistics. Worth pinning, because it is the difference between
	// corner markets being a rarity and being the default.
	//
	// It does NOT mean coverage is universal. A fixture with no statistics block
	// must still parse, and TestAbsentStatisticsFieldYieldsNoneRatherThanGuessing
	// holds that property against a constructed response -- the right place for
	// it now that the capture has no such fixture to offer.
	got, err := ParseFixturesWithStatistics(loadEmbedded(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range got {
		if len(fixture.Statistics) != 2 {
			t.Errorf("fixture %d carried %d teams of statistics, want 2",
				fixture.Fixture.Fixture.ID, len(fixture.Statistics))
		}
	}
}

func TestAbsentStatisticsFieldYieldsNoneRatherThanGuessing(t *testing.T) {
	// The shape is unverified. If the real response nests statistics elsewhere,
	// this is what happens: no statistics, never invented ones. A wrong
	// assumption must read as "the provider did not send them".
	body := []byte(`{"get":"fixtures","errors":[],"results":1,"response":[
	  {"fixture":{"id":7,"status":{"short":"1H","elapsed":10}},"goals":{"home":0,"away":0}}
	]}`)
	got, err := ParseFixturesWithStatistics(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Statistics != nil {
		t.Fatalf("expected one fixture with no statistics, got %+v", got)
	}
}

func TestUnattributableStatisticsAreSkippedNotMisassigned(t *testing.T) {
	// Assigning a corner count to the wrong side is worse than having none.
	body := []byte(`{"get":"fixtures","errors":[],"results":1,"response":[
	  {"fixture":{"id":7,"status":{"short":"1H"}},"statistics":[
	    {"team":{"id":0},"statistics":[{"type":"Corner Kicks","value":5}]},
	    {"team":{"id":42},"statistics":[{"type":"Corner Kicks","value":3}]}
	  ]}
	]}`)
	got, err := ParseFixturesWithStatistics(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got[0].Statistics) != 1 || got[0].Statistics[0].TeamID != 42 {
		t.Fatalf("unattributable block was not skipped: %+v", got[0].Statistics)
	}
}

func TestAMissingFixtureIDIsRejected(t *testing.T) {
	// Without an id the fixture cannot be keyed to anything, and this repository
	// has exactly one canonical match id. Guessing is how team history got split
	// across two ids once.
	_, err := ParseFixturesWithStatistics([]byte(
		`{"get":"fixtures","errors":[],"results":1,"response":[{"fixture":{"id":0}}]}`))
	if err == nil {
		t.Fatal("expected an error for a fixture with no id")
	}
}

func TestAProviderErrorEnvelopeIsSurfaced(t *testing.T) {
	_, err := ParseFixturesWithStatistics([]byte(
		`{"get":"fixtures","errors":{"token":"invalid"},"results":0,"response":[]}`))
	if err == nil {
		t.Fatal("expected the provider error envelope to surface")
	}
}

func TestEmbeddedAndStandaloneStatisticsNormaliseIdentically(t *testing.T) {
	// A corner count that reads differently depending on which endpoint delivered
	// it would only ever show up as an inexplicable market disagreement.
	standalone, err := ParseFixtureStatistics([]byte(`{"get":"fixtures/statistics","errors":[],"results":1,
	  "response":[{"team":{"id":33},"statistics":[
	    {"type":" Corner Kicks ","value":6},
	    {"type":"Ball Possession","value":"57%"},
	    {"type":"Red Cards","value":null}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := ParseFixturesWithStatistics([]byte(`{"get":"fixtures","errors":[],"results":1,
	  "response":[{"fixture":{"id":7},"statistics":[{"team":{"id":33},"statistics":[
	    {"type":" Corner Kicks ","value":6},
	    {"type":"Ball Possession","value":"57%"},
	    {"type":"Red Cards","value":null}]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(embedded[0].Statistics) != 1 {
		t.Fatalf("embedded parse produced %d teams", len(embedded[0].Statistics))
	}
	if got, want := embedded[0].Statistics[0], standalone[0]; got.TeamID != want.TeamID ||
		len(got.Statistics) != len(want.Statistics) {
		t.Fatalf("shapes differ: %+v vs %+v", got, want)
	}
	for i := range standalone[0].Statistics {
		if embedded[0].Statistics[0].Statistics[i] != standalone[0].Statistics[i] {
			t.Fatalf("statistic %d differs: %+v vs %+v",
				i, embedded[0].Statistics[0].Statistics[i], standalone[0].Statistics[i])
		}
	}
}

package entitlement_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/access"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/predictions/entitlement"
)

func sampleState() entitlement.FixtureState {
	cutoff := time.Date(2026, 9, 7, 18, 43, 10, 0, time.UTC)
	return entitlement.FixtureState{
		FixtureID:    1234567,
		Status:       "LIVE_2H",
		Minute:       63,
		HomeGoals:    1,
		AwayGoals:    1,
		HomeTeamLogo: "https://cdn/home.png",
		AwayTeamLogo: "https://cdn/away.png",
		Markets: []entitlement.MarketState{
			{
				Key:   "1X2",
				Label: "Match Result",
				Prediction: &entitlement.Prediction{
					Selection: "HOME", Probability: 0.61, Confidence: "MEDIUM", ConfidenceScore: 0.72,
				},
				DataCutoffAt: cutoff,
				MatchMinute:  63,
			},
			{Key: "NEXT_GOAL", Label: "Next Goal"},
		},
	}
}

func decode(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

// The two payloads must be the same shape. A client that renders a different
// component tree for anonymous and authenticated viewers grows two code paths,
// and the anonymous one is the one nobody looks at.
func TestBothViewersReceiveTheSameSchema(t *testing.T) {
	filter := entitlement.NewFilter()
	anonymous := decode(t, filter.Shape(entitlement.Anonymous(), sampleState()))
	entitled := decode(t, filter.Shape(entitlementViewerFor(t, true), sampleState()))

	for _, key := range []string{
		"fixture_id", "status", "minute", "score", "markets", "locked_count",
		// A crest is not model output, so it is part of the shape both tiers
		// get. Added to this list when the field was added, because the whole
		// point of the list is that nothing new may be tier-dependent.
		"home_team_logo", "away_team_logo",
	} {
		if _, ok := anonymous[key]; !ok {
			t.Errorf("anonymous payload is missing %q", key)
		}
		if _, ok := entitled[key]; !ok {
			t.Errorf("entitled payload is missing %q", key)
		}
	}
	if anonymous["status"] != entitled["status"] || anonymous["minute"] != entitled["minute"] {
		t.Fatal("match state differs between viewers; only the prediction is gated")
	}
	if anonymous["home_team_logo"] != entitled["home_team_logo"] ||
		anonymous["away_team_logo"] != entitled["away_team_logo"] {
		t.Fatal("crests differ between viewers; a logo is decoration, not gated information")
	}
}

// The crest fields on the in-play fixture payload.
//
// Decoration, following the acca.Leg precedent: flat keys, always present, and
// an empty string rather than null or an absent key when the team has no logo
// on record -- the clients key on the empty string to draw an initials shield.
// Nothing resolves a fixture or a side by a logo; that is still FixtureID and
// the fixture's own team ids.
func TestFixtureCarriesTeamCrestsForBothViewers(t *testing.T) {
	filter := entitlement.NewFilter()

	for name, viewer := range map[string]entitlement.Viewer{
		"anonymous": entitlement.Anonymous(),
		"entitled":  entitlementViewerFor(t, true),
	} {
		shaped := decode(t, filter.Shape(viewer, sampleState()))
		if shaped["home_team_logo"] != "https://cdn/home.png" {
			t.Errorf("%s home_team_logo = %v", name, shaped["home_team_logo"])
		}
		if shaped["away_team_logo"] != "https://cdn/away.png" {
			t.Errorf("%s away_team_logo = %v", name, shaped["away_team_logo"])
		}
	}
}

// A fixture whose teams have no crest on record is still served in full, with
// both keys present and empty. A missing logo must never remove a row from the
// board.
func TestFixtureWithoutCrestsKeepsEmptyKeys(t *testing.T) {
	state := sampleState()
	state.HomeTeamLogo = ""
	state.AwayTeamLogo = ""

	shaped := decode(t, entitlement.NewFilter().Shape(entitlement.PublicRead(), state))
	for _, key := range []string{"home_team_logo", "away_team_logo"} {
		value, present := shaped[key]
		if !present {
			t.Fatalf("%s is absent; it must be present and empty", key)
		}
		if value != "" {
			t.Errorf("%s = %v, want an empty string", key, value)
		}
	}
	if shaped["fixture_id"] != float64(1234567) {
		t.Error("a fixture with no crests must still be served")
	}
}

func TestAnonymousMarketsAreLockedAndCarryNoPrediction(t *testing.T) {
	shaped := decode(t, entitlement.NewFilter().Shape(entitlement.Anonymous(), sampleState()))
	markets, _ := shaped["markets"].([]any)
	if len(markets) != 2 {
		t.Fatalf("markets = %d, want 2", len(markets))
	}
	for _, raw := range markets {
		market, _ := raw.(map[string]any)
		if market["locked"] != true {
			t.Errorf("market %v is not locked", market["key"])
		}
		for _, gated := range []string{"prediction", "data_cutoff_at", "match_minute"} {
			if _, present := market[gated]; present {
				t.Errorf("anonymous market %v carries %q", market["key"], gated)
			}
		}
	}
	if shaped["locked_count"] != float64(2) {
		t.Errorf("locked_count = %v, want 2", shaped["locked_count"])
	}
	unlock, _ := shaped["unlock"].(map[string]any)
	if unlock["reason"] != entitlement.ReasonAuthRequired || unlock["cta"] != entitlement.RegisterCTA {
		t.Errorf("unlock = %v", unlock)
	}
}

// An entitled viewer looking at a market the engine has not published yet
// sees an unlocked, empty market. Counting that as locked would fire the
// register CTA at someone who already has an account.
func TestEntitledViewerSeesUnpublishedMarketsUnlockedWithoutACTA(t *testing.T) {
	shaped := decode(t, entitlement.NewFilter().Shape(entitlementViewerFor(t, true), sampleState()))
	markets, _ := shaped["markets"].([]any)

	first, _ := markets[0].(map[string]any)
	if first["locked"] != false {
		t.Error("published market is locked for an entitled viewer")
	}
	prediction, _ := first["prediction"].(map[string]any)
	if prediction["selection"] != "HOME" || prediction["probability"] != 0.61 {
		t.Errorf("prediction = %v", prediction)
	}
	if first["data_cutoff_at"] != "2026-09-07T18:43:10Z" {
		t.Errorf("data_cutoff_at = %v", first["data_cutoff_at"])
	}

	second, _ := markets[1].(map[string]any)
	if second["locked"] != false {
		t.Error("unpublished market is locked for an entitled viewer")
	}
	if _, present := second["prediction"]; present {
		t.Error("unpublished market invented a prediction")
	}
	if shaped["locked_count"] != float64(0) {
		t.Errorf("locked_count = %v, want 0", shaped["locked_count"])
	}
	if _, present := shaped["unlock"]; present {
		t.Error("entitled viewer was shown an unlock CTA")
	}
}

// The viewer is derived from the existing capability machinery, so a guest
// context and an explicitly revoked account are both denied without this
// package knowing anything about tiers or sessions.
func TestViewerFollowsTheAccessCapability(t *testing.T) {
	if entitlement.ForContext(access.GuestContext(7)).Entitled() {
		t.Error("guest context is entitled to in-play predictions")
	}
	if !entitlementViewerFor(t, true).Entitled() {
		t.Error("signed-in account is not entitled to in-play predictions")
	}
	revoked := access.NewContext(
		access.Principal{UserID: 42, Tier: access.TierBasic},
		map[string]json.RawMessage{access.CapabilityInPlayPredictions: json.RawMessage("false")},
	)
	if entitlement.ForContext(revoked).Entitled() {
		t.Error("an explicitly revoked account is still entitled")
	}
}

func TestShapeAllPreservesOrderAndHandlesAnEmptyFeed(t *testing.T) {
	filter := entitlement.NewFilter()
	if shaped := filter.ShapeAll(entitlement.Anonymous(), nil); len(shaped) != 0 {
		t.Fatalf("empty feed shaped to %d fixtures", len(shaped))
	}
	first := sampleState()
	second := sampleState()
	second.FixtureID = 7654321
	shaped := filter.ShapeAll(entitlement.Anonymous(), []entitlement.FixtureState{first, second})
	if len(shaped) != 2 || shaped[0].FixtureID != 1234567 || shaped[1].FixtureID != 7654321 {
		t.Fatalf("feed order changed: %+v", shaped)
	}
}

func entitlementViewerFor(t *testing.T, signedIn bool) entitlement.Viewer {
	t.Helper()
	if !signedIn {
		return entitlement.Anonymous()
	}
	return entitlement.ForContext(access.NewContext(access.Principal{UserID: 42, Tier: access.TierBasic}, nil))
}

// Basis points are the wire format new clients read, so they must appear for an
// entitled viewer -- and they are a probability, so they must be gated exactly
// as tightly as the float. A leak here would be a paid asset handed to an
// anonymous reader in a different numeric dress.
func TestEntitledViewerReceivesBasisPoints(t *testing.T) {
	shaped := decode(t, entitlement.NewFilter().Shape(entitlementViewerFor(t, true), sampleState()))
	markets, _ := shaped["markets"].([]any)
	if len(markets) == 0 {
		t.Fatal("no markets")
	}
	market, _ := markets[0].(map[string]any)
	prediction, _ := market["prediction"].(map[string]any)
	if prediction == nil {
		t.Fatalf("entitled market carries no prediction: %v", market)
	}
	// 0.61 -> 6100 basis points, and the float stays for existing clients.
	if prediction["probability_bp"] != float64(6100) {
		t.Errorf("probability_bp = %v, want 6100", prediction["probability_bp"])
	}
	if prediction["probability"] != 0.61 {
		t.Errorf("probability = %v, want 0.61 retained", prediction["probability"])
	}
}

func TestAnonymousMarketsCarryNoBasisPoints(t *testing.T) {
	shaped := decode(t, entitlement.NewFilter().Shape(entitlement.Anonymous(), sampleState()))
	raw, err := json.Marshal(shaped)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("probability_bp")) {
		t.Errorf("anonymous payload mentions probability_bp: %s", raw)
	}
}

// A grade is gated with the prediction, and this is the test that says why.
//
// The score is public. If an anonymous reader could see "this market won" next
// to a visible 1-1, they would know the BTTS pick was Yes -- the gate would
// leak the exact selection it exists to protect. So settlement is not merely
// gated by convention alongside the prediction; it CANNOT be ungated.
func TestSettlementIsNeverVisibleToAnAnonymousViewer(t *testing.T) {
	state := sampleState()
	state.Markets[0].SettlementState = "won"

	filter := entitlement.NewFilter()
	anonymous := decode(t, filter.Shape(entitlement.Anonymous(), state))
	anonymousMarkets, ok := anonymous["markets"].([]any)
	if !ok || len(anonymousMarkets) == 0 {
		t.Fatalf("anonymous payload carries no markets: %v", anonymous)
	}
	for index, raw := range anonymousMarkets {
		market, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("market %d is not an object", index)
		}
		if _, present := market["settlement"]; present {
			t.Fatalf("market %d leaked a grade to an anonymous viewer: %v", index, market)
		}
	}

	entitled := decode(t, filter.Shape(entitlementViewerFor(t, true), state))
	entitledMarkets, ok := entitled["markets"].([]any)
	if !ok || len(entitledMarkets) == 0 {
		t.Fatalf("entitled payload carries no markets: %v", entitled)
	}
	first, ok := entitledMarkets[0].(map[string]any)
	if !ok {
		t.Fatal("first entitled market is not an object")
	}
	if first["settlement"] != "won" {
		t.Fatalf("an entitled viewer sees the grade, got %v", first["settlement"])
	}
}

// An in-play market is ungraded for most of its life, and an absent key is the
// honest way to say so: "" would make every client invent a meaning for it.
func TestAnUngradedMarketOmitsTheSettlementKey(t *testing.T) {
	state := sampleState()
	state.Markets[0].SettlementState = ""

	entitled := decode(t, entitlement.NewFilter().Shape(entitlementViewerFor(t, true), state))
	markets, ok := entitled["markets"].([]any)
	if !ok || len(markets) == 0 {
		t.Fatalf("entitled payload carries no markets: %v", entitled)
	}
	first, ok := markets[0].(map[string]any)
	if !ok {
		t.Fatal("first market is not an object")
	}
	if _, present := first["settlement"]; present {
		t.Fatalf("an ungraded market omits the key entirely: %v", first)
	}
}

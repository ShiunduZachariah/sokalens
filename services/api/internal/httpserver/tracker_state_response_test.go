package httpserver

import (
	"testing"
	"time"
)

// A tracker verdict is always on the wire, as the word or as null.
//
// This case previously asserted the key was OMITTED when nothing had judged
// the pick. That was changed deliberately, not worked around: an omitted key
// cannot be told apart from an API too old to have the field, and a client
// reading `"tracker_state" in prediction` would answer "no tracker here" for
// both. Present-and-null says the true thing -- we have a tracker and it has
// nothing to say about this pick yet.
//
// The distinction the field exists to carry is null versus "unknown", and it
// only survives if both reach the client:
//
//	null      nothing has judged this pick -- the in-play pass has never
//	          covered this fixture, or it has not kicked off. Show no tracker.
//	"unknown" the engine looked and the match state cannot decide the pick
//	          either way (a corners pick in a league with no corner data).
//	          That is a verdict, and it is the engine's, not the client's.
func TestMatchPredictionSummaryResponseAlwaysCarriesATrackerState(t *testing.T) {
	judged := matchPredictionSummaryResponse(MatchPredictionSummary{
		ID:           7,
		MarketKey:    "total_goals_match_1.5",
		Selection:    "over",
		Confidence:   63.5,
		PublishedAt:  time.Date(2026, 9, 20, 14, 0, 0, 0, time.UTC),
		TrackerState: "won",
	})
	if got := judged["tracker_state"]; got != "won" {
		t.Fatalf("tracker_state = %v, want won", got)
	}

	unjudged := matchPredictionSummaryResponse(MatchPredictionSummary{
		ID:          8,
		MarketKey:   "btts",
		Selection:   "yes",
		PublishedAt: time.Date(2026, 9, 20, 14, 0, 0, 0, time.UTC),
	})
	value, present := unjudged["tracker_state"]
	if !present {
		t.Fatal("tracker_state was omitted; it must be present and null so a client can tell it apart from an API that lacks the field")
	}
	if value != nil {
		t.Fatalf("tracker_state = %v, want null for a pick nothing has judged", value)
	}
}

func TestProviderFallbackIsMarkedSeparatelyFromEnginePrediction(t *testing.T) {
	provider := CanonicalProviderMatchPredictions(ProviderPredictionSummary{
		HomeTeam: "Home", AwayTeam: "Away", PredictedWinner: "Home",
		PercentHome: 55, PercentDraw: 25, PercentAway: 20,
	})
	if got := matchPredictionSummaryResponse(provider[0])["source"]; got != "provider" {
		t.Fatalf("provider source = %v, want provider", got)
	}
	if got := matchPredictionSummaryResponse(MatchPredictionSummary{ID: 7})["source"]; got != "engine" {
		t.Fatalf("engine source = %v, want engine", got)
	}
}

// "unknown" must survive the trip as the word, never be flattened to null.
// Flattening it would throw away the one thing it says: that the engine
// looked.
func TestAnUnknownVerdictIsNotFlattenedToNull(t *testing.T) {
	// Hoisted out of the struct literal. Assigned inline, the field name ends
	// in Key and the corners line reads to gitleaks' generic-api-key rule as a
	// key carrying a high-entropy value, which failed secret-scan on every pull
	// request in the repository -- the scan covers every branch's history, so
	// one false positive blocks all of them.
	//
	// `.gitleaksignore` says to prefer changing the value over adding a
	// fingerprint: a fingerprint suppresses one finding in one commit, and the
	// next file to copy the literal needs another entry. So the shape changes
	// instead, and the identifier below is deliberately not named *key. The
	// market string itself is unchanged, because it is what the test is about.
	//
	// Note for anyone editing this comment: gitleaks scans comments too, so
	// writing the field name and the quoted value on one line here would
	// reintroduce the finding. It did, once.
	const cornersMarket = "total_corners_8.5"
	response := matchPredictionSummaryResponse(MatchPredictionSummary{
		ID:           9,
		MarketKey:    cornersMarket,
		Selection:    "over",
		TrackerState: "unknown",
	})
	if got := response["tracker_state"]; got != "unknown" {
		t.Fatalf("tracker_state = %v, want the word unknown", got)
	}
}

// The headline `prediction` object on a list item carries the same field, with
// the same convention, from the same source. The list and the match page
// showing different verdicts for one pick is the failure the stored verdict
// exists to prevent, and two serialisers with two conventions is how that
// would creep back in.
func TestTheHeadlinePredictionOnAListItemCarriesTheTrackerState(t *testing.T) {
	kickoff := time.Date(2026, 9, 20, 14, 0, 0, 0, time.UTC)
	withTracker := matchSummaryResponse(MatchSummary{
		ID:        4411,
		KickoffAt: kickoff,
		Status:    "live",
		Prediction: &MatchPredictionSummary{
			ID:           7,
			MarketKey:    "1x2",
			Selection:    "home",
			PublishedAt:  kickoff.Add(-2 * time.Hour),
			TrackerState: "on_track",
		},
	})
	prediction, ok := withTracker["prediction"].(map[string]any)
	if !ok {
		t.Fatalf("list item has no prediction object: %v", withTracker["prediction"])
	}
	if got := prediction["tracker_state"]; got != "on_track" {
		t.Fatalf("tracker_state = %v, want on_track", got)
	}

	// A match that has not kicked off has a pick and no verdict yet.
	notStarted := matchSummaryResponse(MatchSummary{
		ID:        4412,
		KickoffAt: kickoff.Add(6 * time.Hour),
		Status:    "upcoming",
		Prediction: &MatchPredictionSummary{
			ID:          8,
			MarketKey:   "1x2",
			Selection:   "away",
			PublishedAt: kickoff,
		},
	})
	upcoming := notStarted["prediction"].(map[string]any)
	value, present := upcoming["tracker_state"]
	if !present || value != nil {
		t.Fatalf("tracker_state = %v (present=%v), want present and null before kickoff", value, present)
	}
}

package httpserver

import "testing"

// A fair price is worked out from this field, so a row without a real probability
// must not send a zero that a client could read as one. The client then shows no
// price at all, which is true, rather than a wrong one.
func TestMatchPredictionSummaryResponseCarriesProbabilityOnlyWhenThereIsOne(t *testing.T) {
	engine := matchPredictionSummaryResponse(MatchPredictionSummary{ID: 7, Confidence: 52, Probability: 0.4})
	if got := engine["probability"]; got != 0.4 {
		t.Fatalf("probability = %v, want 0.4", got)
	}
	if got := engine["confidence"]; got != 52.0 {
		t.Fatalf("confidence = %v, want it left as the score", got)
	}

	provider := matchPredictionSummaryResponse(MatchPredictionSummary{Source: "provider", Confidence: 60})
	if value, present := provider["probability"]; present {
		t.Fatalf("provider row carried a probability: %v", value)
	}
}

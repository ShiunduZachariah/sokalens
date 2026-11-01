package inplay

import (
	"database/sql"
	"strings"
	"testing"
)

// The contract's vocabulary is what three clients render, so the mapping from
// the grader's two columns is pinned here rather than trusted.
func TestSettlementStateMapsTheContractVocabulary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  sql.NullString
		correct sql.NullBool
		want    string
	}{
		{"ungraded reads empty", sql.NullString{}, sql.NullBool{}, ""},
		{"settled and right is won",
			sql.NullString{String: "settled", Valid: true},
			sql.NullBool{Bool: true, Valid: true}, "won"},
		{"settled and wrong is lost",
			sql.NullString{String: "settled", Valid: true},
			sql.NullBool{Bool: false, Valid: true}, "lost"},
		// A void is not a wrong answer and must never be counted as one.
		{"void is void, whatever is_correct says",
			sql.NullString{String: "void", Valid: true},
			sql.NullBool{Bool: false, Valid: true}, "void"},
		{"ungradeable is reported, not hidden",
			sql.NullString{String: "unsettled_data_unavailable", Valid: true},
			sql.NullBool{}, "unsettled_data_unavailable"},
		// A settled row with no verdict is a grader bug. Empty is the honest
		// answer: better an absent chip than a coin flip shown as a result.
		{"settled with no verdict reads empty",
			sql.NullString{String: "settled", Valid: true},
			sql.NullBool{}, ""},
		// A status we do not know must be added deliberately.
		{"an unknown status reads empty",
			sql.NullString{String: "something_new", Valid: true},
			sql.NullBool{Bool: true, Valid: true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := settlementState(tc.status, tc.correct); got != tc.want {
				t.Fatalf("settlementState() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Both in-play reads share scanStates, so both must select the grade, in the
// same order, keyed the same way.
func TestBothInPlayQueriesReadTheGradeForThePublishedPrediction(t *testing.T) {
	for name, query := range map[string]string{
		"live list":        selectInPlay,
		"retained fixture": selectRetainedFixture,
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(query, "res.settlement_status") || !strings.Contains(query, "res.is_correct") {
				t.Fatal("query does not select the grade")
			}
			// Keyed on the published prediction's own id. Keying on fixture and
			// market instead would let an older generation's result appear
			// beside a different selection -- a wrong answer rather than a
			// missing one.
			if !strings.Contains(query, "res.prediction_id = pred.prediction_id") {
				t.Fatal("the grade must be keyed on the published prediction's own id")
			}
			// LEFT, so an ungraded market never drops a fixture from the board.
			if !strings.Contains(query, "LEFT JOIN prediction_results res") {
				t.Fatal("the grade join must be LEFT")
			}
		})
	}
}

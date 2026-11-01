package search

import (
	"strings"
	"testing"
)

func TestTokenizeDropsTheWordJoiningTwoTeamNames(t *testing.T) {
	for _, testCase := range []struct {
		query string
		want  []string
	}{
		{"sunderland vs mancity", []string{"sunderland", "mancity"}},
		{"Sunderland  VS.  Man City", []string{"sunderland", "man", "city"}},
		{"arsenal v chelsea", []string{"arsenal", "chelsea"}},
		{"real madrid", []string{"real", "madrid"}},
		// A query that is nothing but connectors still has to search for
		// something: "V" is a club in more than one league.
		{"vs", []string{"vs"}},
	} {
		got := Tokenize(testCase.query)
		if strings.Join(got, "|") != strings.Join(testCase.want, "|") {
			t.Errorf("Tokenize(%q) = %v, want %v", testCase.query, got, testCase.want)
		}
	}
}

func TestNormalizeFoldsPunctuationAndCase(t *testing.T) {
	for query, want := range map[string]string{
		"  Manchester   City  ": "manchester city",
		"Brighton & Hove":       "brighton hove",
		"Bayer 04 Leverkusen":   "bayer 04 leverkusen",
		"St. Pauli":             "st pauli",
	} {
		if got := Normalize(query); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", query, got, want)
		}
	}
}

// The whole point of the parser: a query naming two teams has to be able to
// match a row that names them in the other order.
func TestStrictClauseMatchesEitherTeamOrder(t *testing.T) {
	var args []any
	clause := Clause("hay", ParseTerms("sunderland vs mancity"), MatchAll, &args)

	if strings.Contains(clause, " OR (hay LIKE $1)") {
		t.Fatalf("terms must be ANDed, not ORed: %s", clause)
	}
	if count := strings.Count(clause, " AND "); count == 0 {
		t.Fatalf("expected the two team terms to be ANDed: %s", clause)
	}
	// "mancity" has to reach the database as Manchester *and* City, otherwise
	// no row can satisfy it.
	joined := patternsOf(args)
	for _, want := range []string{"%sunderland%", "%mancity%", "%manchester%", "%city%"} {
		if !strings.Contains(joined, want) {
			t.Errorf("clause args missing %q; got %s", want, joined)
		}
	}
}

func TestAliasesCoverTheAbbreviationsPeopleType(t *testing.T) {
	for query, want := range map[string]string{
		"mancity":    "manchester city",
		"man utd":    "manchester united",
		"spurs":      "tottenham",
		"barca":      "barcelona",
		"psg":        "paris saint germain",
		"epl":        "premier league",
		"sunderland": "sunderland",
	} {
		if got := TypesenseQuery(query); got != want {
			t.Errorf("TypesenseQuery(%q) = %q, want %q", query, got, want)
		}
	}
	if got := TypesenseQuery("sunderland vs mancity"); got != "sunderland manchester city" {
		t.Errorf("TypesenseQuery = %q", got)
	}
}

// The run-together reading is a fallback, not a default. If it ran on every
// query, "arsenal" would also match anything containing "ars" and "enal".
func TestGluedSplitsOnlyApplyOnTheRelaxedPass(t *testing.T) {
	strict := ParseTerms("arsenal")
	if len(strict) != 1 || len(strict[0].Alternatives) != 1 {
		t.Fatalf("strict pass for a plain word should be a single alternative: %+v", strict)
	}
	relaxed := RelaxTerms(strict, "arsenal")
	if len(relaxed[0].Alternatives) <= 1 {
		t.Fatalf("relaxed pass should add run-together readings: %+v", relaxed)
	}

	plan := Plan("arsenal")
	if len(plan) == 0 || plan[0].Name != "strict" || plan[0].Mode != MatchAll {
		t.Fatalf("the first pass must be the strict one: %+v", plan)
	}
	// A single unambiguous word must not pay for an every-term-optional pass.
	for _, pass := range plan {
		if pass.Mode == MatchAny {
			t.Fatalf("a one-word query should not get an any-term pass: %+v", plan)
		}
	}
}

func TestPlanEndsWithAnAnyTermPassForMultiWordQueries(t *testing.T) {
	plan := Plan("sunderland vs mancity")
	if len(plan) < 2 {
		t.Fatalf("multi-word query should have more than one pass: %+v", plan)
	}
	last := plan[len(plan)-1]
	if last.Mode != MatchAny {
		t.Fatalf("last pass = %+v, want the any-term fallback so a partial answer beats none", last)
	}
}

// Two entirely different queries must not produce the same SQL by accident,
// and no user input may ever reach the statement as text.
func TestClauseBindsEveryPatternAsAParameter(t *testing.T) {
	var args []any
	clause := Clause("hay", ParseTerms("robert'); drop table teams;--"), MatchAll, &args)
	if strings.Contains(clause, "drop table") || strings.Contains(clause, "'") {
		t.Fatalf("query text leaked into the statement: %s", clause)
	}
	if len(args) == 0 {
		t.Fatalf("expected bound parameters, got none")
	}
	for index := range args {
		if !strings.Contains(clause, placeholder(index+1)) {
			t.Fatalf("clause %s does not reference %s", clause, placeholder(index+1))
		}
	}
}

func TestClauseWithNoTermsMatchesEverythingRatherThanFailing(t *testing.T) {
	var args []any
	if got := Clause("hay", nil, MatchAll, &args); got != "true" {
		t.Fatalf("Clause(nil) = %q, want a valid always-true predicate", got)
	}
}

func patternsOf(args []any) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		value, _ := arg.(string)
		parts = append(parts, value)
	}
	return strings.Join(parts, " ")
}

func placeholder(index int) string {
	return "$" + itoa(index)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

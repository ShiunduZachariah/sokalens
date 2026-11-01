package search

import (
	"fmt"
	"strings"
	"unicode"
)

// A user's search box is not a LIKE pattern.
//
// Search used to take the raw query string, wrap it in `%...%`, and compare it
// to one column at a time. That works for exactly one shape of query — a
// single word that is a literal substring of one name — and silently returns
// nothing for everything else. "sunderland vs mancity" matched no row in the
// database because no team, league or fixture is literally named that, and the
// fixture the user wanted was stored as "Manchester City vs Sunderland":
// reversed, spelled out, and joined by a word the user typed as "vs".
//
// So the query is parsed instead. It becomes a list of Terms — the separate
// things the user asked for — and a row has to satisfy every one of them, in
// any order, across any of the fields that make up that row's identity.

// Alternative is one way of satisfying a Term: every pattern in it must match
// the row's haystack.
type Alternative []string

// Term is one concept from the query. A row satisfies it when at least one of
// its alternatives matches.
type Term struct {
	Alternatives []Alternative
}

// Words that join two team names rather than naming anything. They are dropped
// so "sunderland vs mancity" asks for two teams and not for a row containing
// the literal word "vs" — but only while something else survives, so a search
// for the Italian club "Vicenza" abbreviated to "v" is not silently emptied.
var connectorTokens = map[string]bool{
	"vs": true, "v": true, "versus": true, "against": true,
	"and": true, "x": true, "at": true, "the": true,
}

// Abbreviations football supporters actually type. These are high precision —
// nobody types "mancity" meaning anything other than Manchester City — so they
// are expanded in the strict pass alongside the literal token.
//
// This table is deliberately short and deliberately not the only mechanism:
// GluedSplits below handles the same shape generically for clubs nobody
// thought to list here. Add entries when QA reports a real miss, not
// speculatively.
var queryAliases = map[string][]string{
	"mancity":    {"manchester city"},
	"mancty":     {"manchester city"},
	"mcfc":       {"manchester city"},
	"citeh":      {"manchester city"},
	"manutd":     {"manchester united"},
	"manu":       {"manchester united"},
	"mufc":       {"manchester united"},
	"man":        {"manchester"},
	"utd":        {"united"},
	"spurs":      {"tottenham"},
	"thfc":       {"tottenham"},
	"gunners":    {"arsenal"},
	"afcb":       {"bournemouth"},
	"lfc":        {"liverpool"},
	"cfc":        {"chelsea"},
	"nufc":       {"newcastle"},
	"whu":        {"west ham"},
	"westham":    {"west ham"},
	"wolves":     {"wolverhampton"},
	"forest":     {"nottingham forest"},
	"sheffutd":   {"sheffield united"},
	"sheffwed":   {"sheffield wednesday"},
	"barca":      {"barcelona"},
	"atleti":     {"atletico madrid"},
	"realmadrid": {"real madrid"},
	"psg":        {"paris saint germain"},
	"juve":       {"juventus"},
	"gladbach":   {"monchengladbach"},
	"bvb":        {"borussia dortmund"},
	"epl":        {"premier league"},
	"ucl":        {"champions league"},
	"uel":        {"europa league"},
}

// Normalize folds a query to the comparable form used for both matching and
// exact-match ranking: lower case, accents kept as-is (PostgreSQL compares
// them consistently on both sides), and every run of non-alphanumeric
// characters collapsed to a single space.
func Normalize(raw string) string {
	var b strings.Builder
	b.Grow(len(raw))
	lastSpace := true
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteRune(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}

// Tokenize splits a normalized query into the words that carry meaning.
func Tokenize(raw string) []string {
	fields := strings.Fields(Normalize(raw))
	kept := make([]string, 0, len(fields))
	for _, field := range fields {
		if !connectorTokens[field] {
			kept = append(kept, field)
		}
	}
	// Every token was a connector ("vs", "v"): treat them as the query rather
	// than returning nothing at all.
	if len(kept) == 0 {
		kept = fields
	}
	// More than a handful of terms is a pasted sentence, not a search. Each
	// extra term is another AND clause against every candidate row.
	const maxTokens = 6
	if len(kept) > maxTokens {
		kept = kept[:maxTokens]
	}
	return kept
}

// gluedSplits returns the ways a single run-together token could be two words,
// as "mancity" is "man" + "city". Both halves must be long enough that the
// split says something: splitting on one or two characters matches most of the
// catalogue and is worse than no result.
func gluedSplits(token string) []Alternative {
	const minPart = 3
	if len(token) < minPart*2 {
		return nil
	}
	alternatives := make([]Alternative, 0, len(token))
	for cut := minPart; cut <= len(token)-minPart; cut++ {
		alternatives = append(alternatives, Alternative{
			containsPattern(token[:cut]),
			containsPattern(token[cut:]),
		})
	}
	return alternatives
}

func containsPattern(value string) string {
	return "%" + escapeLike(value) + "%"
}

// phraseAlternative turns "manchester city" into the requirement that the row
// contain both words.
func phraseAlternative(phrase string) Alternative {
	words := strings.Fields(phrase)
	alternative := make(Alternative, 0, len(words))
	for _, word := range words {
		alternative = append(alternative, containsPattern(word))
	}
	return alternative
}

// ParseTerms builds the strict term list: each token as typed, plus any known
// abbreviation it stands for.
func ParseTerms(raw string) []Term {
	tokens := Tokenize(raw)
	terms := make([]Term, 0, len(tokens))
	for _, token := range tokens {
		term := Term{Alternatives: []Alternative{{containsPattern(token)}}}
		for _, expansion := range queryAliases[token] {
			term.Alternatives = append(term.Alternatives, phraseAlternative(expansion))
		}
		terms = append(terms, term)
	}
	return terms
}

// RelaxTerms adds the run-together readings of each token. It is a second
// pass rather than part of ParseTerms because "arsenal" would otherwise also
// match anything containing "ars" and "enal", polluting a query that already
// had a perfectly good answer.
func RelaxTerms(terms []Term, raw string) []Term {
	tokens := Tokenize(raw)
	if len(tokens) != len(terms) {
		return terms
	}
	relaxed := make([]Term, 0, len(terms))
	for index, term := range terms {
		alternatives := make([]Alternative, 0, len(term.Alternatives)+len(tokens[index]))
		alternatives = append(alternatives, term.Alternatives...)
		alternatives = append(alternatives, gluedSplits(tokens[index])...)
		relaxed = append(relaxed, Term{Alternatives: alternatives})
	}
	return relaxed
}

// MatchMode decides whether a row has to satisfy every term or just one.
type MatchMode int

const (
	// MatchAll requires every term: "sunderland manchester city" means all
	// three words describe the row the user is after.
	MatchAll MatchMode = iota
	// MatchAny is the last resort for a multi-word query that matched nothing.
	// Half an answer — the Sunderland fixtures — beats an empty page.
	MatchAny
)

// Clause renders the terms as SQL against a haystack expression, appending the
// pattern values to args. Patterns are always bound parameters; only the
// caller-supplied haystack expression is interpolated, and every caller passes
// a constant.
func Clause(haystack string, terms []Term, mode MatchMode, args *[]any) string {
	if len(terms) == 0 {
		return "true"
	}
	rendered := make([]string, 0, len(terms))
	for _, term := range terms {
		alternatives := make([]string, 0, len(term.Alternatives))
		for _, alternative := range term.Alternatives {
			conditions := make([]string, 0, len(alternative))
			for _, pattern := range alternative {
				*args = append(*args, pattern)
				conditions = append(conditions, fmt.Sprintf("%s LIKE $%d", haystack, len(*args)))
			}
			if len(conditions) == 0 {
				continue
			}
			alternatives = append(alternatives, "("+strings.Join(conditions, " AND ")+")")
		}
		if len(alternatives) == 0 {
			continue
		}
		rendered = append(rendered, "("+strings.Join(alternatives, " OR ")+")")
	}
	if len(rendered) == 0 {
		return "true"
	}
	joiner := " AND "
	if mode == MatchAny {
		joiner = " OR "
	}
	return "(" + strings.Join(rendered, joiner) + ")"
}

// Passes is the ordered list of attempts made for one query. The first pass
// that returns anything wins, so an unambiguous single-word search costs
// exactly what it costs today and only a query that found nothing pays for the
// looser readings.
type Pass struct {
	Name  string
	Terms []Term
	Mode  MatchMode
}

// Plan returns the passes to try, in order.
func Plan(raw string) []Pass {
	strict := ParseTerms(raw)
	if len(strict) == 0 {
		return nil
	}
	passes := []Pass{{Name: "strict", Terms: strict, Mode: MatchAll}}
	relaxed := RelaxTerms(strict, raw)
	if !sameTerms(strict, relaxed) {
		passes = append(passes, Pass{Name: "relaxed", Terms: relaxed, Mode: MatchAll})
	}
	if len(strict) > 1 {
		passes = append(passes, Pass{Name: "any", Terms: relaxed, Mode: MatchAny})
	}
	return passes
}

func sameTerms(a, b []Term) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if len(a[index].Alternatives) != len(b[index].Alternatives) {
			return false
		}
	}
	return true
}

// TypesenseQuery renders the query for the search index: the meaningful tokens
// with known abbreviations spelled out, so "sunderland vs mancity" reaches the
// index as "sunderland manchester city" instead of asking it to find a team
// whose name contains "vs".
func TypesenseQuery(raw string) string {
	tokens := Tokenize(raw)
	if len(tokens) == 0 {
		return strings.TrimSpace(raw)
	}
	out := make([]string, 0, len(tokens)+2)
	for _, token := range tokens {
		if expansions := queryAliases[token]; len(expansions) > 0 {
			out = append(out, expansions[0])
			continue
		}
		out = append(out, token)
	}
	return strings.Join(out, " ")
}

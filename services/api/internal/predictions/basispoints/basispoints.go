// Package basispoints converts model probabilities to the integer wire format
// the in-play clients consume.
//
// WHY INTEGERS. Floating point probabilities on the wire give every client its
// own rounding, so web and the Flutter app can render the same market as 64.1%
// and 64.2%. An integer out of 10,000 is exact, identical everywhere, and still
// finer than anything a user can perceive.
//
// WHY NOT ROUND ON WRITE. Storage stays floating point. Rounding in the
// database would lose precision the model and the calibration work need, and
// would make a stored probability disagree with the matrix it was read off.
// This package runs at the API boundary only.
//
// WHY LARGEST REMAINDER. Rounding each selection independently does not
// preserve the total: three selections at one third each floor to 3333 and sum
// to 9999, and a client showing "99.99%" looks broken to a user and wrong to a
// reviewer. Distributing the shortfall to the largest fractional parts is the
// standard apportionment fix, and it keeps every selection within one basis
// point of its true value.
package basispoints

import (
	"sort"
	"strconv"
	"strings"
)

// Total is the denominator: probabilities are expressed out of 10,000, so one
// unit is 0.01%.
const Total = 10000

// FromProbability converts a single probability to basis points, clamped to
// [0, Total]. Use Distribute instead for a set of selections that must sum to
// Total -- this function makes no such guarantee, by design, because a lone
// probability has nothing to sum with.
func FromProbability(p float64) int {
	switch {
	case p <= 0:
		return 0
	case p >= 1:
		return Total
	}
	// Round half away from zero. p is non-negative here.
	return int(p*Total + 0.5)
}

// Distribute converts a market's selections to basis points, apportioned within
// each mutually exclusive group so every group sums to exactly Total.
//
// MOST MARKETS ARE ONE GROUP -- 1X2's three outcomes sum to 1, and so do BTTS's
// two. A LINE LADDER IS NOT: total-goals carries an over/under pair per line, so
// ten selections across five lines sum to 5, and apportioning them together
// divides every number by five. That is not hypothetical; it shipped. An
// `over_1.5` the model put at 81.65% went onto the wire as 1633 basis points.
//
// Keys are returned in a map because callers address selections by name. The
// apportionment is deterministic: ties in the fractional part are broken by
// selection name, so the same input always produces the same output and a test
// can assert an exact value.
//
// A group whose probabilities do not sum to 1 is scaled to sum to Total rather
// than rejected. The model can emit a set that is a few parts per million off
// after a Dixon-Coles correction, and refusing to serve it would take a market
// off the page over a rounding artefact. A group summing to zero returns all
// zeros, because there is nothing to apportion.
//
// Mirrors inplay_model.basis_points in the Python engine, which writes the
// snapshot series. The two are exercised against the same cases so a snapshot and
// a live payload cannot disagree about the same market.
func Distribute(selections map[string]float64) map[string]int {
	if len(selections) == 0 {
		return map[string]int{}
	}
	out := make(map[string]int, len(selections))
	for _, group := range selectionGroups(selections) {
		// A line group of one is HALF a pair, not a complete market. Apportioning
		// it would scale it to the full total and assert certainty -- turning an
		// over_2.5 the model put at 98% into 100%. Convert it directly instead
		// and leave the value alone. A plain group of one is a genuine
		// single-outcome market and does normalise.
		if len(group) == 1 && isLineSelection(group[0]) {
			out[group[0]] = FromProbability(selections[group[0]])
			continue
		}
		subset := make(map[string]float64, len(group))
		for _, name := range group {
			subset[name] = selections[name]
		}
		for name, value := range apportion(subset) {
			out[name] = value
		}
	}
	return out
}

// isLineSelection reports whether a name is one side of an over/under line.
func isLineSelection(name string) bool {
	index := strings.LastIndex(name, "_")
	if index <= 0 {
		return false
	}
	if prefix := name[:index]; prefix != "over" && prefix != "under" {
		return false
	}
	_, err := strconv.ParseFloat(name[index+1:], 64)
	return err == nil
}

// selectionGroups splits selections into mutually exclusive sets, grouping
// over_X/under_X by their line so each pair is apportioned on its own.
func selectionGroups(selections map[string]float64) [][]string {
	names := make([]string, 0, len(selections))
	for name := range selections {
		names = append(names, name)
	}
	sort.Strings(names)

	lines := map[string][]string{}
	lineKeys := make([]float64, 0, len(names))
	plain := make([]string, 0, len(names))
	for _, name := range names {
		index := strings.LastIndex(name, "_")
		if index <= 0 {
			plain = append(plain, name)
			continue
		}
		prefix, suffix := name[:index], name[index+1:]
		if prefix != "over" && prefix != "under" {
			plain = append(plain, name)
			continue
		}
		line, err := strconv.ParseFloat(suffix, 64)
		if err != nil {
			plain = append(plain, name)
			continue
		}
		if _, seen := lines[suffix]; !seen {
			lineKeys = append(lineKeys, line)
		}
		lines[suffix] = append(lines[suffix], name)
	}

	groups := make([][]string, 0, len(lines)+1)
	if len(plain) > 0 {
		groups = append(groups, plain)
	}
	sort.Float64s(lineKeys)
	for _, line := range lineKeys {
		groups = append(groups, lines[strconv.FormatFloat(line, 'f', -1, 64)])
	}
	return groups
}

func apportion(selections map[string]float64) map[string]int {
	names := make([]string, 0, len(selections))
	sum := 0.0
	for name, p := range selections {
		names = append(names, name)
		if p > 0 {
			sum += p
		}
	}
	sort.Strings(names)

	out := make(map[string]int, len(selections))
	if sum <= 0 {
		for _, name := range names {
			out[name] = 0
		}
		return out
	}

	type remainder struct {
		name string
		frac float64
	}
	remainders := make([]remainder, 0, len(names))
	assigned := 0
	for _, name := range names {
		p := selections[name]
		if p < 0 {
			p = 0
		}
		exact := p / sum * Total
		floor := int(exact)
		out[name] = floor
		assigned += floor
		remainders = append(remainders, remainder{name: name, frac: exact - float64(floor)})
	}

	// Largest fractional part first; name ascending as the tie-break so the
	// result is stable across runs and map iteration order cannot leak in.
	sort.Slice(remainders, func(a, b int) bool {
		if remainders[a].frac != remainders[b].frac {
			return remainders[a].frac > remainders[b].frac
		}
		return remainders[a].name < remainders[b].name
	})

	for i := 0; assigned < Total; i++ {
		out[remainders[i%len(remainders)].name]++
		assigned++
	}
	return out
}

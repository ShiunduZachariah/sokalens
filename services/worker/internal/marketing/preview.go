package marketing

import (
	"fmt"
	"strings"
)

// minFormMatches is the least history a form line is stated on. Below it a
// "last 5" line would be a "last 2" line pretending otherwise.
const minFormMatches = 3

// PreviewLines are the true, checkable sentences on a "before kick-off" card,
// computed only from finished matches in our own table.
func PreviewLines(p Pick, homeForm, awayForm, meetings []FormResult) []string {
	var lines []string
	if len(homeForm) >= minFormMatches {
		lines = append(lines, fmt.Sprintf("%s, last %d: %s", p.Home, len(homeForm), formString(homeForm, p.HomeTeamID)))
	}
	if len(awayForm) >= minFormMatches {
		lines = append(lines, fmt.Sprintf("%s, last %d: %s", p.Away, len(awayForm), formString(awayForm, p.AwayTeamID)))
	}
	if len(meetings) >= 2 {
		homeWins, draws, awayWins := 0, 0, 0
		for _, m := range meetings {
			switch outcomeFor(m, p.HomeTeamID) {
			case "W":
				homeWins++
			case "L":
				awayWins++
			default:
				draws++
			}
		}
		lines = append(lines, fmt.Sprintf("Last %d meetings: %s %d, draws %d, %s %d", len(meetings), p.Home, homeWins, draws, p.Away, awayWins))
	} else if len(homeForm) >= minFormMatches && len(awayForm) >= minFormMatches {
		lines = append(lines, fmt.Sprintf("Their recent games average %.1f goals", averageGoals(append(append([]FormResult{}, homeForm...), awayForm...))))
	}
	return lines
}

// formString is newest first: "W W D L W".
func formString(results []FormResult, teamID int64) string {
	letters := make([]string, 0, len(results))
	for _, r := range results {
		letters = append(letters, outcomeFor(r, teamID))
	}
	return strings.Join(letters, " ")
}

func outcomeFor(r FormResult, teamID int64) string {
	ours, theirs := r.HomeGoals, r.AwayGoals
	if r.AwayTeamID == teamID {
		ours, theirs = theirs, ours
	}
	switch {
	case ours > theirs:
		return "W"
	case ours < theirs:
		return "L"
	}
	return "D"
}

func averageGoals(results []FormResult) float64 {
	if len(results) == 0 {
		return 0
	}
	total := 0
	for _, r := range results {
		total += r.HomeGoals + r.AwayGoals
	}
	return float64(total) / float64(len(results))
}

// Package acca contains the bounded, worker-side ACCA inventory composer.
// It has no HTTP or provider dependencies so request paths cannot invoke it.
package acca

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"
)

const MaxActiveInventory = 3000

type Candidate struct {
	Source, PredictionID, Market, Selection string
	PredictionVersion                       string
	MatchID                                 int64
	Probability, Rank                       float64
	Kickoff                                 time.Time
}

type Intent struct {
	Target, Legs             int
	MinFairOdds, MaxFairOdds float64
	Tab                      string
	Period, GenerationIntent string
	Seed                     int64
	Now                      time.Time
	MaxAttempts              int
}

type Leg struct {
	Source, PredictionID, PredictionVersion, Market, Selection string
	MatchID                                                    int64
	Probability, FairOdds                                      float64
	Kickoff                                                    time.Time
}

type Slip struct {
	CompositionHash, ContentHash, Tab, OddsBand         string
	Legs                                                []Leg
	CombinedFairOdds, CombinedProbability, RankingScore float64
	EarliestKickoff, LatestKickoff                      time.Time
}

func Compose(candidates []Candidate, intent Intent) []Slip {
	if intent.Target <= 0 || intent.Legs < 2 || intent.Legs > 20 {
		return nil
	}
	if intent.Target > MaxActiveInventory {
		intent.Target = MaxActiveInventory
	}
	if intent.MaxFairOdds <= 0 || intent.MaxFairOdds > 1000 {
		intent.MaxFairOdds = 1000
	}
	if intent.MinFairOdds <= 0 {
		intent.MinFairOdds = 1
	}
	if floor := MinimumSystemCombinedFairOdds(intent.Legs); intent.MinFairOdds < floor {
		intent.MinFairOdds = floor
	}
	if intent.Now.IsZero() {
		intent.Now = time.Now().UTC()
	}
	if intent.MaxAttempts <= 0 {
		intent.MaxAttempts = intent.Target * 30
	}
	eligible := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.MatchID == 0 || math.IsNaN(c.Probability) || c.Probability <= 0 || c.Probability >= 1 || !c.Kickoff.After(intent.Now) || !tabAllows(intent.Tab, c.Market) {
			continue
		}
		// System ACCAs must not add near-certain legs that contribute almost no
		// return while still adding another event that can fail the bundle.
		fairOdds := 1 / c.Probability
		if math.Round(fairOdds*1e6) < MinimumSystemLegFairOdds*1e6 {
			continue
		}
		eligible = append(eligible, c)
	}
	// Rank first, then shuffle the bounded source pool so repeated runs are varied but reproducible.
	sort.SliceStable(eligible, func(i, j int) bool { return eligible[i].Rank > eligible[j].Rank })
	r := rand.New(rand.NewSource(intent.Seed))
	r.Shuffle(len(eligible), func(i, j int) { eligible[i], eligible[j] = eligible[j], eligible[i] })
	matchUse, selectionUse := map[int64]int{}, map[string]int{}
	out := make([]Slip, 0, intent.Target)
	for attempt := 0; attempt < intent.MaxAttempts && len(out) < intent.Target; attempt++ {
		pool := append([]Candidate(nil), eligible...)
		legs := make([]Leg, 0, intent.Legs)
		used := map[int64]bool{}
		for len(legs) < intent.Legs && len(pool) > 0 {
			i := r.Intn(len(pool))
			c := pool[i]
			pool = append(pool[:i], pool[i+1:]...)
			if used[c.MatchID] {
				continue
			}
			key := fmt.Sprintf("%d:%s:%s", c.MatchID, c.Market, c.Selection)
			if matchUse[c.MatchID] >= maxExposure(intent.Target) || selectionUse[key] >= maxSelectionExposure(intent.Target) {
				continue
			}
			legs = append(legs, Leg{Source: c.Source, PredictionID: c.PredictionID, PredictionVersion: c.PredictionVersion, MatchID: c.MatchID, Market: c.Market, Selection: c.Selection, Probability: c.Probability, FairOdds: 1 / c.Probability, Kickoff: c.Kickoff})
			used[c.MatchID] = true
		}
		if len(legs) != intent.Legs {
			continue
		}
		if normalizedTab(intent.Tab) == "mixed" && len(marketFamilies(legs)) < requiredFamilies(len(legs)) {
			continue
		}
		odds, prob := 1.0, 1.0
		rank := 0.0
		earliest, latest := legs[0].Kickoff, legs[0].Kickoff
		for _, l := range legs {
			odds *= l.FairOdds
			prob *= l.Probability
			rank += l.Probability
			if l.Kickoff.Before(earliest) {
				earliest = l.Kickoff
			}
			if l.Kickoff.After(latest) {
				latest = l.Kickoff
			}
		}
		if odds < intent.MinFairOdds || odds > intent.MaxFairOdds {
			continue
		}
		sort.Slice(legs, func(i, j int) bool { return canonicalLeg(legs[i]) < canonicalLeg(legs[j]) })
		s := Slip{Legs: legs, CombinedFairOdds: odds, CombinedProbability: prob, RankingScore: rank / float64(len(legs)), EarliestKickoff: earliest, LatestKickoff: latest, Tab: normalizedTab(intent.Tab), OddsBand: OddsBand(odds)}
		s.CompositionHash = hashLegs(legs)
		s.ContentHash = hashContent(s)
		out = append(out, s)
		for _, l := range legs {
			matchUse[l.MatchID]++
			selectionUse[fmt.Sprintf("%d:%s:%s", l.MatchID, l.Market, l.Selection)]++
		}
	}
	return out
}

func maxExposure(target int) int          { return max(3, int(math.Ceil(float64(target)*0.10))) }
func maxSelectionExposure(target int) int { return max(1, int(math.Ceil(float64(target)*0.05))) }
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func canonicalLeg(l Leg) string {
	return fmt.Sprintf("%s:%s:%d:%s:%s", l.Source, l.PredictionID, l.MatchID, l.Market, l.Selection)
}
func hashLegs(legs []Leg) string {
	h := sha256.New()
	for _, l := range legs {
		h.Write([]byte(canonicalLeg(l)))
		h.Write([]byte{'|'})
	}
	return hex.EncodeToString(h.Sum(nil))
}
func hashContent(s Slip) string {
	h := sha256.New()
	fmt.Fprintf(h, "%.8f:%.8f:%.8f", s.CombinedFairOdds, s.CombinedProbability, s.RankingScore)
	for _, l := range s.Legs {
		fmt.Fprintf(h, "|%.8f:%.8f:%s:%s", l.Probability, l.FairOdds, l.PredictionVersion, l.Kickoff.UTC().Format(time.RFC3339Nano))
	}
	return hex.EncodeToString(h.Sum(nil))
}
func normalizedTab(tab string) string {
	if tab == "" {
		return "mixed"
	}
	return tab
}
func tabAllows(tab, market string) bool {
	if tab == "" || tab == "all" || tab == "mixed" {
		return true
	}
	switch tab {
	case "1x2":
		return market == "1x2"
	case "btts":
		return market == "btts"
	case "over_2_5":
		return market == "ou_2_5" || market == "total_goals"
	case "corners":
		return market == "corners" || market == "total_corners"
	case "cards":
		return market == "cards" || market == "total_cards"
	}
	return market == tab
}

func marketFamilies(legs []Leg) map[string]struct{} {
	out := map[string]struct{}{}
	for _, l := range legs {
		out[marketFamily(l.Market)] = struct{}{}
	}
	return out
}
func marketFamily(m string) string {
	switch m {
	case "1x2", "double_chance":
		return "result"
	case "btts":
		return "btts"
	case "ou_2_5", "total_goals", "team_ou_goals":
		return "goals"
	case "corners", "total_corners":
		return "corners"
	case "cards", "total_cards":
		return "cards"
	}
	return m
}
func requiredFamilies(legs int) int {
	switch {
	case legs <= 3:
		return 2
	case legs <= 7:
		return 3
	case legs <= 12:
		return 4
	default:
		return 5
	}
}

func OddsBand(odds float64) string {
	bands := []float64{3, 5, 10, 25, 50, 100, 250, 500, 1000}
	prev := 1.5
	for i, max := range bands {
		if odds <= max {
			return fmt.Sprintf("B%d", i+1)
		}
		prev = max
	}
	_ = prev
	return "B9"
}

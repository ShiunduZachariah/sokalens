package recommendations

import (
	"fmt"
	"sort"
	"time"
)

type Input struct {
	ItemType     string
	ItemID       string
	MatchID      int64
	LeagueID     int64
	LeagueKey    string
	LeagueName   string
	HomeTeamID   int64
	HomeTeamKey  string
	HomeTeamName string
	AwayTeamID   int64
	AwayTeamKey  string
	AwayTeamName string
	Market       string
	Confidence   float64
	StartsAt     time.Time
	FreshnessAt  time.Time
	Sponsored    bool
	Editorial    bool
}

type Signals struct {
	Favorites        map[string]bool
	Interests        map[string]float64
	RecentSearches   map[string]bool
	RecentEngagement map[string]float64
	Viewed           map[string]bool
	Dismissed        map[string]bool
}

type Candidate struct {
	ItemType             string
	ItemID               string
	Score                float64
	ReasonKey            string
	ReasonText           string
	InterestKey          string
	PredictionConfidence float64
	StartsAt             time.Time
	FreshnessAt          time.Time
	Sponsored            bool
	Editorial            bool
	GenerationKey        string
	ExpiresAt            time.Time
	Title                string
	Subtitle             string
	Href                 string
}

type RankOptions struct {
	Weights                 map[string]float64
	HighConfidenceThreshold float64
	StartingSoonWindow      time.Duration
	Maximum                 int
	GenerationKey           string
	ExpiresAt               time.Time
	Now                     time.Time
}

type scoredReason struct {
	key      string
	text     string
	interest string
	value    float64
}

func Rank(inputs []Input, signals Signals, opts RankOptions) []Candidate {
	if opts.HighConfidenceThreshold <= 0 {
		opts.HighConfidenceThreshold = 0.70
	}
	if opts.StartingSoonWindow <= 0 {
		opts.StartingSoonWindow = 6 * time.Hour
	}
	if opts.Maximum <= 0 {
		opts.Maximum = 50
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}

	best := make(map[string]Candidate, len(inputs))
	for _, input := range inputs {
		identity := itemKey(input.ItemType, input.ItemID)
		if signals.Dismissed[identity] {
			continue
		}
		reasons := scoreReasons(input, signals, opts)
		score := 0.0
		primary := scoredReason{key: "exploration", text: "Relevant match to explore", value: weight(opts.Weights, "exploration")}
		for _, reason := range reasons {
			score += reason.value
			if reason.value > primary.value {
				primary = reason
			}
		}
		if len(reasons) == 0 {
			score += primary.value
		}
		candidate := Candidate{
			ItemType:             input.ItemType,
			ItemID:               input.ItemID,
			Score:                score,
			ReasonKey:            primary.key,
			ReasonText:           primary.text,
			InterestKey:          primary.interest,
			PredictionConfidence: input.Confidence,
			StartsAt:             input.StartsAt,
			FreshnessAt:          input.FreshnessAt,
			Sponsored:            input.Sponsored,
			Editorial:            input.Editorial,
			GenerationKey:        opts.GenerationKey,
			ExpiresAt:            opts.ExpiresAt,
			Title:                input.HomeTeamName + " vs " + input.AwayTeamName,
			Subtitle:             input.LeagueName + " · " + input.Market,
			Href:                 fmt.Sprintf("/matches/%d", input.MatchID),
		}
		if existing, ok := best[identity]; !ok || candidate.Score > existing.Score {
			best[identity] = candidate
		}
	}

	candidates := make([]Candidate, 0, len(best))
	for _, candidate := range best {
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		if !candidates[i].StartsAt.Equal(candidates[j].StartsAt) {
			return candidates[i].StartsAt.Before(candidates[j].StartsAt)
		}
		if candidates[i].ItemType != candidates[j].ItemType {
			return candidates[i].ItemType < candidates[j].ItemType
		}
		return candidates[i].ItemID < candidates[j].ItemID
	})
	if len(candidates) > opts.Maximum {
		candidates = candidates[:opts.Maximum]
	}
	return candidates
}

func scoreReasons(input Input, signals Signals, opts RankOptions) []scoredReason {
	reasons := make([]scoredReason, 0, 10)
	match := fmt.Sprintf("match:%d", input.MatchID)
	league := fmt.Sprintf("league:%d", input.LeagueID)
	leagueSlug := "league:" + input.LeagueKey
	home := fmt.Sprintf("team:%d", input.HomeTeamID)
	homeSlug := "team:" + input.HomeTeamKey
	away := fmt.Sprintf("team:%d", input.AwayTeamID)
	awaySlug := "team:" + input.AwayTeamKey
	market := "market:" + input.Market
	identity := itemKey(input.ItemType, input.ItemID)

	if signals.Favorites[match] {
		reasons = append(reasons, scoredReason{key: "favorite_match", text: "Saved match", interest: match, value: weight(opts.Weights, "favorite_match")})
	}
	if signals.Favorites[home] || signals.Favorites[away] || signals.Favorites[homeSlug] || signals.Favorites[awaySlug] {
		interest := home
		if signals.Favorites[away] || signals.Favorites[awaySlug] {
			interest = away
		} else if signals.Favorites[homeSlug] {
			interest = homeSlug
		}
		reasons = append(reasons, scoredReason{key: "favorite_team", text: "Features a favorite team", interest: interest, value: weight(opts.Weights, "favorite_team")})
	}
	if signals.Favorites[league] || signals.Favorites[leagueSlug] {
		reasons = append(reasons, scoredReason{key: "favorite_league", text: "From a favorite league", interest: league, value: weight(opts.Weights, "favorite_league")})
	}
	for _, key := range []string{match, home, homeSlug, away, awaySlug, league, leagueSlug, market} {
		if interestWeight := signals.Interests[key]; interestWeight != 0 {
			reasons = append(reasons, scoredReason{key: "explicit_interest", text: "Matches your selected interests", interest: key, value: interestWeight})
		}
		if signals.RecentSearches[key] {
			reasons = append(reasons, scoredReason{key: "recent_search", text: "Related to a recent search", interest: key, value: weight(opts.Weights, "recent_search")})
		}
	}
	if signals.Interests[market] != 0 {
		reasons = append(reasons, scoredReason{key: "preferred_market", text: "Matches a preferred market", interest: market, value: weight(opts.Weights, "preferred_market")})
	}
	if input.Confidence >= opts.HighConfidenceThreshold {
		reasons = append(reasons, scoredReason{key: "high_confidence", text: "High-confidence model signal", value: weight(opts.Weights, "high_confidence")})
	}
	untilStart := input.StartsAt.Sub(opts.Now)
	if untilStart >= 0 && untilStart <= opts.StartingSoonWindow {
		reasons = append(reasons, scoredReason{key: "starting_soon", text: "Starting soon", value: weight(opts.Weights, "starting_soon")})
	}
	if affinity := signals.RecentEngagement[identity]; affinity > 0 {
		reasons = append(reasons, scoredReason{key: "recent_engagement", text: "Related to recent activity", value: weight(opts.Weights, "recent_engagement") * min(affinity, 1)})
	}
	if signals.Viewed[identity] {
		reasons = append(reasons, scoredReason{key: "already_viewed", text: "Previously viewed", value: weight(opts.Weights, "already_viewed")})
	}
	return reasons
}

func weight(weights map[string]float64, key string) float64 {
	return weights[key]
}

func itemKey(itemType, itemID string) string {
	return itemType + ":" + itemID
}

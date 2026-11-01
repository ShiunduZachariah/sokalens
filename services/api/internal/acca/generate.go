package acca

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// On-demand composition backs the generator page. The precomputed inventory is
// still searched first; this only runs when that search returns nothing, which
// is common for the narrow corners of the filter grid. The background composer
// draws legs at random and throws away anything outside its odds window, so a
// 20-leg slip inside 10..1000 combined odds needs an average leg near 1.25 and
// almost never survives that lottery -- while the vetted pool holds hundreds of
// selections that satisfy it. Composing here is SQL over prepared rows plus
// CPU: no provider, Python, or AI call enters the request path, and nothing is
// written back to the inventory.
const (
	generateCandidateLimit = 800
	generateMaxAttempts    = 48
	generateBeamWidth      = 96
	generateBeamChoices    = 16
	generateConcurrency    = 2
	// A leg pool this small cannot be re-cut into a different slip, so the
	// jitter that varies repeated searches is pointless below it.
	generateJitterWindow = 6
)

// generateBudget bounds the whole fallback. The inventory answer is already in
// hand by the time this runs, so composing must never be what makes a page
// slow: past this the caller keeps the empty inventory result rather than
// spending the request's remaining budget here. The browse surfaces reach this
// path only on a filter corner with no stored slip, but that is exactly the
// case where the candidate query is least predictable.
const generateBudget = 1500 * time.Millisecond

// Held back from the request's remaining time so the response can still be
// written after composing returns.
const generateDeadlineMargin = 250 * time.Millisecond

// Below this there is not enough time to read the candidate pool, let alone
// compose from it.
const generateMinimumBudget = 200 * time.Millisecond

// generateBudgetFor is how long composing may run, and whether it is worth
// starting at all.
//
// Separated from generate so it can be tested without a database: the bug it
// exists to prevent only appears when the request has already spent most of its
// time, which is exactly the state that is awkward to reach through the query
// path.
func generateBudgetFor(ctx context.Context, now time.Time) (time.Duration, bool) {
	budget := generateBudget
	if deadline, ok := ctx.Deadline(); ok {
		// Leave room for the caller to serialise its response after this
		// returns. Without the margin the request finishes at exactly the
		// deadline, which is the same race in a thinner disguise.
		remaining := deadline.Sub(now) - generateDeadlineMargin
		if remaining < budget {
			budget = remaining
		}
	}
	// Below the floor there is no point starting: the candidate read alone will
	// not finish, and a cancelled attempt costs the same as a skipped one
	// without the chance of an answer.
	if budget < generateMinimumBudget {
		return 0, false
	}
	return budget, true
}

// Request-time generation is optional enrichment. Never let anonymous bursts
// turn it into a database-pool or CPU queue: excess requests keep the already
// computed inventory response instead of waiting for a slot.
var generateSlots = make(chan struct{}, generateConcurrency)

// GeneratedOrigin marks a slip composed for one request. It is not stored, so
// its id must never be treated as an inventory key.
const GeneratedOrigin = "on_demand"

// candidate is one vetted selection that may become a leg. It carries the same
// display fields as an inventory leg so a generated slip serialises identically.
type candidate struct {
	Leg
	Probability float64
	Rank        float64
	LogOdds     float64
}

type generateIntent struct {
	Legs                     int
	MinFairOdds, MaxFairOdds float64
	Tab, Period              string
	Seed                     int64
	Now                      time.Time
}

// generate composes a single slip for the query, or reports that the filters
// genuinely cannot be satisfied by the current pool.
func (s *Source) generate(ctx context.Context, q Query) (Slip, bool, error) {
	if s == nil || s.db == nil {
		return Slip{}, false, nil
	}
	legs := 5
	if q.Legs != nil {
		legs = *q.Legs
	}
	if legs < 2 || legs > 20 {
		return Slip{}, false, nil
	}
	minOdds, maxOdds := 1.0, 1000.0
	if q.MinFairOdds != nil && *q.MinFairOdds > minOdds {
		minOdds = *q.MinFairOdds
	}
	if q.MaxFairOdds != nil && *q.MaxFairOdds > 0 && *q.MaxFairOdds < maxOdds {
		maxOdds = *q.MaxFairOdds
	}
	if minOdds > maxOdds {
		return Slip{}, false, nil
	}
	// The budget is whatever is left of the request, capped at generateBudget --
	// not generateBudget flat.
	//
	// Flat was the bug. This runs after the inventory query has already spent
	// part of the request, so a 1500ms budget on top of a 1400ms read overruns
	// API_REQUEST_TIMEOUT (2750ms); the handler's context is cancelled, List
	// returns a context error and the endpoint answers 503. Production logged
	// exactly that: duration_ms=2750, duration_ms=2751, on the narrow corners
	// of the filter grid -- 5 legs between 70 and 100 fair odds -- which are
	// precisely the queries that reach this path because no stored slip fits.
	//
	// Giving up early is the documented behaviour of this function: the
	// inventory answer is already in hand, and the caller keeps its empty
	// result. An empty result is a page that says no ACCA matched. A 503 is a
	// page that says the feature is broken.
	budget, worth := generateBudgetFor(ctx, time.Now())
	if !worth {
		return Slip{}, false, nil
	}

	bounded, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	select {
	case generateSlots <- struct{}{}:
		defer func() { <-generateSlots }()
	default:
		return Slip{}, false, nil
	}
	pool, err := s.candidates(bounded, q)
	if err != nil {
		return Slip{}, false, err
	}
	intent := generateIntent{
		Legs: legs, MinFairOdds: minOdds, MaxFairOdds: maxOdds,
		Tab: normalizedGenerateTab(q.Tab), Period: q.Period, Seed: seedValue(q.Seed), Now: time.Now().UTC(),
	}
	slip, ok := composeOnDemandContext(bounded, pool, intent)
	return slip, ok, nil
}

// candidates reads the vetted prediction pool under the same geographic and
// market filters the inventory search used. One row per fixture and market
// keeps a single slip from leaning on one fixture's correlated selections.
func (s *Source) candidates(ctx context.Context, q Query) ([]candidate, error) {
	where := []string{"v.kickoff_at > now()", "v.market_key <> 'correct_score'", "v.probability > 0", "v.probability < 1"}
	args := []any{}
	next := func() int { return len(args) + 1 }
	if markets := tabMarkets(q.Tab); len(markets) > 0 {
		placeholders := make([]string, 0, len(markets))
		for _, market := range markets {
			placeholders = append(placeholders, fmt.Sprintf("$%d", next()))
			args = append(args, market)
		}
		where = append(where, fmt.Sprintf("v.market_key IN (%s)", strings.Join(placeholders, ",")))
	}
	if q.Selection != "" {
		where = append(where, fmt.Sprintf("v.selection=$%d", next()))
		args = append(args, q.Selection)
	}
	if q.Country != "" && q.Country != "all" {
		where = append(where, fmt.Sprintf("lower(lg.country_code)=lower($%d)", next()))
		args = append(args, q.Country)
	}
	if q.League != "" && q.League != "all" {
		where = append(where, fmt.Sprintf("(lower(lg.name)=lower($%d) OR cast(lg.id as text)=$%d)", next(), next()+1))
		args = append(args, q.League, q.League)
	}
	query := fmt.Sprintf(`WITH ranked AS (
 SELECT v.prediction_source,v.prediction_id,v.match_id,v.market_key,v.selection,v.probability,v.fair_odds,v.ranking_score,v.kickoff_at,
  ht.name AS home_team,at.name AS away_team,COALESCE(ht.logo_url,'') AS home_team_logo,COALESCE(at.logo_url,'') AS away_team_logo,
  lg.name AS league,COALESCE(lg.country_code,'') AS country,
  ROW_NUMBER() OVER (PARTITION BY v.match_id,v.market_key ORDER BY v.ranking_score DESC,v.prediction_id) AS fixture_rank
 FROM %s.eligible_acca_predictions_v v
 JOIN matches m ON m.id=v.match_id
 JOIN teams ht ON ht.id=m.home_team_id
 JOIN teams at ON at.id=m.away_team_id
 JOIN leagues lg ON lg.id=m.league_id
 WHERE %s
)
SELECT prediction_source,prediction_id,match_id,market_key,selection,probability,fair_odds,ranking_score,kickoff_at,home_team,away_team,home_team_logo,away_team_logo,league,country
FROM ranked WHERE fixture_rank=1
ORDER BY ranking_score DESC,kickoff_at,match_id,market_key
LIMIT %d`, s.schema, strings.Join(where, " AND "), generateCandidateLimit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]candidate, 0, generateCandidateLimit)
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.Source, &c.PredictionID, &c.MatchID, &c.Market, &c.Selection, &c.Probability, &c.FairOdds,
			&c.Rank, &c.KickoffAt, &c.HomeTeam, &c.AwayTeam, &c.HomeTeamLogo, &c.AwayTeamLogo, &c.League, &c.Country); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// composeOnDemand picks legs whose combined fair odds land inside the requested
// window. Unlike the background composer it aims at the window instead of
// sampling and hoping: each slot takes the leg closest to the per-slot odds the
// remaining slots still need, which is what makes long high-odds slips
// reachable at all.
func composeOnDemand(pool []candidate, intent generateIntent) (Slip, bool) {
	return composeOnDemandContext(context.Background(), pool, intent)
}

func composeOnDemandContext(ctx context.Context, pool []candidate, intent generateIntent) (Slip, bool) {
	if intent.Legs < 2 || intent.Legs > 20 || intent.MinFairOdds > intent.MaxFairOdds {
		return Slip{}, false
	}
	if intent.Now.IsZero() {
		intent.Now = time.Now().UTC()
	}
	eligible := eligibleCandidates(pool, intent)
	if !feasible(eligible, intent) {
		return Slip{}, false
	}
	minLog, maxLog := math.Log(math.Max(intent.MinFairOdds, 1)), math.Log(intent.MaxFairOdds)
	target := (minLog + maxLog) / 2
	for attempt := 0; attempt < generateMaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return Slip{}, false
		}
		// Every attempt but the last picks among near-equal legs using the
		// seed, so "Show another" returns a different slip while the same
		// search stays reproducible. The last attempt drops the jitter and
		// takes the strict best fit, which is the composition most likely to
		// land inside a tight window.
		legs := pickLegs(eligible, intent, target, attempt, attempt < generateMaxAttempts-1)
		if len(legs) != intent.Legs {
			continue
		}
		total := 0.0
		for _, leg := range legs {
			total += math.Log(leg.FairOdds)
		}
		if total < minLog || total > maxLog {
			continue
		}
		return assembleSlip(legs, intent), true
	}
	// A greedy nearest-leg walk can miss a valid asymmetric combination (for
	// example, two long/short legs that multiply into the window while several
	// medium-priced decoys do not). Keep a small, deterministic beam as the
	// bounded fallback so those reachable corners are not falsely rejected.
	if legs := beamSearchLegs(ctx, eligible, intent, target, minLog, maxLog); len(legs) == intent.Legs {
		return assembleSlip(legs, intent), true
	}
	return Slip{}, false
}

type beamState struct {
	legs []Leg
	used map[int64]struct{}
	sum  float64
}

func beamSearchLegs(ctx context.Context, eligible []candidate, intent generateIntent, target, minLog, maxLog float64) []Leg {
	states := []beamState{{used: make(map[int64]struct{}, intent.Legs)}}
	for slot := 0; slot < intent.Legs; slot++ {
		if ctx.Err() != nil {
			return nil
		}
		next := make([]beamState, 0, len(states)*generateBeamChoices)
		for _, state := range states {
			if ctx.Err() != nil {
				return nil
			}
			need := (target - state.sum) / float64(intent.Legs-slot)
			for _, choice := range beamCandidates(eligible, state.used, need) {
				used := make(map[int64]struct{}, len(state.used)+1)
				for matchID := range state.used {
					used[matchID] = struct{}{}
				}
				used[choice.MatchID] = struct{}{}
				legs := append(append(make([]Leg, 0, len(state.legs)+1), state.legs...), choice.Leg)
				next = append(next, beamState{legs: legs, used: used, sum: state.sum + choice.LogOdds})
			}
		}
		if len(next) == 0 {
			return nil
		}
		filled := slot + 1
		sort.SliceStable(next, func(i, j int) bool {
			left := math.Abs(next[i].sum - target*float64(filled)/float64(intent.Legs))
			right := math.Abs(next[j].sum - target*float64(filled)/float64(intent.Legs))
			return left < right
		})
		if len(next) > generateBeamWidth {
			next = next[:generateBeamWidth]
		}
		states = next
	}
	for _, state := range states {
		if state.sum >= minLog && state.sum <= maxLog {
			return state.legs
		}
	}
	return nil
}

func beamCandidates(eligible []candidate, used map[int64]struct{}, need float64) []candidate {
	type scored struct {
		item     candidate
		distance float64
	}
	nearest := make([]scored, 0, generateBeamChoices)
	for _, item := range eligible {
		if _, exists := used[item.MatchID]; exists {
			continue
		}
		entry := scored{item: item, distance: math.Abs(item.LogOdds - need)}
		position := sort.Search(len(nearest), func(i int) bool { return nearest[i].distance > entry.distance })
		if position >= generateBeamChoices-2 {
			continue
		}
		nearest = append(nearest, scored{})
		copy(nearest[position+1:], nearest[position:])
		nearest[position] = entry
		if len(nearest) > generateBeamChoices-2 {
			nearest = nearest[:generateBeamChoices-2]
		}
	}
	out := make([]candidate, 0, generateBeamChoices)
	seen := make(map[string]struct{}, generateBeamChoices)
	add := func(item candidate) {
		key := fmt.Sprintf("%d:%s:%s", item.MatchID, item.Market, item.Selection)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	for _, entry := range nearest {
		add(entry.item)
	}
	for _, edge := range []struct{ index, step int }{{0, 1}, {len(eligible) - 1, -1}} {
		for index := edge.index; index >= 0 && index < len(eligible); index += edge.step {
			item := eligible[index]
			if _, exists := used[item.MatchID]; !exists {
				add(item)
				break
			}
		}
	}
	return out
}

func eligibleCandidates(pool []candidate, intent generateIntent) []candidate {
	out := make([]candidate, 0, len(pool))
	for _, c := range pool {
		if c.MatchID <= 0 || c.Market == "" || c.Selection == "" || !finite(c.Probability) || c.Probability <= 0 || c.Probability >= 1 {
			continue
		}
		if !finitePositive(c.FairOdds) {
			c.FairOdds = 1 / c.Probability
		}
		// A leg must clear the stored-odds floor and the ceiling the read path
		// rejects slips on, or the slip would be dropped after composition.
		if c.FairOdds <= 1.000001 || c.FairOdds > 1000 || c.KickoffAt.IsZero() || !c.KickoffAt.After(intent.Now) {
			continue
		}
		if !tabAllowsMarket(intent.Tab, c.Market) {
			continue
		}
		c.LogOdds = math.Log(c.FairOdds)
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].LogOdds != out[j].LogOdds {
			return out[i].LogOdds < out[j].LogOdds
		}
		return out[i].MatchID < out[j].MatchID
	})
	return out
}

// feasible answers whether any selection of distinct fixtures could land in the
// window. When it cannot, the filters really are empty and the caller says so
// rather than searching 48 times for something that does not exist.
func feasible(eligible []candidate, intent generateIntent) bool {
	cheapest, priciest := 0.0, 0.0
	seenLow, seenHigh := map[int64]bool{}, map[int64]bool{}
	low, high := 0, 0
	for _, c := range eligible {
		if low == intent.Legs || seenLow[c.MatchID] {
			continue
		}
		seenLow[c.MatchID] = true
		cheapest += c.LogOdds
		low++
	}
	for i := len(eligible) - 1; i >= 0 && high < intent.Legs; i-- {
		if seenHigh[eligible[i].MatchID] {
			continue
		}
		seenHigh[eligible[i].MatchID] = true
		priciest += eligible[i].LogOdds
		high++
	}
	if low < intent.Legs || high < intent.Legs {
		return false
	}
	return cheapest <= math.Log(intent.MaxFairOdds) && priciest >= math.Log(math.Max(intent.MinFairOdds, 1))
}

func pickLegs(eligible []candidate, intent generateIntent, target float64, attempt int, jitter bool) []Leg {
	random := rand.New(rand.NewSource(intent.Seed + int64(attempt)))
	used := make(map[int64]bool, intent.Legs)
	families := map[string]struct{}{}
	wanted := requiredFamilies(intent.Legs)
	legs := make([]Leg, 0, intent.Legs)
	accumulated := 0.0
	for slot := 0; slot < intent.Legs; slot++ {
		need := (target - accumulated) / float64(intent.Legs-slot)
		// Market spread is best effort, not a rejection rule: a country or
		// league filter can leave a pool with one family in it, and refusing
		// there would put the empty state back on screen for a pool that can
		// answer the question.
		choice, ok := nearest(eligible, used, need, random, jitter, families, len(families) < wanted)
		if !ok {
			choice, ok = nearest(eligible, used, need, random, jitter, families, false)
		}
		if !ok {
			return nil
		}
		used[choice.MatchID] = true
		families[marketFamily(choice.Market)] = struct{}{}
		accumulated += choice.LogOdds
		legs = append(legs, choice.Leg)
	}
	return legs
}

// nearest returns the unused leg whose odds sit closest to what this slot needs.
// With newFamily set it only considers markets the slip has not used yet.
func nearest(eligible []candidate, used map[int64]bool, need float64, random *rand.Rand, jitter bool, families map[string]struct{}, newFamily bool) (candidate, bool) {
	type scored struct {
		item     candidate
		distance float64
	}
	best := make([]scored, 0, generateJitterWindow)
	for _, c := range eligible {
		if used[c.MatchID] {
			continue
		}
		if newFamily {
			if _, seen := families[marketFamily(c.Market)]; seen {
				continue
			}
		}
		entry := scored{item: c, distance: math.Abs(c.LogOdds - need)}
		position := sort.Search(len(best), func(i int) bool { return best[i].distance > entry.distance })
		if position >= generateJitterWindow {
			continue
		}
		best = append(best, scored{})
		copy(best[position+1:], best[position:])
		best[position] = entry
		if len(best) > generateJitterWindow {
			best = best[:generateJitterWindow]
		}
	}
	if len(best) == 0 {
		return candidate{}, false
	}
	if !jitter || len(best) == 1 {
		return best[0].item, true
	}
	return best[random.Intn(len(best))].item, true
}

func assembleSlip(legs []Leg, intent generateIntent) Slip {
	sort.SliceStable(legs, func(i, j int) bool {
		if !legs[i].KickoffAt.Equal(legs[j].KickoffAt) {
			return legs[i].KickoffAt.Before(legs[j].KickoffAt)
		}
		return legs[i].MatchID < legs[j].MatchID
	})
	odds, rank := 1.0, 0.0
	earliest, latest := legs[0].KickoffAt, legs[0].KickoffAt
	digest := sha256.New()
	for index := range legs {
		legs[index].Position = index
		odds *= legs[index].FairOdds
		rank += 1 / legs[index].FairOdds
		if legs[index].KickoffAt.Before(earliest) {
			earliest = legs[index].KickoffAt
		}
		if legs[index].KickoffAt.After(latest) {
			latest = legs[index].KickoffAt
		}
		fmt.Fprintf(digest, "%d:%s:%s|", legs[index].MatchID, legs[index].Market, legs[index].Selection)
	}
	period := intent.Period
	if period == "" || period == "all" {
		period = "seven_days"
	}
	return Slip{
		ID:               "generated-" + hex.EncodeToString(digest.Sum(nil))[:24],
		MarketTab:        intent.Tab,
		Period:           period,
		LegCount:         len(legs),
		CombinedFairOdds: odds,
		RankingScore:     rank / float64(len(legs)),
		OddsBand:         oddsBand(odds),
		EarliestKickoff:  earliest,
		LatestKickoff:    latest,
		Legs:             legs,
		Origin:           GeneratedOrigin,
	}
}

func normalizedGenerateTab(tab string) string {
	if tab == "" || tab == "all" {
		return "mixed"
	}
	return tab
}

// tabMarkets and tabAllowsMarket mirror the worker composer's tab mapping so a
// generated slip belongs to the same tab the inventory would have filed it under.
func tabMarkets(tab string) []string {
	switch tab {
	case "", "all", "mixed":
		return nil
	case "over_2_5":
		return []string{"ou_2_5", "total_goals"}
	case "corners":
		return []string{"corners", "total_corners"}
	case "cards":
		return []string{"cards", "total_cards"}
	default:
		return []string{tab}
	}
}

func tabAllowsMarket(tab, market string) bool {
	markets := tabMarkets(tab)
	if len(markets) == 0 {
		return true
	}
	for _, allowed := range markets {
		if allowed == market {
			return true
		}
	}
	return false
}

func marketFamily(market string) string {
	switch market {
	case "1x2", "double_chance", "draw_no_bet", "match_result":
		return "result"
	case "btts":
		return "btts"
	case "ou_2_5", "total_goals", "team_ou_goals", "team_over_under_goals":
		return "goals"
	case "corners", "total_corners", "team_corners_home", "team_corners_away":
		return "corners"
	case "cards", "total_cards":
		return "cards"
	}
	return market
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

func oddsBand(odds float64) string {
	for index, ceiling := range []float64{3, 5, 10, 25, 50, 100, 250, 500, 1000} {
		if odds <= ceiling {
			return fmt.Sprintf("B%d", index+1)
		}
	}
	return "B9"
}

// seedValue turns the caller's opaque seed into a reproducible int64 so the
// same search returns the same slip while a new search returns a fresh one.
func seedValue(seed string) int64 {
	if seed == "" {
		return 1
	}
	digest := sha256.Sum256([]byte(seed))
	value := int64(0)
	for _, b := range digest[:8] {
		value = value<<8 | int64(b)
	}
	if value < 0 {
		value = -value
	}
	return value
}

// Short because nothing waits on this write, and a slow one should be
// abandoned rather than queued behind the next.
const demandWriteTimeout = 2 * time.Second

// recordDemand notes a filter corner the inventory could not answer.
//
// The precomputed inventory cannot cover the whole grid -- 12 market tabs by
// 19 leg counts by the reachable odds windows is more cells than the 3,000
// active slips the readiness gate allows -- and composing at request time
// cannot cover the gap either, being capped at two concurrent by design. So
// the misses are recorded and the background composer stocks what readers
// actually ask for, instead of what someone guessed they would.
//
// Fire-and-forget, on its own context. The reader is already being told
// nothing was found; making that answer slower, or failing it because the
// demand table was busy, helps no one. It cannot use the request's context
// either: the compose budget above has consumed it by construction, so the
// write would be cancelled before it ran -- losing exactly the signal worth
// keeping.
func (s *Source) recordDemand(q Query) {
	if s == nil || s.db == nil {
		return
	}
	legs := 0
	if q.Legs != nil {
		legs = *q.Legs
	}
	if legs < 2 || legs > 20 {
		// Outside what any inventory can hold, so recording it would ask the
		// composer to chase something it must refuse.
		return
	}
	minOdds, maxOdds := 1.0, 1000.0
	if q.MinFairOdds != nil && *q.MinFairOdds > minOdds {
		minOdds = *q.MinFairOdds
	}
	if q.MaxFairOdds != nil && *q.MaxFairOdds > 0 && *q.MaxFairOdds < maxOdds {
		maxOdds = *q.MaxFairOdds
	}
	if minOdds >= maxOdds {
		return
	}
	tab := strings.ToLower(strings.TrimSpace(q.Tab))
	if tab == "" {
		tab = "mixed"
	}
	period := strings.ToLower(strings.TrimSpace(q.Period))
	if period == "" {
		period = "seven_days"
	}

	db, schema := s.db, s.schema
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), demandWriteTimeout)
		defer cancel()
		// satisfied_at clears on a repeat miss: inventory expires, so a corner
		// stocked yesterday and missing today is outstanding again.
		_, _ = db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s.acca_demand (market_tab, leg_count, min_fair_odds, max_fair_odds, period)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (market_tab, leg_count, min_fair_odds, max_fair_odds, period) DO UPDATE
SET request_count = acca_demand.request_count + 1,
    last_seen_at  = now(),
    satisfied_at  = NULL`, schema), tab, legs, minOdds, maxOdds, period)
	}()
}

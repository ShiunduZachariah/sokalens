package acca

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

const GenerateInventoryJobKey = "acca.generate-inventory"

// GenerationFollowUp refreshes inventory only after the source prediction job
// has committed its changes. The source job ID keeps retries idempotent while
// allowing a later generation in the same schedule bucket to refresh again.
func GenerationFollowUp(source jobs.Job, now time.Time) jobs.NewJob {
	return jobs.NewJob{
		JobKey: GenerateInventoryJobKey, Queue: "acca", Priority: 40,
		Payload:              []byte(`{"target":3000,"period":"seven_days","intent":"core"}`),
		IdempotencyKey:       fmt.Sprintf("followup:acca:prediction-generation:%d", source.ID),
		DeduplicateCompleted: true, RunAfter: now.UTC(),
	}
}

type CandidateSource interface {
	EligibleCandidates(context.Context, time.Time) ([]Candidate, error)
}
type InventoryRepository interface {
	Expire(context.Context) (sql.Result, error)
	InvalidateStale(context.Context) (sql.Result, error)
	UpsertBatch(context.Context, string, string, []Slip) error
	Prune(context.Context, time.Duration, int) (int64, error)
}

// Slips that are no longer active are kept this long before deletion.
//
// Six hours, and the earlier reasoning for three days is worth keeping because
// it was right about everything except the cost that mattered. An 'invalid'
// slip was never servable, so its only remaining value is explaining a bad
// generation run -- a thing you investigate the same day or not at all. Six
// hours is still the same day.
//
// What that reasoning priced was storage and the readiness gate, and it
// accepted "settles the table around 180k rows" as a good outcome. It is not,
// because of a cost it did not consider: /v1/accas builds country and league
// facets by walking every leg of the active pool, and the active rows are
// scattered through whatever else the table holds. At 200,571 slips and
// 1,707,869 legs -- 98.7% of them dead -- that query took 3,456ms cold against
// an API_REQUEST_TIMEOUT of 2750ms, and 62-86ms warm. So it failed or not
// depending purely on whether the pages were still in shared_buffers, which is
// why production saw 111 failures against 108 successes over ten hours and a
// visible "ACCA inventory is temporarily unavailable" on any uncached request.
//
// Three days also happened to be almost exactly the age of the table, so not
// one row had ever become eligible: the oldest non-active slip was 68.6 hours
// old when this was measured, 3.4 hours short of the cutoff. The prune was
// correct and had simply never had anything to do.
//
// At the observed rate -- 51,198 slips on 2026-09-19, 62,880 on the 20th,
// 80,754 on the 21st -- six hours settles the table near 7,700 rows against a
// bounded active pool of 3,000. Small enough to stay resident, which is what
// makes the facet query's cost predictable rather than a coin flip.
const (
	DefaultRetainNonActiveFor = 6 * time.Hour
	defaultPruneBatch         = 5000

	// Odds windows grow by this factor. See oddsTargetsFor.
	inventoryOddsWindowStep = 2.2
	// The schema caps combined_fair_odds at 1000.
	maxInventoryFairOdds = 1000.0
	// Beyond this the windows at a given leg count stop being reachable with
	// the legs the pool actually holds, and the attempts are wasted.
	maxInventoryOddsWindows = 5
)

type Job struct {
	Source              CandidateSource
	Repository          InventoryRepository
	Target              int
	Legs                int
	Period, Tab, Intent string
	Now                 func() time.Time
	// Zero means DefaultRetainNonActiveFor.
	RetainNonActiveFor time.Duration
}

type payload struct {
	Target, Legs        int
	Period, Tab, Intent string
	Seed                int64
}

// oddsTargetsFor is the set of odds windows worth composing at this many legs.
//
// The inventory used to hug the standards floor. The composer ranks candidates
// by confidence, so left without an odds target it returns the most confident
// slip that clears MinimumSystemCombinedFairOdds -- and it clears it barely.
// Measured on production: every 5-leg slip in a 3,000-slip inventory landed
// between 2.76 and 3.55, against a floor of 2.76. Two-leg slips spanned 1.50
// to 1.66. Nineteen leg counts produced 201 stocked combinations out of a
// 1,596-cell grid, and a reader asking for five legs between 70 and 100 got
// nothing -- not because it is impossible, but because nothing had ever tried.
//
// It is reachable: the eligible pool holds 4,553 legs between 2.0 and 3.0 and
// 825 beyond 11.0, so five legs averaging 2.34 make 70. The gap was never the
// data.
//
// Windows are multiplicative, because combined odds are. Each is roughly
// 2.2x the last, which is wide enough that the composer can land inside one
// without an exhaustive search and narrow enough that consecutive windows sit
// in different odds bands.
//
// Windows below the floor are dropped rather than clamped: clamping would
// point several of them at the same reachable range and re-create the
// clustering this exists to break.
func oddsTargetsFor(legs int) [][2]float64 {
	floor := MinimumSystemCombinedFairOdds(legs)
	windows := make([][2]float64, 0, 6)
	for low := floor; low < maxInventoryFairOdds; low *= inventoryOddsWindowStep {
		high := low * inventoryOddsWindowStep
		if high > maxInventoryFairOdds {
			high = maxInventoryFairOdds
		}
		windows = append(windows, [2]float64{low, high})
		if len(windows) == maxInventoryOddsWindows {
			break
		}
	}
	if len(windows) == 0 {
		windows = append(windows, [2]float64{floor, maxInventoryFairOdds})
	}
	return windows
}

// composedInWindow reports whether anything was actually composed inside the
// corner that was asked for.
//
// Compose already filters to the intent's range, so this is a guard against
// the case where it returned nothing at all -- and a statement of what
// "satisfied" has to mean, which is not "the composer was asked".
func composedInWindow(slips []Slip, intent Intent) bool {
	for _, slip := range slips {
		if slip.CombinedFairOdds >= intent.MinFairOdds && slip.CombinedFairOdds <= intent.MaxFairOdds {
			return true
		}
	}
	return false
}

// demandGenerationIntent marks slips composed because a reader asked for them
// rather than because the grid said so. Kept distinct so the two populations
// can be told apart when judging whether the grid is well shaped.
const demandGenerationIntent = "demand"

// maxDemandIntentsPerRun bounds how much of a run is spent chasing demand.
//
// Without a bound, a burst of unusual searches -- or a crawler walking the
// filter form -- would consume the whole run and starve the grid that serves
// everybody else. Ten is enough to keep up with real traffic between runs and
// small enough that a strange day cannot take the inventory over.
const maxDemandIntentsPerRun = 10

// demandIntents turns recorded misses into composition targets.
//
// Failure here is deliberately silent: the demand table is an optimisation,
// and a run that cannot read it should still stock the grid rather than fail.
func demandIntents(ctx context.Context, repo InventoryRepository, now time.Time) []Intent {
	source, ok := repo.(DemandSource)
	if !ok {
		return nil
	}
	rows, err := source.OutstandingDemand(ctx, maxDemandIntentsPerRun)
	if err != nil || len(rows) == 0 {
		return nil
	}
	out := make([]Intent, 0, len(rows))
	for _, row := range rows {
		out = append(out, Intent{
			// One slip per corner. The point is that the corner stops being
			// empty, not that it becomes deep -- depth is what the grid is for.
			Target:           1,
			Legs:             row.Legs,
			Tab:              row.Tab,
			MinFairOdds:      row.MinFairOdds,
			MaxFairOdds:      row.MaxFairOdds,
			Period:           row.Period,
			GenerationIntent: demandGenerationIntent,
			Now:              now,
		})
	}
	return out
}

// DemandRow is one filter corner readers asked for and did not get.
type DemandRow struct {
	Tab                      string
	Legs                     int
	MinFairOdds, MaxFairOdds float64
	Period                   string
}

// DemandSource is implemented by repositories that can read the demand table.
// Optional so a repository without it -- the test fake, for one -- still
// drives the rest of the job.
type DemandSource interface {
	OutstandingDemand(ctx context.Context, limit int) ([]DemandRow, error)
	MarkDemandSatisfied(ctx context.Context, intent Intent)
}

// CoreIntents allocates a bounded core inventory across the public market
// tabs and supported leg counts. It deliberately does not multiply by
// countries or leagues; those remain retrieval filters over this shared pool.
func CoreIntents(target, legs int, period, intent string, tabs []string) []Intent {
	if target <= 0 {
		target = 300
	}
	if target > MaxActiveInventory {
		target = MaxActiveInventory
	}
	if period == "" {
		period = "today"
	}
	if intent == "" {
		intent = "core"
	}
	tabs = normalizedInventoryTabs(tabs)
	legCounts := make([]int, 0, 19)
	if legs >= 2 && legs <= 20 {
		legCounts = append(legCounts, legs)
	} else {
		for count := 2; count <= 20; count++ {
			legCounts = append(legCounts, count)
		}
	}
	// Odds is the third dimension. Tab and leg count were already spread
	// across; without this one the composer decides the odds by itself and
	// decides the same thing every time.
	type cell struct {
		legs int
		tab  string
		odds [2]float64
	}
	cells := make([]cell, 0, len(tabs)*len(legCounts)*maxInventoryOddsWindows)
	for _, legCount := range legCounts {
		for _, window := range oddsTargetsFor(legCount) {
			for _, tab := range tabs {
				cells = append(cells, cell{legs: legCount, tab: tab, odds: window})
			}
		}
	}
	if len(cells) == 0 {
		return nil
	}

	base, remainder := target/len(cells), target%len(cells)
	out := make([]Intent, 0, len(cells))
	for index, c := range cells {
		count := base
		if index < remainder {
			count++
		}
		if count == 0 {
			// More cells than slips. Spreading one slip each over a prefix
			// would stock the low-leg corners and leave the rest empty, which
			// is the shape being fixed, so the remainder walks the whole grid
			// instead.
			continue
		}
		out = append(out, Intent{
			Target:           count,
			Legs:             c.legs,
			Tab:              c.tab,
			MinFairOdds:      c.odds[0],
			MaxFairOdds:      c.odds[1],
			Period:           period,
			GenerationIntent: intent,
		})
	}
	return out
}

// InventoryTabs derives the public inventory dimensions from candidates which
// have already cleared the eligibility projection. Mixed stays available as a
// cross-market composition while each qualified market gets an exact tab.
func InventoryTabs(candidates []Candidate) []string {
	seen := make(map[string]struct{}, len(candidates))
	markets := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		market := strings.TrimSpace(candidate.Market)
		if market == "" || market == "mixed" || market == "correct_score" {
			continue
		}
		if _, ok := seen[market]; ok {
			continue
		}
		seen[market] = struct{}{}
		markets = append(markets, market)
	}
	sort.Strings(markets)
	return append([]string{"mixed"}, markets...)
}

func normalizedInventoryTabs(tabs []string) []string {
	seen := make(map[string]struct{}, len(tabs))
	out := make([]string, 0, len(tabs))
	for _, tab := range tabs {
		tab = strings.TrimSpace(tab)
		if tab == "" || tab == "all" || tab == "correct_score" {
			continue
		}
		if _, ok := seen[tab]; ok {
			continue
		}
		seen[tab] = struct{}{}
		out = append(out, tab)
	}
	if len(out) == 0 {
		return []string{"mixed"}
	}
	return out
}

func (j Job) Run(ctx context.Context, queued jobs.Job) error {
	if j.Source == nil || j.Repository == nil {
		return fmt.Errorf("acca inventory dependencies are required")
	}
	now := time.Now().UTC()
	if j.Now != nil {
		now = j.Now()
	}
	candidates, err := j.Source.EligibleCandidates(ctx, now)
	if err != nil {
		return err
	}
	p := payload{Target: j.Target, Legs: j.Legs, Period: j.Period, Tab: j.Tab, Intent: j.Intent}
	// Payload is deliberately optional for scheduled jobs; defaults keep the job bounded.
	if len(queued.Payload) > 0 && string(queued.Payload) != "{}" {
		if err := json.Unmarshal(queued.Payload, &p); err != nil {
			return err
		}
	}
	if p.Target == 0 {
		p.Target = 300
	}
	if p.Period == "" {
		p.Period = "today"
	}
	if p.Intent == "" {
		p.Intent = "core"
	}
	// Started slips must not occupy the bounded active pool while replacements
	// are being generated, including runs with no remaining candidates.
	if _, err := j.Repository.Expire(ctx); err != nil {
		return fmt.Errorf("expire ACCA inventory: %w", err)
	}
	if _, err := j.Repository.InvalidateStale(ctx); err != nil {
		return fmt.Errorf("invalidate stale ACCA inventory: %w", err)
	}
	if p.Tab != "" && p.Tab != "all" {
		if p.Legs < 2 || p.Legs > 20 {
			p.Legs = 5
		}
		slips := Compose(candidates, Intent{Target: p.Target, Legs: p.Legs, Tab: p.Tab, Seed: p.Seed, Now: now})
		if err := j.Repository.UpsertBatch(ctx, p.Period, p.Intent, slips); err != nil {
			return err
		}
	} else {
		intents := CoreIntents(p.Target, p.Legs, p.Period, p.Intent, InventoryTabs(candidates))
		// Corners readers actually asked for and did not get, composed before
		// the grid is swept. The grid is a guess about what people want; this
		// is a record of it, so it goes first and its slips are the ones most
		// likely to be wanted again.
		intents = append(demandIntents(ctx, j.Repository, now), intents...)
		for i, intent := range intents {
			intent.Now = now
			intent.Seed = p.Seed + int64(i)
			slips := Compose(candidates, intent)
			if err := j.Repository.UpsertBatch(ctx, intent.Period, intent.GenerationIntent, slips); err != nil {
				return err
			}
			if intent.GenerationIntent == demandGenerationIntent && composedInWindow(slips, intent) {
				// Stocked: stop asking for it. A later miss clears this again,
				// because inventory expires.
				//
				// Checked against the window rather than len(slips) > 0. A slip
				// can be composed and then retired by the overflow trim in the
				// same run, and marking the corner satisfied on composition
				// alone had acca_demand reporting 5 legs at 70-100 as stocked
				// while the endpoint served nothing for it.
				if source, ok := j.Repository.(DemandSource); ok {
					source.MarkDemandSatisfied(ctx, intent)
				}
			}
		}
	}
	// Predictions can be superseded after the candidate snapshot is loaded,
	// including while batches are published. Recheck the completed inventory so
	// those stale candidates do not remain active until the next scheduled run.
	if _, err := j.Repository.InvalidateStale(ctx); err != nil {
		return fmt.Errorf("invalidate stale ACCA inventory after generation: %w", err)
	}
	// Pruned last, and deliberately non-fatal: this is housekeeping on rows that
	// are already dead, so a failure here must not fail a run whose inventory was
	// generated correctly. Falling behind for one cycle costs a slower readiness
	// gate; failing the run costs the inventory itself.
	retainFor := j.RetainNonActiveFor
	if retainFor == 0 {
		retainFor = DefaultRetainNonActiveFor
	}
	// Non-fatal, but no longer silent. Discarding both return values is how a
	// prune that never deleted anything for three days went unnoticed while the
	// table grew to 200k rows and started timing out the ACCA endpoint.
	pruned, err := j.Repository.Prune(ctx, retainFor, defaultPruneBatch)
	if err != nil {
		slog.Warn("acca slip prune failed",
			"retain_for", retainFor.String(), "error", err)
	} else {
		slog.Info("acca slips pruned",
			"pruned", pruned, "retain_for", retainFor.String())
	}
	return nil
}

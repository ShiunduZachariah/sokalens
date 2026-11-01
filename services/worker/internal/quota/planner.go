package quota

import (
	"context"
	"database/sql"
	"time"
)

// Priority is the P0..P5 priority class of provider work.
type Priority int

const (
	P0 Priority = iota // live critical (scores, status, events)
	P1                 // imminent matches (lineups, odds, pre-match changes)
	P2                 // near future (upcoming fixtures, prediction inputs)
	P3                 // reference / maintenance (teams, leagues, metadata)
	P4                 // historical backfill
	P5                 // optional enrichment
)

// DemandSnapshot is the current fixture-calendar demand used by the planner.
type DemandSnapshot struct {
	Live int // in-play or half-time etc., excluding matches whose live claim has expired
	// LiveWatch is the calendar-driven polling window: any fixture that could
	// plausibly be in play right now, whether or not the database has noticed
	// yet.
	//
	// Live polling used to be gated on Live > 0, which is circular -- a match
	// is only recorded as live once a live poll observes it, and the live poll
	// only runs once a match is recorded as live. The loop was broken by the
	// periodic today-refresh, so kickoff was missed by up to that interval
	// every time. Deriving the window from kickoff_at instead removes the
	// dependency on the database already knowing.
	LiveWatch     int
	Prematch0_2h  int // kickoff within 2 hours
	Prematch2_6h  int // kickoff 2-6 hours out
	Prematch6_24h int // kickoff 6-24 hours out
	Tomorrow      int // kickoff 24-48 hours out
	Upcoming      int // kickoff 0-7 days out (excluding live)
}

// DemandSource produces the fixture demand the planner reasons about.
type DemandSource interface {
	Demand(ctx context.Context) (DemandSnapshot, error)
}

// PlannerConfig is the tunable quota-planning policy. Every value is an
// estimate; the planner corrects from actual usage as it runs.
type PlannerConfig struct {
	DailyLimit   int
	DailyReserve int
	MinuteLimit  int

	// Estimated provider requests per match per day.
	Prematch0_2hPerMatch  float64 // lineups + odds + status refreshes
	Prematch2_6hPerMatch  float64 // odds + prediction inputs
	Prematch6_24hPerMatch float64 // one-off refresh
	TomorrowPerMatch      float64 // low-frequency refresh
	PredictionPerUpcoming float64 // engine/feature refresh cost share

	// Live polling is bulk (fixtures?live=all = one request per poll), so the
	// forecast is driven by the healthy poll cadence and the hours left today.
	LivePollSeconds int // healthy live poll interval in seconds (e.g. 30)

	// The batched in-play statistics poll (fixtures?ids=).
	//
	// Unlike the live poll, this one is NOT O(1) in match count: it covers
	// twenty fixtures per request, so its cost scales with how many matches
	// are in play. At a 200-match weekend peak that is ten requests a minute,
	// about 600 an hour -- small against 75,000 a day, and not small enough to
	// leave out of the forecast. Omitted, it would spend from the same
	// allowance as the live poll with neither knowing, and the first symptom
	// would be live scores freezing on a Saturday afternoon, which is exactly
	// how the quota was exhausted on 2026-09-21.
	//
	// Projected as zero while the poll is disabled, which is its default: a
	// forecast that reserves budget for a job that is not running would
	// throttle everything else for nothing.
	InPlayStatisticsPollEnabled bool
	InPlayStatisticsPollSeconds int // poll interval in seconds (e.g. 60)
	InPlayStatisticsBatchSize   int // fixtures per request (provider cap: 20)

	// FixedDailyOverhead is the recurring schedule's own cost for a whole UTC
	// day, in requests: the date-scoped fixture sweeps, the odds sweeps, the
	// daily reference jobs, the hourly results reconcile and quota sync.
	//
	// It is here because leaving it out was a real error, not a rounding one.
	// Every other term in this forecast scales with the fixture calendar, and
	// the recurring schedule scales with nothing -- it costs the same on a
	// quiet Tuesday as on a 600-fixture Saturday. With defaults that is
	// roughly 290 requests a day for today's five-minute refresh alone, plus
	// 24 for tomorrow's hourly one, plus 54 for the other nine dates in the
	// window, plus odds and reference work: several hundred requests that the
	// planner did not know it had already committed.
	//
	// Against 75,000 that omission was invisible. Against 7,500 it is most of
	// the emergency reserve, and it was being handed to the pacer as surplus
	// to spend on backfill -- so the budget was overcommitted by design, and
	// the first thing to notice would have been live scores freezing.
	FixedDailyOverhead int

	EmergencyReserveFraction float64 // 0.10-0.20 of daily limit
	ForecastSafetyFactor     float64 // multiply projected demand (>= 1.0)

	LivePollIntervalHealthy  time.Duration
	LivePollIntervalPressure time.Duration

	// DailyBudgetSource lets the planner read current remaining quota.
	Remaining func() int
	// Limit reads the provider-confirmed daily allowance. It wins over a stale
	// environment value after an account changes plans.
	Limit func() int
	Now   func() time.Time
}

// DefaultPlannerConfig returns sane defaults for the 7,500/day package.
//
// The reserve is 900 (12%), not 4,000. Four thousand was correct for 75,000,
// where it is 5% of the day; carried unchanged to 7,500 it is 53%, and a
// governor that protects half the package protects it from us. The minute
// limit is 300, provider-confirmed, not the 450 the Ultra plan allowed. The number that matters
// is not the ratio either way -- it is that the reserve has to be large enough
// to cover live polling through the end of the day and small enough to leave a
// working platform, and at this package size that is a few hundred requests.
func DefaultPlannerConfig() PlannerConfig {
	return PlannerConfig{
		DailyLimit:   7500,
		DailyReserve: 900,
		MinuteLimit:  300,

		// Sixty seconds, down from thirty. This halves the single largest
		// recurring line in the budget -- a full day of polling is 1,440
		// requests at 60s against 2,880 at 30s, and on a Saturday watch
		// window that is the difference between 11% and 23% of everything we
		// have. For a predictions product the trade is slight: a score
		// arrives up to a minute later than it could, and nothing we publish
		// is graded on sub-minute latency.
		LivePollSeconds: 60,
		// Sixty seconds because that is how often the provider refreshes match
		// statistics, and twenty because that is the provider's cap on `ids=`.
		// Enabled is deliberately absent here: it defaults to false, and the
		// worker sets it from configuration.
		InPlayStatisticsPollSeconds: 60,
		InPlayStatisticsBatchSize:   20,

		// Per-match costs, corrected to what the schedule actually issues.
		//
		// The old numbers -- 10, 6, 4, 2, totalling 22.5 a match -- were an
		// estimate written before the jobs existed, and they describe a design
		// where each fixture is refreshed individually. That is not what runs.
		// Fixtures, odds and statuses are all fetched per date in bulk, one
		// request covering every match on the card, and those sweeps are
		// counted in FixedDailyOverhead instead. What genuinely scales per
		// match is the per-fixture provider-prediction fetch, and that is
		// capped at 200 a day by its own planner.
		//
		// This matters in both directions. At 22.5 a match the forecast said a
		// 500-fixture Saturday needed 11,250 requests, so the planner declared
		// critical pressure and paused everything discretionary on precisely
		// the days with the most to cover. Set too low, the pacer treats the
		// difference as surplus and spends it on backfill. These are measured
		// against the recurring schedule in planner.go; QUOTA_* env vars
		// override them, and the per-endpoint ledger is what should replace
		// them with observation.
		Prematch0_2hPerMatch:  1.0,
		Prematch2_6hPerMatch:  0.3,
		Prematch6_24hPerMatch: 0.1,
		TomorrowPerMatch:      0.05,
		PredictionPerUpcoming: 0.1,

		// Measured from the recurring schedule at its defaults: 288 today +
		// 24 tomorrow + 54 for the rest of the date window + 12 odds sweeps
		// and their pages + 24 results-reconcile + 24 quota-sync + the daily
		// reference jobs, rounded up for the odds pages the sweeps follow.
		FixedDailyOverhead: 900,

		EmergencyReserveFraction: 0.12,
		ForecastSafetyFactor:     1.2,

		LivePollIntervalHealthy:  60 * time.Second,
		LivePollIntervalPressure: 180 * time.Second,
	}
}

// Pressure is the current quota pressure level.
type Pressure int

const (
	PressureHealthy  Pressure = iota // P0..P5 allowed
	PressureModerate                 // P5 paused
	PressureHigh                     // P4+P5 paused
	PressureCritical                 // P0 + essential P1 only
)

// Decision is the planner output for one recalculation cycle.
type Decision struct {
	DailyLimit          int
	DailyRemaining      int
	ProjectedLive       int
	ProjectedPrematch   int
	ProjectedPrediction int
	ProjectedReference  int
	// ProjectedInPlayStats is the batched statistics poll's share. Reported
	// separately rather than folded into ProjectedLive because the two scale
	// differently -- one is flat in match count and the other is not -- and an
	// operator reading a tight forecast needs to see which one grew.
	ProjectedInPlayStats int
	// ProjectedFixed is the recurring schedule's remaining cost for today --
	// the share of FixedDailyOverhead still ahead of us. Reported separately
	// because it is the one line an operator cannot reduce by covering fewer
	// matches; it changes only by changing a cadence.
	ProjectedFixed               int
	EmergencyReserve             int
	SafeDiscretionary            int
	Pressure                     Pressure
	Allowed                      map[Priority]bool
	BackfillPaused               bool
	LivePollInterval             time.Duration
	InPlayStatisticsPollInterval time.Duration
	Reason                       string
	LiveMatches                  int
	// LiveWatch gates whether live polling is scheduled at all. It is separate
	// from LiveMatches, which counts what the database currently believes is
	// in play and drives the spend projection.
	LiveWatch int

	// BallastPerMinute is how many discretionary requests may be issued in the
	// next minute to spend the surplus down to zero by 00:00 UTC.
	//
	// Everything else on this struct is a permission; this is a target. The
	// distinction is the whole point of it. Permissions alone produced a
	// platform that spent 12,000 requests of a 75,000/day plan and destroyed
	// the rest at reset, because nothing was ever obliged to find work for the
	// quota it was allowed to use. See pacer.go.
	//
	// Zero means backfill is paused or there is no genuine surplus.
	BallastPerMinute int

	// MinutesLeftToday is the pacer's denominator, published so an operator
	// can reproduce the rate rather than having to trust it.
	MinutesLeftToday int

	// MinuteLimit is the provider's per-minute rate limit, carried here so the
	// pacer can cap against it. The daily budget and the minute limit are
	// independent constraints and a rate can satisfy one while breaking the
	// other.
	MinuteLimit int
}

// Planner converts the fixture calendar + remaining quota into a Decision.
type Planner struct {
	cfg    PlannerConfig
	demand DemandSource
}

// NewPlanner creates a planner. demand may be nil (yields zero demand) for
// tests that only exercise budget math.
func NewPlanner(cfg PlannerConfig, demand DemandSource) *Planner {
	if cfg.DailyLimit <= 0 {
		cfg.DailyLimit = 7500
	}
	if cfg.EmergencyReserveFraction <= 0 || cfg.EmergencyReserveFraction > 0.5 {
		cfg.EmergencyReserveFraction = 0.12
	}
	if cfg.ForecastSafetyFactor < 1.0 {
		cfg.ForecastSafetyFactor = 1.0
	}
	if cfg.LivePollIntervalHealthy <= 0 {
		cfg.LivePollIntervalHealthy = 60 * time.Second
	}
	if cfg.LivePollIntervalPressure <= 0 {
		cfg.LivePollIntervalPressure = 180 * time.Second
	}
	if cfg.Remaining == nil {
		cfg.Remaining = func() int { return cfg.DailyLimit }
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Planner{cfg: cfg, demand: demand}
}

// Plan recalculates the decision. demandErr is tolerated: on demand-query
// failure the planner falls back to a conservative decision (protect live).
func (p *Planner) Plan(ctx context.Context) Decision {
	demand := DemandSnapshot{}
	if p.demand != nil {
		if got, err := p.demand.Demand(ctx); err == nil {
			demand = got
		}
	}
	remaining := p.cfg.Remaining()
	if remaining < 0 {
		remaining = 0
	}
	limit := p.cfg.DailyLimit
	if p.cfg.Limit != nil && p.cfg.Limit() > 0 {
		limit = p.cfg.Limit()
	}
	// The Pro allowance cannot sustain a minute-by-minute poll for every
	// concurrent match. Keep the documented 20-ID batch and sample it every
	// five minutes, while retaining the old cadence on larger plans.
	statsInterval := time.Duration(p.cfg.InPlayStatisticsPollSeconds) * time.Second
	if statsInterval <= 0 {
		statsInterval = time.Minute
	}
	if limit <= 7500 && statsInterval < 5*time.Minute {
		statsInterval = 5 * time.Minute
	}
	liveHealthy := p.cfg.LivePollIntervalHealthy
	livePressure := p.cfg.LivePollIntervalPressure
	if limit <= 7500 {
		if liveHealthy < time.Minute {
			liveHealthy = time.Minute
		}
		if livePressure < 2*time.Minute {
			livePressure = 2 * time.Minute
		}
	}

	// Bulk live endpoint: one request per poll for the rest of the UTC day.
	pollSeconds := p.cfg.LivePollSeconds
	if pollSeconds <= 0 {
		pollSeconds = 60
	}
	if limit <= 7500 && pollSeconds < 60 {
		pollSeconds = 60
	}
	hoursLeft := 24.0 - float64(p.cfg.Now().UTC().Hour()) - float64(p.cfg.Now().UTC().Minute())/60.0
	if hoursLeft < 0 {
		hoursLeft = 0
	}
	projectedLive := 0
	if demand.LiveWatch > 0 || demand.Live > 0 {
		projectedLive = roundF(hoursLeft * 3600 / float64(pollSeconds))
	}
	projectedPrematch := roundF(
		float64(demand.Prematch0_2h)*p.cfg.Prematch0_2hPerMatch +
			float64(demand.Prematch2_6h)*p.cfg.Prematch2_6hPerMatch +
			float64(demand.Prematch6_24h)*p.cfg.Prematch6_24hPerMatch +
			float64(demand.Tomorrow)*p.cfg.TomorrowPerMatch,
	)
	projectedPrediction := roundF(float64(demand.Upcoming) * p.cfg.PredictionPerUpcoming)
	projectedInPlayStats := p.projectInPlayStatistics(demand, hoursLeft, statsInterval)
	// The recurring schedule's remaining share of the day. Pro-rated by hours
	// left rather than charged in full, because the morning's sweeps are
	// already spent and counted in `remaining`; charging the whole day's
	// overhead at 23:00 would invent a deficit out of money nobody is going to
	// spend.
	projectedFixed := 0
	if p.cfg.FixedDailyOverhead > 0 {
		projectedFixed = roundF(float64(p.cfg.FixedDailyOverhead) * hoursLeft / 24.0)
	}
	projected := projectedLive + projectedPrematch + projectedPrediction + projectedInPlayStats + projectedFixed
	projected = roundF(float64(projected) * p.cfg.ForecastSafetyFactor)

	emergencyReserve := roundF(float64(limit) * p.cfg.EmergencyReserveFraction)
	safeDiscretionary := remaining - projected - emergencyReserve
	if safeDiscretionary < 0 {
		safeDiscretionary = 0
	}

	decision := Decision{
		DailyLimit:                   limit,
		DailyRemaining:               remaining,
		ProjectedLive:                projectedLive,
		ProjectedPrematch:            projectedPrematch,
		ProjectedPrediction:          projectedPrediction,
		ProjectedInPlayStats:         projectedInPlayStats,
		ProjectedFixed:               projectedFixed,
		EmergencyReserve:             emergencyReserve,
		SafeDiscretionary:            safeDiscretionary,
		LiveMatches:                  demand.Live,
		LiveWatch:                    demand.LiveWatch,
		InPlayStatisticsPollInterval: statsInterval,
		Allowed:                      map[Priority]bool{},
	}

	// Pressure classification.
	switch {
	case remaining <= p.cfg.DailyReserve+emergencyReserve || projected >= remaining:
		decision.Pressure = PressureCritical
		decision.Reason = "critical_quota"
	case remaining-projected < p.cfg.DailyReserve:
		decision.Pressure = PressureHigh
		decision.Reason = "high_pressure"
	case safeDiscretionary < emergencyReserve:
		decision.Pressure = PressureModerate
		decision.Reason = "moderate_pressure"
	default:
		decision.Pressure = PressureHealthy
		decision.Reason = "healthy"
	}

	switch decision.Pressure {
	case PressureCritical:
		decision.Allowed[P0] = true
		decision.Allowed[P1] = true
		decision.BackfillPaused = true
	case PressureHigh:
		decision.Allowed[P0] = true
		decision.Allowed[P1] = true
		decision.Allowed[P2] = true
		decision.Allowed[P3] = true
		decision.BackfillPaused = true
	case PressureModerate:
		decision.Allowed[P0] = true
		decision.Allowed[P1] = true
		decision.Allowed[P2] = true
		decision.Allowed[P3] = true
		decision.Allowed[P4] = true
		decision.BackfillPaused = false
	default:
		decision.Allowed[P0] = true
		decision.Allowed[P1] = true
		decision.Allowed[P2] = true
		decision.Allowed[P3] = true
		decision.Allowed[P4] = true
		decision.Allowed[P5] = true
		decision.BackfillPaused = false
	}

	if decision.Pressure == PressureHealthy && demand.Live > 0 {
		decision.LivePollInterval = liveHealthy
	} else {
		decision.LivePollInterval = livePressure
	}

	// Paced last, because it consumes the pressure classification above: a
	// surplus the planner has already decided is not safe to spend must not
	// then be handed to the pacer as a rate.
	now := p.cfg.Now()
	decision.MinuteLimit = p.cfg.MinuteLimit
	decision.MinutesLeftToday = minutesUntilUTCMidnight(now)
	decision.BallastPerMinute = ballastPerMinute(decision, p.cfg.DailyReserve, now)
	return decision
}

// SQLDemandSource queries the canonical matches table for the fixture calendar.
type SQLDemandSource struct {
	db *sql.DB
}

func NewSQLDemandSource(db *sql.DB) SQLDemandSource {
	return SQLDemandSource{db: db}
}

func (s SQLDemandSource) Demand(ctx context.Context) (DemandSnapshot, error) {
	var d DemandSnapshot
	// The live count deliberately excludes matches whose live claim has
	// expired. A fixture stranded in 'second_half' used to keep this above
	// zero indefinitely, which kept live polling scheduled for a match that
	// was not being played and kept the whole rest of the day's live spend in
	// the projection -- squeezing backfill out of quota it could have used.
	//
	// The watch window is calendar-driven and independent of it: polling
	// starts five minutes before kickoff whether or not the database has
	// noticed the match, and stops once every candidate is past the plausible
	// duration.
	err := s.db.QueryRowContext(ctx, `
SELECT
  COUNT(*) FILTER (
    WHERE status IN ('live','halftime','second_half','extra_time','penalties','break')
      AND (live_state_at IS NULL OR live_state_at > now() - interval '15 minutes')
      AND kickoff_at > now() - interval '210 minutes'
  ),
  COUNT(*) FILTER (
    WHERE status NOT IN ('finished','ft','aet','pen','canceled','cancelled','abandoned','postponed','awarded','walkover','unknown')
      AND kickoff_at > now() - interval '210 minutes'
      AND kickoff_at <= now() + interval '5 minutes'
  ),
  COUNT(*) FILTER (WHERE kickoff_at > now() AND kickoff_at <= now() + interval '2 hours' AND status NOT IN ('finished','ft','aet','pen','canceled','cancelled','abandoned','postponed')),
  COUNT(*) FILTER (WHERE kickoff_at > now() + interval '2 hours' AND kickoff_at <= now() + interval '6 hours' AND status NOT IN ('finished','ft','aet','pen','canceled','cancelled','abandoned','postponed')),
  COUNT(*) FILTER (WHERE kickoff_at > now() + interval '6 hours' AND kickoff_at <= now() + interval '24 hours' AND status NOT IN ('finished','ft','aet','pen','canceled','cancelled','abandoned','postponed')),
  COUNT(*) FILTER (WHERE kickoff_at > now() + interval '24 hours' AND kickoff_at <= now() + interval '48 hours'),
  COUNT(*) FILTER (WHERE kickoff_at > now() AND kickoff_at <= now() + interval '7 days' AND status NOT IN ('finished','ft','aet','pen','canceled','cancelled','abandoned','postponed'))
FROM matches`).Scan(&d.Live, &d.LiveWatch, &d.Prematch0_2h, &d.Prematch2_6h, &d.Prematch6_24h, &d.Tomorrow, &d.Upcoming)
	if err != nil {
		return DemandSnapshot{}, err
	}
	return d, nil
}

func roundF(value float64) int {
	if value < 0 {
		return 0
	}
	rounded := int(value + 0.5)
	return rounded
}

// projectInPlayStatistics is the batched statistics poll's cost for the rest of
// the UTC day, in requests.
//
// Driven by `Live` rather than `LiveWatch`, and that is the one judgement worth
// stating. `LiveWatch` is the calendar's view -- every fixture that could
// plausibly be in play -- and the live poll is forecast from it because that
// poll is what discovers kickoff, and it costs one request however many matches
// it covers. This poll is different on both counts: it reads the live set from
// our own database, so it only ever covers matches already known to be in play,
// and its cost scales with that count. Forecasting it from the calendar would
// reserve budget for matches it will not poll.
func (p Planner) projectInPlayStatistics(demand DemandSnapshot, hoursLeft float64, interval time.Duration) int {
	if !p.cfg.InPlayStatisticsPollEnabled || demand.Live <= 0 || hoursLeft <= 0 {
		return 0
	}
	batchSize := p.cfg.InPlayStatisticsBatchSize
	if batchSize <= 0 {
		batchSize = 20
	}
	// Ceiling, not a ratio: three live matches still cost a whole request, and
	// rounding that to nothing would make the forecast read zero for every
	// weekday evening.
	requestsPerPoll := (demand.Live + batchSize - 1) / batchSize
	polls := roundF(hoursLeft * 3600 / interval.Seconds())
	return polls * requestsPerPoll
}

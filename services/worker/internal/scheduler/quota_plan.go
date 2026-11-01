package scheduler

import (
	"context"
	"database/sql"
	"log/slog"
	"strconv"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/ingestion"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/quota"
)

// QuotaOptions tunes how the quota decision shapes the schedule.
type QuotaOptions struct {
	// DB is used to pick backfill candidates from the canonical catalogue.
	DB *sql.DB
	// BackfillMinSafeBudget is the minimum safe_discretionary budget (requests)
	// before any P4 backfill is enqueued.
	BackfillMinSafeBudget int
	// MaxBackfillPerCycle is the safety ceiling on backfill jobs per cycle.
	//
	// It used to be the operating point, and at its default of three it was the
	// single throttle that held the platform to 12,000 requests a day against a
	// 75,000 plan. The pacer now picks the operating point from actual surplus;
	// this is only the bound it may not cross.
	MaxBackfillPerCycle int
	// StatisticsBackfillMaxPerCycle reserves at most this many of the paced
	// backfill slots for finished fixture statistics. Zero disables the producer.
	StatisticsBackfillMaxPerCycle int

	// BackfillMaxRequestsPerDay is the hard ceiling on discretionary backfill
	// spend for one UTC day, in provider requests. Zero disables the ceiling.
	//
	// MaxBackfillPerCycle bounds a cycle and cannot bound a day, and the
	// difference is not academic: on 2026-09-21 a restart left 4,076 backfill
	// jobs queued, every per-cycle bound was respected, and the day spent
	// about 74,000 requests. Against the 75,000 package that was survivable.
	// Against 7,500 it is the whole package before lunch, with live coverage
	// taking the shortfall.
	//
	// Counted from the jobs table rather than held in memory, deliberately.
	// The incident was caused by a restart, and a counter that a restart
	// resets is no protection against the thing that caused it.
	BackfillMaxRequestsPerDay int

	// CycleInterval is how often the scheduler replans, used to convert the
	// pacer's per-minute rate into a per-cycle count.
	CycleInterval time.Duration
	// BackfillDays is the size of the backfill window in days ending today.
	BackfillDays int
}

// ApplyQuotaPlan enriches the base plan with quota-aware jobs: live polling
// when P0 is allowed, and opportunistic backfill when discretionary budget
// exists. It logs the planner decision for observability.
func ApplyQuotaPlan(ctx context.Context, base []ScheduledJob, decision quota.Decision, opts QuotaOptions, now time.Time, logger *slog.Logger) []ScheduledJob {
	if logger == nil {
		logger = slog.Default()
	}
	plan := make([]ScheduledJob, 0, len(base)+1)
	for _, scheduled := range base {
		if scheduled.Job.JobKey == ingestion.InPlayStatisticsJobKey() {
			interval := decision.InPlayStatisticsPollInterval
			if interval <= 0 {
				interval = time.Minute
			}
			// The base planner runs every minute to support the mock provider.
			// Real calls are admitted only at the cadence priced by the quota
			// planner, and only while a match is actually in play.
			if decision.LiveMatches == 0 || now.UTC().Truncate(time.Minute) != now.UTC().Truncate(interval) || !decision.Allowed[quota.P1] {
				continue
			}
		}
		plan = append(plan, scheduled)
	}

	logger.Info("quota planner",
		"daily_remaining", decision.DailyRemaining,
		"projected_live", decision.ProjectedLive,
		"projected_prematch", decision.ProjectedPrematch,
		"projected_prediction", decision.ProjectedPrediction,
		"projected_inplay_statistics", decision.ProjectedInPlayStats,
		"projected_fixed", decision.ProjectedFixed,
		"emergency_reserve", decision.EmergencyReserve,
		"safe_discretionary", decision.SafeDiscretionary,
		"pressure", pressureName(decision.Pressure),
		"backfill_paused", decision.BackfillPaused,
		"live_poll_interval", decision.LivePollInterval.String(),
		"live_matches", decision.LiveMatches,
		"live_watch", decision.LiveWatch,
		"reason", decision.Reason,
		"ballast_per_minute", decision.BallastPerMinute,
		"minutes_left_today", decision.MinutesLeftToday,
		"backfill_this_cycle", pacedBackfillCount(decision, opts),
		"statistics_backfill_ceiling", opts.StatisticsBackfillMaxPerCycle,
	)

	// Gated on the calendar watch window, not on what the database currently
	// believes is in play. The old condition could not become true until a
	// live poll had already run, so kickoff was missed by up to one
	// today-refresh interval every time.
	if decision.Allowed[quota.P0] && (decision.LiveWatch > 0 || decision.LiveMatches > 0) {
		plan = append(plan, livePollJob(now, decision.LivePollInterval))
	}
	if decision.Allowed[quota.P4] && !decision.BackfillPaused && decision.SafeDiscretionary >= opts.BackfillMinSafeBudget {
		slots := pacedBackfillCount(decision, opts)
		if ceiling := backfillSlotsLeftToday(ctx, opts, now, logger); ceiling >= 0 && ceiling < slots {
			logger.Info("quota planner backfill day ceiling",
				"slots_requested", slots,
				"slots_allowed", ceiling,
				"max_requests_per_day", opts.BackfillMaxRequestsPerDay,
			)
			slots = ceiling
		}
		if slots <= 0 {
			return plan
		}
		statisticsSlots := opts.StatisticsBackfillMaxPerCycle
		if statisticsSlots > slots {
			statisticsSlots = slots
		}
		if statisticsSlots > 0 {
			statistics := statisticsBackfillJobs(ctx, opts, statisticsSlots, now, logger)
			plan = append(plan, statistics...)
			// The pacer budgets two requests per fixture job. A statistics job
			// costs one, so reserving one complete slot errs toward underspend.
			slots -= len(statistics)
		}
		plan = append(plan, backfillJobs(ctx, opts, slots, now, logger)...)
	}
	return plan
}

func livePollJob(now time.Time, pollInterval time.Duration) ScheduledJob {
	interval := pollInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	bucket := now.Truncate(interval).Format("20060102T150405Z")
	return ScheduledJob{
		Job: jobs.NewJob{
			JobKey:               ingestion.LiveFixturesJobKey(),
			Queue:                "provider-fixtures",
			Priority:             10,
			Payload:              []byte(`{"live":true}`),
			IdempotencyKey:       "schedule:provider:api-football:live:" + bucket,
			DeduplicateCompleted: true,
			RunAfter:             now,
		},
		Provider: "api-football",
		CallMode: "real",
		Schedule: "live",
		Bucket:   bucket,
	}
}

// backfillJobs picks a bounded number of active league-seasons with a measured
// prediction gap or live history request. It excludes
// work already scheduled in the current six-hour bucket before applying LIMIT:
// otherwise the same top few leagues consume every scheduler cycle while the
// rest of the catalogue never gets a turn.
func backfillJobs(ctx context.Context, opts QuotaOptions, max int, now time.Time, logger *slog.Logger) []ScheduledJob {
	if opts.DB == nil || max <= 0 {
		return nil
	}
	days := opts.BackfillDays
	if days <= 0 {
		days = 30
	}
	bucket := now.Truncate(6 * time.Hour).Format("20060102T150405Z")
	rows, err := opts.DB.QueryContext(ctx, `
WITH upcoming AS MATERIALIZED (
  SELECT fixture.league_id, fixture.season_id,
         count(*) AS fixture_count,
         count(*) FILTER (WHERE NOT EXISTS (
           SELECT 1 FROM engine_predictions AS prediction
           WHERE prediction.fixture_id = fixture.id
         )) AS missing_predictions,
         count(*) FILTER (WHERE EXISTS (
           SELECT 1 FROM data_backfill_requests AS request
           WHERE request.target_fixture_id = fixture.id
             AND request.status IN ('pending', 'in_progress')
             AND request.expires_at > now()
         )) AS active_requests
  FROM fixtures AS fixture
  WHERE fixture.kickoff_at > now()
    AND fixture.kickoff_at <= now() + interval '7 days'
    AND fixture.season_id IS NOT NULL
    AND lower(fixture.status) NOT IN
      ('ft', 'aet', 'pen', 'finished', 'canceled', 'cancelled', 'abandoned', 'postponed')
  GROUP BY fixture.league_id, fixture.season_id
)
SELECT pem.provider_entity_id, upcoming.season_id
FROM upcoming
JOIN leagues AS league ON league.id = upcoming.league_id
JOIN provider_entity_maps AS pem
  ON pem.canonical_entity_id = league.id::text
 AND pem.canonical_entity_type = 'league'
 AND pem.provider = 'api-football'
 AND pem.entity_type = 'league'
 AND pem.retired_at IS NULL
WHERE (upcoming.active_requests > 0 OR upcoming.missing_predictions > 0)
  AND NOT EXISTS (
  SELECT 1 FROM jobs AS scheduled
  WHERE scheduled.idempotency_key =
    'schedule:provider:api-football:backfill:' || pem.provider_entity_id ||
    ':' || upcoming.season_id::text || ':' || $2
)
ORDER BY upcoming.active_requests DESC, upcoming.missing_predictions DESC,
         league.priority_score DESC, upcoming.fixture_count DESC, league.id ASC
LIMIT $1`, max, bucket)
	if err != nil {
		logger.Warn("quota planner backfill candidate query failed", "error", err)
		return nil
	}
	defer rows.Close()

	from := now.AddDate(0, 0, -days).UTC().Format("2006-01-02")
	to := now.UTC().Format("2006-01-02")

	plan := []ScheduledJob{}
	for rows.Next() {
		var providerLeagueID string
		var season int
		if err := rows.Scan(&providerLeagueID, &season); err != nil {
			logger.Warn("quota planner backfill candidate scan failed", "error", err)
			continue
		}
		payload := fixtureBackfillPayload(providerLeagueID, season, from, to)
		plan = append(plan, ScheduledJob{
			Job: jobs.NewJob{
				JobKey:               ingestion.BackfillFixturesJobKey(),
				Queue:                "provider-fixtures",
				Priority:             40,
				Payload:              payload,
				IdempotencyKey:       "schedule:provider:api-football:backfill:" + providerLeagueID + ":" + strconv.Itoa(season) + ":" + bucket,
				DeduplicateCompleted: true,
				RunAfter:             now,
			},
			Provider: "api-football",
			CallMode: "real",
			Schedule: "backfill",
			Bucket:   bucket,
		})
	}
	return plan
}

func fixtureBackfillPayload(providerLeagueID string, season int, from, to string) []byte {
	// API-Football's fixtures endpoint rejects a page parameter. The date
	// window is the unit of work for this backfill job.
	return []byte(`{"league_id":` + providerLeagueID + `,"season":` + strconv.Itoa(season) + `,"from":"` + from + `","to":"` + to + `"}`)
}

// backfillSlotsLeftToday reports how many backfill jobs may still be enqueued
// today under BackfillMaxRequestsPerDay, or -1 when no ceiling applies.
//
// Jobs enqueued, not requests sent, is the quantity being bounded -- and that
// is the point. A queued job is a committed request: it will be claimed and it
// will spend, whether that happens this minute or after the restart that
// drains the backlog. Bounding what has already been sent would have stopped
// nothing on 2026-09-21, when the spend was sitting in the queue the whole
// time.
func backfillSlotsLeftToday(ctx context.Context, opts QuotaOptions, now time.Time, logger *slog.Logger) int {
	if opts.DB == nil || opts.BackfillMaxRequestsPerDay <= 0 {
		return -1
	}
	var enqueued int
	err := opts.DB.QueryRowContext(ctx, `
SELECT count(*)
FROM jobs
WHERE created_at >= date_trunc('day', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
  AND (
    job_key = $1
    OR idempotency_key LIKE 'schedule:provider:api-football:backfill:%'
    OR idempotency_key LIKE 'schedule:provider:api-football:stats-demand:%'
  )`, ingestion.BackfillTeamHistoryJobKey()).Scan(&enqueued)
	if err != nil {
		// Fail closed. An unknown backlog is the state this ceiling exists to
		// survive, and guessing "none" would hand the pacer a full allowance
		// in exactly the circumstances -- a database under load after a
		// restart -- that produced the incident.
		logger.Warn("quota planner backfill day ceiling query failed; pausing backfill this cycle", "error", err)
		return 0
	}
	return backfillSlotsFromCeiling(enqueued, opts.BackfillMaxRequestsPerDay)
}

// backfillSlotsFromCeiling is the ceiling arithmetic, separated from the query
// so it can be tested without a database.
func backfillSlotsFromCeiling(enqueuedToday, maxRequestsPerDay int) int {
	if maxRequestsPerDay <= 0 {
		return -1
	}
	spent := enqueuedToday * requestsPerBackfillJob
	left := maxRequestsPerDay - spent
	if left <= 0 {
		return 0
	}
	return left / requestsPerBackfillJob
}

func pressureName(pressure quota.Pressure) string {
	switch pressure {
	case quota.PressureCritical:
		return "critical"
	case quota.PressureHigh:
		return "high"
	case quota.PressureModerate:
		return "moderate"
	default:
		return "healthy"
	}
}

// requestsPerBackfillJob is what one league-season backfill costs the provider.
//
// A single `fixtures?league&season&from&to` call, plus a page or two on a full
// season. Two is a deliberate over-estimate: the error is one-directional, and
// over-estimating the cost makes the pacer spend less than it could, which
// wastes quota. Under-estimating makes it overshoot the daily budget, which
// takes live coverage down with it.
const requestsPerBackfillJob = 2

// pacedBackfillCount converts the pacer's per-minute rate into this cycle's
// job count.
//
// This is the join between the two halves of the fix. The pacer computes how
// much may be spent per minute; nothing spent it, because the number of jobs
// created each cycle was a constant. A rate that nothing consumes is exactly
// as useful as no rate at all -- which is the shape of the original defect,
// where SafeDiscretionary was computed correctly every cycle and then read by
// nobody.
func pacedBackfillCount(decision quota.Decision, opts QuotaOptions) int {
	// Zero is the operator kill switch. Config accepts zero, so treating it as
	// an implicit default would make an emergency pause silently ineffective.
	if decision.BallastPerMinute <= 0 || opts.MaxBackfillPerCycle <= 0 {
		return 0
	}
	cycle := opts.CycleInterval
	if cycle <= 0 {
		cycle = 30 * time.Second
	}
	// Requests this cycle may issue, from the per-minute allowance.
	perCycle := float64(decision.BallastPerMinute) * cycle.Seconds() / 60.0
	count := int(perCycle / float64(requestsPerBackfillJob))

	ceiling := opts.MaxBackfillPerCycle
	if count > ceiling {
		count = ceiling
	}
	// A positive allowance always buys at least one job. Truncating a real
	// allowance to zero would stall backfill entirely on a quiet day, which is
	// the day with the most quota to lose.
	if count < 1 {
		count = 1
	}
	return count
}

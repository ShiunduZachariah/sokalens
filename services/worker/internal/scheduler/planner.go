package scheduler

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/billingevents"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/ingestion"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/maintenance"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/matchstate"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/predictions"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/recommendations"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/seourl"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/snapshots"
)

const (
	ProviderAPIFootball      = "api-football"
	ProviderFootballDataOrg  = "football-data-org"
	ProviderTheSportsDB      = "thesportsdb"
	ProviderHybrid           = "hybrid"
	defaultOddsDelay         = time.Minute
	defaultPublicDetailHours = 24
	defaultRawRetentionDays  = 2
)

// todayFixturesRefreshInterval refreshes today's fixture statuses (including
// full-time results) near-real-time so finished matches stop showing LIVE
// promptly. The live=all poll drops a fixture as soon as it ends and never
// carries FT, so today's date-scoped job is the FT source of truth.
const todayFixturesRefreshInterval = 5 * time.Minute
const tomorrowFixturesRefreshInterval = time.Hour

// Keep a complete rolling week in the catalogue and revisit the prior three
// UTC dates. The latter catches matches that disappeared from live=all before
// their final FT/postponed state was persisted.
const fixtureLookaheadDays = 7
const fixtureStatusCatchupDays = 3

type PlanOptions struct {
	ProviderMode              string
	EnableRealAPIFootball     bool
	EnableMockAPIFootball     bool
	EnableRealFootballDataOrg bool
	EnableRealTheSportsDB     bool
	ProviderInterval          time.Duration
	PredictionInterval        time.Duration
	RecommendationInterval    time.Duration
	OddsDelay                 time.Duration
	PublicResultDetailHours   int
	RawResultRetentionDays    int
	// InPlayStatisticsPoll schedules the batched fixtures?ids= statistics
	// poll. Off by default for the reason config.InPlayStatisticsPollEnabled
	// records: the embedded shape is unconfirmed, and getting it wrong is
	// silent. Nothing is planned while it is false, so the provider budget is
	// untouched.
	InPlayStatisticsPoll bool
}

type ScheduledJob struct {
	Job      jobs.NewJob
	Provider string
	CallMode string
	Schedule string
	Bucket   string
}

type Planner struct {
	opts PlanOptions
}

func NewPlanner(opts PlanOptions) (Planner, error) {
	switch opts.ProviderMode {
	case ProviderAPIFootball, ProviderFootballDataOrg, ProviderHybrid:
	default:
		return Planner{}, fmt.Errorf("unsupported provider mode %q", opts.ProviderMode)
	}
	if opts.ProviderInterval <= 0 {
		return Planner{}, errors.New("provider schedule interval must be positive")
	}
	if opts.PredictionInterval <= 0 {
		return Planner{}, errors.New("prediction schedule interval must be positive")
	}
	if opts.RecommendationInterval <= 0 {
		opts.RecommendationInterval = opts.PredictionInterval
	}
	if opts.OddsDelay < 0 {
		return Planner{}, errors.New("odds schedule delay must not be negative")
	}
	if opts.OddsDelay == 0 {
		opts.OddsDelay = defaultOddsDelay
	}
	if opts.PublicResultDetailHours == 0 {
		opts.PublicResultDetailHours = defaultPublicDetailHours
	}
	if opts.PublicResultDetailHours < 1 || opts.PublicResultDetailHours > 168 {
		return Planner{}, errors.New("public result detail hours must be between 1 and 168")
	}
	if opts.RawResultRetentionDays == 0 {
		opts.RawResultRetentionDays = defaultRawRetentionDays
	}
	if opts.RawResultRetentionDays < 1 || opts.RawResultRetentionDays > 30 {
		return Planner{}, errors.New("raw result retention days must be between 1 and 30")
	}
	return Planner{opts: opts}, nil
}

func (p Planner) Plan(now time.Time) []ScheduledJob {
	now = normalizedNow(now)
	dailyBucket := now.Format("2006-01-02")
	providerBucket := cadenceBucket(now, p.opts.ProviderInterval)
	predictionBucket := cadenceBucket(now, p.opts.PredictionInterval)
	recommendationBucket := cadenceBucket(now, p.opts.RecommendationInterval)
	reminderBucket := cadenceBucket(now, time.Hour)
	// Two minutes: often enough that a stranded match is bounded well inside
	// the window a user would notice, cheap enough that the sweep costs one
	// indexed query against a handful of rows.
	reaperBucket := cadenceBucket(now, 2*time.Minute)
	plan := make([]ScheduledJob, 0, 16)

	if p.apiFootballEnabled() {
		callMode := "mock"
		if p.opts.EnableRealAPIFootball {
			callMode = "real"
		}
		plan = append(plan, providerJob(now, ProviderAPIFootball, callMode, "reference", dailyBucket, "countries", "", ingestion.CountriesJobKey(), ingestion.DefaultProviderQueue, 20, []byte(`{"endpoint":"countries"}`)))
		if !p.opts.EnableRealAPIFootball {
			// Mock mode schedules the live poll here, and nothing else.
			//
			// In real mode the poll is planned by the quota planner, which
			// main.go wires only for real calls -- rightly, because that
			// planner also schedules backfill, and the mock serves neither
			// leagues nor fixtures-by-date nor odds. Routing mock mode through
			// it would queue jobs that 404.
			//
			// Without this, QA can never exercise in-play at all: the only job
			// it scheduled was countries, so the live snapshot and event
			// writers had nothing to write from. QA's last live poll before
			// this ran 38 hours before it was noticed, and the silence looked
			// exactly like a feature that was merely quiet.
			//
			// Thirty seconds matches the planner's healthy cadence. Mock calls
			// cost no provider quota, so there is no budget to pace against.
			liveBucket := cadenceBucket(now, 30*time.Second)
			plan = append(plan, ScheduledJob{Job: jobs.NewJob{
				JobKey: ingestion.LiveFixturesJobKey(), Queue: "provider-fixtures", Priority: 10,
				Payload: []byte(`{"live":true}`), IdempotencyKey: "schedule:provider:api-football:live:" + liveBucket,
				DeduplicateCompleted: true, RunAfter: now,
			}, Provider: ProviderAPIFootball, CallMode: callMode, Schedule: "live", Bucket: liveBucket})
		}
		// The batched in-play statistics poll, on a 60-second cadence.
		//
		// Sixty seconds because that is how often the provider refreshes match
		// statistics; polling faster spends quota to re-read numbers that have
		// not moved, and the writer would drop the repeat as a duplicate
		// cutoff anyway.
		//
		// Planned in both real and mock mode, and only when the flag is on.
		// While it is off nothing is queued at all, so the provider budget is
		// exactly as it was -- which is why this sits outside the quota
		// planner for now. Before this is enabled against the real provider,
		// its spend (about 10 requests a minute at a 200-match peak, ~6,800
		// for a full Saturday) has to be accounted for there, or it competes
		// with the live poll for the same daily budget without either knowing.
		if p.opts.InPlayStatisticsPoll {
			statsBucket := cadenceBucket(now, time.Minute)
			plan = append(plan, ScheduledJob{Job: jobs.NewJob{
				JobKey: ingestion.InPlayStatisticsJobKey(), Queue: "provider-fixtures", Priority: 46,
				Payload: []byte(`{}`), IdempotencyKey: "schedule:provider:api-football:inplay-statistics:" + statsBucket,
				DeduplicateCompleted: true, RunAfter: now,
			}, Provider: ProviderAPIFootball, CallMode: callMode, Schedule: "inplay-statistics", Bucket: statsBucket})
		}
		if p.opts.EnableRealAPIFootball {
			plan = append(plan, providerJob(now, ProviderAPIFootball, callMode, "reference", dailyBucket, "leagues", "", ingestion.LeaguesJobKey(), ingestion.DefaultProviderQueue, 25, []byte(`{"endpoint":"leagues","current":true}`)))
			for offset := -fixtureStatusCatchupDays; offset <= fixtureLookaheadDays; offset++ {
				date := now.AddDate(0, 0, offset).Format("2006-01-02")
				bucket := providerBucket
				if offset == 0 {
					bucket = cadenceBucket(now, todayFixturesRefreshInterval)
				} else if offset == 1 {
					// Tomorrow's slate changes throughout the provider day. Four-hour
					// refreshes left newly listed fixtures invisible for too long.
					bucket = cadenceBucket(now, tomorrowFixturesRefreshInterval)
				}
				plan = append(plan, providerJob(
					now,
					ProviderAPIFootball,
					callMode,
					"fixtures",
					bucket,
					"fixtures",
					date,
					ingestion.FixturesJobKey(),
					"provider-fixtures",
					30,
					[]byte(`{"endpoint":"fixtures","date":"`+date+`"}`),
				))
			}
			for offset := 0; offset <= 1; offset++ {
				date := now.AddDate(0, 0, offset).Format("2006-01-02")
				odds := providerJob(
					now,
					ProviderAPIFootball,
					callMode,
					"odds",
					providerBucket,
					"odds",
					date,
					ingestion.OddsJobKey(),
					"provider-odds",
					35,
					[]byte(`{"date":"`+date+`","page":1}`),
				)
				odds.Job.RunAfter = now.Add(p.opts.OddsDelay)
				plan = append(plan, odds)
			}
			// Batch planner for provider predictions: the scheduled job selects
			// fixtures missing a fresh prediction and enqueues one per-fixture
			// fetch job each (daily cadence; per-fixture jobs are quota-governed).
			// Hourly sweep for matches played without a result. Real mode only:
			// it asks the provider by fixture id, which the mock does not serve.
			//
			// The date-window refresh reaches three days back, so a result that
			// never arrived inside that window was never asked for again. This
			// asks by id, so a fixture cannot age out of being fixable.
			reconcileBucket := cadenceBucket(now, time.Hour)
			plan = append(plan, ScheduledJob{Job: jobs.NewJob{
				JobKey: ingestion.ResultsReconcileJobKey(), Queue: "provider-fixtures", Priority: 44,
				Payload: []byte(`{}`), IdempotencyKey: "schedule:provider:api-football:results-reconcile:" + reconcileBucket,
				DeduplicateCompleted: true, RunAfter: now,
			}, Provider: ProviderAPIFootball, CallMode: callMode, Schedule: "results-reconcile", Bucket: reconcileBucket})
			plan = append(plan, ScheduledJob{Job: jobs.NewJob{
				JobKey: ingestion.ProviderPredictionsJobKey(), Queue: "provider-fixtures", Priority: 33,
				Payload: []byte(`{}`), IdempotencyKey: "schedule:provider:api-football:predictions:" + dailyBucket,
				DeduplicateCompleted: true, RunAfter: now,
			}, Provider: ProviderAPIFootball, CallMode: callMode, Schedule: "provider-predictions", Bucket: dailyBucket})
			// Daily reference-catalogue sync: applies league logos from stored
			// metadata and enqueues per-league teams backfills (logos/flags).
			plan = append(plan, ScheduledJob{Job: jobs.NewJob{
				JobKey: ingestion.ReferenceSyncJobKey(), Queue: ingestion.DefaultProviderQueue, Priority: 22,
				Payload: []byte(`{}`), IdempotencyKey: "schedule:provider:api-football:reference-sync:" + dailyBucket,
				DeduplicateCompleted: true, RunAfter: now,
			}, Provider: ProviderAPIFootball, CallMode: callMode, Schedule: "reference-sync", Bucket: dailyBucket})
		}
	}

	if p.footballDataEnabled() {
		plan = append(plan, providerJob(
			now,
			ProviderFootballDataOrg,
			"real",
			"reference",
			dailyBucket,
			"competitions",
			"",
			ingestion.FootballDataCompetitionsJobKey(),
			ingestion.DefaultProviderQueue,
			20,
			[]byte(`{"endpoint":"competitions"}`),
		))
		// The free results feed is complementary in hybrid mode: scheduling it
		// keeps covered competitions populated even when API-Football is under
		// quota pressure.
		for offset := -1; offset <= fixtureLookaheadDays; offset++ {
			date := now.AddDate(0, 0, offset).Format("2006-01-02")
			plan = append(plan, providerJob(
				now,
				ProviderFootballDataOrg,
				"real",
				"fixtures",
				providerBucket,
				"matches",
				date,
				ingestion.FootballDataMatchesJobKey(),
				"provider-fixtures",
				30,
				[]byte(`{"endpoint":"matches","date":"`+date+`"}`),
			))
		}
	}

	// TheSportsDB is a second-tier reference provider (free logos/metadata): a
	// single daily logo backfill for canonical teams and leagues missing one.
	if p.thesportsdbEnabled() {
		plan = append(plan, ScheduledJob{Job: jobs.NewJob{
			JobKey: ingestion.TheSportsDBReferenceSyncJobKey(), Queue: ingestion.DefaultProviderQueue, Priority: 24,
			Payload: []byte(`{}`), IdempotencyKey: "schedule:provider:thesportsdb:reference-sync:" + dailyBucket,
			DeduplicateCompleted: true, RunAfter: now,
		}, Provider: ProviderTheSportsDB, CallMode: "real", Schedule: "reference-sync", Bucket: dailyBucket})
	}

	plan = append(plan,
		predictionJob(
			now,
			"generate",
			predictionBucket,
			predictions.GenerateUpcomingJobKey,
			4,
			// A 2,000-fixture run exceeds the Python runner's five-minute limit
			// when the seven-day backlog grows. The query skips fixtures with
			// current predictions, so subsequent cadence windows drain the rest.
			[]byte(`{"scope":"upcoming","batch_size":250}`),
		),
		predictionJob(
			now,
			"settle",
			predictionBucket,
			predictions.SettleFinishedJobKey,
			45,
			[]byte(`{"scope":"finished","batch_size":100}`),
		),
		predictionJob(
			now,
			"accuracy",
			dailyBucket,
			predictions.SnapshotAccuracyJobKey,
			50,
			[]byte(`{}`),
		),
		// On the prediction cadence, not the daily one. A fixture that ended
		// twenty minutes ago should already have its permanent record: waiting
		// for a nightly sweep leaves the "how did we do" view blank for the rest
		// of the day, and leaves the frozen row exposed to a partition drop if
		// the month turns over first.
		predictionJob(
			now,
			"finalise-snapshots",
			predictionBucket,
			predictions.FinaliseSnapshotsJobKey,
			48,
			[]byte(`{"batch_size":500}`),
		),
		// Pre-match trackers, on the prediction cadence.
		//
		// This was the gap that made the whole tracker invisible: the engine
		// command existed and nothing ever ran it, so prediction_tracker_state
		// stayed empty and the API served no tracker on any pick. The field
		// read as dead rather than as absent-until-judged, which is a worse
		// failure than having no tracker at all -- a client cannot tell the two
		// apart, and an integrator would reasonably conclude the feature does
		// not work.
		//
		// Costs no provider quota: it reads our own pre-match picks against the
		// live score and upserts a verdict.
		predictionJob(
			now,
			"refresh-trackers",
			predictionBucket,
			predictions.RefreshTrackersJobKey,
			47,
			[]byte(`{"batch_size":200}`),
		),
		predictionJob(
			now,
			"aggregate-prune",
			dailyBucket,
			predictions.AggregatePruneJobKey,
			52,
			[]byte(fmt.Sprintf(
				`{"public_detail_hours":%d,"raw_retention_days":%d}`,
				p.opts.PublicResultDetailHours,
				p.opts.RawResultRetentionDays,
			)),
		),
	)
	plan = append(plan, ScheduledJob{Job: jobs.NewJob{
		JobKey: recommendations.GenerateCandidatesJobKey, Queue: "personalization", Priority: 60,
		Payload: []byte(`{}`), IdempotencyKey: "schedule:recommendations:" + recommendationBucket,
		DeduplicateCompleted: true, RunAfter: now,
	}, Schedule: "recommendations", Bucket: recommendationBucket})
	plan = append(plan, ScheduledJob{Job: jobs.NewJob{
		JobKey: billingevents.SubscriptionReminderJobKey, Queue: billingevents.BillingQueue, Priority: 55,
		Payload: []byte(`{}`), IdempotencyKey: "schedule:billing:subscription-reminders:" + reminderBucket,
		DeduplicateCompleted: true, RunAfter: now,
	}, Schedule: "subscription-reminders", Bucket: reminderBucket})
	plan = append(plan, ScheduledJob{Job: jobs.NewJob{
		JobKey: maintenance.PruneJobKey, Queue: maintenance.Queue, Priority: 90,
		Payload: []byte(`{}`), IdempotencyKey: "schedule:maintenance:retention:" + dailyBucket,
		DeduplicateCompleted: true, RunAfter: now,
	}, Schedule: "retention", Bucket: dailyBucket})
	// The raw prediction drain has its own short hourly lease and independent
	// feature gate. It must not wait behind the legacy daily retention pass.
	rawPredictionBucket := cadenceBucket(now, time.Hour)
	plan = append(plan, ScheduledJob{Job: jobs.NewJob{
		JobKey: maintenance.RawPredictionPruneJobKey, Queue: maintenance.Queue, Priority: 89,
		Payload: []byte(`{}`), IdempotencyKey: "schedule:maintenance:raw-predictions:" + rawPredictionBucket,
		DeduplicateCompleted: true, RunAfter: now,
	}, Schedule: "raw-predictions", Bucket: rawPredictionBucket})
	// Reconcile our view of the provider's daily quota with the provider's.
	//
	// Hourly, and unconditionally. Everything else here is ingestion, which is
	// gated on provider flags and on having budget to spend; this is the job
	// that finds out whether the budget we think we have is real, so gating it
	// on the same conditions would silence it exactly when it matters.
	//
	// Priority 95 -- above retention, above ingestion. A worker with a wrong
	// idea of its quota does nothing useful until this runs.
	//
	// The hourly bucket also gives a run shortly after 00:00 UTC, which is
	// when the provider resets and when our number is most likely to be a day
	// out of date. That is the exact condition that froze live scores for
	// fifteen hours on 2026-09-22.
	quotaBucket := cadenceBucket(now, time.Hour)
	plan = append(plan, ScheduledJob{Job: jobs.NewJob{
		JobKey: ingestion.QuotaSyncJobKey(), Queue: ingestion.QuotaSyncQueue, Priority: 95,
		Payload: []byte(`{}`), IdempotencyKey: "schedule:provider:quota-sync:" + quotaBucket,
		DeduplicateCompleted: true, RunAfter: now,
	}, Schedule: "provider-quota-sync", Bucket: quotaBucket})
	// The match-state sweep is scheduled unconditionally, and deliberately so.
	// It is the guard against a match claiming to be in play hours after
	// kickoff, and that claim can outlive the provider that created it -- a
	// row stranded while api-football was the active provider stays stranded
	// after a switch away from it. Gating the sweep on the same provider flags
	// as ingestion would switch off the cleanup exactly when a provider change
	// makes stranded rows most likely. It reads its own table and, past the
	// hard bound, needs no provider at all.
	// The crawlable URL set, in two bands on two clocks.
	//
	// The volatile band every fifteen minutes, because a fixture ages out of
	// the index by nothing but the passage of time -- no event fires, no row
	// is written, and only something that looks again notices. A sitemap that
	// advertises a page the page itself noindexes is how a property teaches
	// Google to distrust the whole file.
	//
	// The stable band daily. Manchester United will still be Manchester United
	// tomorrow, and re-deriving 13,763 team rows every quarter hour to discover
	// that none of them changed is how a sweep becomes the thing somebody
	// switches off.
	seoVolatileBucket := now.Truncate(15 * time.Minute).Format("20060102T1504Z")
	plan = append(plan, ScheduledJob{Job: jobs.NewJob{
		JobKey: seourl.VolatileJobKey, Queue: seourl.Queue, Priority: 55,
		Payload: []byte(`{}`), IdempotencyKey: "schedule:seo:volatile:" + seoVolatileBucket,
		DeduplicateCompleted: true, RunAfter: now,
	}, Schedule: "seo-urls-volatile", Bucket: seoVolatileBucket})

	seoStableBucket := now.UTC().Format("20060102")
	plan = append(plan, ScheduledJob{Job: jobs.NewJob{
		JobKey: seourl.StableJobKey, Queue: seourl.Queue, Priority: 60,
		Payload: []byte(`{}`), IdempotencyKey: "schedule:seo:stable:" + seoStableBucket,
		DeduplicateCompleted: true, RunAfter: now,
	}, Schedule: "seo-urls-stable", Bucket: seoStableBucket})

	plan = append(plan, ScheduledJob{Job: jobs.NewJob{
		JobKey: matchstate.ReaperJobKey, Queue: matchstate.Queue, Priority: 30,
		Payload: []byte(`{}`), IdempotencyKey: "schedule:matchstate:reap:" + reaperBucket,
		DeduplicateCompleted: true, RunAfter: now,
	}, Schedule: "match-state-reaper", Bucket: reaperBucket})

	// Partition maintenance runs daily and unconditionally, including while
	// snapshot writing is switched off. A partition that exists and is never
	// written costs an empty table; a partition that does not exist is an
	// insert failure the moment the feature is turned on -- and that switch is
	// far more likely to be flipped before a fixture window than during a quiet
	// Tuesday.
	plan = append(plan, ScheduledJob{Job: jobs.NewJob{
		JobKey: snapshots.PartitionJobKey, Queue: snapshots.Queue, Priority: 80,
		Payload: []byte(`{}`), IdempotencyKey: "schedule:snapshots:partitions:" + dailyBucket,
		DeduplicateCompleted: true, RunAfter: now,
	}, Schedule: "snapshot-partitions", Bucket: dailyBucket})
	return plan
}

func (p Planner) apiFootballEnabled() bool {
	selected := p.opts.ProviderMode == ProviderAPIFootball || p.opts.ProviderMode == ProviderHybrid
	return selected && (p.opts.EnableRealAPIFootball || p.opts.EnableMockAPIFootball)
}

func (p Planner) footballDataEnabled() bool {
	selected := p.opts.ProviderMode == ProviderFootballDataOrg || p.opts.ProviderMode == ProviderHybrid
	return selected && p.opts.EnableRealFootballDataOrg
}

// thesportsdbEnabled gates the second-tier TheSportsDB reference schedule. It
// is independent of FOOTBALL_PROVIDER mode: TheSportsDB only backfills logos
// and reference metadata and never serves fixtures/predictions.
func (p Planner) thesportsdbEnabled() bool {
	return p.opts.EnableRealTheSportsDB
}

func providerJob(
	now time.Time,
	provider string,
	callMode string,
	schedule string,
	bucket string,
	endpoint string,
	date string,
	jobKey string,
	queue string,
	priority int,
	payload []byte,
) ScheduledJob {
	keyParts := []string{"schedule", "provider", provider, endpoint}
	if date != "" {
		keyParts = append(keyParts, date)
	}
	keyParts = append(keyParts, bucket)
	return ScheduledJob{
		Job: jobs.NewJob{
			JobKey:               jobKey,
			Queue:                queue,
			Priority:             priority,
			Payload:              payload,
			IdempotencyKey:       strings.Join(keyParts, ":"),
			DeduplicateCompleted: true,
			RunAfter:             now,
		},
		Provider: provider,
		CallMode: callMode,
		Schedule: schedule,
		Bucket:   bucket,
	}
}

func predictionJob(now time.Time, action, bucket, jobKey string, priority int, payload []byte) ScheduledJob {
	idempotencyKey := strings.Join([]string{"schedule", "predictions", action, bucket}, ":")
	return ScheduledJob{
		Job: jobs.NewJob{
			JobKey:               jobKey,
			Queue:                "predictions",
			Priority:             priority,
			Payload:              payload,
			IdempotencyKey:       idempotencyKey,
			DeduplicateCompleted: true,
			RunAfter:             now,
		},
		Schedule: "predictions",
		Bucket:   bucket,
	}
}

func cadenceBucket(now time.Time, cadence time.Duration) string {
	return now.Truncate(cadence).Format("20060102T150405Z")
}

func normalizedNow(now time.Time) time.Time {
	if now.IsZero() {
		now = time.Now()
	}
	return now.UTC()
}

func scheduleLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}

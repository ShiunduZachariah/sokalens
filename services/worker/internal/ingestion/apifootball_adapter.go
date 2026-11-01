package ingestion

import (
	"context"
	"database/sql"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/metrics"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
)

// NewAPIFootballAdapter builds the API-Football job handlers.
//
// snapshotWriter is nil when the append-only live history is switched off,
// which is the default. Only the live-poll handler receives it: the backfill
// and date handlers see completed fixtures, and a snapshot of a match that
// already finished is not a point on its timeline.
//
// eventWriter is the same arrangement for the event timeline, and is nil unless
// ENABLE_FIXTURE_EVENTS is on. Both are passed in rather than constructed here
// so that the flag is read in one place -- cmd/worker -- and a writer that
// reaches this function is one an operator asked for. Building the repository
// here instead would make the flag decorative and ship the feature switched on.
// inPlayStatisticsEnabled gates the batched fixtures?ids= statistics poll. It
// is a parameter rather than something the job reads for itself so that the
// only place the decision is made is where configuration is read: a job that
// consults the environment directly cannot be tested with it off.
func NewAPIFootballAdapter(db *sql.DB, client ProviderClient, jobRepo jobs.Repository, dedupe *RequestDedupe, workerID string, registry *metrics.Registry, snapshotWriter liveSnapshotWriter, eventWriter liveEventWriter, inPlayStatisticsEnabled bool) ProviderAdapter {
	referenceSync := NewReferenceSyncRepository(db)
	fixtureLocker := PostgresAdvisoryLocker{DB: db}
	exclusiveFixtureIngestion := func(handler JobHandler) JobHandler {
		return ExclusiveHandler{Handler: handler, Locker: fixtureLocker, LockKey: "pipeline:api-football:fixtures", RetryAfter: defaultContentionBackoff}
	}
	return ProviderAdapter{
		Provider: apifootball.ProviderName,
		Queues: map[string]map[string]JobHandler{
			DefaultProviderQueue: {
				// Asks the provider what our quota actually is, hourly.
				//
				// On this queue rather than provider-fixtures on purpose: it
				// has to be able to run while the fixtures pipeline is refusing
				// to run anything, which is the situation it exists to end.
				//
				// Budget is wired only when the client can be corrected in
				// memory. When it cannot, the job still writes the provider's
				// number to provider_rate_limits, so the correction lands at
				// the next worker start instead of never.
				QuotaSyncJobKey(): NewQuotaSyncJob(QuotaSyncJobOptions{
					Client:      client,
					RequestLogs: NewRequestLogRepository(db),
					RateLimits:  NewProviderRateLimitRepository(db),
					Budget:      budgetObserverFor(client),
				}),
				CountriesJobKey(): NewCountriesJob(CountriesJobOptions{
					Client:      client,
					RequestLogs: NewRequestLogRepository(db),
					EntityMaps:  NewProviderEntityMapRepository(db),
					RawPayloads: NewRawPayloadRepository(db),
					RateLimits:  NewProviderRateLimitRepository(db),
					Catalog:     NewCatalogRepository(db),
					Reference:   referenceSync,
				}),
				LeaguesJobKey(): NewLeaguesJob(LeaguesJobOptions{
					Client:      client,
					RequestLogs: NewRequestLogRepository(db),
					EntityMaps:  NewProviderEntityMapRepository(db),
					RawPayloads: NewRawPayloadRepository(db),
					RateLimits:  NewProviderRateLimitRepository(db),
					Catalog:     NewCatalogRepository(db),
				}),
				// Reference catalogue sync: daily batch planner (applies league
				// logos from metadata + enqueues per-league teams backfills) and
				// the per-league teams fetch job (teams?league&season, logos).
				ReferenceSyncJobKey(): NewReferenceSyncJob(TeamsReferenceJobOptions{
					Reference: referenceSync,
					Jobs:      jobRepo,
				}),
				TeamsReferenceJobKey(): NewTeamsReferenceJob(TeamsReferenceJobOptions{
					Client:      client,
					RequestLogs: NewRequestLogRepository(db),
					EntityMaps:  NewProviderEntityMapRepository(db),
					RawPayloads: NewRawPayloadRepository(db),
					RateLimits:  NewProviderRateLimitRepository(db),
					Catalog:     NewCatalogRepository(db),
					Reference:   referenceSync,
				}),
				PlayersJobKey(): NewPlayersJob(PlayersJobOptions{
					Client:      client,
					RequestLogs: NewRequestLogRepository(db),
					EntityMaps:  NewProviderEntityMapRepository(db),
					RawPayloads: NewRawPayloadRepository(db),
					RateLimits:  NewProviderRateLimitRepository(db),
					Catalog:     NewCatalogRepository(db),
					Jobs:        jobRepo,
				}),
				VenuesJobKey(): NewVenuesJob(VenuesJobOptions{
					Client:      client,
					RequestLogs: NewRequestLogRepository(db),
					RawPayloads: NewRawPayloadRepository(db),
					RateLimits:  NewProviderRateLimitRepository(db),
					Catalog:     NewCatalogRepository(db),
				}),
			},
			"provider-fixtures": {
				FixturesJobKey(): exclusiveFixtureIngestion(NewFixturesJob(FixturesJobOptions{
					Client:      client,
					RequestLogs: NewRequestLogRepository(db),
					EntityMaps:  NewProviderEntityMapRepository(db),
					RawPayloads: NewRawPayloadRepository(db),
					RateLimits:  NewProviderRateLimitRepository(db),
					Catalog:     NewCatalogRepository(db),
					Reference:   NewReferenceSyncRepository(db),
					Jobs:        jobRepo,
					Dedupe:      dedupe,
					WorkerID:    workerID,
					Metrics:     registry,
				})),
				// Live polling (fixtures?live=all) and league/season backfill reuse
				// the same ingestion path with different payloads.
				LiveFixturesJobKey(): exclusiveFixtureIngestion(NewFixturesJob(FixturesJobOptions{
					Client:      client,
					RequestLogs: NewRequestLogRepository(db),
					EntityMaps:  NewProviderEntityMapRepository(db),
					RawPayloads: NewRawPayloadRepository(db),
					RateLimits:  NewProviderRateLimitRepository(db),
					Catalog:     NewCatalogRepository(db),
					Reference:   NewReferenceSyncRepository(db),
					Jobs:        jobRepo,
					Dedupe:      dedupe,
					WorkerID:    workerID,
					Metrics:     registry,
					Snapshots:   snapshotWriter,
					Events:      eventWriter,
				})),
				BackfillFixturesJobKey(): exclusiveFixtureIngestion(NewFixturesJob(FixturesJobOptions{
					Client:      client,
					RequestLogs: NewRequestLogRepository(db),
					EntityMaps:  NewProviderEntityMapRepository(db),
					RawPayloads: NewRawPayloadRepository(db),
					RateLimits:  NewProviderRateLimitRepository(db),
					Catalog:     NewCatalogRepository(db),
					Reference:   NewReferenceSyncRepository(db),
					Jobs:        jobRepo,
					Dedupe:      dedupe,
					WorkerID:    workerID,
					Metrics:     registry,
				})),
				// Fetches results for matches played without one. Reuses the
				// fixtures job via ids= rather than fetching here.
				ResultsReconcileJobKey(): NewResultsReconcileJob(ResultsReconcileJobOptions{
					Stale: func(ctx context.Context, limit int) ([]int64, error) {
						return NewResultsReconcilePlanner(db).Stale(ctx, apifootball.ProviderName, limit)
					},
					Jobs: jobRepo,
				}),
				ProviderPredictionsJobKey(): NewProviderPredictionsJob(ProviderPredictionsJobOptions{
					Client:      client,
					RequestLogs: NewRequestLogRepository(db),
					EntityMaps:  NewProviderEntityMapRepository(db),
					RateLimits:  NewProviderRateLimitRepository(db),
					Predictions: NewProviderPredictionsRepository(db),
					Planner:     NewProviderPredictionsPlanner(db),
					Jobs:        jobRepo,
				}),
				// The batched in-play poll. Twenty fixtures per request against
				// the per-fixture endpoint's one, which is the difference
				// between ~600 requests an hour at a 200-match peak and
				// 12,000 -- the latter exhausts the daily allowance before the
				// afternoon kick-offs finish.
				InPlayStatisticsJobKey(): NewInPlayStatisticsJob(InPlayStatisticsJobOptions{
					Client:      client,
					RequestLogs: NewRequestLogRepository(db),
					EntityMaps:  NewProviderEntityMapRepository(db),
					RawPayloads: NewRawPayloadRepository(db),
					RateLimits:  NewProviderRateLimitRepository(db),
					Live:        NewLiveFixturesRepository(db),
					Statistics:  NewInPlayStatisticsRepository(db),
					Enabled:     inPlayStatisticsEnabled,
					MaxFixtures: DefaultLiveFixtureLimit,
				}),
				StatisticsJobKey(): NewFixtureStatisticsJob(FixtureStatisticsJobOptions{
					Client:      client,
					RequestLogs: NewRequestLogRepository(db),
					EntityMaps:  NewProviderEntityMapRepository(db),
					RawPayloads: NewRawPayloadRepository(db),
					RateLimits:  NewProviderRateLimitRepository(db),
					Catalog:     NewCatalogRepository(db),
					Statistics:  NewStatisticsRepository(db),
					// The adaptive-TTL ladder, which was written for this job
					// and then never connected to it. Both fields are optional,
					// so the job fetched every fixture it was handed, every
					// time it was handed it.
					//
					// On 2026-09-21 that was 94,220 calls covering 8,757
					// distinct fixtures -- each one refetched about eleven
					// times in a day -- which is 96% of all provider traffic
					// and more than the entire 75,000 daily allowance. It
					// exhausted the quota, and the exhausted quota is what
					// froze live scores for fifteen hours.
					//
					// Most of that is spent re-reading matches that have
					// finished. A settled fixture's statistics are final:
					// Advance marks them Immutable and the ladder then never
					// asks again, which is where the bulk of the saving is.
					//
					// Five minutes to twelve hours for everything else. The
					// floor is well inside a half, so an in-play fixture's
					// statistics stay current, and the ceiling lets a fixture
					// whose numbers keep coming back identical fall away to
					// twice a day instead of holding a slot.
					ResourceState: NewSQLResourceStateRepository(db),
					Settled:       NewFixtureSettledRepository(db),
					TTL:           TTLTier{Min: 6 * time.Hour, Max: 24 * time.Hour},
				}),
			},
			"provider-odds": {
				OddsJobKey(): NewOddsJob(OddsJobOptions{
					Client:      client,
					RequestLogs: NewRequestLogRepository(db),
					EntityMaps:  NewProviderEntityMapRepository(db),
					RawPayloads: NewRawPayloadRepository(db),
					RateLimits:  NewProviderRateLimitRepository(db),
					Odds:        NewOddsRepository(db),
					Jobs:        jobRepo,
				}),
			},
		},
	}
}

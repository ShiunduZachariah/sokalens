package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
)

// statisticsWriter persists one team's fixture statistics.
type statisticsWriter interface {
	Upsert(context.Context, int64, int64, StatRecord) error
}

// fixtureSettledLookup reports whether a fixture has finished.
//
// This is what makes the TTL ladder terminate. Without it the ladder can only
// back off on an unchanged response hash, so a match played last August is
// still re-read twice a day forever -- and with ~9,400 fixtures on file that
// is tens of thousands of requests a day to re-learn numbers that stopped
// changing at full time.
//
// It reads our own database, not the provider, so knowing this costs nothing.
type fixtureSettledLookup interface {
	Settled(ctx context.Context, fixtureID int64) (bool, error)
}

// FixtureStatisticsJobOptions wires the per-fixture statistics ingestion. The
// Catalog dependency is carried for parity with the fixtures job; fixture and
// team identity are resolved through EntityMaps, so Catalog is optional here.
type FixtureStatisticsJobOptions struct {
	Client      ProviderClient
	RequestLogs requestLogger
	EntityMaps  entityMapResolver
	RawPayloads rawPayloadWriter
	RateLimits  rateLimitWriter
	Catalog     catalogWriter
	Statistics  statisticsWriter
	// Settled marks a finished fixture's statistics immutable so they are
	// never fetched again. Optional; without it the ladder still backs off,
	// but it never stops.
	Settled fixtureSettledLookup
	// ResourceState is the adaptive-TTL ladder (architecture v2 4.2). Optional:
	// left nil the job fetches on every run, which is the behaviour before this
	// existed. Set, it skips fetches for a fixture whose statistics keep coming
	// back identical.
	ResourceState *ResourceStateRepository
	// TTL is the interval band this job is allowed to move within. Zero values
	// are normalised rather than rejected.
	TTL TTLTier
	Now func() time.Time
}

// FixtureStatisticsJob fetches API-Football fixtures/statistics for one
// provider fixture and upserts the normalized per-team statistics.
type FixtureStatisticsJob struct {
	opts FixtureStatisticsJobOptions
}

type fixtureStatisticsJobPayload struct {
	FixtureID int64 `json:"fixture_id"`
}

func NewFixtureStatisticsJob(opts FixtureStatisticsJobOptions) FixtureStatisticsJob {
	return FixtureStatisticsJob{opts: opts}
}

func (j FixtureStatisticsJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Client == nil {
		return errors.New("fixture statistics job provider client is required")
	}
	if j.opts.RequestLogs == nil {
		return errors.New("fixture statistics job request log repository is required")
	}
	if j.opts.EntityMaps == nil {
		return errors.New("fixture statistics job entity map repository is required")
	}
	if j.opts.Statistics == nil {
		return errors.New("fixture statistics job repository is required")
	}
	now := time.Now().UTC
	if j.opts.Now != nil {
		now = j.opts.Now
	}

	payload := fixtureStatisticsJobPayload{}
	if len(job.Payload) > 0 && string(job.Payload) != "{}" {
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return err
		}
	}
	if payload.FixtureID <= 0 {
		return errors.New("fixture statistics job requires a positive fixture_id")
	}

	resourceKey := ResourceKey("fixture_stats", payload.FixtureID)
	priorState := ResourceState{Provider: apifootball.ProviderName, ResourceKey: resourceKey}
	if j.opts.ResourceState != nil {
		loaded, stateErr := j.opts.ResourceState.Load(ctx, apifootball.ProviderName, resourceKey)
		if stateErr != nil {
			// The ladder is a scheduling cache, not a source of truth. If it
			// cannot be read, fetch: over-fetching once is cheaper than a job
			// that fails and retries, which would fetch anyway and burn a
			// scheduling slot doing it.
			slog.Warn("adaptive TTL state unreadable, fetching anyway",
				"provider", apifootball.ProviderName,
				"resource_key", resourceKey,
				"error", stateErr,
			)
		} else {
			priorState = loaded
			if !loaded.DueAt(now()) {
				slog.Debug("fixture statistics fetch skipped by adaptive TTL",
					"provider", apifootball.ProviderName,
					"resource_key", resourceKey,
					"unchanged_streak", loaded.UnchangedStreak,
					"current_interval_s", int(loaded.CurrentInterval.Seconds()),
					"next_eligible_at", loaded.NextEligibleAt,
					"immutable", loaded.Immutable,
				)
				// Not an error. The job did its work: it decided the request was
				// not worth making.
				return nil
			}
		}
	}

	params := url.Values{}
	params.Set("fixture", strconv.FormatInt(payload.FixtureID, 10))
	resp, err := j.opts.Client.Get(ctx, "fixtures/statistics", params)
	paramsHash := hashParams(params)
	log := RequestLog{
		JobID:         job.ID,
		Provider:      nonEmpty(resp.Provider, apifootball.ProviderName),
		Endpoint:      nonEmpty(resp.Endpoint, "fixtures/statistics"),
		Method:        "GET",
		ParamsHash:    paramsHash,
		StatusCode:    resp.StatusCode,
		QuotaCost:     providerQuotaCost(resp),
		Latency:       resp.Latency,
		ResponseBytes: resp.ResponseBytes,
		RateLimit:     resp.RateLimit,
		Reason:        "statistics",
		Priority:      40,
		RequestedAt:   now(),
	}
	if err != nil {
		log.ErrorCategory = categorizeProviderError(resp, err)
		if logErr := j.opts.RequestLogs.Insert(ctx, log); logErr != nil {
			return logErr
		}
		return err
	}
	if err := j.opts.RequestLogs.Insert(ctx, log); err != nil {
		return err
	}
	if j.opts.RateLimits != nil {
		if err := j.opts.RateLimits.UpdateFromHeaders(ctx, ProviderRateLimitUpdate{
			Provider:        apifootball.ProviderName,
			DailyLimit:      resp.RateLimit.DailyLimit,
			DailyRemaining:  resp.RateLimit.DailyRemaining,
			MinuteLimit:     resp.RateLimit.MinuteLimit,
			MinuteRemaining: resp.RateLimit.MinuteRemaining,
			UpdatedAt:       now(),
		}); err != nil {
			return err
		}
	}
	if j.opts.RawPayloads != nil {
		if err := j.opts.RawPayloads.InsertMetadata(ctx, RawPayloadMetadata{
			Provider:      apifootball.ProviderName,
			EndpointKey:   "fixtures.statistics",
			ParamsHash:    paramsHash,
			StatusCode:    resp.StatusCode,
			ResponseHash:  ResponseHash(resp.Body),
			ResponseBytes: resp.ResponseBytes,
			Headers:       resp.RateLimit.Raw,
			StoragePolicy: "metadata_only",
			FetchedAt:     now(),
		}); err != nil {
			return err
		}
	}

	canonicalID, ok, err := j.opts.EntityMaps.LookupCanonicalEntityID(ctx, apifootball.ProviderName, "match", strconv.FormatInt(payload.FixtureID, 10))
	if err != nil {
		return err
	}
	if !ok {
		slog.Warn("fixture statistics skipped: provider fixture is unmapped",
			"provider", apifootball.ProviderName,
			"job_id", job.ID,
			"provider_fixture_id", payload.FixtureID,
		)
		return nil
	}
	fixtureID, err := ParseCanonicalID(canonicalID)
	if err != nil {
		return err
	}

	stats, err := apifootball.ParseFixtureStatistics(resp.Body)
	if err != nil {
		return err
	}
	teamsStored := 0
	teamsSkipped := 0
	for _, team := range stats {
		teamCanonicalID, mapped, err := j.opts.EntityMaps.LookupCanonicalEntityID(ctx, apifootball.ProviderName, "team", strconv.Itoa(team.TeamID))
		if err != nil {
			return err
		}
		if !mapped {
			slog.Warn("fixture statistics team skipped: provider team is unmapped",
				"provider", apifootball.ProviderName,
				"job_id", job.ID,
				"provider_fixture_id", payload.FixtureID,
				"provider_team_id", team.TeamID,
			)
			teamsSkipped++
			continue
		}
		teamID, err := ParseCanonicalID(teamCanonicalID)
		if err != nil {
			return err
		}
		if err := j.opts.Statistics.Upsert(ctx, fixtureID, teamID, statRecordFromItems(team.Statistics)); err != nil {
			return err
		}
		teamsStored++
	}

	// Advance the adaptive-TTL ladder now that we know what came back. Placed
	// after the writes and deliberately non-fatal: the statistics are already
	// stored, and failing here would retry the whole job -- spending another
	// request to correct a scheduling hint.
	//
	// Significant is left unset. Nothing in a statistics payload reliably says
	// "a goal just went in", and inventing that signal from the data at hand
	// would back the ladder off at exactly the wrong moment.
	//
	// Terminal is different: it is not in the payload, but it is in our own
	// database, and asking costs nothing. A fixture that has finished has
	// final statistics, so the right number of future requests for it is zero
	// -- and saying so is what turns the ladder from "fetch less often" into
	// "stop". Without it a match played last August is still re-read twice a
	// day forever.
	terminal := false
	if j.opts.Settled != nil {
		settled, settledErr := j.opts.Settled.Settled(ctx, fixtureID)
		if settledErr != nil {
			// Not fatal, and deliberately biased towards fetching again: a
			// fixture wrongly marked immutable is never revisited, which is a
			// worse failure than one extra request.
			slog.Warn("could not determine whether fixture is settled; leaving it refetchable",
				"fixture_id", fixtureID, "error", settledErr)
		} else {
			terminal = settled
		}
	}
	if j.opts.ResourceState != nil {
		advanced := Advance(priorState, Observation{
			ResponseHash: ResponseHash(resp.Body),
			FetchedAt:    now(),
			Terminal:     terminal,
		}, j.opts.TTL)
		if stateErr := j.opts.ResourceState.Save(ctx, advanced); stateErr != nil {
			slog.Warn("could not record adaptive TTL state",
				"provider", apifootball.ProviderName,
				"resource_key", resourceKey,
				"error", stateErr,
			)
		}
	}

	slog.Info("fixture statistics stored",
		"provider", apifootball.ProviderName,
		"endpoint", "fixtures/statistics",
		"job_id", job.ID,
		"provider_fixture_id", payload.FixtureID,
		"canonical_fixture_id", fixtureID,
		"teams_fetched", len(stats),
		"teams_stored", teamsStored,
		"teams_skipped", teamsSkipped,
	)
	return nil
}

// StatisticsJobKey identifies the API-Football fixture-statistics job.
func StatisticsJobKey() string {
	return ProviderJobKey(apifootball.ProviderName, "fixtures.statistics")
}

// statRecordFromItems maps raw provider statistic items onto the nullable
// StatRecord columns and derives card_points (cards shown) from the counts.
func statRecordFromItems(items []apifootball.StatisticItem) StatRecord {
	var record StatRecord
	for _, item := range items {
		switch normalizeStatType(item.Type) {
		case "shots on goal":
			record.ShotsOnTarget = parseStatInt(item.Value)
		case "total shots":
			record.ShotsTotal = parseStatInt(item.Value)
		case "blocked shots":
			record.ShotsBlocked = parseStatInt(item.Value)
		case "shots insidebox":
			record.ShotsInsideBox = parseStatInt(item.Value)
		case "shots outsidebox":
			record.ShotsOutsideBox = parseStatInt(item.Value)
		case "ball possession":
			record.PossessionPct = parseStatFloat(item.Value)
		case "corner kicks":
			record.Corners = parseStatInt(item.Value)
		case "fouls":
			record.Fouls = parseStatInt(item.Value)
		case "offsides":
			record.Offsides = parseStatInt(item.Value)
		case "yellow cards":
			record.YellowCards = parseStatInt(item.Value)
		case "red cards":
			record.RedCards = parseStatInt(item.Value)
		case "goalkeeper saves":
			record.GoalkeeperSaves = parseStatInt(item.Value)
		case "total passes":
			record.PassesTotal = parseStatInt(item.Value)
		case "passes accurate":
			record.PassesAccurate = parseStatInt(item.Value)
		case "passes %":
			record.PassAccuracy = parseStatFloat(item.Value)
		case "expected_goals":
			record.XG = parseStatFloat(item.Value)
		}
	}
	record.CardPoints = cardCount(record)
	return record
}

// cardCount is the cards shown to the team: yellow + red.
//
// card_points was once yellow*10 + red*25 here while the football-data
// importer wrote yellow + 2*red, and the prediction engine forecast and
// settled total_cards lines of 2.5-5.5 on the column -- so one yellow on a
// fixture this job stored settled "over 5.5" as won. The engine now derives
// the count from yellow_cards and red_cards itself; this keeps the stored
// column in the same unit.
//
// API-Football reports a zero as null, so both counts null on a row that
// captured other statistics is a clean match (0), and on a row that captured
// nothing is unknown (nil). The same rule lives in the engine's
// _CARD_COUNT_SQL and the API's discipline query.
func cardCount(record StatRecord) *int {
	if record.YellowCards == nil && record.RedCards == nil {
		if record.Fouls == nil && record.ShotsTotal == nil && record.Corners == nil {
			return nil
		}
		zero := 0
		return &zero
	}
	count := 0
	if record.YellowCards != nil {
		count += *record.YellowCards
	}
	if record.RedCards != nil {
		count += *record.RedCards
	}
	return &count
}

func normalizeStatType(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func parseStatInt(value string) *int {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return nil
	}
	return &parsed
}

func parseStatFloat(value string) *float64 {
	value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "%"))
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return nil
	}
	return &parsed
}

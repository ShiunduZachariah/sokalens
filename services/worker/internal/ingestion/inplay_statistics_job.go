package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strconv"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
)

// inPlayStatisticsWriter appends one team's statistics to the in-play series.
type inPlayStatisticsWriter interface {
	AppendSnapshot(ctx context.Context, fixtureID int64, teamID int64, capturedAt time.Time, matchMinute *int, stats StatRecord) (bool, error)
}

// liveProviderFixtureLister returns the provider fixture ids of matches that
// are in play right now, most recently active first.
type liveProviderFixtureLister interface {
	LiveProviderFixtureIDs(ctx context.Context, limit int) ([]int64, error)
}

// InPlayStatisticsJobOptions wires the batched in-play statistics poll.
type InPlayStatisticsJobOptions struct {
	Client      ProviderClient
	RequestLogs requestLogger
	EntityMaps  entityMapResolver
	RateLimits  rateLimitWriter
	RawPayloads rawPayloadWriter
	Live        liveProviderFixtureLister
	Statistics  inPlayStatisticsWriter

	// Enabled gates the whole job, and it defaults to false.
	//
	// THIS IS NOT A FEATURE TOGGLE. It is a gate on an unverified assumption.
	// API-Football documents `fixtures?ids=` as embedding statistics, but the
	// exact nesting of that block has never been observed on a live response
	// from this account -- the capture that would confirm it needs a key and a
	// match in play, and cannot be run from a build environment.
	//
	// The failure mode if the assumption is wrong is the reason for a gate
	// rather than a try-and-see: a mismatched shape does not error, it decodes
	// to zero teams. The poll would then run every minute, spend quota, store
	// nothing, and every corner and card market would read "this league has no
	// statistics" and quietly decline to publish. A silent, plausible,
	// self-consistent wrong answer is the worst outcome available here, so the
	// path stays off until a capture confirms the shape.
	//
	// services/worker/cmd/capturefixtureids is that capture. Run it against
	// live fixtures, check the shape it reports, then turn this on.
	Enabled bool

	// MaxFixtures caps how many live fixtures one run covers. Zero means all
	// of them.
	MaxFixtures int

	Now func() time.Time
}

// InPlayStatisticsJob polls `fixtures?ids=` in batches of twenty and appends
// the embedded per-team statistics to the in-play series.
//
// WHY BATCHED IDS AND NOT fixtures/statistics PER FIXTURE. The per-fixture
// endpoint is one request per match: 200 concurrent matches at a 60-second
// cadence is 200 requests a minute, which is 12,000 an hour against a daily
// budget of 75,000 -- the weekend would exhaust the quota before the early
// kick-offs finished. Batched at twenty, the same coverage is 10 requests a
// minute, ~600 an hour, and around 6,800 for a full Saturday of live
// statistics. That difference is the whole reason this job exists.
type InPlayStatisticsJob struct {
	opts InPlayStatisticsJobOptions
}

func NewInPlayStatisticsJob(opts InPlayStatisticsJobOptions) InPlayStatisticsJob {
	return InPlayStatisticsJob{opts: opts}
}

type inPlayStatisticsJobPayload struct {
	// IDs overrides the live lookup, for a targeted backfill or a manual run.
	IDs []int64 `json:"ids"`
}

func (j InPlayStatisticsJob) Run(ctx context.Context, job jobs.Job) error {
	if !j.opts.Enabled {
		// Deliberately not an error: the job is scheduled but switched off, and
		// a failing job would retry, alert and look like a broken poll.
		slog.Debug("in-play statistics poll disabled; the embedded statistics shape is unconfirmed",
			"job_id", job.ID,
			"capture", "services/worker/cmd/capturefixtureids",
		)
		return nil
	}
	if j.opts.Client == nil {
		return errors.New("in-play statistics job provider client is required")
	}
	if j.opts.RequestLogs == nil {
		return errors.New("in-play statistics job request log repository is required")
	}
	if j.opts.EntityMaps == nil {
		return errors.New("in-play statistics job entity map repository is required")
	}
	if j.opts.Statistics == nil {
		return errors.New("in-play statistics job repository is required")
	}

	now := time.Now().UTC
	if j.opts.Now != nil {
		now = j.opts.Now
	}

	payload := inPlayStatisticsJobPayload{}
	if len(job.Payload) > 0 && string(job.Payload) != "{}" {
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return err
		}
	}

	providerIDs := normalizeFixtureIDs(payload.IDs)
	if len(providerIDs) == 0 {
		if j.opts.Live == nil {
			return errors.New("in-play statistics job needs either explicit ids or a live fixture lister")
		}
		live, err := j.opts.Live.LiveProviderFixtureIDs(ctx, j.opts.MaxFixtures)
		if err != nil {
			return err
		}
		providerIDs = normalizeFixtureIDs(live)
	}
	if len(providerIDs) == 0 {
		// No match is in play. Nothing to poll, and no request spent finding
		// that out -- the live set comes from our own database.
		slog.Debug("in-play statistics poll found no live fixtures", "job_id", job.ID)
		return nil
	}

	var (
		batches           int
		fixturesSeen      int
		fixturesWithStats int
		capturesStored    int
		teamsSkipped      int
	)

	for _, batch := range batchFixtureIDs(providerIDs, apifootball.MaxCaptureIDs) {
		batches++
		seen, withStats, stored, skipped, err := j.pollBatch(ctx, job, batch, now)
		fixturesSeen += seen
		fixturesWithStats += withStats
		capturesStored += stored
		teamsSkipped += skipped
		if err != nil {
			// One failed batch does not abandon the rest. A live pass covering
			// ten batches must not lose nine matches' statistics because the
			// provider rate-limited the first -- and the next tick is only a
			// minute away, so a lost batch costs one series point, while an
			// abandoned pass costs every match after it.
			slog.Warn("in-play statistics batch failed; continuing with the remaining batches",
				"job_id", job.ID,
				"batch_size", len(batch),
				"error", err,
			)
		}
	}

	// The shape check that the gate exists for.
	//
	// Fixtures came back and not one carried statistics. That is either a real
	// provider gap (possible, and ordinary for a minor league) or the embedded
	// block is nested differently from what the parser assumes -- which
	// decodes to zero teams without erroring. Either way the poll is spending
	// quota and storing nothing, and it must say so rather than look healthy.
	if fixturesSeen > 0 && fixturesWithStats == 0 {
		slog.Warn("in-play statistics poll stored nothing: every fixture came back without statistics",
			"job_id", job.ID,
			"fixtures_seen", fixturesSeen,
			"hint", "confirm the embedded statistics shape with services/worker/cmd/capturefixtureids",
		)
	}

	slog.Info("in-play statistics polled",
		"provider", apifootball.ProviderName,
		"endpoint", "fixtures",
		"job_id", job.ID,
		"live_fixtures", len(providerIDs),
		"batches", batches,
		"fixtures_seen", fixturesSeen,
		"fixtures_with_statistics", fixturesWithStats,
		"captures_stored", capturesStored,
		"teams_skipped", teamsSkipped,
	)
	return nil
}

// pollBatch fetches one batch of up to twenty provider fixture ids and writes
// whatever statistics came with them.
func (j InPlayStatisticsJob) pollBatch(
	ctx context.Context,
	job jobs.Job,
	providerIDs []int64,
	now func() time.Time,
) (fixturesSeen int, fixturesWithStats int, capturesStored int, teamsSkipped int, err error) {
	params := url.Values{}
	params.Set("ids", fixtureIDsParam(providerIDs))

	resp, fetchErr := j.opts.Client.Get(ctx, "fixtures", params)
	paramsHash := hashParams(params)
	log := RequestLog{
		JobID:         job.ID,
		Provider:      nonEmpty(resp.Provider, apifootball.ProviderName),
		Endpoint:      nonEmpty(resp.Endpoint, "fixtures"),
		Method:        "GET",
		ParamsHash:    paramsHash,
		StatusCode:    resp.StatusCode,
		QuotaCost:     providerQuotaCost(resp),
		Latency:       resp.Latency,
		ResponseBytes: resp.ResponseBytes,
		RateLimit:     resp.RateLimit,
		Reason:        "inplay_statistics",
		Priority:      46,
		RequestedAt:   now(),
	}
	if fetchErr != nil {
		log.ErrorCategory = categorizeProviderError(resp, fetchErr)
		if logErr := j.opts.RequestLogs.Insert(ctx, log); logErr != nil {
			return 0, 0, 0, 0, logErr
		}
		return 0, 0, 0, 0, fetchErr
	}
	if logErr := j.opts.RequestLogs.Insert(ctx, log); logErr != nil {
		return 0, 0, 0, 0, logErr
	}
	if j.opts.RateLimits != nil {
		if rlErr := j.opts.RateLimits.UpdateFromHeaders(ctx, ProviderRateLimitUpdate{
			Provider:        apifootball.ProviderName,
			DailyLimit:      resp.RateLimit.DailyLimit,
			DailyRemaining:  resp.RateLimit.DailyRemaining,
			MinuteLimit:     resp.RateLimit.MinuteLimit,
			MinuteRemaining: resp.RateLimit.MinuteRemaining,
			UpdatedAt:       now(),
		}); rlErr != nil {
			return 0, 0, 0, 0, rlErr
		}
	}
	if j.opts.RawPayloads != nil {
		if payloadErr := j.opts.RawPayloads.InsertMetadata(ctx, RawPayloadMetadata{
			Provider:      apifootball.ProviderName,
			EndpointKey:   "fixtures.inplay_statistics",
			ParamsHash:    paramsHash,
			StatusCode:    resp.StatusCode,
			ResponseHash:  ResponseHash(resp.Body),
			ResponseBytes: resp.ResponseBytes,
			Headers:       resp.RateLimit.Raw,
			StoragePolicy: "metadata_only",
			FetchedAt:     now(),
		}); payloadErr != nil {
			return 0, 0, 0, 0, payloadErr
		}
	}

	fixtures, parseErr := apifootball.ParseFixturesWithStatistics(resp.Body)
	if parseErr != nil {
		return 0, 0, 0, 0, parseErr
	}

	// One capture time for the whole batch. The twenty fixtures came from one
	// response, so they share a data cutoff; stamping each with the moment its
	// row happened to be written would make the series claim a precision the
	// provider does not offer, and would put two teams of the same match at
	// different captured_at values, which the series reads as two points.
	capturedAt := now().UTC()

	for _, fixture := range fixtures {
		fixturesSeen++
		if len(fixture.Statistics) == 0 {
			continue
		}
		fixturesWithStats++

		canonicalID, ok, lookupErr := j.opts.EntityMaps.LookupCanonicalEntityID(ctx, apifootball.ProviderName, "match", strconv.Itoa(fixture.Fixture.Fixture.ID))
		if lookupErr != nil {
			return fixturesSeen, fixturesWithStats, capturesStored, teamsSkipped, lookupErr
		}
		if !ok {
			// Skipped, never guessed. Rule 1 of the in-play contract: a
			// provider id is not canonical, and inventing a mapping once split
			// one team's history across two ids.
			slog.Warn("in-play statistics skipped: provider fixture is unmapped",
				"provider", apifootball.ProviderName,
				"job_id", job.ID,
				"provider_fixture_id", fixture.Fixture.Fixture.ID,
			)
			continue
		}
		fixtureID, idErr := ParseCanonicalID(canonicalID)
		if idErr != nil {
			return fixturesSeen, fixturesWithStats, capturesStored, teamsSkipped, idErr
		}

		minute := embeddedMatchMinute(fixture.Fixture)

		for _, team := range fixture.Statistics {
			teamCanonicalID, mapped, teamErr := j.opts.EntityMaps.LookupCanonicalEntityID(ctx, apifootball.ProviderName, "team", strconv.Itoa(team.TeamID))
			if teamErr != nil {
				return fixturesSeen, fixturesWithStats, capturesStored, teamsSkipped, teamErr
			}
			if !mapped {
				slog.Warn("in-play statistics team skipped: provider team is unmapped",
					"provider", apifootball.ProviderName,
					"job_id", job.ID,
					"provider_fixture_id", fixture.Fixture.Fixture.ID,
					"provider_team_id", team.TeamID,
				)
				teamsSkipped++
				continue
			}
			teamID, teamIDErr := ParseCanonicalID(teamCanonicalID)
			if teamIDErr != nil {
				return fixturesSeen, fixturesWithStats, capturesStored, teamsSkipped, teamIDErr
			}
			stored, writeErr := j.opts.Statistics.AppendSnapshot(ctx, fixtureID, teamID, capturedAt, minute, statRecordFromItems(team.Statistics))
			if writeErr != nil {
				return fixturesSeen, fixturesWithStats, capturesStored, teamsSkipped, writeErr
			}
			if stored {
				capturesStored++
			}
		}
	}

	return fixturesSeen, fixturesWithStats, capturesStored, teamsSkipped, nil
}

// embeddedMatchMinute is the minute of play the statistics describe, or nil.
//
// nil rather than zero when the provider sends no clock: zero is a real minute
// and the ladder conditions on this value, so a missing clock recorded as
// minute 0 would say a 70th-minute corner count was observed at kick-off.
func embeddedMatchMinute(fixture apifootball.FixtureItem) *int {
	elapsed := fixture.Fixture.Status.Elapsed
	if elapsed == nil || *elapsed < 0 {
		return nil
	}
	minute := *elapsed
	return &minute
}

// batchFixtureIDs splits ids into runs of at most size.
func batchFixtureIDs(ids []int64, size int) [][]int64 {
	if size <= 0 {
		size = apifootball.MaxCaptureIDs
	}
	batches := make([][]int64, 0, (len(ids)+size-1)/size)
	for start := 0; start < len(ids); start += size {
		end := start + size
		if end > len(ids) {
			end = len(ids)
		}
		batches = append(batches, ids[start:end])
	}
	return batches
}

// InPlayStatisticsJobKey identifies the batched in-play statistics poll.
func InPlayStatisticsJobKey() string {
	return ProviderJobKey(apifootball.ProviderName, "fixtures.inplay_statistics")
}

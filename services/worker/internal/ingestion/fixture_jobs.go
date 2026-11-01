package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/inplay"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/metrics"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/predictions"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/snapshots"
	"golang.org/x/text/unicode/norm"
)

type entityMapResolver interface {
	entityMapWriter
	LookupCanonicalEntityID(context.Context, string, string, string) (string, bool, error)
	RepointCanonicalEntity(context.Context, string, string, string, string, string, time.Time) error
}

type normalizedFixtureStatus struct {
	Status string
}

type catalogWriter interface {
	UpsertLeague(context.Context, LeagueRecord) (int64, error)
	UpsertTeam(context.Context, TeamRecord) (int64, error)
	TeamExists(context.Context, int64) (bool, error)
	InsertMatch(context.Context, MatchRecord) (int64, error)
	ReconcileMatch(context.Context, int64, MatchRecord) (int64, error)
	FindSuspectedDuplicate(context.Context, MatchRecord, time.Duration) (int64, bool, error)
	FindExactCanonicalFixture(context.Context, string, string, string, time.Time, int64) (int64, bool, error)
	FetchMatchIdentity(context.Context, int64) (MatchIdentity, error)
}

// referenceAttributeWriter stores the crest and country that already arrive in
// the fixtures payload. Fetching them again from the teams endpoint costs a
// request per league per day for data that effectively never changes.
type referenceAttributeWriter interface {
	ApplyTeamAttributes(ctx context.Context, teamID int64, countryCode, logoURL string) error
	ApplyLeagueLogo(ctx context.Context, leagueID int64, logoURL string) error
}

type LeaguesJobOptions struct {
	Client      ProviderClient
	RequestLogs requestLogger
	EntityMaps  entityMapWriter
	RawPayloads rawPayloadWriter
	RateLimits  rateLimitWriter
	Catalog     catalogWriter
	Now         func() time.Time
}

type LeaguesJob struct {
	opts LeaguesJobOptions
}

func NewLeaguesJob(opts LeaguesJobOptions) LeaguesJob {
	return LeaguesJob{opts: opts}
}

func (j LeaguesJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Client == nil {
		return errors.New("leagues job provider client is required")
	}
	if j.opts.RequestLogs == nil {
		return errors.New("leagues job request log repository is required")
	}
	if j.opts.EntityMaps == nil {
		return errors.New("leagues job entity map repository is required")
	}
	now := time.Now().UTC
	if j.opts.Now != nil {
		now = j.opts.Now
	}

	params := url.Values{}
	params.Set("current", "true")
	resp, err := j.opts.Client.Get(ctx, "leagues", params)
	paramsHash := hashParams(params)
	log := RequestLog{
		JobID:         job.ID,
		Provider:      nonEmpty(resp.Provider, apifootball.ProviderName),
		Endpoint:      nonEmpty(resp.Endpoint, "leagues"),
		Method:        "GET",
		ParamsHash:    paramsHash,
		StatusCode:    resp.StatusCode,
		QuotaCost:     providerQuotaCost(resp),
		Latency:       resp.Latency,
		ResponseBytes: resp.ResponseBytes,
		RateLimit:     resp.RateLimit,
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
			EndpointKey:   "leagues",
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

	leagues, err := apifootball.ParseLeagues(resp.Body)
	if err != nil {
		return err
	}
	for _, item := range leagues {
		providerLeagueID := strconv.Itoa(item.League.ID)
		canonicalLeagueID := ""
		if j.opts.Catalog != nil {
			leagueID, err := j.opts.Catalog.UpsertLeague(ctx, LeagueRecord{
				SportSlug:   "football",
				SportName:   "Football",
				CountryCode: item.Country.Code,
				CountryName: item.Country.Name,
				Slug:        canonicalLeagueSlug(item.Country.Name, item.League.Name),
				Name:        item.League.Name,
			})
			if err != nil {
				return err
			}
			canonicalLeagueID = strconv.FormatInt(leagueID, 10)
		}
		mapping := ProviderEntityMap{
			Provider:          apifootball.ProviderName,
			EntityType:        "league",
			ProviderEntityID:  providerLeagueID,
			ProviderEntityKey: item.League.Name,
			CanonicalType:     "league",
			CanonicalID:       canonicalLeagueID,
			MatchStatus:       "resolved",
			Metadata: map[string]any{
				"name":    item.League.Name,
				"type":    item.League.Type,
				"country": item.Country.Name,
				"code":    item.Country.Code,
				"logo":    item.League.Logo,
			},
			SeenAt: now(),
		}
		if mapping.CanonicalID == "" {
			mapping.CanonicalType = ""
			mapping.MatchStatus = "unresolved"
		}
		if err := j.opts.EntityMaps.Upsert(ctx, mapping); err != nil {
			return err
		}
	}
	if err := recordYield(ctx, j.opts.RequestLogs, log, len(leagues)); err != nil {
		slog.Warn("could not record request yield",
			"provider", apifootball.ProviderName,
			"endpoint", "leagues",
			"job_id", job.ID,
			"error", err,
		)
	}
	slog.Info("provider normalization completed",
		"provider", apifootball.ProviderName,
		"endpoint", "leagues",
		"job_id", job.ID,
		"records_fetched", len(leagues),
		"records_normalized", len(leagues),
	)

	return nil
}

type FixturesJobOptions struct {
	Client      ProviderClient
	RequestLogs requestLogger
	EntityMaps  entityMapResolver
	RawPayloads rawPayloadWriter
	RateLimits  rateLimitWriter
	Catalog     catalogWriter
	Reference   referenceAttributeWriter
	Jobs        predictionJobEnqueuer
	Dedupe      *RequestDedupe
	WorkerID    string
	Metrics     *metrics.Registry
	// Snapshots records the append-only live history. Nil disables it
	// entirely, which is the default until an operator turns it on: the
	// feature adds a write per in-play fixture per poll, and that is a
	// deliberate decision rather than something that arrives with a deploy.
	Snapshots liveSnapshotWriter
	// Events persists the event list already embedded in fixtures?live=all.
	// Nil disables it, which is the default until ENABLE_FIXTURE_EVENTS is on:
	// only the live job is ever given a writer, so no other fixture path parses
	// or stores events.
	Events liveEventWriter
	Now    func() time.Time
}

// liveSnapshotWriter is the narrow surface the fixtures job needs. An
// interface rather than the concrete repository so the job can be tested
// without a database, and so a nil value cleanly means "disabled".
type liveSnapshotWriter interface {
	Append(context.Context, snapshots.LiveSnapshot) (bool, error)
}

// liveEventWriter is the same arrangement for the event timeline.
type liveEventWriter interface {
	Append(context.Context, int64, string, time.Time, apifootball.FixtureEvent) (bool, error)
	ReconcileScoringEvents(context.Context, int64, string, time.Time, []apifootball.FixtureEvent, int64, int64, *int, *int) (int, error)
}

type FixturesJob struct {
	opts FixturesJobOptions
}

type fixturesJobPayload struct {
	Date     string `json:"date"`
	TeamID   int    `json:"team_id"`
	Last     int    `json:"last"`
	LeagueID int    `json:"league_id"`
	Season   int    `json:"season"`
	From     string `json:"from"`
	To       string `json:"to"`
	Live     bool   `json:"live"`
	// IDs requests specific provider fixture ids in one call. It is the
	// batching branch the in-play architecture (4.1) marks [VERIFY]; the
	// verification was correct, it did not exist.
	//
	// The rule it enforces is "never call the provider once per fixture". A
	// targeted refresh of twenty stranded matches is one request here and
	// twenty without it, and the same cap applies to every future caller that
	// needs specific fixtures rather than a whole day.
	IDs []int64 `json:"ids"`
}

// MaxFixtureIDsPerRequest is the provider cap on `ids=`. Requests beyond it are
// rejected rather than silently truncated: a caller that quietly loses the
// twenty-first fixture would leave it stranded with no signal that anything was
// dropped.
const MaxFixtureIDsPerRequest = 20

// normalizeFixtureIDs produces the canonical form of an id set.
//
// Sorted and deduplicated, and that ordering is load-bearing rather than
// cosmetic. The cross-worker dedupe lease and the request log both key on a
// hash of the outgoing params, so `ids=5-3-9` and `ids=9-5-3` would otherwise
// be two different requests for identical data -- two provider calls, two log
// rows, and a lease that fails to prevent the concurrent double-fire it exists
// for (4.4).
func normalizeFixtureIDs(ids []int64) []int64 {
	seen := map[int64]bool{}
	normalized := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		normalized = append(normalized, id)
	}
	sort.Slice(normalized, func(a, b int) bool { return normalized[a] < normalized[b] })
	return normalized
}

// fixtureIDsParam renders the provider's hyphen-separated id list.
func fixtureIDsParam(ids []int64) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, "-")
}

// requestReason classifies the provider request for quota cost accounting.
func (p fixturesJobPayload) requestReason() (reason string, priority int) {
	switch {
	case p.Live:
		return "live", 0
	case len(p.IDs) > 0:
		// Corrective work: it ranks above routine pre-match refresh because
		// something is already wrong, and below the live poll that would have
		// made it unnecessary.
		return "reconcile", 5
	case p.LeagueID > 0:
		return "backfill", 40
	case p.TeamID > 0:
		return "team_history", 30
	case strings.TrimSpace(p.Date) != "" && p.Date < time.Now().UTC().Format("2006-01-02"):
		return "backfill", 40
	default:
		return "prematch", 10
	}
}

func NewFixturesJob(opts FixturesJobOptions) FixturesJob {
	return FixturesJob{opts: opts}
}

func (j FixturesJob) Run(ctx context.Context, job jobs.Job) error {
	if j.opts.Client == nil {
		return errors.New("fixtures job provider client is required")
	}
	if j.opts.RequestLogs == nil {
		return errors.New("fixtures job request log repository is required")
	}
	if j.opts.EntityMaps == nil {
		return errors.New("fixtures job entity map repository is required")
	}
	if j.opts.Catalog == nil {
		return errors.New("fixtures job catalog repository is required")
	}
	now := time.Now().UTC
	if j.opts.Now != nil {
		now = j.opts.Now
	}

	payload := fixturesJobPayload{}
	if len(job.Payload) > 0 && string(job.Payload) != "{}" {
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return err
		}
	}
	params := url.Values{}
	switch {
	case payload.Live:
		// Bulk live endpoint: one request returns all in-play fixtures.
		params.Set("live", "all")
	case len(payload.IDs) > 0:
		// Targeted batch: specific fixtures in one request rather than one
		// request per fixture.
		ids := normalizeFixtureIDs(payload.IDs)
		if len(ids) == 0 {
			return errors.New("fixtures job ids must contain at least one positive fixture id")
		}
		if len(ids) > MaxFixtureIDsPerRequest {
			return fmt.Errorf("fixtures job accepts at most %d ids per request, got %d", MaxFixtureIDsPerRequest, len(ids))
		}
		params.Set("ids", fixtureIDsParam(ids))
	case payload.LeagueID > 0 && payload.Season > 0:
		// Historical/backfill window for one league+season. API-Football's
		// fixtures endpoint rejects page, including page=1.
		params.Set("league", strconv.Itoa(payload.LeagueID))
		params.Set("season", strconv.Itoa(payload.Season))
		if strings.TrimSpace(payload.From) != "" {
			params.Set("from", payload.From)
		}
		if strings.TrimSpace(payload.To) != "" {
			params.Set("to", payload.To)
		}
	case payload.TeamID > 0:
		params.Set("team", strconv.Itoa(payload.TeamID))
		if payload.Last > 0 {
			params.Set("last", strconv.Itoa(payload.Last))
		}
	default:
		requestDate := now().Format("2006-01-02")
		if strings.TrimSpace(payload.Date) != "" {
			requestDate = payload.Date
		}
		params.Set("date", requestDate)
	}
	reason, reasonPriority := payload.requestReason()
	// Cross-worker dedupe: claim the exact provider request so two workers can
	// never send it concurrently. If another worker holds the lease, defer the
	// job (retryable) instead of double-firing the provider.
	fingerprint := ""
	if j.opts.Dedupe != nil {
		fingerprint = RequestFingerprint(apifootball.ProviderName, "fixtures", params)
		proceed, err := j.opts.Dedupe.Claim(ctx, fingerprint, j.opts.WorkerID)
		if err != nil {
			return fmt.Errorf("claim provider request lease %s: %w", fingerprint, err)
		}
		if !proceed {
			if j.opts.Metrics != nil {
				j.opts.Metrics.IncCounter("worker_requests_deduplicated_total")
			}
			slog.Warn("provider request deduplicated by another worker",
				"job_id", job.ID, "job_key", job.JobKey, "endpoint", "fixtures", "fingerprint", fingerprint)
			return ErrDeduplicated
		}
		defer func() {
			// Best-effort release with a fresh context so a cancelled job still
			// frees the lease for the deferred worker's retry.
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			if err := j.opts.Dedupe.Release(releaseCtx, fingerprint, j.opts.WorkerID); err != nil {
				slog.Warn("release provider request lease", "fingerprint", fingerprint, "error", err)
			}
		}()
	}
	resp, err := j.opts.Client.Get(ctx, "fixtures", params)
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
		Reason:        reason,
		Priority:      reasonPriority,
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
			EndpointKey:   "fixtures",
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

	fixtures, err := apifootball.ParseFixtures(resp.Body)
	if err != nil {
		return err
	}
	// Parsed once for the whole response, not once per fixture: the events are
	// embedded in the same body the scores came from, so this costs no provider
	// request and no second decode of the payload per fixture.
	eventsByProviderFixture := map[int64][]apifootball.FixtureEvent{}
	eventsParsed := false
	if payload.Live && j.opts.Events != nil {
		events, eventErr := apifootball.ParseLiveFixtureEvents(resp.Body)
		if eventErr != nil {
			// Event history must not freeze the live scoreboard. The match
			// parser and canonical writes remain authoritative for scores.
			slog.Error("live fixture events not parsed",
				"job_id", job.ID,
				"provider", apifootball.ProviderName,
				"error", eventErr,
			)
			if j.opts.Metrics != nil {
				j.opts.Metrics.IncCounter("worker_live_event_parse_failures_total")
			}
		} else {
			eventsParsed = true
			for _, event := range events {
				eventsByProviderFixture[event.FixtureID] = append(eventsByProviderFixture[event.FixtureID], event)
			}
		}
	}
	// How many of this batch are being played right now. Drives the in-play
	// prediction loop below; counted from the provider status the snapshot
	// writer already inspects rather than re-derived.
	liveFixtures := 0
	for _, fixture := range fixtures {
		kickoffAt, err := time.Parse(time.RFC3339, fixture.Fixture.Date)
		if err != nil {
			return fmt.Errorf("parse fixture kickoff %d: %w", fixture.Fixture.ID, err)
		}
		leagueID, err := j.resolveLeague(ctx, fixture.League)
		if err != nil {
			return err
		}
		homeTeamID, err := j.resolveTeam(ctx, fixture.Teams.Home, fixture.League)
		if err != nil {
			return err
		}
		awayTeamID, err := j.resolveTeam(ctx, fixture.Teams.Away, fixture.League)
		if err != nil {
			return err
		}
		matchRecord := MatchRecord{
			Source:            apifootball.ProviderName,
			LeagueID:          leagueID,
			SeasonID:          firstPositive(fixture.League.Season, kickoffAt.UTC().Year()),
			HomeTeamID:        homeTeamID,
			AwayTeamID:        awayTeamID,
			KickoffAt:         kickoffAt,
			Status:            normalizeFixtureStatus(fixture.Fixture.Status.Short, fixture.Fixture.Status.Long).Status,
			RawStatus:         fixture.Fixture.Status.Short,
			RawStatusLabel:    fixture.Fixture.Status.Long,
			StatusElapsed:     fixture.Fixture.Status.Elapsed,
			VenueName:         fixture.Fixture.Venue.Name,
			VenueCity:         fixture.Fixture.Venue.City,
			HomeScore:         fixture.Goals.Home,
			AwayScore:         fixture.Goals.Away,
			HalfTimeHomeScore: fixture.Score.Halftime.Home,
			HalfTimeAwayScore: fixture.Score.Halftime.Away,
		}
		providerFixtureID := strconv.Itoa(fixture.Fixture.ID)
		canonicalID, ok, err := j.opts.EntityMaps.LookupCanonicalEntityID(
			ctx,
			apifootball.ProviderName,
			"match",
			providerFixtureID,
		)
		if err != nil {
			return err
		}
		if ok {
			mappedMatchID, err := ParseCanonicalID(canonicalID)
			if err != nil {
				return err
			}
			// Identity collision detection: a provider fixture id that already
			// maps to a canonical match should keep describing the same real
			// match. When the stored identity diverges from the incoming
			// payload, the provider reused the fixture id for a different match
			// (or the same match arrived under two ids). Signal loudly but keep
			// today's reconcile semantics so ingestion is never blocked.
			stored, fetchErr := j.opts.Catalog.FetchMatchIdentity(ctx, mappedMatchID)
			if fetchErr != nil {
				// Detection is best-effort: a missing or unreadable stored
				// identity must not break ingestion.
				slog.Warn("fetch stored match identity for collision detection",
					"job_id", job.ID,
					"provider", apifootball.ProviderName,
					"provider_fixture_id", providerFixtureID,
					"canonical_match_id", mappedMatchID,
					"error", fetchErr,
				)
			} else if fixtureIdentityMismatch(stored, matchRecord, fixtureIdentityTolerance) {
				slog.Warn("provider fixture identity collision detected",
					"job_id", job.ID,
					"provider", apifootball.ProviderName,
					"provider_fixture_id", providerFixtureID,
					"canonical_match_id", mappedMatchID,
					"stored_league_id", stored.LeagueID,
					"incoming_league_id", matchRecord.LeagueID,
					"stored_home_team_id", stored.HomeTeamID,
					"incoming_home_team_id", matchRecord.HomeTeamID,
					"stored_away_team_id", stored.AwayTeamID,
					"incoming_away_team_id", matchRecord.AwayTeamID,
					"stored_kickoff_at", stored.KickoffAt.UTC(),
					"incoming_kickoff_at", matchRecord.KickoffAt.UTC(),
				)
				if j.opts.Metrics != nil {
					j.opts.Metrics.IncCounter("worker_fixture_identity_collisions_total")
				}
			}
			// A provider may publish the same real fixture under a second fixture or
			// team id. A previously cancelled mapped row may safely converge on the
			// sole active canonical row. If the mapped row is still active, fail
			// closed: repointing without retiring the loser would let the mapping
			// oscillate between two public duplicates on successive polls.
			duplicateID, found, dupErr := j.opts.Catalog.FindExactCanonicalFixture(ctx, fixture.League.Name, fixture.Teams.Home.Name, fixture.Teams.Away.Name, matchRecord.KickoffAt, mappedMatchID)
			if dupErr != nil {
				if errors.Is(dupErr, ErrAmbiguousExactCanonicalFixture) {
					if len(fixtures) < 2 || job.JobKey == BackfillTeamHistoryJobKey() {
						return fmt.Errorf("find exact canonical fixture for mapped provider fixture %s: %w", providerFixtureID, dupErr)
					}
					// This fixture needs a reviewed identity decision. Retrying the
					// entire provider batch cannot resolve it and discards unrelated
					// fixture updates, including confirmed final scores.
					slog.Warn("ambiguous canonical fixture; skipping mapped provider fixture",
						"job_id", job.ID, "provider_fixture_id", providerFixtureID,
						"mapped_match_id", mappedMatchID, "error", dupErr)
					if j.opts.Metrics != nil {
						j.opts.Metrics.IncCounter("worker_fixture_ambiguous_identity_skipped_total")
					}
					continue
				}
				return fmt.Errorf("find exact canonical fixture for mapped provider fixture %s: %w", providerFixtureID, dupErr)
			}
			if found {
				if !isCancelledMatchStatus(stored.Status) {
					// Refusing to converge these two rows is right. Refusing to
					// ingest the rest of the batch because of them is not.
					//
					// This used to return, which aborts the whole job: one
					// ambiguous pair discarded every other fixture in the same
					// request. On 2026-09-22 that killed 69 of 78 fixtures jobs
					// in three hours, all with this error, and those jobs are
					// how a finished match gets its result -- the reaper
					// reconciles by enqueuing one. So matches that had ended
					// never got a score and stayed on the in-play board: 89
					// advertised as live while the prediction engine saw 5, and
					// 44 of them past every plausible duration.
					//
					// The branch below already holds the correct principle for
					// the same class of problem -- "signal loudly without
					// merging; InsertMatch still runs so ingestion is never
					// blocked". This one disagreed with it.
					//
					// So the pair is still not merged, and is still reported,
					// but the batch continues. The duplicate needs a human;
					// every other fixture in the request needs nothing.
					slog.Warn("active canonical fixture duplicate; skipping this fixture and continuing the batch",
						"job_id", job.ID,
						"provider_fixture_id", providerFixtureID,
						"mapped_match_id", mappedMatchID,
						"exact_match_id", duplicateID,
					)
					if j.opts.Metrics != nil {
						j.opts.Metrics.IncCounter("worker_fixture_active_duplicate_skipped_total")
					}
					continue
				}
				canonicalIdentity, identityErr := j.opts.Catalog.FetchMatchIdentity(ctx, duplicateID)
				if identityErr != nil {
					return fmt.Errorf("fetch exact canonical fixture %d: %w", duplicateID, identityErr)
				}
				mappedMatchID = duplicateID
				matchRecord = matchWithCanonicalIdentity(matchRecord, canonicalIdentity)
				if err := j.repointFixtureEntityMaps(ctx, fixture, canonicalIdentity); err != nil {
					return err
				}
			}
			matchID, err := j.opts.Catalog.ReconcileMatch(ctx, mappedMatchID, matchRecord)
			if err != nil {
				return err
			}
			canonicalID = strconv.FormatInt(matchID, 10)
		} else {
			// Suspected duplicate detection: a brand-new provider fixture id can still
			// be the same real match already ingested under another id (a provider
			// reschedule or duplicate). Signal loudly without merging; InsertMatch
			// still runs so ingestion is never blocked.
			duplicateID, duplicateFound, dupErr := j.opts.Catalog.FindSuspectedDuplicate(ctx, matchRecord, suspectedDuplicateWindow)
			if dupErr != nil {
				slog.Warn("find suspected duplicate for fixture ingestion",
					"job_id", job.ID,
					"provider_fixture_id", providerFixtureID,
					"error", dupErr,
				)
			} else if duplicateFound {
				slog.Warn("provider fixture suspected duplicate detected",
					"job_id", job.ID,
					"provider_fixture_id", providerFixtureID,
					"league_id", matchRecord.LeagueID,
					"home_team_id", matchRecord.HomeTeamID,
					"away_team_id", matchRecord.AwayTeamID,
					"kickoff_at", matchRecord.KickoffAt.UTC(),
					"existing_match_id", duplicateID,
				)
				if j.opts.Metrics != nil {
					j.opts.Metrics.IncCounter("worker_fixture_suspected_duplicates_total")
				}
			}
			var matchID int64
			exactID, exactFound, exactErr := j.opts.Catalog.FindExactCanonicalFixture(ctx, fixture.League.Name, fixture.Teams.Home.Name, fixture.Teams.Away.Name, matchRecord.KickoffAt, 0)
			if exactErr != nil {
				if errors.Is(exactErr, ErrAmbiguousExactCanonicalFixture) {
					if len(fixtures) < 2 || job.JobKey == BackfillTeamHistoryJobKey() {
						return fmt.Errorf("find exact canonical fixture for new provider fixture %s: %w", providerFixtureID, exactErr)
					}
					slog.Warn("ambiguous canonical fixture; skipping new provider fixture",
						"job_id", job.ID, "provider_fixture_id", providerFixtureID,
						"error", exactErr)
					if j.opts.Metrics != nil {
						j.opts.Metrics.IncCounter("worker_fixture_ambiguous_identity_skipped_total")
					}
					continue
				}
				return fmt.Errorf("find exact canonical fixture for new provider fixture %s: %w", providerFixtureID, exactErr)
			}
			if exactFound {
				duplicateIdentity, identityErr := j.opts.Catalog.FetchMatchIdentity(ctx, exactID)
				if identityErr != nil {
					return fmt.Errorf("fetch exact canonical fixture %d: %w", exactID, identityErr)
				}
				matchRecord = matchWithCanonicalIdentity(matchRecord, duplicateIdentity)
				matchID, err = j.opts.Catalog.ReconcileMatch(ctx, exactID, matchRecord)
				if err == nil {
					err = j.repointFixtureEntityMaps(ctx, fixture, duplicateIdentity)
				}
			}
			if matchID == 0 && err == nil {
				matchID, err = j.opts.Catalog.InsertMatch(ctx, matchRecord)
			}
			if err != nil {
				return err
			}
			canonicalID = strconv.FormatInt(matchID, 10)
		}
		if j.appendLiveSnapshot(ctx, job, canonicalID, fixture, now()) {
			liveFixtures++
		}
		if eventsParsed {
			j.appendLiveEvents(ctx, job, canonicalID, eventsByProviderFixture[int64(fixture.Fixture.ID)], int64(fixture.Teams.Home.ID), int64(fixture.Teams.Away.ID), fixture.Goals.Home, fixture.Goals.Away, now())
		}
		if err := j.opts.EntityMaps.Upsert(ctx, ProviderEntityMap{
			Provider:          apifootball.ProviderName,
			EntityType:        "match",
			ProviderEntityID:  providerFixtureID,
			ProviderEntityKey: strings.TrimSpace(fixture.Teams.Home.Name + " vs " + fixture.Teams.Away.Name),
			CanonicalType:     "match",
			CanonicalID:       canonicalID,
			MatchStatus:       "resolved",
			Metadata: map[string]any{
				"status":       fixture.Fixture.Status.Short,
				"status_label": fixture.Fixture.Status.Long,
				"league_id":    fixture.League.ID,
				"season":       fixture.League.Season,
				"round":        fixture.League.Round,
			},
			SeenAt: now(),
		}); err != nil {
			return err
		}
	}
	if len(fixtures) > 0 {
		if err := enqueuePredictionGeneration(ctx, j.opts.Jobs, now()); err != nil {
			return err
		}
	}
	// Architecture v2 7.6, the event-driven half. A goal reaches the engine on
	// the poll that observed it rather than on the next timer tick, because
	// this is the same pass that just wrote the snapshot carrying the new
	// score. Best-effort on purpose: the snapshots are already committed and
	// the timer backstop will pick the fixture up regardless, so failing the
	// ingest here would cost a refetch to fix a scheduling hiccup.
	if liveFixtures > 0 && j.opts.Jobs != nil {
		if err := inplay.Enqueue(ctx, j.opts.Jobs, now(), inplay.DefaultTick); err != nil {
			slog.Warn("could not start in-play prediction loop",
				"job_id", job.ID,
				"live_fixtures", liveFixtures,
				"error", err,
			)
		}
	}
	// Yield for the request ledger. Deliberately after the writes and
	// deliberately non-fatal: the rows are already committed, and failing the
	// job here would refetch them on retry to correct a reporting number.
	if err := recordYield(ctx, j.opts.RequestLogs, log, len(fixtures)); err != nil {
		slog.Warn("could not record request yield",
			"provider", apifootball.ProviderName,
			"endpoint", "fixtures",
			"job_id", job.ID,
			"error", err,
		)
	}
	slog.Info("provider normalization completed",
		"provider", apifootball.ProviderName,
		"endpoint", "fixtures",
		"job_id", job.ID,
		"records_fetched", len(fixtures),
		"records_normalized", len(fixtures),
	)

	return nil
}

// fixtureIdentityTolerance is the maximum accepted kickoff drift between the
// stored canonical match and an incoming provider fixture before the identity
// counts as a collision. It absorbs provider clock skew (e.g. a kickoff
// published a minute later on a subsequent poll) without flagging every
// re-fetch.
const fixtureIdentityTolerance = 90 * time.Second

// suspectedDuplicateWindow is the kickoff window within which a same-teams,
// same-league match is treated as a suspected duplicate or reschedule during
// ingestion. Detection signals loudly but never merges silently.
const suspectedDuplicateWindow = 3 * time.Hour

func matchWithCanonicalIdentity(match MatchRecord, identity MatchIdentity) MatchRecord {
	match.LeagueID = identity.LeagueID
	match.HomeTeamID = identity.HomeTeamID
	match.AwayTeamID = identity.AwayTeamID
	match.KickoffAt = identity.KickoffAt
	return match
}

func (j FixturesJob) repointFixtureEntityMaps(ctx context.Context, fixture apifootball.FixtureItem, identity MatchIdentity) error {
	entries := []struct {
		entityType string
		providerID int
		key        string
		canonical  int64
	}{
		{"league", fixture.League.ID, fixture.League.Name, identity.LeagueID},
		{"team", fixture.Teams.Home.ID, fixture.Teams.Home.Name, identity.HomeTeamID},
		{"team", fixture.Teams.Away.ID, fixture.Teams.Away.Name, identity.AwayTeamID},
	}
	for _, entry := range entries {
		if entry.providerID <= 0 || entry.canonical <= 0 {
			continue
		}
		if err := j.opts.EntityMaps.RepointCanonicalEntity(
			ctx,
			apifootball.ProviderName,
			entry.entityType,
			strconv.Itoa(entry.providerID),
			entry.entityType,
			strconv.FormatInt(entry.canonical, 10),
			j.now(),
		); err != nil {
			return err
		}
	}
	return nil
}

// fixtureIdentityMismatch reports whether an incoming provider match record
// diverges from the stored natural identity of the canonical match its
// provider fixture id currently maps to. Divergence means the provider reused
// the fixture id for a different real match (or the same real match arrived
// under two provider ids) and ingestion is about to silently reconcile or
// merge it. League, home team, or away team differences always collide; kickoff
// differences collide only beyond tolerance. It is a pure helper (no DB).
func fixtureIdentityMismatch(stored MatchIdentity, incoming MatchRecord, tolerance time.Duration) bool {
	if stored.LeagueID != incoming.LeagueID {
		return true
	}
	if stored.HomeTeamID != incoming.HomeTeamID {
		return true
	}
	if stored.AwayTeamID != incoming.AwayTeamID {
		return true
	}
	drift := incoming.KickoffAt.UTC().Sub(stored.KickoffAt.UTC())
	if drift < 0 {
		drift = -drift
	}
	return drift > tolerance
}

func isCancelledMatchStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "canceled", "cancelled":
		return true
	default:
		return false
	}
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func enqueuePredictionGeneration(
	ctx context.Context,
	enqueuer predictionJobEnqueuer,
	now time.Time,
) error {
	if enqueuer == nil {
		return nil
	}
	_, err := enqueuer.Enqueue(ctx, jobs.NewJob{
		JobKey:               predictions.GenerateUpcomingJobKey,
		Queue:                "predictions",
		Priority:             40,
		Payload:              []byte(`{"scope":"upcoming"}`),
		IdempotencyKey:       predictions.IdempotencyKey("generate", now, predictions.DefaultRefreshWindow),
		DeduplicateCompleted: true,
		RunAfter:             now.UTC(),
	})
	return err
}

// inPlayProviderStatuses are the API-Football short codes that mean the match
// is happening right now.
//
// Deliberately the provider vocabulary rather than the canonical one: this
// reads fixture.Fixture.Status.Short directly, so translating to canonical
// statuses first would add a mapping layer with nothing to gain.
var inPlayProviderStatuses = map[string]bool{
	"1H": true, "HT": true, "2H": true, "ET": true, "BT": true, "P": true, "LIVE": true,
}

// appendLiveSnapshot records one observation of a match in play.
//
// IT NEVER FAILS THE JOB, and that is a deliberate trade rather than
// carelessness. A snapshot is valuable precisely because it cannot be
// re-fetched, so the instinct is to treat a failed write as fatal. But the
// same job updates `matches`, which is what the live scoreboard reads: failing
// on a snapshot error would stop live scores updating for every fixture in the
// batch, repeatedly, for as long as the underlying cause persisted. A missing
// monthly partition would do exactly that -- and would turn a lost dataset
// into a frozen scoreboard, which is the defect this system has just finished
// fixing.
//
// So the write is best-effort at the call site, and the safety net is upstream:
// the partition maintainer keeps three months ahead, and its failure is what
// should page. Losing snapshots quietly is only acceptable because something
// else is responsible for making it loud.
// appendLiveSnapshot reports whether the fixture it was handed is in play, so
// the caller can start the in-play prediction loop (architecture v2 7.6)
// without asking the provider payload the same question a second time.
//
// It reports on the MATCH BEING LIVE, not on the write succeeding. The write
// is best-effort by design (see above), and a failed snapshot is precisely
// when the engine most needs to be asked to look: the previous snapshot may
// still be inside the freshness window and carry a publishable state.
func (j FixturesJob) appendLiveSnapshot(ctx context.Context, job jobs.Job, canonicalID string, fixture apifootball.FixtureItem, capturedAt time.Time) bool {
	status := strings.ToUpper(strings.TrimSpace(fixture.Fixture.Status.Short))
	if !inPlayProviderStatuses[status] {
		return false
	}
	if j.opts.Snapshots == nil {
		return true
	}
	fixtureID, err := ParseCanonicalID(canonicalID)
	if err != nil {
		return true
	}
	snapshot := snapshots.LiveSnapshot{
		FixtureID:     fixtureID,
		Provider:      apifootball.ProviderName,
		CapturedAt:    capturedAt,
		Status:        status,
		Period:        status,
		ElapsedMinute: fixture.Fixture.Status.Elapsed,
		HomeGoals:     fixture.Goals.Home,
		AwayGoals:     fixture.Goals.Away,
	}
	if fixture.Fixture.Timestamp > 0 {
		providerAt := time.Unix(int64(fixture.Fixture.Timestamp), 0).UTC()
		snapshot.ProviderTimestamp = &providerAt
	}
	if _, err := j.opts.Snapshots.Append(ctx, snapshot); err != nil {
		slog.Error("live snapshot not recorded",
			"job_id", job.ID,
			"provider", apifootball.ProviderName,
			"fixture_id", fixtureID,
			"error", err,
		)
		if j.opts.Metrics != nil {
			j.opts.Metrics.IncCounter("worker_live_snapshot_write_failures_total")
		}
	}
	return true
}

// appendLiveEvents writes the already-parsed events for one provider fixture
// against the canonical match the job just resolved.
//
// It never fails the job, for the reason appendLiveSnapshot gives above:
// optional history must not freeze the live scoreboard. Team ids are stored as
// the provider sent them -- the read path resolves the side through
// provider_entity_maps, and a canonical id here would match nothing and cost
// every event its side without erroring anywhere.
func (j FixturesJob) appendLiveEvents(ctx context.Context, job jobs.Job, canonicalID string, events []apifootball.FixtureEvent, homeTeamID, awayTeamID int64, homeGoals, awayGoals *int, observedAt time.Time) {
	if j.opts.Events == nil {
		return
	}
	fixtureID, err := ParseCanonicalID(canonicalID)
	if err != nil {
		return
	}
	appendFailed := false
	for _, event := range events {
		if _, err := j.opts.Events.Append(ctx, fixtureID, apifootball.ProviderName, observedAt, event); err != nil {
			appendFailed = true
			slog.Error("live fixture event not recorded",
				"job_id", job.ID,
				"provider", apifootball.ProviderName,
				"fixture_id", fixtureID,
				"identity_key", event.IdentityKey,
				"error", err,
			)
			if j.opts.Metrics != nil {
				j.opts.Metrics.IncCounter("worker_live_event_write_failures_total")
			}
		}
	}
	if appendFailed {
		return
	}
	if retired, err := j.opts.Events.ReconcileScoringEvents(ctx, fixtureID, apifootball.ProviderName, observedAt, events, homeTeamID, awayTeamID, homeGoals, awayGoals); err != nil {
		slog.Error("live scoring events not reconciled",
			"job_id", job.ID,
			"provider", apifootball.ProviderName,
			"fixture_id", fixtureID,
			"error", err,
		)
		if j.opts.Metrics != nil {
			j.opts.Metrics.IncCounter("worker_live_event_write_failures_total")
		}
	} else if retired > 0 {
		slog.Info("superseded live scoring events reconciled",
			"fixture_id", fixtureID,
			"changed", retired,
		)
	}
}

func LeaguesJobKey() string {
	return ProviderJobKey(apifootball.ProviderName, "leagues")
}

func FixturesJobKey() string {
	return ProviderJobKey(apifootball.ProviderName, "fixtures")
}

// LiveFixturesJobKey is the bulk live-status polling job (fixtures?live=all).
func LiveFixturesJobKey() string {
	return ProviderJobKey(apifootball.ProviderName, "live")
}

// BackfillFixturesJobKey is the granular league+season backfill job.
func BackfillFixturesJobKey() string {
	return ProviderJobKey(apifootball.ProviderName, "backfill")
}

func canonicalLeagueSlug(country, name string) string {
	countryPart := canonicalNameSlug(country, false)
	namePart := canonicalNameSlug(name, false)
	switch {
	case countryPart == "spain" && namePart == "primera-division":
		namePart = "la-liga"
	case namePart == "fifa-world-cup":
		namePart = "world-cup"
	case countryPart == "brazil" && namePart == "campeonato-brasileiro-serie-a":
		namePart = "serie-a"
	}
	return joinCanonicalEntitySlug("league", countryPart, namePart)
}

// now returns the job clock, overridable in tests.
func (j FixturesJob) now() time.Time {
	if j.opts.Now != nil {
		return j.opts.Now()
	}
	return time.Now().UTC()
}

// resolveTeam maps a provider team to exactly one canonical row.
//
// Identity used to be keyed on a slug built from the *competition's* country.
// API-Football reports a continental competition's country as "World", so a
// Champions League fixture minted `team-world-bayern-munich` beside the
// existing `team-germany-bayern-munich`: two rows for one club. Crests landed
// on the domestic row while fixtures pointed at the stub, and each club's match
// history was split across the two, leaving the prediction engine reading a
// fraction of a team's record.
//
// The provider's own team id is stable and competition-independent, so it is
// the identity. The slug remains only as the fallback for a payload without one.
func (j FixturesJob) resolveTeam(ctx context.Context, team apifootball.FixtureTeam, league apifootball.FixtureLeague) (int64, error) {
	providerID := ""
	if team.ID > 0 {
		providerID = strconv.Itoa(team.ID)
	}
	if providerID != "" && j.opts.EntityMaps != nil {
		canonicalID, found, err := j.opts.EntityMaps.LookupCanonicalEntityID(ctx, apifootball.ProviderName, "team", providerID)
		if err != nil {
			return 0, err
		}
		if found {
			if teamID, err := strconv.ParseInt(canonicalID, 10, 64); err == nil && teamID > 0 {
				exists, err := j.opts.Catalog.TeamExists(ctx, teamID)
				if err != nil {
					return 0, err
				}
				if exists {
					if err := j.applyTeamAttributes(ctx, teamID, team); err != nil {
						return 0, err
					}
					return teamID, nil
				}
				// A deleted team can leave its provider map behind. Resolve the
				// fixture against a real team row and replace the stale mapping
				// instead of passing a nonexistent ID to matches.
			}
		}
	}

	teamID, err := j.opts.Catalog.UpsertTeam(ctx, TeamRecord{
		Slug: canonicalTeamSlug(league.Country, team.Name),
		Name: team.Name,
	})
	if err != nil {
		return 0, err
	}
	if err := j.applyTeamAttributes(ctx, teamID, team); err != nil {
		return 0, err
	}
	// Record the mapping so the next fixture resolves by id and converges on
	// this row rather than minting another one.
	if providerID != "" && j.opts.EntityMaps != nil {
		if err := j.opts.EntityMaps.Upsert(ctx, ProviderEntityMap{
			Provider:          apifootball.ProviderName,
			EntityType:        "team",
			ProviderEntityID:  providerID,
			ProviderEntityKey: team.Name,
			CanonicalType:     "team",
			CanonicalID:       strconv.FormatInt(teamID, 10),
			MatchStatus:       "resolved",
			SeenAt:            j.now(),
		}); err != nil {
			return 0, err
		}
	}
	return teamID, nil
}

func (j FixturesJob) resolveLeague(ctx context.Context, league apifootball.FixtureLeague) (int64, error) {
	providerID := ""
	if league.ID > 0 {
		providerID = strconv.Itoa(league.ID)
	}
	if providerID != "" && j.opts.EntityMaps != nil {
		canonicalID, found, err := j.opts.EntityMaps.LookupCanonicalEntityID(ctx, apifootball.ProviderName, "league", providerID)
		if err != nil {
			return 0, err
		}
		if found {
			if leagueID, err := strconv.ParseInt(canonicalID, 10, 64); err == nil && leagueID > 0 {
				if err := j.applyLeagueLogo(ctx, leagueID, league.Logo); err != nil {
					return 0, err
				}
				return leagueID, nil
			}
		}
	}

	leagueID, err := j.opts.Catalog.UpsertLeague(ctx, LeagueRecord{
		SportSlug:   "football",
		SportName:   "Football",
		CountryName: league.Country,
		Slug:        canonicalLeagueSlug(league.Country, league.Name),
		Name:        league.Name,
	})
	if err != nil {
		return 0, err
	}
	if err := j.applyLeagueLogo(ctx, leagueID, league.Logo); err != nil {
		return 0, err
	}
	if providerID != "" && j.opts.EntityMaps != nil {
		if err := j.opts.EntityMaps.Upsert(ctx, ProviderEntityMap{
			Provider:          apifootball.ProviderName,
			EntityType:        "league",
			ProviderEntityID:  providerID,
			ProviderEntityKey: league.Name,
			CanonicalType:     "league",
			CanonicalID:       strconv.FormatInt(leagueID, 10),
			MatchStatus:       "resolved",
			SeenAt:            j.now(),
		}); err != nil {
			return 0, err
		}
	}
	return leagueID, nil
}

// applyTeamAttributes stores the crest that came free with this payload.
// ApplyTeamAttributes only ever fills a blank column, so a stored crest is
// never overwritten and the write is a no-op once populated.
func (j FixturesJob) applyTeamAttributes(ctx context.Context, teamID int64, team apifootball.FixtureTeam) error {
	if j.opts.Reference == nil || strings.TrimSpace(team.Logo) == "" {
		return nil
	}
	return j.opts.Reference.ApplyTeamAttributes(ctx, teamID, "", team.Logo)
}

func (j FixturesJob) applyLeagueLogo(ctx context.Context, leagueID int64, logo string) error {
	if j.opts.Reference == nil || strings.TrimSpace(logo) == "" {
		return nil
	}
	return j.opts.Reference.ApplyLeagueLogo(ctx, leagueID, logo)
}

func canonicalTeamSlug(country, name string) string {
	return canonicalEntitySlug("team", country, name, true)
}

func canonicalEntitySlug(kind, country, name string, stripClubDesignators bool) string {
	countryPart := canonicalNameSlug(country, false)
	namePart := canonicalNameSlug(name, stripClubDesignators)
	return joinCanonicalEntitySlug(kind, countryPart, namePart)
}

func joinCanonicalEntitySlug(kind, countryPart, namePart string) string {
	if namePart == "" {
		namePart = kind
	}
	if countryPart == "" {
		return kind + "-" + namePart
	}
	return kind + "-" + countryPart + "-" + namePart
}

func canonicalNameSlug(value string, stripClubDesignators bool) string {
	decomposed := norm.NFKD.String(strings.TrimSpace(strings.ToLower(value)))
	var folded strings.Builder
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		folded.WriteRune(r)
	}
	parts := strings.FieldsFunc(slug(folded.String()), func(r rune) bool { return r == '-' })
	if !stripClubDesignators {
		return strings.Join(parts, "-")
	}
	filtered := parts[:0]
	hadClubDesignator := false
	for _, part := range parts {
		switch part {
		case "fc", "afc", "cf", "sc", "ac", "fbc", "fsv", "sv", "club":
			hadClubDesignator = true
			continue
		default:
			filtered = append(filtered, part)
		}
	}
	if hadClubDesignator && len(filtered) > 1 && filtered[0] == "1" {
		filtered = filtered[1:]
	}
	normalized := strings.Join(filtered, "-")
	switch normalized {
	case "bayern-munchen":
		return "bayern-munich"
	default:
		return normalized
	}
}

func normalizeFixtureStatus(short string, long string) normalizedFixtureStatus {
	status := strings.ToUpper(strings.TrimSpace(short))
	label := strings.ToLower(strings.TrimSpace(long))
	switch status {
	case "NS", "TBD":
		return normalizedFixtureStatus{Status: "upcoming"}
	case "1H", "LIVE":
		return normalizedFixtureStatus{Status: "live"}
	case "HT":
		return normalizedFixtureStatus{Status: "halftime"}
	case "2H":
		return normalizedFixtureStatus{Status: "second_half"}
	case "ET":
		return normalizedFixtureStatus{Status: "extra_time"}
	case "BT":
		return normalizedFixtureStatus{Status: "break"}
	case "P":
		return normalizedFixtureStatus{Status: "penalties"}
	case "FT", "AET", "PEN":
		return normalizedFixtureStatus{Status: "finished"}
	case "PST":
		return normalizedFixtureStatus{Status: "postponed"}
	case "CANC":
		return normalizedFixtureStatus{Status: "canceled"}
	case "ABD":
		return normalizedFixtureStatus{Status: "abandoned"}
	case "SUSP":
		return normalizedFixtureStatus{Status: "suspended"}
	case "INT":
		return normalizedFixtureStatus{Status: "interrupted"}
	case "AWD":
		return normalizedFixtureStatus{Status: "awarded"}
	case "WO":
		return normalizedFixtureStatus{Status: "walkover"}
	default:
		if strings.Contains(label, "delay") {
			return normalizedFixtureStatus{Status: "delayed"}
		}
		return normalizedFixtureStatus{Status: "upcoming"}
	}
}

package ingestion

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/footballdata"
)

type fakeProviderJobEnqueuer struct {
	jobs []jobs.NewJob
	err  error
}

func (f *fakeProviderJobEnqueuer) Enqueue(_ context.Context, job jobs.NewJob) (int64, error) {
	f.jobs = append(f.jobs, job)
	return int64(len(f.jobs)), f.err
}

func TestFootballDataCompetitionsJobLogsAndMapsCompetitions(t *testing.T) {
	client := &fakeProviderClient{response: provider.Response{
		Provider:      footballdata.ProviderName,
		Endpoint:      "competitions",
		StatusCode:    200,
		Body:          []byte(`{"count":1,"competitions":[{"id":2021,"area":{"id":2072,"name":"England","code":"ENG"},"name":"Premier League","code":"PL","type":"LEAGUE","emblem":"https://crests.football-data.org/PL.png","plan":"TIER_ONE","currentSeason":{"id":2400,"startDate":"2026-08-15","endDate":"2027-05-24","currentMatchday":1}}]}`),
		Latency:       20 * time.Millisecond,
		ResponseBytes: 256,
		RateLimit: provider.RateLimitHeaders{
			MinuteLimit:     10,
			MinuteRemaining: 8,
			Raw:             map[string]string{"x-requestsavailable": "8"},
		},
	}}
	logs := &fakeRequestLogger{}
	maps := &fakeEntityMapWriter{}
	payloads := &fakeRawPayloadWriter{}
	rateLimits := &fakeRateLimitWriter{}
	catalog := &fakeCatalog{}

	job := NewFootballDataCompetitionsJob(FootballDataCompetitionsJobOptions{
		Client:      client,
		RequestLogs: logs,
		EntityMaps:  maps,
		RawPayloads: payloads,
		RateLimits:  rateLimits,
		Catalog:     catalog,
	})
	if err := job.Run(context.Background(), jobs.Job{}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if client.endpoint != "competitions" {
		t.Fatalf("endpoint = %q, want competitions", client.endpoint)
	}
	if len(maps.maps) != 1 {
		t.Fatalf("maps = %d, want 1", len(maps.maps))
	}
	if maps.maps[0].Provider != footballdata.ProviderName || maps.maps[0].CanonicalType != "league" {
		t.Fatalf("mapping = %+v, want football-data.org canonical league", maps.maps[0])
	}
	if len(rateLimits.updates) != 1 || rateLimits.updates[0].Provider != footballdata.ProviderName {
		t.Fatalf("rate limit updates = %+v, want football-data.org update", rateLimits.updates)
	}
	if len(payloads.payloads) != 1 || payloads.payloads[0].Provider != footballdata.ProviderName {
		t.Fatalf("payloads = %+v, want football-data.org metadata", payloads.payloads)
	}
}

func TestFootballDataCompetitionsJobEnqueuesDailyCurrentSeasonHistoryForTierOne(t *testing.T) {
	now := time.Date(2026, 7, 14, 9, 30, 0, 0, time.UTC)
	body := []byte(`{"count":3,"competitions":[
		{"id":2021,"area":{"name":"England","code":"ENG"},"name":"Premier League","code":"PL","type":"LEAGUE","plan":"TIER_ONE","currentSeason":{"id":2400,"startDate":"2025-08-15","endDate":"2026-05-24"}},
		{"id":2014,"area":{"name":"Spain","code":"ESP"},"name":"Primera Division","code":"PD","type":"LEAGUE","plan":"TIER_ONE","currentSeason":{"id":2401,"startDate":"2025-08-10","endDate":"2026-05-20"}},
		{"id":9999,"area":{"name":"Test","code":"TST"},"name":"Paid League","code":"PAID","type":"LEAGUE","plan":"TIER_TWO","currentSeason":{"id":2402,"startDate":"2025-08-01","endDate":"2026-05-01"}}
	]}`)
	client := &fakeProviderClient{response: provider.Response{
		Provider:      footballdata.ProviderName,
		Endpoint:      "competitions",
		StatusCode:    200,
		Body:          body,
		ResponseBytes: len(body),
	}}
	logs := &fakeRequestLogger{}
	enqueuer := &fakeProviderJobEnqueuer{}
	job := NewFootballDataCompetitionsJob(FootballDataCompetitionsJobOptions{
		Client:      client,
		RequestLogs: logs,
		EntityMaps:  &fakeEntityMapWriter{},
		Catalog:     &fakeCatalog{},
		Jobs:        enqueuer,
		Now:         func() time.Time { return now },
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 808}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(enqueuer.jobs) != 2 {
		t.Fatalf("child jobs = %+v, want 2 TIER_ONE competitions", enqueuer.jobs)
	}
	for _, child := range enqueuer.jobs {
		if child.JobKey != FootballDataMatchesJobKey() || child.Queue != "provider-fixtures" {
			t.Fatalf("child = %+v, want football-data matches fixture queue", child)
		}
		if !child.DeduplicateCompleted {
			t.Fatalf("child = %+v, want completed-job deduplication", child)
		}
		if !strings.HasSuffix(child.IdempotencyKey, ":2026-07-14") {
			t.Fatalf("idempotency key = %q, want daily bucket", child.IdempotencyKey)
		}
		var payload footballDataMatchesPayload
		if err := json.Unmarshal(child.Payload, &payload); err != nil {
			t.Fatalf("unmarshal child payload: %v", err)
		}
		if payload.Competition == "" || payload.Season != "2025" || payload.Status != "" {
			t.Fatalf("payload = %+v, want competition and complete current season for history plus fixture fallback", payload)
		}
	}
	if len(logs.logs) != 1 || logs.logs[0].JobID != 808 {
		t.Fatalf("logs = %+v, want parent job id 808", logs.logs)
	}
}

func TestFootballDataMatchesJobNormalizesNullableScheduledAndFinishedMatches(t *testing.T) {
	body := []byte(`{"resultSet":{"count":2,"played":1},"matches":[{"area":{"id":2072,"name":"England","code":"ENG"},"competition":{"id":2021,"name":"Premier League","code":"PL","type":"LEAGUE"},"season":{"id":2400,"startDate":"2026-08-15","endDate":"2027-05-24","currentMatchday":1},"id":501,"utcDate":"2026-08-15T14:00:00Z","status":"TIMED","minute":null,"venue":null,"matchday":1,"stage":"REGULAR_SEASON","homeTeam":{"id":57,"name":"Arsenal FC","shortName":"Arsenal","tla":"ARS"},"awayTeam":{"id":61,"name":"Chelsea FC","shortName":"Chelsea","tla":"CHE"},"score":{"winner":null,"duration":"REGULAR","fullTime":{"home":null,"away":null},"halfTime":{"home":null,"away":null}}},{"area":{"id":2072,"name":"England","code":"ENG"},"competition":{"id":2021,"name":"Premier League","code":"PL","type":"LEAGUE"},"season":{"id":2400,"startDate":"2026-08-15","endDate":"2027-05-24","currentMatchday":1},"id":502,"utcDate":"2026-08-16T14:00:00Z","status":"FINISHED","minute":90,"venue":"Test Ground","matchday":1,"stage":"REGULAR_SEASON","homeTeam":{"id":64,"name":"Liverpool FC","shortName":"Liverpool","tla":"LIV"},"awayTeam":{"id":62,"name":"Everton FC","shortName":"Everton","tla":"EVE"},"score":{"winner":"HOME_TEAM","duration":"REGULAR","fullTime":{"home":2,"away":1},"halfTime":{"home":1,"away":0}}}]}`)
	client := &fakeProviderClient{response: provider.Response{
		Provider:      footballdata.ProviderName,
		Endpoint:      "competitions/PL/matches",
		StatusCode:    200,
		Body:          body,
		Latency:       20 * time.Millisecond,
		ResponseBytes: len(body),
	}}
	logs := &fakeRequestLogger{}
	maps := &fakeEntityMapResolver{
		lookup: map[string]string{
			footballdata.ProviderName + ":match:502": "77",
		},
	}
	catalog := &fakeCatalog{reconcileTo: 88}
	enqueuer := &fakeProviderJobEnqueuer{}

	job := NewFootballDataMatchesJob(FootballDataMatchesJobOptions{
		Client:      client,
		RequestLogs: logs,
		EntityMaps:  maps,
		Catalog:     catalog,
		Jobs:        enqueuer,
		Now: func() time.Time {
			return time.Date(2026, 8, 4, 16, 47, 0, 0, time.UTC)
		},
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 7001, Payload: []byte(`{"competition":"PL","status":"FINISHED","date_from":"2026-08-01","date_to":"2026-09-01"}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if client.endpoint != "competitions/PL/matches" {
		t.Fatalf("endpoint = %q, want competitions/PL/matches", client.endpoint)
	}
	if got := client.query.Get("dateFrom"); got != "2026-08-01" {
		t.Fatalf("dateFrom = %q, want 2026-08-01", got)
	}
	if got := client.query.Get("status"); got != "FINISHED" {
		t.Fatalf("status = %q, want FINISHED", got)
	}
	if len(catalog.inserted) != 1 || len(catalog.reconciled) != 1 {
		t.Fatalf("inserted=%d reconciled=%d, want 1 and 1", len(catalog.inserted), len(catalog.reconciled))
	}
	if catalog.inserted[0].Status != "upcoming" {
		t.Fatalf("inserted status = %q, want upcoming", catalog.inserted[0].Status)
	}
	if catalog.inserted[0].SeasonID != 2026 {
		t.Fatalf("inserted season = %d, want football-data season start 2026", catalog.inserted[0].SeasonID)
	}
	if catalog.inserted[0].HomeScore != nil || catalog.inserted[0].AwayScore != nil {
		t.Fatalf("scheduled scores should be nil: %+v", catalog.inserted[0])
	}
	updated := catalog.reconciled[77]
	if updated.Status != "finished" {
		t.Fatalf("updated status = %q, want finished", updated.Status)
	}
	if updated.HomeScore == nil || *updated.HomeScore != 2 || updated.HalfTimeHomeScore == nil || *updated.HalfTimeHomeScore != 1 {
		t.Fatalf("updated scores = %+v, want fulltime 2 and halftime 1", updated)
	}
	if len(maps.maps) != 2 || maps.maps[1].CanonicalID != "88" {
		t.Fatalf("maps = %+v, want converged canonical id 88 for provider match 502", maps.maps)
	}
	if len(enqueuer.jobs) != 1 {
		t.Fatalf("prediction jobs = %+v, want one post-normalization trigger", enqueuer.jobs)
	}
	if got := enqueuer.jobs[0]; got.JobKey != "predictions.generate.upcoming" || got.Queue != "predictions" || !got.DeduplicateCompleted {
		t.Fatalf("prediction job = %+v, want deduplicated upcoming generation", got)
	}
	if got := enqueuer.jobs[0].IdempotencyKey; got != "schedule:predictions:generate:20260804T164500Z" {
		t.Fatalf("prediction idempotency key = %q, want global refresh bucket", got)
	}
}

func TestFootballDataMatchesJobSkipsScheduledPlaceholderPairings(t *testing.T) {
	body := []byte(`{"matches":[{"area":{"id":2267,"name":"World","code":"INT"},"competition":{"id":2000,"name":"FIFA World Cup","code":"WC"},"season":{"id":2407,"startDate":"2026-06-11"},"id":9001,"utcDate":"2026-07-14T19:00:00Z","status":"SCHEDULED","stage":"LAST_16","homeTeam":{"id":null,"name":null},"awayTeam":{"id":null,"name":null},"score":{"fullTime":{"home":null,"away":null}}},{"area":{"id":2267,"name":"World","code":"INT"},"competition":{"id":2000,"name":"FIFA World Cup","code":"WC"},"season":{"id":2407,"startDate":"2026-06-11"},"id":9002,"utcDate":"2026-07-15T19:00:00Z","status":"TIMED","stage":"LAST_16","homeTeam":{"id":57,"name":"Arsenal FC"},"awayTeam":{"id":61,"name":"Chelsea FC"},"score":{"fullTime":{"home":null,"away":null}}}]}`)
	client := &fakeProviderClient{response: provider.Response{
		Provider:      footballdata.ProviderName,
		Endpoint:      "competitions/WC/matches",
		StatusCode:    200,
		Body:          body,
		ResponseBytes: len(body),
	}}
	catalog := &fakeCatalog{}
	maps := &fakeEntityMapResolver{}
	enqueuer := &fakeProviderJobEnqueuer{}
	job := NewFootballDataMatchesJob(FootballDataMatchesJobOptions{
		Client:      client,
		RequestLogs: &fakeRequestLogger{},
		EntityMaps:  maps,
		Catalog:     catalog,
		Jobs:        enqueuer,
	})

	if err := job.Run(context.Background(), jobs.Job{ID: 7002, Payload: []byte(`{"competition":"WC","season":"2026"}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(catalog.inserted) != 1 {
		t.Fatalf("inserted = %d, want only the fully identified match", len(catalog.inserted))
	}
	if len(maps.maps) != 1 || maps.maps[0].ProviderEntityID != "9002" {
		t.Fatalf("maps = %+v, want only match 9002", maps.maps)
	}
	if len(enqueuer.jobs) != 1 {
		t.Fatalf("prediction jobs = %+v, want one trigger for normalized records", enqueuer.jobs)
	}
}

func TestFootballDataMatchesJobDoesNotTriggerPredictionsForOnlyPlaceholders(t *testing.T) {
	body := []byte(`{"matches":[{"area":{"id":2267,"name":"World","code":"INT"},"competition":{"id":2000,"name":"FIFA World Cup","code":"WC"},"season":{"id":2407,"startDate":"2026-06-11"},"id":537390,"utcDate":"2026-07-19T19:00:00Z","status":"TIMED","stage":"FINAL","homeTeam":{"id":null,"name":null},"awayTeam":{"id":null,"name":null},"score":{"fullTime":{"home":null,"away":null}}}]}`)
	client := &fakeProviderClient{response: provider.Response{
		Provider:      footballdata.ProviderName,
		StatusCode:    200,
		Body:          body,
		ResponseBytes: len(body),
	}}
	catalog := &fakeCatalog{}
	maps := &fakeEntityMapResolver{}
	enqueuer := &fakeProviderJobEnqueuer{}
	job := NewFootballDataMatchesJob(FootballDataMatchesJobOptions{
		Client:      client,
		RequestLogs: &fakeRequestLogger{},
		EntityMaps:  maps,
		Catalog:     catalog,
		Jobs:        enqueuer,
	})

	if err := job.Run(context.Background(), jobs.Job{ID: 7003, Payload: []byte(`{"competition":"WC","season":"2026"}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(catalog.inserted) != 0 || len(maps.maps) != 0 || len(enqueuer.jobs) != 0 {
		t.Fatalf("inserted=%d maps=%d prediction_jobs=%d, want all zero", len(catalog.inserted), len(maps.maps), len(enqueuer.jobs))
	}
}

func TestFootballDataTeamsJobMapsCompetitionTeams(t *testing.T) {
	body := []byte(`{"count":1,"competition":{"id":2021,"name":"Premier League","code":"PL"},"teams":[{"id":57,"area":{"id":2072,"name":"England","code":"ENG"},"name":"Arsenal FC","shortName":"Arsenal","tla":"ARS","crest":"https://crests.football-data.org/57.png","venue":"Emirates Stadium"}]}`)
	client := &fakeProviderClient{response: provider.Response{
		Provider:      footballdata.ProviderName,
		Endpoint:      "competitions/PL/teams",
		StatusCode:    200,
		Body:          body,
		Latency:       20 * time.Millisecond,
		ResponseBytes: len(body),
	}}
	logs := &fakeRequestLogger{}
	maps := &fakeEntityMapWriter{}
	catalog := &fakeCatalog{}

	job := NewFootballDataTeamsJob(FootballDataTeamsJobOptions{
		Client:      client,
		RequestLogs: logs,
		EntityMaps:  maps,
		Catalog:     catalog,
	})
	if err := job.Run(context.Background(), jobs.Job{Payload: []byte(`{"competition":"PL","season":"2026"}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if client.endpoint != "competitions/PL/teams" {
		t.Fatalf("endpoint = %q, want competitions/PL/teams", client.endpoint)
	}
	if got := client.query.Get("season"); got != "2026" {
		t.Fatalf("season = %q, want 2026", got)
	}
	if len(maps.maps) != 1 || maps.maps[0].ProviderEntityID != "57" {
		t.Fatalf("maps = %+v, want team mapping for provider id 57", maps.maps)
	}
	if maps.maps[0].CanonicalType != "team" || maps.maps[0].CanonicalID == "" {
		t.Fatalf("mapping = %+v, want resolved canonical team", maps.maps[0])
	}
}

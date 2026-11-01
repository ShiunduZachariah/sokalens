package ingestion

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/metrics"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
)

type fakeEntityMapResolver struct {
	maps          []ProviderEntityMap
	lookup        map[string]string
	upsertErr     error
	lookupErr     error
	lookupQueries []string
}

func (f *fakeEntityMapResolver) Upsert(_ context.Context, mapping ProviderEntityMap) error {
	f.maps = append(f.maps, mapping)
	return f.upsertErr
}

func (f *fakeEntityMapResolver) LookupCanonicalEntityID(
	_ context.Context,
	provider string,
	entityType string,
	providerEntityID string,
) (string, bool, error) {
	f.lookupQueries = append(f.lookupQueries, provider+":"+entityType+":"+providerEntityID)
	if f.lookupErr != nil {
		return "", false, f.lookupErr
	}
	value, ok := f.lookup[provider+":"+entityType+":"+providerEntityID]
	return value, ok, nil
}

func (f *fakeEntityMapResolver) RepointCanonicalEntity(_ context.Context, provider, entityType, providerEntityID, canonicalType, canonicalID string, seenAt time.Time) error {
	f.maps = append(f.maps, ProviderEntityMap{Provider: provider, EntityType: entityType, ProviderEntityID: providerEntityID, CanonicalType: canonicalType, CanonicalID: canonicalID, MatchStatus: "resolved", SeenAt: seenAt})
	return f.upsertErr
}

type fakeCatalog struct {
	leaguesBySlug            map[string]int64
	teamsBySlug              map[string]int64
	missingTeamIDs           map[int64]bool
	inserted                 []MatchRecord
	updated                  map[int64]MatchRecord
	reconciled               map[int64]MatchRecord
	reconcileTo              int64
	nextID                   int64
	identities               map[int64]MatchIdentity
	suspectedDuplicateID     int64
	suspectedDuplicateFound  bool
	suspectedDuplicateErr    error
	exactDuplicateID         int64
	exactDuplicateFound      bool
	exactDuplicateErr        error
	exactDuplicateErrForHome string
	identityErr              error
}

func (f *fakeCatalog) TeamExists(_ context.Context, teamID int64) (bool, error) {
	return !f.missingTeamIDs[teamID], nil
}

func TestFixturesJobRepairsMapToDeletedTeamBeforeMatchInsertion(t *testing.T) {
	entityMaps := &fakeEntityMapResolver{lookup: map[string]string{
		"api-football:team:12649": "36363",
	}}
	catalog := &fakeCatalog{nextID: 100, missingTeamIDs: map[int64]bool{36363: true}}
	job := NewFixturesJob(FixturesJobOptions{Catalog: catalog, EntityMaps: entityMaps})

	teamID, err := job.resolveTeam(context.Background(),
		apifootball.FixtureTeam{ID: 12649, Name: "Konyaspor"},
		apifootball.FixtureLeague{Country: "Sweden"},
	)
	if err != nil {
		t.Fatalf("resolveTeam: %v", err)
	}
	if teamID != 101 {
		t.Fatalf("team ID = %d, want newly created canonical team 101", teamID)
	}
	if got := catalog.teamsBySlug["team-sweden-konyaspor"]; got != teamID {
		t.Fatalf("canonical slug points to %d, want %d", got, teamID)
	}
	if len(entityMaps.maps) != 1 || entityMaps.maps[0].CanonicalID != "101" {
		t.Fatalf("provider map was not repaired: %+v", entityMaps.maps)
	}
}

func (f *fakeCatalog) UpsertLeague(_ context.Context, league LeagueRecord) (int64, error) {
	if f.leaguesBySlug == nil {
		f.leaguesBySlug = map[string]int64{}
	}
	if id, ok := f.leaguesBySlug[league.Slug]; ok {
		return id, nil
	}
	f.nextID++
	f.leaguesBySlug[league.Slug] = f.nextID
	return f.nextID, nil
}

func (f *fakeCatalog) UpsertTeam(_ context.Context, team TeamRecord) (int64, error) {
	if f.teamsBySlug == nil {
		f.teamsBySlug = map[string]int64{}
	}
	if id, ok := f.teamsBySlug[team.Slug]; ok {
		return id, nil
	}
	f.nextID++
	f.teamsBySlug[team.Slug] = f.nextID
	return f.nextID, nil
}

func (f *fakeCatalog) InsertMatch(_ context.Context, match MatchRecord) (int64, error) {
	f.inserted = append(f.inserted, match)
	f.nextID++
	return f.nextID, nil
}

func (f *fakeCatalog) UpdateMatch(_ context.Context, matchID int64, match MatchRecord) error {
	if f.updated == nil {
		f.updated = map[int64]MatchRecord{}
	}
	f.updated[matchID] = match
	return nil
}

func (f *fakeCatalog) ReconcileMatch(_ context.Context, matchID int64, match MatchRecord) (int64, error) {
	if f.reconciled == nil {
		f.reconciled = map[int64]MatchRecord{}
	}
	f.reconciled[matchID] = match
	if f.reconcileTo > 0 {
		return f.reconcileTo, nil
	}
	return matchID, nil
}

func (f *fakeCatalog) FetchMatchIdentity(_ context.Context, matchID int64) (MatchIdentity, error) {
	if f.identityErr != nil {
		return MatchIdentity{}, f.identityErr
	}
	identity, ok := f.identities[matchID]
	if !ok {
		return MatchIdentity{}, sql.ErrNoRows
	}
	return identity, nil
}

func (f *fakeCatalog) FindSuspectedDuplicate(_ context.Context, _ MatchRecord, _ time.Duration) (int64, bool, error) {
	return f.suspectedDuplicateID, f.suspectedDuplicateFound, f.suspectedDuplicateErr
}

func (f *fakeCatalog) FindExactCanonicalFixture(_ context.Context, _, homeName, _ string, _ time.Time, _ int64) (int64, bool, error) {
	if f.exactDuplicateErrForHome != "" && homeName != f.exactDuplicateErrForHome {
		return 0, false, nil
	}
	return f.exactDuplicateID, f.exactDuplicateFound, f.exactDuplicateErr
}

func TestLeaguesJobLogsAndMapsLeagues(t *testing.T) {
	client := &fakeProviderClient{response: provider.Response{
		Provider:      "api-football",
		Endpoint:      "leagues",
		StatusCode:    200,
		Body:          []byte(`{"errors":[],"response":[{"league":{"id":39,"name":"Premier League","type":"League"},"country":{"name":"England","code":"GB"},"seasons":[{"year":2026,"current":true,"coverage":{"fixtures":{"events":true,"lineups":true,"statistics_fixtures":true,"statistics_players":true},"standings":true,"players":true,"top_scorers":true,"top_assists":true,"top_cards":true,"injuries":true,"predictions":true,"odds":true}}]}]}`),
		Latency:       20 * time.Millisecond,
		ResponseBytes: 256,
	}}
	logs := &fakeRequestLogger{}
	maps := &fakeEntityMapWriter{}
	catalog := &fakeCatalog{}

	job := NewLeaguesJob(LeaguesJobOptions{
		Client:      client,
		RequestLogs: logs,
		EntityMaps:  maps,
		Catalog:     catalog,
	})
	if err := job.Run(context.Background(), jobs.Job{}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if client.endpoint != "leagues" {
		t.Fatalf("endpoint = %q, want leagues", client.endpoint)
	}
	if len(logs.logs) != 1 {
		t.Fatalf("logs = %d, want 1", len(logs.logs))
	}
	if len(maps.maps) != 1 {
		t.Fatalf("maps = %d, want 1", len(maps.maps))
	}
	if maps.maps[0].CanonicalType != "league" || maps.maps[0].CanonicalID == "" {
		t.Fatalf("mapping = %+v, want resolved canonical league", maps.maps[0])
	}
}

func TestFixturesJobInsertsAndUpdatesMatchesUsingEntityMapResolution(t *testing.T) {
	body := []byte(`{"errors":[],"response":[{"fixture":{"id":1001,"date":"2026-05-13T18:00:00+00:00","status":{"short":"NS","long":"Not Started"}},"league":{"id":39,"name":"Premier League","season":2026,"round":"Regular Season - 1"},"teams":{"home":{"id":1,"name":"Arsenal"},"away":{"id":2,"name":"Chelsea"}},"goals":{"home":null,"away":null},"score":{"halftime":{"home":null,"away":null},"fulltime":{"home":null,"away":null},"extratime":{"home":null,"away":null},"penalty":{"home":null,"away":null}}},{"fixture":{"id":1002,"date":"2026-05-13T20:00:00+00:00","status":{"short":"FT","long":"Match Finished"}},"league":{"id":39,"name":"Premier League","season":2026,"round":"Regular Season - 1"},"teams":{"home":{"id":3,"name":"Liverpool"},"away":{"id":4,"name":"Everton"}},"goals":{"home":2,"away":1},"score":{"halftime":{"home":1,"away":0},"fulltime":{"home":2,"away":1},"extratime":{"home":null,"away":null},"penalty":{"home":null,"away":null}}}]}`)
	client := &fakeProviderClient{response: provider.Response{
		Provider:      "api-football",
		Endpoint:      "fixtures",
		StatusCode:    200,
		Body:          body,
		Latency:       30 * time.Millisecond,
		ResponseBytes: len(body),
	}}
	logs := &fakeRequestLogger{}
	entityMaps := &fakeEntityMapResolver{
		lookup: map[string]string{
			"api-football:match:1002": "55",
		},
	}
	catalog := &fakeCatalog{reconcileTo: 81}
	enqueuer := &fakeProviderJobEnqueuer{}

	job := NewFixturesJob(FixturesJobOptions{
		Client:      client,
		RequestLogs: logs,
		EntityMaps:  entityMaps,
		Catalog:     catalog,
		Jobs:        enqueuer,
		Now: func() time.Time {
			return time.Date(2026, 5, 14, 12, 7, 0, 0, time.UTC)
		},
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 9001}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(catalog.inserted) != 1 {
		t.Fatalf("inserted matches = %d, want 1", len(catalog.inserted))
	}
	if len(catalog.reconciled) != 1 {
		t.Fatalf("reconciled matches = %d, want 1", len(catalog.reconciled))
	}
	if catalog.inserted[0].Status != "upcoming" {
		t.Fatalf("inserted status = %q, want upcoming", catalog.inserted[0].Status)
	}
	if catalog.inserted[0].SeasonID != 2026 {
		t.Fatalf("inserted season = %d, want provider season 2026", catalog.inserted[0].SeasonID)
	}
	updated, ok := catalog.reconciled[55]
	if !ok {
		t.Fatal("missing update for canonical match id 55")
	}
	if updated.Status != "finished" {
		t.Fatalf("updated status = %q, want finished", updated.Status)
	}
	if updated.RawStatus != "FT" || updated.RawStatusLabel != "Match Finished" {
		t.Fatalf("updated raw status = %q/%q, want FT/Match Finished", updated.RawStatus, updated.RawStatusLabel)
	}
	if updated.HomeScore == nil || *updated.HomeScore != 2 {
		t.Fatalf("updated home score = %+v, want 2", updated.HomeScore)
	}
	// Teams and leagues are now mapped by the provider's own id as well, so
	// select by canonical type rather than by position. Recording those keeps a
	// club on one canonical row instead of minting a country-less duplicate for
	// every continental fixture.
	matchMaps := mapsOfType(entityMaps.maps, "match")
	if len(matchMaps) != 2 {
		t.Fatalf("match maps = %d, want 2", len(matchMaps))
	}
	if matchMaps[1].CanonicalID != "81" {
		t.Fatalf("second match map canonical id = %q, want converged id 81", matchMaps[1].CanonicalID)
	}
	if got := mapsOfType(entityMaps.maps, "team"); len(got) == 0 {
		t.Fatal("expected provider team ids to be mapped to canonical teams")
	}
	if got := mapsOfType(entityMaps.maps, "league"); len(got) == 0 {
		t.Fatal("expected provider league ids to be mapped to canonical leagues")
	}
	if len(enqueuer.jobs) != 1 {
		t.Fatalf("prediction jobs = %+v, want one post-normalization trigger", enqueuer.jobs)
	}
	if got := enqueuer.jobs[0]; got.JobKey != "predictions.generate.upcoming" || got.Queue != "predictions" || !got.DeduplicateCompleted {
		t.Fatalf("prediction job = %+v, want deduplicated upcoming generation", got)
	}
	if got := enqueuer.jobs[0].IdempotencyKey; got != "schedule:predictions:generate:20260514T120000Z" {
		t.Fatalf("prediction idempotency key = %q, want global refresh bucket", got)
	}
}

func TestPredictionGenerationTriggersCoalesceAcrossSourcesWithinRefreshWindow(t *testing.T) {
	enqueuer := &fakeProviderJobEnqueuer{}
	start := time.Date(2026, 8, 5, 10, 1, 0, 0, time.UTC)
	for _, now := range []time.Time{start, start.Add(13 * time.Minute), start.Add(15 * time.Minute)} {
		if err := enqueuePredictionGeneration(context.Background(), enqueuer, now); err != nil {
			t.Fatalf("enqueuePredictionGeneration returned error: %v", err)
		}
	}
	if len(enqueuer.jobs) != 3 {
		t.Fatalf("captured jobs = %d, want 3", len(enqueuer.jobs))
	}
	if enqueuer.jobs[0].IdempotencyKey != enqueuer.jobs[1].IdempotencyKey {
		t.Fatalf("same-window keys differ: %q and %q", enqueuer.jobs[0].IdempotencyKey, enqueuer.jobs[1].IdempotencyKey)
	}
	if enqueuer.jobs[1].IdempotencyKey == enqueuer.jobs[2].IdempotencyKey {
		t.Fatalf("next-window key did not advance: %q", enqueuer.jobs[2].IdempotencyKey)
	}
	for _, job := range enqueuer.jobs {
		if !job.DeduplicateCompleted {
			t.Fatalf("refresh job does not deduplicate completed bucket: %+v", job)
		}
	}
}

func TestFixturesJobFetchesTeamHistoryWhenTeamAndLastAreProvided(t *testing.T) {
	body := []byte(`{"errors":[],"response":[]}`)
	client := &fakeProviderClient{response: provider.Response{
		Provider:      "api-football",
		Endpoint:      "fixtures",
		StatusCode:    200,
		Body:          body,
		Latency:       30 * time.Millisecond,
		ResponseBytes: len(body),
	}}
	logs := &fakeRequestLogger{}
	entityMaps := &fakeEntityMapResolver{}
	catalog := &fakeCatalog{}

	job := NewFixturesJob(FixturesJobOptions{
		Client:      client,
		RequestLogs: logs,
		EntityMaps:  entityMaps,
		Catalog:     catalog,
	})
	if err := job.Run(context.Background(), jobs.Job{Payload: []byte(`{"endpoint":"fixtures","team_id":123,"last":15}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if client.endpoint != "fixtures" {
		t.Fatalf("endpoint = %q, want fixtures", client.endpoint)
	}
	if got := client.query.Get("team"); got != "123" {
		t.Fatalf("team query = %q, want 123", got)
	}
	if got := client.query.Get("last"); got != "15" {
		t.Fatalf("last query = %q, want 15", got)
	}
	if got := client.query.Get("date"); got != "" {
		t.Fatalf("date query = %q, want empty for team-history backfill", got)
	}
}

func TestFixturesBackfillIgnoresLegacyPagePayload(t *testing.T) {
	body := []byte(`{"errors":[],"response":[]}`)
	client := &fakeProviderClient{response: provider.Response{
		Provider: "api-football", Endpoint: "fixtures", StatusCode: 200,
		Body: body, ResponseBytes: len(body),
	}}
	job := NewFixturesJob(FixturesJobOptions{
		Client: client, RequestLogs: &fakeRequestLogger{},
		EntityMaps: &fakeEntityMapResolver{}, Catalog: &fakeCatalog{},
	})
	// Jobs queued before the scheduler fix can still carry page=1.
	payload := []byte(`{"league_id":135,"season":2026,"from":"2026-08-24","to":"2026-09-23","page":1}`)
	if err := job.Run(context.Background(), jobs.Job{Payload: payload}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if got := client.query.Get("league"); got != "135" {
		t.Fatalf("league query = %q, want 135", got)
	}
	if got := client.query.Get("season"); got != "2026" {
		t.Fatalf("season query = %q, want 2026", got)
	}
	if got := client.query.Get("from"); got != "2026-08-24" {
		t.Fatalf("from query = %q, want 2026-08-24", got)
	}
	if got := client.query.Get("to"); got != "2026-09-23" {
		t.Fatalf("to query = %q, want 2026-09-23", got)
	}
	if _, exists := client.query["page"]; exists {
		t.Fatalf("fixtures endpoint rejects page; query = %v", client.query)
	}
}

func TestCanonicalSlugsAreProviderNeutralAndNormalizeCommonClubNames(t *testing.T) {
	apiLeague := canonicalLeagueSlug("England", "Premier League")
	footballDataLeague := canonicalLeagueSlug(" England ", "Premier League")
	if apiLeague != footballDataLeague || apiLeague != "league-england-premier-league" {
		t.Fatalf("league slugs = %q/%q, want league-england-premier-league", apiLeague, footballDataLeague)
	}
	if canonicalLeagueSlug("Spain", "La Liga") != canonicalLeagueSlug("Spain", "Primera Division") {
		t.Fatalf("Spanish league aliases did not normalize to one canonical slug")
	}
	if canonicalLeagueSlug("World", "FIFA World Cup") != canonicalLeagueSlug("World", "World Cup") {
		t.Fatalf("World Cup aliases did not normalize to one canonical slug")
	}
	if canonicalLeagueSlug("Brazil", "Campeonato Brasileiro Série A") != canonicalLeagueSlug("Brazil", "Serie A") {
		t.Fatalf("Brazil Serie A aliases did not normalize to one canonical slug")
	}

	apiTeam := canonicalTeamSlug("England", "Arsenal")
	footballDataTeam := canonicalTeamSlug("England", "Arsenal FC")
	if apiTeam != footballDataTeam || apiTeam != "team-england-arsenal" {
		t.Fatalf("team slugs = %q/%q, want team-england-arsenal", apiTeam, footballDataTeam)
	}

	if got := canonicalTeamSlug("Germany", "1. FC Köln"); got != "team-germany-koln" {
		t.Fatalf("accented team slug = %q, want team-germany-koln", got)
	}
	if canonicalTeamSlug("Germany", "FC Bayern München") != canonicalTeamSlug("Germany", "Bayern Munich") {
		t.Fatalf("Bayern aliases did not normalize to one canonical slug")
	}
	if canonicalTeamSlug("Brazil", "Coritiba FBC") != canonicalTeamSlug("Brazil", "Coritiba") {
		t.Fatalf("Coritiba aliases did not normalize to one canonical slug")
	}
}

func TestFixturesJobLinksRequestLogToClaimedJob(t *testing.T) {
	client := &fakeProviderClient{response: provider.Response{
		Provider:   "api-football",
		Endpoint:   "fixtures",
		StatusCode: 200,
		Body:       []byte(`{"errors":[],"response":[]}`),
	}}
	logs := &fakeRequestLogger{}
	job := NewFixturesJob(FixturesJobOptions{
		Client:      client,
		RequestLogs: logs,
		EntityMaps:  &fakeEntityMapResolver{},
		Catalog:     &fakeCatalog{},
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 321}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(logs.logs) != 1 || logs.logs[0].JobID != 321 {
		t.Fatalf("logs = %+v, want job id 321", logs.logs)
	}
}

func TestNormalizeFixtureStatus(t *testing.T) {
	tests := []struct {
		short string
		long  string
		want  string
	}{
		{short: "NS", long: "Not Started", want: "upcoming"},
		{short: "TBD", long: "Time To Be Defined", want: "upcoming"},
		{short: "1H", long: "First Half", want: "live"},
		{short: "HT", long: "Halftime", want: "halftime"},
		{short: "2H", long: "Second Half", want: "second_half"},
		{short: "ET", long: "Extra Time", want: "extra_time"},
		{short: "BT", long: "Break Time", want: "break"},
		{short: "P", long: "Penalty In Progress", want: "penalties"},
		{short: "PEN", long: "Penalty Shootout", want: "finished"},
		{short: "FT", long: "Match Finished", want: "finished"},
		{short: "AET", long: "After Extra Time", want: "finished"},
		{short: "PST", long: "Match Postponed", want: "postponed"},
		{short: "CANC", long: "Match Cancelled", want: "canceled"},
		{short: "ABD", long: "Match Abandoned", want: "abandoned"},
		{short: "SUSP", long: "Match Suspended", want: "suspended"},
		{short: "INT", long: "Match Interrupted", want: "interrupted"},
		{short: "AWD", long: "Technical Loss", want: "awarded"},
		{short: "WO", long: "Walkover", want: "walkover"},
		{short: "X", long: "Delayed", want: "delayed"},
	}
	for _, tt := range tests {
		if got := normalizeFixtureStatus(tt.short, tt.long).Status; got != tt.want {
			t.Fatalf("normalizeFixtureStatus(%q, %q) = %q, want %q", tt.short, tt.long, got, tt.want)
		}
	}
}

func TestLeaguesAndFixturesJobKeys(t *testing.T) {
	if got := LeaguesJobKey(); got == "" {
		t.Fatal("LeaguesJobKey returned empty key")
	}
	if got := FixturesJobKey(); got == "" {
		t.Fatal("FixturesJobKey returned empty key")
	}
}

func TestHashParamsStableForSameInput(t *testing.T) {
	values := url.Values{}
	values.Set("next", "100")
	values.Set("league", strconv.Itoa(39))
	first := hashParams(values)
	second := hashParams(values)
	if first == "" || second == "" || first != second {
		t.Fatalf("hashParams instability: first=%q second=%q", first, second)
	}
}

func TestFixtureIdentityMismatch(t *testing.T) {
	kickoff := time.Date(2026, 9, 1, 19, 30, 0, 0, time.UTC)
	stored := MatchIdentity{
		LeagueID:   39,
		HomeTeamID: 101,
		AwayTeamID: 202,
		KickoffAt:  kickoff,
	}
	same := MatchRecord{
		LeagueID:   39,
		HomeTeamID: 101,
		AwayTeamID: 202,
		KickoffAt:  kickoff,
		Status:     "upcoming",
	}
	tolerance := fixtureIdentityTolerance

	tests := []struct {
		name      string
		stored    MatchIdentity
		incoming  MatchRecord
		tolerance time.Duration
		want      bool
	}{
		{name: "same identity", stored: stored, incoming: same, tolerance: tolerance, want: false},
		{name: "different league", stored: stored, incoming: MatchRecord{LeagueID: 40, HomeTeamID: 101, AwayTeamID: 202, KickoffAt: kickoff}, tolerance: tolerance, want: true},
		{name: "different home team", stored: stored, incoming: MatchRecord{LeagueID: 39, HomeTeamID: 111, AwayTeamID: 202, KickoffAt: kickoff}, tolerance: tolerance, want: true},
		{name: "different away team", stored: stored, incoming: MatchRecord{LeagueID: 39, HomeTeamID: 101, AwayTeamID: 222, KickoffAt: kickoff}, tolerance: tolerance, want: true},
		{name: "kickoff drift 30s within tolerance", stored: stored, incoming: MatchRecord{LeagueID: 39, HomeTeamID: 101, AwayTeamID: 202, KickoffAt: kickoff.Add(30 * time.Second)}, tolerance: tolerance, want: false},
		{name: "kickoff drift 5min beyond tolerance", stored: stored, incoming: MatchRecord{LeagueID: 39, HomeTeamID: 101, AwayTeamID: 202, KickoffAt: kickoff.Add(5 * time.Minute)}, tolerance: tolerance, want: true},
		{name: "kickoff drift negative 5min beyond tolerance", stored: stored, incoming: MatchRecord{LeagueID: 39, HomeTeamID: 101, AwayTeamID: 202, KickoffAt: kickoff.Add(-5 * time.Minute)}, tolerance: tolerance, want: true},
		{name: "kickoff exactly at tolerance boundary", stored: stored, incoming: MatchRecord{LeagueID: 39, HomeTeamID: 101, AwayTeamID: 202, KickoffAt: kickoff.Add(tolerance)}, tolerance: tolerance, want: false},
		{name: "zero tolerance any drift", stored: stored, incoming: MatchRecord{LeagueID: 39, HomeTeamID: 101, AwayTeamID: 202, KickoffAt: kickoff.Add(1 * time.Second)}, tolerance: 0, want: true},
		{name: "all fields differ", stored: stored, incoming: MatchRecord{LeagueID: 1, HomeTeamID: 2, AwayTeamID: 3, KickoffAt: kickoff.Add(2 * time.Hour)}, tolerance: tolerance, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fixtureIdentityMismatch(tt.stored, tt.incoming, tt.tolerance); got != tt.want {
				t.Fatalf("fixtureIdentityMismatch(%+v, %+v, %s) = %v, want %v", tt.stored, tt.incoming, tt.tolerance, got, tt.want)
			}
		})
	}
}

// fixtureIdentityCollisionRun drives one fixture through FixturesJob.Run where
// the provider fixture id maps to a canonical match whose stored identity
// differs from the incoming payload (provider fixture-id reuse for a different
// real match). It returns the catalog to assert reconcile still happened.
func fixtureIdentityCollisionRun(t *testing.T, registry *metrics.Registry) *fakeCatalog {
	t.Helper()
	body := []byte(`{"errors":[],"response":[{"fixture":{"id":2001,"date":"2026-05-13T18:00:00+00:00","status":{"short":"NS","long":"Not Started"}},"league":{"id":39,"name":"Premier League","season":2026,"round":"Regular Season - 1"},"teams":{"home":{"id":1,"name":"Arsenal"},"away":{"id":2,"name":"Chelsea"}},"goals":{"home":null,"away":null},"score":{"halftime":{"home":null,"away":null},"fulltime":{"home":null,"away":null},"extratime":{"home":null,"away":null},"penalty":{"home":null,"away":null}}}]}`)
	client := &fakeProviderClient{response: provider.Response{
		Provider:      "api-football",
		Endpoint:      "fixtures",
		StatusCode:    200,
		Body:          body,
		Latency:       30 * time.Millisecond,
		ResponseBytes: len(body),
	}}
	logs := &fakeRequestLogger{}
	entityMaps := &fakeEntityMapResolver{
		lookup: map[string]string{
			"api-football:match:2001": "77",
		},
	}
	catalog := &fakeCatalog{
		reconcileTo: 77,
		identities: map[int64]MatchIdentity{
			77: {
				LeagueID:   90,
				HomeTeamID: 901,
				AwayTeamID: 902,
				KickoffAt:  time.Date(2026, 5, 13, 18, 0, 0, 0, time.UTC),
			},
		},
	}

	job := NewFixturesJob(FixturesJobOptions{
		Client:      client,
		RequestLogs: logs,
		EntityMaps:  entityMaps,
		Catalog:     catalog,
		Metrics:     registry,
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 9002}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	return catalog
}

func TestFixturesJobSignalsProviderFixtureIdentityCollision(t *testing.T) {
	registry := metrics.New()
	catalog := fixtureIdentityCollisionRun(t, registry)
	if len(catalog.reconciled) != 1 {
		t.Fatalf("reconciled matches = %d, want 1 (collision must not change upsert semantics)", len(catalog.reconciled))
	}
	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	text, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	if !strings.Contains(string(text), "worker_fixture_identity_collisions_total 1") {
		t.Fatalf("metrics output missing collision counter:\n%s", text)
	}
}
func TestFixturesJobSignalsSuspectedDuplicate(t *testing.T) {
	body := []byte(`{"errors":[],"response":[{"fixture":{"id":4001,"date":"2026-05-13T18:00:00+00:00","status":{"short":"NS","long":"Not Started"}},"league":{"id":39,"name":"Premier League","season":2026,"round":"Regular Season - 1"},"teams":{"home":{"id":1,"name":"Arsenal"},"away":{"id":2,"name":"Chelsea"}},"goals":{"home":null,"away":null},"score":{"halftime":{"home":null,"away":null},"fulltime":{"home":null,"away":null},"extratime":{"home":null,"away":null},"penalty":{"home":null,"away":null}}}]}`)
	client := &fakeProviderClient{response: provider.Response{Provider: "api-football", Endpoint: "fixtures", StatusCode: 200, Body: body}}
	logs := &fakeRequestLogger{}
	entityMaps := &fakeEntityMapResolver{lookup: map[string]string{}}
	catalog := &fakeCatalog{
		suspectedDuplicateID:    88,
		suspectedDuplicateFound: true,
		exactDuplicateID:        88,
		exactDuplicateFound:     true,
		identities: map[int64]MatchIdentity{
			88: {LeagueID: 900, HomeTeamID: 901, AwayTeamID: 902, KickoffAt: time.Date(2026, 5, 13, 18, 0, 0, 0, time.UTC)},
		},
	}
	registry := metrics.New()

	job := NewFixturesJob(FixturesJobOptions{Client: client, RequestLogs: logs, EntityMaps: entityMaps, Catalog: catalog, Metrics: registry})
	if err := job.Run(context.Background(), jobs.Job{ID: 9004}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(catalog.inserted) != 0 {
		t.Fatalf("inserted matches = %d, want 0 for an exact named duplicate", len(catalog.inserted))
	}
	if len(catalog.reconciled) != 1 {
		t.Fatalf("reconciled matches = %d, want 1", len(catalog.reconciled))
	}
	if got := catalog.reconciled[88]; got.LeagueID != 900 || got.HomeTeamID != 901 || got.AwayTeamID != 902 {
		t.Fatalf("reconciled identity = (%d, %d, %d), want canonical (900, 901, 902)", got.LeagueID, got.HomeTeamID, got.AwayTeamID)
	}
	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	text, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	if !strings.Contains(string(text), "worker_fixture_suspected_duplicates_total 1") {
		t.Fatalf("metrics output missing suspected-duplicate counter: %s", text)
	}
}

func TestFixturesJobKeepsGenuineRescheduleSeparate(t *testing.T) {
	body := []byte(`{"errors":[],"response":[{"fixture":{"id":4002,"date":"2026-05-13T18:00:00+00:00","status":{"short":"NS","long":"Not Started"}},"league":{"id":39,"name":"Premier League","season":2026},"teams":{"home":{"id":1,"name":"Arsenal"},"away":{"id":2,"name":"Chelsea"}},"goals":{"home":null,"away":null},"score":{"halftime":{"home":null,"away":null},"fulltime":{"home":null,"away":null}}}]}`)
	catalog := &fakeCatalog{
		suspectedDuplicateID:    88,
		suspectedDuplicateFound: true,
		identities: map[int64]MatchIdentity{
			88: {LeagueID: 900, HomeTeamID: 901, AwayTeamID: 902, KickoffAt: time.Date(2026, 5, 13, 17, 30, 0, 0, time.UTC)},
		},
	}
	job := NewFixturesJob(FixturesJobOptions{
		Client:      &fakeProviderClient{response: provider.Response{Provider: "api-football", Endpoint: "fixtures", StatusCode: 200, Body: body}},
		RequestLogs: &fakeRequestLogger{},
		EntityMaps:  &fakeEntityMapResolver{lookup: map[string]string{}},
		Catalog:     catalog,
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 9005}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(catalog.inserted) != 1 || len(catalog.reconciled) != 0 {
		t.Fatalf("inserted=%d reconciled=%d, want a separate rescheduled fixture", len(catalog.inserted), len(catalog.reconciled))
	}
}

func TestFixturesJobRedirectsMappedCancelledDuplicateToActiveCanonical(t *testing.T) {
	body := []byte(`{"errors":[],"response":[{"fixture":{"id":5001,"date":"2026-05-13T18:00:00+00:00","status":{"short":"NS","long":"Not Started"}},"league":{"id":39,"name":"Premier League","season":2026},"teams":{"home":{"id":1,"name":"Arsenal"},"away":{"id":2,"name":"Chelsea"}},"goals":{"home":null,"away":null},"score":{"halftime":{"home":null,"away":null},"fulltime":{"home":null,"away":null}}}]}`)
	entityMaps := &fakeEntityMapResolver{lookup: map[string]string{"api-football:match:5001": "77"}}
	catalog := &fakeCatalog{
		suspectedDuplicateID:    88,
		suspectedDuplicateFound: true,
		exactDuplicateID:        88,
		exactDuplicateFound:     true,
		identities: map[int64]MatchIdentity{
			77: {LeagueID: 1, HomeTeamID: 2, AwayTeamID: 3, KickoffAt: time.Date(2026, 5, 13, 18, 0, 0, 0, time.UTC), Status: "cancelled"},
			88: {LeagueID: 900, HomeTeamID: 901, AwayTeamID: 902, KickoffAt: time.Date(2026, 5, 13, 18, 0, 0, 0, time.UTC), Status: "upcoming"},
		},
	}
	job := NewFixturesJob(FixturesJobOptions{
		Client:      &fakeProviderClient{response: provider.Response{Provider: "api-football", Endpoint: "fixtures", StatusCode: 200, Body: body}},
		RequestLogs: &fakeRequestLogger{},
		EntityMaps:  entityMaps,
		Catalog:     catalog,
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 9006}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if _, ok := catalog.reconciled[88]; !ok {
		t.Fatalf("reconciled ids = %+v, want active canonical id 88", catalog.reconciled)
	}
	matchMaps := mapsOfType(entityMaps.maps, "match")
	if len(matchMaps) != 1 || matchMaps[0].CanonicalID != "88" {
		t.Fatalf("match maps = %+v, want provider id redirected to canonical 88", matchMaps)
	}
}

func TestFixturesJobFailsClosedForTwoActiveMappedDuplicates(t *testing.T) {
	body := []byte(`{"errors":[],"response":[{"fixture":{"id":5002,"date":"2026-05-13T18:00:00+00:00","status":{"short":"NS","long":"Not Started"}},"league":{"id":39,"name":"Premier League","season":2026},"teams":{"home":{"id":1,"name":"Arsenal"},"away":{"id":2,"name":"Chelsea"}},"goals":{"home":null,"away":null},"score":{"halftime":{"home":null,"away":null},"fulltime":{"home":null,"away":null}}}]}`)
	entityMaps := &fakeEntityMapResolver{lookup: map[string]string{"api-football:match:5002": "77"}}
	catalog := &fakeCatalog{
		exactDuplicateID:    88,
		exactDuplicateFound: true,
		identities: map[int64]MatchIdentity{
			77: {LeagueID: 900, HomeTeamID: 901, AwayTeamID: 902, KickoffAt: time.Date(2026, 5, 13, 18, 0, 0, 0, time.UTC), Status: "upcoming"},
			88: {LeagueID: 900, HomeTeamID: 901, AwayTeamID: 902, KickoffAt: time.Date(2026, 5, 13, 18, 0, 0, 0, time.UTC), Status: "upcoming"},
		},
	}
	job := NewFixturesJob(FixturesJobOptions{
		Client:      &fakeProviderClient{response: provider.Response{Provider: "api-football", Endpoint: "fixtures", StatusCode: 200, Body: body}},
		RequestLogs: &fakeRequestLogger{},
		EntityMaps:  entityMaps,
		Catalog:     catalog,
	})
	// Still refuses to converge the pair: no reconcile, no repoint. That part
	// is right -- repointing without retiring the loser lets the mapping
	// oscillate between two public duplicates on successive polls.
	//
	// It no longer returns an error, because returning aborted the whole batch.
	if err := job.Run(context.Background(), jobs.Job{ID: 9007}); err != nil {
		t.Fatalf("an ambiguous pair must not fail the job: %v", err)
	}
	if len(catalog.reconciled) != 0 {
		t.Fatalf("reconciled ids = %+v, want none", catalog.reconciled)
	}
	if got := mapsOfType(entityMaps.maps, "match"); len(got) != 0 {
		t.Fatalf("match maps = %+v, want no repoint", got)
	}
}

// One ambiguous pair must not discard every other fixture in the request.
//
// This is the bug the fail-closed branch caused. Those jobs are how a finished
// match gets its result -- the reaper reconciles a stranded match by enqueuing
// one -- so aborting the batch meant matches that had ended never got a score.
// On 2026-09-22 it killed 69 of 78 fixtures jobs in three hours, every one with
// this error, and the in-play board advertised 89 matches as live while the
// prediction engine saw 5.
func TestActiveDuplicateDoesNotDiscardTheRestOfTheBatch(t *testing.T) {
	// Two fixtures in one response: 5002 is the ambiguous one, 5003 is fine.
	body := []byte(`{"errors":[],"response":[` +
		`{"fixture":{"id":5002,"date":"2026-05-13T18:00:00+00:00","status":{"short":"NS","long":"Not Started"}},"league":{"id":39,"name":"Premier League","season":2026},"teams":{"home":{"id":1,"name":"Arsenal"},"away":{"id":2,"name":"Chelsea"}},"goals":{"home":null,"away":null},"score":{"halftime":{"home":null,"away":null},"fulltime":{"home":null,"away":null}}},` +
		`{"fixture":{"id":5003,"date":"2026-05-13T20:00:00+00:00","status":{"short":"FT","long":"Match Finished"}},"league":{"id":39,"name":"Premier League","season":2026},"teams":{"home":{"id":3,"name":"Spurs"},"away":{"id":4,"name":"Fulham"}},"goals":{"home":2,"away":1},"score":{"halftime":{"home":1,"away":0},"fulltime":{"home":2,"away":1}}}` +
		`]}`)
	entityMaps := &fakeEntityMapResolver{lookup: map[string]string{"api-football:match:5002": "77"}}
	catalog := &fakeCatalog{
		exactDuplicateID:    88,
		exactDuplicateFound: true,
		identities: map[int64]MatchIdentity{
			77: {LeagueID: 900, HomeTeamID: 901, AwayTeamID: 902, KickoffAt: time.Date(2026, 5, 13, 18, 0, 0, 0, time.UTC), Status: "upcoming"},
			88: {LeagueID: 900, HomeTeamID: 901, AwayTeamID: 902, KickoffAt: time.Date(2026, 5, 13, 18, 0, 0, 0, time.UTC), Status: "upcoming"},
		},
	}
	job := NewFixturesJob(FixturesJobOptions{
		Client:      &fakeProviderClient{response: provider.Response{Provider: "api-football", Endpoint: "fixtures", StatusCode: 200, Body: body}},
		RequestLogs: &fakeRequestLogger{},
		EntityMaps:  entityMaps,
		Catalog:     catalog,
	})

	if err := job.Run(context.Background(), jobs.Job{ID: 9008}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	// The finished match behind the duplicate still lands. Without this, a
	// result sits unreachable behind an unrelated catalogue defect.
	if len(catalog.inserted) == 0 && len(catalog.reconciled) == 0 {
		t.Fatal("the second fixture was never ingested: one ambiguous pair " +
			"discarded the rest of the batch, which is how finished matches " +
			"stay on the in-play board")
	}
}

func TestAmbiguousExactIdentitySkipsOnlyAffectedFixture(t *testing.T) {
	for _, mapped := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "mapped"}[mapped], func(t *testing.T) {
			body := []byte(`{"errors":[],"response":[` +
				`{"fixture":{"id":5002,"date":"2026-05-13T18:00:00+00:00","status":{"short":"NS","long":"Not Started"}},"league":{"id":39,"name":"Premier League","season":2026},"teams":{"home":{"id":1,"name":"Arsenal"},"away":{"id":2,"name":"Chelsea"}},"goals":{"home":null,"away":null},"score":{"halftime":{"home":null,"away":null},"fulltime":{"home":null,"away":null}}},` +
				`{"fixture":{"id":5003,"date":"2026-05-13T20:00:00+00:00","status":{"short":"FT","long":"Match Finished"}},"league":{"id":39,"name":"Premier League","season":2026},"teams":{"home":{"id":3,"name":"Spurs"},"away":{"id":4,"name":"Fulham"}},"goals":{"home":2,"away":1},"score":{"halftime":{"home":1,"away":0},"fulltime":{"home":2,"away":1}}}` +
				`]}`)
			maps := &fakeEntityMapResolver{lookup: map[string]string{}}
			if mapped {
				maps.lookup["api-football:match:5002"] = "77"
			}
			catalog := &fakeCatalog{
				exactDuplicateErr:        ErrAmbiguousExactCanonicalFixture,
				exactDuplicateErrForHome: "Arsenal",
				identities: map[int64]MatchIdentity{
					77: {LeagueID: 39, HomeTeamID: 1, AwayTeamID: 2, KickoffAt: time.Date(2026, 5, 13, 18, 0, 0, 0, time.UTC), Status: "upcoming"},
				},
			}
			job := NewFixturesJob(FixturesJobOptions{
				Client:      &fakeProviderClient{response: provider.Response{Provider: "api-football", Endpoint: "fixtures", StatusCode: 200, Body: body}},
				RequestLogs: &fakeRequestLogger{}, EntityMaps: maps, Catalog: catalog,
			})
			if err := job.Run(context.Background(), jobs.Job{ID: 9009}); err != nil {
				t.Fatalf("one ambiguous fixture failed the batch: %v", err)
			}
			if len(catalog.inserted) != 1 || catalog.inserted[0].Status != "finished" {
				t.Fatalf("inserted = %+v, want only the unrelated finished fixture", catalog.inserted)
			}
			if len(catalog.reconciled) != 0 {
				t.Fatalf("ambiguous fixture was reconciled: %+v", catalog.reconciled)
			}
			matchMaps := mapsOfType(maps.maps, "match")
			if len(matchMaps) != 1 || matchMaps[0].ProviderEntityID != "5003" {
				t.Fatalf("match mappings = %+v, want only fixture 5003", matchMaps)
			}
		})
	}
}

func TestAmbiguousExactIdentityKeepsSingleAndBackfillJobsRetryable(t *testing.T) {
	first := `{"fixture":{"id":5002,"date":"2026-05-13T18:00:00+00:00","status":{"short":"NS","long":"Not Started"}},"league":{"id":39,"name":"Premier League","season":2026},"teams":{"home":{"id":1,"name":"Arsenal"},"away":{"id":2,"name":"Chelsea"}},"goals":{"home":null,"away":null},"score":{"halftime":{"home":null,"away":null},"fulltime":{"home":null,"away":null}}}`
	second := `{"fixture":{"id":5003,"date":"2026-05-13T20:00:00+00:00","status":{"short":"FT","long":"Match Finished"}},"league":{"id":39,"name":"Premier League","season":2026},"teams":{"home":{"id":3,"name":"Spurs"},"away":{"id":4,"name":"Fulham"}},"goals":{"home":2,"away":1},"score":{"halftime":{"home":1,"away":0},"fulltime":{"home":2,"away":1}}}`
	for _, tc := range []struct {
		name    string
		jobKey  string
		payload string
	}{
		{"single", FixturesJobKey(), `{"errors":[],"response":[` + first + `]}`},
		{"backfill", BackfillTeamHistoryJobKey(), `{"errors":[],"response":[` + first + `,` + second + `]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			maps := &fakeEntityMapResolver{lookup: map[string]string{}}
			catalog := &fakeCatalog{exactDuplicateErr: ErrAmbiguousExactCanonicalFixture, exactDuplicateErrForHome: "Arsenal"}
			job := NewFixturesJob(FixturesJobOptions{
				Client:      &fakeProviderClient{response: provider.Response{Provider: "api-football", Endpoint: "fixtures", StatusCode: 200, Body: []byte(tc.payload)}},
				RequestLogs: &fakeRequestLogger{}, EntityMaps: maps, Catalog: catalog,
			})
			if err := job.Run(context.Background(), jobs.Job{ID: 9010, JobKey: tc.jobKey}); !errors.Is(err, ErrAmbiguousExactCanonicalFixture) {
				t.Fatalf("Run error = %v, want retryable identity ambiguity", err)
			}
			if len(catalog.inserted) != 0 || len(mapsOfType(maps.maps, "match")) != 0 {
				t.Fatal("ambiguous backfill/single fixture changed match or provider mapping")
			}
		})
	}
}

func TestFixturesJobSkipsCollisionCounterWhenMetricsNil(t *testing.T) {
	catalog := fixtureIdentityCollisionRun(t, nil)
	if len(catalog.reconciled) != 1 {
		t.Fatalf("reconciled matches = %d, want 1 (nil metrics must not block reconcile)", len(catalog.reconciled))
	}
}

func TestFixturesJobIgnoresMissingStoredIdentityForDetection(t *testing.T) {
	body := []byte(`{"errors":[],"response":[{"fixture":{"id":3001,"date":"2026-05-13T18:00:00+00:00","status":{"short":"NS","long":"Not Started"}},"league":{"id":39,"name":"Premier League","season":2026,"round":"Regular Season - 1"},"teams":{"home":{"id":1,"name":"Arsenal"},"away":{"id":2,"name":"Chelsea"}},"goals":{"home":null,"away":null},"score":{"halftime":{"home":null,"away":null},"fulltime":{"home":null,"away":null},"extratime":{"home":null,"away":null},"penalty":{"home":null,"away":null}}}]}`)
	client := &fakeProviderClient{response: provider.Response{
		Provider:      "api-football",
		Endpoint:      "fixtures",
		StatusCode:    200,
		Body:          body,
		Latency:       30 * time.Millisecond,
		ResponseBytes: len(body),
	}}
	logs := &fakeRequestLogger{}
	entityMaps := &fakeEntityMapResolver{
		lookup: map[string]string{
			"api-football:match:3001": "55",
		},
	}
	// The mapped canonical row has no stored identity (nil identities map),
	// simulating a stale map entry or a missing row. Detection is best-effort:
	// the job must still reconcile and must not fail.
	catalog := &fakeCatalog{reconcileTo: 55}
	registry := metrics.New()

	job := NewFixturesJob(FixturesJobOptions{
		Client:      client,
		RequestLogs: logs,
		EntityMaps:  entityMaps,
		Catalog:     catalog,
		Metrics:     registry,
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 9003}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(catalog.reconciled) != 1 {
		t.Fatalf("reconciled matches = %d, want 1", len(catalog.reconciled))
	}
	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	text, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatalf("read metrics: %v", err)
	}
	if strings.Contains(string(text), "worker_fixture_identity_collisions_total 1") {
		t.Fatalf("metrics output contains collision counter for missing stored identity:\n%s", text)
	}
}

// mapsOfType selects the recorded entity maps for one canonical entity type.
func mapsOfType(entries []ProviderEntityMap, canonicalType string) []ProviderEntityMap {
	selected := make([]ProviderEntityMap, 0, len(entries))
	for _, entry := range entries {
		if entry.CanonicalType == canonicalType {
			selected = append(selected, entry)
		}
	}
	return selected
}

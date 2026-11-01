package ingestion

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/thesportsdb"
)

type scriptedTheSportsDBClient struct {
	responses map[string]provider.Response
	errors    map[string]error
	calls     []string
}

func (c *scriptedTheSportsDBClient) Get(_ context.Context, endpoint string, query url.Values) (provider.Response, error) {
	key := endpoint + "?" + query.Encode()
	c.calls = append(c.calls, key)
	if err, ok := c.errors[key]; ok {
		return provider.Response{Provider: thesportsdb.ProviderName, Endpoint: endpoint}, err
	}
	if resp, ok := c.responses[key]; ok {
		return resp, nil
	}
	if resp, ok := c.responses["default"]; ok {
		return resp, nil
	}
	return provider.Response{Provider: thesportsdb.ProviderName, Endpoint: endpoint, StatusCode: 200, Body: []byte(`{"teams":null}`)}, nil
}

type fakeTheSportsDBSelector struct {
	teams        []TheSportsDBTeamSelection
	leagues      []TheSportsDBLeagueSelection
	fetchedToday bool
	err          error
	teamLimits   []int
	leagueLimits []int
}

func (f *fakeTheSportsDBSelector) SelectTeamsWithoutLogos(_ context.Context, limit int) ([]TheSportsDBTeamSelection, error) {
	f.teamLimits = append(f.teamLimits, limit)
	return f.teams, f.err
}

func (f *fakeTheSportsDBSelector) SelectLeaguesWithoutLogos(_ context.Context, limit int) ([]TheSportsDBLeagueSelection, error) {
	f.leagueLimits = append(f.leagueLimits, limit)
	return f.leagues, f.err
}

func (f *fakeTheSportsDBSelector) AllLeaguesFetchedToday(_ context.Context, _ time.Time) (bool, error) {
	return f.fetchedToday, f.err
}

type recordingReferenceDB struct {
	execQueries []string
	execArgs    [][]any
	err         error
}

func (r *recordingReferenceDB) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	r.execQueries = append(r.execQueries, query)
	r.execArgs = append(r.execArgs, args)
	if r.err != nil {
		return nil, r.err
	}
	return noopResult{}, nil
}

func (*recordingReferenceDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, sql.ErrNoRows
}

func TestTheSportsDBReferenceSyncJobBackfillsTeamAndLeagueLogos(t *testing.T) {
	client := &scriptedTheSportsDBClient{responses: map[string]provider.Response{
		"searchteams.php?t=Arsenal+FC": {
			Provider: thesportsdb.ProviderName, Endpoint: "searchteams.php", StatusCode: 200,
			Body:          []byte(`{"teams":[{"idTeam":"133604","strTeam":"Arsenal FC","strCountry":"England","strTeamBadge":"https://thesportsdb.com/arsenal-badge.png","strTeamLogo":"https://thesportsdb.com/arsenal-logo.png"}]}`),
			ResponseBytes: 210,
		},
		"searchteams.php?t=Liverpool+FC": {
			Provider: thesportsdb.ProviderName, Endpoint: "searchteams.php", StatusCode: 200,
			Body:          []byte(`{"teams":[{"idTeam":"133607","strTeam":"Liverpool FC","strCountry":"England","strTeamBadge":"https://thesportsdb.com/liverpool-badge.png"}]}`),
			ResponseBytes: 180,
		},
		"searchteams.php?t=Mystery+XI": {
			Provider: thesportsdb.ProviderName, Endpoint: "searchteams.php", StatusCode: 200,
			Body:          []byte(`{"teams":null}`),
			ResponseBytes: 13,
		},
		"all_leagues.php?": {
			Provider: thesportsdb.ProviderName, Endpoint: "all_leagues.php", StatusCode: 200,
			Body:          []byte(`{"leagues":[{"idLeague":"4328","strLeague":"English Premier League","strSport":"Soccer","strBadge":"https://thesportsdb.com/epl-badge.png"},{"idLeague":"4332","strLeague":"Cricket Premier League","strSport":"Cricket","strBadge":"https://thesportsdb.com/cpl-badge.png"}]}`),
			ResponseBytes: 320,
		},
	}}
	logs := &fakeRequestLogger{}
	referenceDB := &recordingReferenceDB{}
	selector := &fakeTheSportsDBSelector{
		teams: []TheSportsDBTeamSelection{
			{ID: 11, Name: "Arsenal FC", Country: "England"},
			{ID: 12, Name: "Liverpool FC", Country: "England"},
			{ID: 13, Name: "Mystery XI", Country: "England"},
		},
		leagues: []TheSportsDBLeagueSelection{
			{ID: 7, Name: "English Premier League"},
		},
	}
	job := NewTheSportsDBReferenceSyncJob(TheSportsDBReferenceSyncJobOptions{
		Client:      client,
		RequestLogs: logs,
		Reference:   NewReferenceSyncRepository(referenceDB),
		Selector:    selector,
		MaxPerCycle: 20,
	})

	if err := job.Run(context.Background(), jobs.Job{ID: 909}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if len(client.calls) != 4 {
		t.Fatalf("provider calls = %v, want 3 team searches + 1 all_leagues", client.calls)
	}
	if len(logs.logs) != 4 {
		t.Fatalf("request logs = %d, want 4", len(logs.logs))
	}
	for _, log := range logs.logs {
		if log.Reason != "reference" || log.Priority != 20 {
			t.Fatalf("request log = %+v, want reason reference priority 20", log)
		}
		if log.Provider != thesportsdb.ProviderName {
			t.Fatalf("request log provider = %q, want thesportsdb", log.Provider)
		}
	}
	if len(referenceDB.execQueries) != 3 {
		t.Fatalf("reference writes = %d, want 2 team attribute + 1 league logo", len(referenceDB.execQueries))
	}
	assertReferenceArgs(t, referenceDB, 0, []any{int64(11), "", "https://thesportsdb.com/arsenal-logo.png"})
	assertReferenceArgs(t, referenceDB, 1, []any{int64(12), "", "https://thesportsdb.com/liverpool-badge.png"})
	assertReferenceArgs(t, referenceDB, 2, []any{int64(7), "https://thesportsdb.com/epl-badge.png"})
}

func TestTheSportsDBReferenceSyncJobGatesAllLeaguesFetchToOncePerDay(t *testing.T) {
	client := &scriptedTheSportsDBClient{responses: map[string]provider.Response{
		"searchteams.php?t=Arsenal+FC": {
			Provider: thesportsdb.ProviderName, Endpoint: "searchteams.php", StatusCode: 200,
			Body:          []byte(`{"teams":[{"idTeam":"133604","strTeam":"Arsenal FC","strTeamLogo":"https://thesportsdb.com/arsenal-logo.png"}]}`),
			ResponseBytes: 150,
		},
	}}
	logs := &fakeRequestLogger{}
	referenceDB := &recordingReferenceDB{}
	selector := &fakeTheSportsDBSelector{
		teams:        []TheSportsDBTeamSelection{{ID: 11, Name: "Arsenal FC", Country: "England"}},
		fetchedToday: true,
	}
	job := NewTheSportsDBReferenceSyncJob(TheSportsDBReferenceSyncJobOptions{
		Client:      client,
		RequestLogs: logs,
		Reference:   NewReferenceSyncRepository(referenceDB),
		Selector:    selector,
	})

	if err := job.Run(context.Background(), jobs.Job{ID: 910}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	for _, call := range client.calls {
		if strings.HasPrefix(call, "all_leagues.php") {
			t.Fatalf("all_leagues.php fetched again on the same day: %v", client.calls)
		}
	}
	if len(client.calls) != 1 || len(logs.logs) != 1 {
		t.Fatalf("calls=%v logs=%d, want only the team search", client.calls, len(logs.logs))
	}
}

func TestTheSportsDBReferenceSyncJobContinuesAfterTeamLookupFailure(t *testing.T) {
	client := &scriptedTheSportsDBClient{
		responses: map[string]provider.Response{
			"searchteams.php?t=Arsenal+FC": {
				Provider: thesportsdb.ProviderName, Endpoint: "searchteams.php", StatusCode: 200,
				Body:          []byte(`{"teams":[{"idTeam":"133604","strTeam":"Arsenal FC","strTeamLogo":"https://thesportsdb.com/arsenal-logo.png"}]}`),
				ResponseBytes: 150,
			},
			"all_leagues.php?": {
				Provider: thesportsdb.ProviderName, Endpoint: "all_leagues.php", StatusCode: 200,
				Body:          []byte(`{"leagues":[]}`),
				ResponseBytes: 14,
			},
		},
		errors: map[string]error{
			"searchteams.php?t=Broken+FC": errors.New("boom"),
		},
	}
	logs := &fakeRequestLogger{}
	referenceDB := &recordingReferenceDB{}
	selector := &fakeTheSportsDBSelector{
		teams: []TheSportsDBTeamSelection{
			{ID: 11, Name: "Arsenal FC", Country: "England"},
			{ID: 14, Name: "Broken FC", Country: "England"},
		},
	}
	job := NewTheSportsDBReferenceSyncJob(TheSportsDBReferenceSyncJobOptions{
		Client:      client,
		RequestLogs: logs,
		Reference:   NewReferenceSyncRepository(referenceDB),
		Selector:    selector,
	})

	if err := job.Run(context.Background(), jobs.Job{ID: 911}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(referenceDB.execQueries) != 1 {
		t.Fatalf("reference writes = %d, want only Arsenal logo (Broken FC skipped)", len(referenceDB.execQueries))
	}
	if len(logs.logs) != 3 {
		t.Fatalf("request logs = %d, want 3 (1 failed search + 1 ok search + 1 all_leagues)", len(logs.logs))
	}
	foundErrorLog := false
	for _, log := range logs.logs {
		if log.Endpoint == "searchteams.php" && log.ErrorCategory == "provider_transport" {
			foundErrorLog = true
		}
	}
	if !foundErrorLog {
		t.Fatalf("no error-categorized searchteams log: %+v", logs.logs)
	}
}

func TestTheSportsDBReferenceSyncJobPassesNormalizedCapToSelector(t *testing.T) {
	client := &scriptedTheSportsDBClient{}
	logs := &fakeRequestLogger{}
	selector := &fakeTheSportsDBSelector{}
	job := NewTheSportsDBReferenceSyncJob(TheSportsDBReferenceSyncJobOptions{
		Client:      client,
		RequestLogs: logs,
		Reference:   NewReferenceSyncRepository(&recordingReferenceDB{}),
		Selector:    selector,
	})
	if err := job.Run(context.Background(), jobs.Job{ID: 912}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(selector.teamLimits) != 1 || selector.teamLimits[0] != 20 {
		t.Fatalf("team selection limits = %v, want default 20", selector.teamLimits)
	}
	if len(selector.leagueLimits) != 1 || selector.leagueLimits[0] != 20 {
		t.Fatalf("league selection limits = %v, want default 20", selector.leagueLimits)
	}
}

func TestNormalizeTheSportsDBMaxPerCycleBounds(t *testing.T) {
	if got := normalizeTheSportsDBMaxPerCycle(0); got != 20 {
		t.Fatalf("normalize(0) = %d, want default 20", got)
	}
	if got := normalizeTheSportsDBMaxPerCycle(250); got != 100 {
		t.Fatalf("normalize(250) = %d, want cap 100", got)
	}
	if got := normalizeTheSportsDBMaxPerCycle(7); got != 7 {
		t.Fatalf("normalize(7) = %d, want 7", got)
	}
}

func TestTheSportsDBReferenceSyncJobRequiresDependencies(t *testing.T) {
	client := &scriptedTheSportsDBClient{}
	logs := &fakeRequestLogger{}
	selector := &fakeTheSportsDBSelector{}
	reference := NewReferenceSyncRepository(&recordingReferenceDB{})
	tests := []TheSportsDBReferenceSyncJobOptions{
		{},
		{Client: client},
		{Client: client, RequestLogs: logs},
		{Client: client, RequestLogs: logs, Selector: selector},
		{Client: client, RequestLogs: logs, Selector: selector, Reference: ReferenceSyncRepository{}},
		{Client: client, RequestLogs: logs, Reference: reference},
	}
	for index, opts := range tests {
		job := NewTheSportsDBReferenceSyncJob(opts)
		if err := job.Run(context.Background(), jobs.Job{}); err == nil {
			t.Fatalf("case %d: Run returned nil error", index)
		}
	}
}

func assertReferenceArgs(t *testing.T, db *recordingReferenceDB, index int, want []any) {
	t.Helper()
	if index >= len(db.execArgs) {
		t.Fatalf("reference write %d missing (have %d)", index, len(db.execArgs))
	}
	got := db.execArgs[index]
	if len(got) != len(want) {
		t.Fatalf("reference write %d args = %v, want %v", index, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reference write %d arg %d = %v (%T), want %v (%T)", index, i, got[i], got[i], want[i], want[i])
		}
	}
}

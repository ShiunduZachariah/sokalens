package ingestion

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
)

func TestTeamsReferencePreservesProviderIdentity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mapping   string
		lookupErr error
		wantID    int64
		wantNew   bool
		wantErr   bool
	}{
		{name: "country enrichment retains World team", mapping: "6684752", wantID: 6684752},
		{name: "unmapped provider team is created", wantID: 1, wantNew: true},
		{name: "invalid mapping is not replaced", mapping: "broken", wantErr: true},
		{name: "zero mapping is not replaced", mapping: "0", wantErr: true},
		{name: "negative mapping is not replaced", mapping: "-1", wantErr: true},
		{name: "lookup failure does not create identity", lookupErr: errors.New("unavailable"), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			maps := &fakeEntityMapResolver{lookup: map[string]string{}, lookupErr: tc.lookupErr}
			if tc.mapping != "" {
				maps.lookup["api-football:team:21481"] = tc.mapping
			}
			catalog := &fakeCatalog{}
			db := &referenceSyncFakeDB{}
			job := NewTeamsReferenceJob(TeamsReferenceJobOptions{
				Client:      &fakeProviderClient{response: provider.Response{StatusCode: 200, Body: []byte(`{"response":[{"team":{"id":21481,"name":"Wales U18","country":"Wales","logo":"https://example.com/wales.png"}}]}`)}},
				RequestLogs: &fakeRequestLogger{}, EntityMaps: maps, Catalog: catalog,
				Reference: NewReferenceSyncRepository(db),
			})
			err := job.Run(context.Background(), jobs.Job{Payload: []byte(`{"league_id":10,"season":2026}`)})
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected identity resolution error")
				}
				if len(catalog.teamsBySlug) != 0 || len(maps.maps) != 0 || db.execQuery != "" {
					t.Fatal("failed lookup modified team identity or attributes")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(maps.maps) != 1 || maps.maps[0].CanonicalID != strconv.FormatInt(tc.wantID, 10) {
				t.Fatalf("provider mapping changed: %#v", maps.maps)
			}
			if len(db.execArgs) != 3 || db.execArgs[0] != tc.wantID {
				t.Fatalf("attributes target = %v, want team %d", db.execArgs, tc.wantID)
			}
			if (len(catalog.teamsBySlug) > 0) != tc.wantNew {
				t.Fatalf("created teams = %v, want new = %v", catalog.teamsBySlug, tc.wantNew)
			}
		})
	}
}

type referenceSyncFakeDB struct {
	execQuery string
	execArgs  []any
	execErr   error
}

func (f *referenceSyncFakeDB) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	f.execQuery = query
	f.execArgs = args
	if f.execErr != nil {
		return nil, f.execErr
	}
	return noopResult{}, nil
}

func (*referenceSyncFakeDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, sql.ErrNoRows
}

func TestApplyCountryFlagWritesOnlyNonEmptyURLs(t *testing.T) {
	fake := &referenceSyncFakeDB{}
	repo := NewReferenceSyncRepository(fake)

	if err := repo.ApplyCountryFlag(context.Background(), "ke", "https://example.com/ke.png"); err != nil {
		t.Fatalf("ApplyCountryFlag returned error: %v", err)
	}
	if !strings.Contains(fake.execQuery, "UPDATE countries SET flag_url = $2") {
		t.Fatalf("query = %s, want countries flag update", fake.execQuery)
	}
	if fake.execArgs[0] != "KE" {
		t.Fatalf("code arg = %v, want normalized KE", fake.execArgs[0])
	}

	// Empty URL must not touch the DB.
	fake.execQuery = ""
	if err := repo.ApplyCountryFlag(context.Background(), "ke", "  "); err != nil {
		t.Fatalf("ApplyCountryFlag(empty) returned error: %v", err)
	}
	if fake.execQuery != "" {
		t.Fatalf("empty flag wrote a query: %s", fake.execQuery)
	}
}

func TestApplyTeamAttributesPreservesExistingValues(t *testing.T) {
	fake := &referenceSyncFakeDB{}
	repo := NewReferenceSyncRepository(fake)

	if err := repo.ApplyTeamAttributes(context.Background(), 42, "gb", "https://example.com/ars.png"); err != nil {
		t.Fatalf("ApplyTeamAttributes returned error: %v", err)
	}
	if !strings.Contains(fake.execQuery, "UPDATE teams") ||
		!strings.Contains(fake.execQuery, "COALESCE(NULLIF($2, ''), country_code)") ||
		!strings.Contains(fake.execQuery, "logo_url = COALESCE(NULLIF($3, ''), logo_url)") {
		t.Fatalf("query = %s, want preserve-on-empty team update", fake.execQuery)
	}
	if fake.execArgs[0] != int64(42) || fake.execArgs[1] != "GB" {
		t.Fatalf("args = %v, want (42, GB, logo)", fake.execArgs)
	}

	// Invalid country code is normalized away and must not blank the column.
	fake.execQuery = ""
	if err := repo.ApplyTeamAttributes(context.Background(), 42, "not-a-code", ""); err != nil {
		t.Fatalf("ApplyTeamAttributes(empty) returned error: %v", err)
	}
	if fake.execQuery != "" {
		t.Fatalf("empty attrs wrote a query: %s", fake.execQuery)
	}
}

func TestApplyLeagueLogoWritesOnlyNonEmptyURLs(t *testing.T) {
	fake := &referenceSyncFakeDB{}
	repo := NewReferenceSyncRepository(fake)

	if err := repo.ApplyLeagueLogo(context.Background(), 7, "https://example.com/epl.png"); err != nil {
		t.Fatalf("ApplyLeagueLogo returned error: %v", err)
	}
	if !strings.Contains(fake.execQuery, "UPDATE leagues SET logo_url = $2 WHERE id = $1") {
		t.Fatalf("query = %s, want league logo update", fake.execQuery)
	}

	fake.execQuery = ""
	if err := repo.ApplyLeagueLogo(context.Background(), 7, ""); err != nil {
		t.Fatalf("ApplyLeagueLogo(empty) returned error: %v", err)
	}
	if fake.execQuery != "" {
		t.Fatalf("empty logo wrote a query: %s", fake.execQuery)
	}
}

func TestReferenceSyncRepositorySurfacesDBErrors(t *testing.T) {
	fake := &referenceSyncFakeDB{execErr: sql.ErrConnDone}
	repo := NewReferenceSyncRepository(fake)
	if err := repo.ApplyCountryFlag(context.Background(), "ke", "https://x/y.png"); err == nil {
		t.Fatal("ApplyCountryFlag error = nil, want DB failure surfaced")
	}
}

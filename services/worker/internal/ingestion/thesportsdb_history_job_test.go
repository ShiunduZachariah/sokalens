package ingestion

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/thesportsdb"
	_ "modernc.org/sqlite"
)

func TestBestTheSportsDBTeamExactMatch(t *testing.T) {
	items := []thesportsdb.TeamItem{
		{ID: 101, Name: "Arsenal FC"},
		{ID: 202, Name: "arsenal"},
	}

	got, ok := bestTheSportsDBTeam("Arsenal", items)
	if !ok {
		t.Fatalf("expected an exact match to be found")
	}
	if got.ID != 202 {
		t.Fatalf("expected the case/whitespace-insensitive exact match (ID 202), got %+v", got)
	}
}

// TestBestTheSportsDBTeamNoMatchDoesNotFallBack is a regression test for a
// real production data-integrity defect: bestTheSportsDBTeam used to fall
// back to items[0] when no candidate's name matched the canonical team name,
// silently attributing history to the wrong team. It must now report "no
// match" instead of guessing.
func TestBestTheSportsDBTeamNoMatchDoesNotFallBack(t *testing.T) {
	items := []thesportsdb.TeamItem{
		{ID: 999, Name: "Totally Unrelated FC"},
		{ID: 998, Name: "Another Unrelated Club"},
	}

	got, ok := bestTheSportsDBTeam("Arsenal", items)
	if ok {
		t.Fatalf("expected no match, but got a false match: %+v", got)
	}
	if got.ID != 0 {
		t.Fatalf("expected the zero value on no match, got %+v", got)
	}
}

func TestBestTheSportsDBTeamNoCandidates(t *testing.T) {
	got, ok := bestTheSportsDBTeam("Arsenal", nil)
	if ok {
		t.Fatalf("expected no match with zero candidates")
	}
	if got.ID != 0 {
		t.Fatalf("expected the zero value with zero candidates, got %+v", got)
	}
}

func TestBestTheSportsDBTeamMatchesSafeNormalizedClubDesignators(t *testing.T) {
	items := []thesportsdb.TeamItem{
		{ID: 101, Name: "Zamalek"},
		{ID: 202, Name: "Unrelated Club"},
	}

	got, ok := bestTheSportsDBTeam("Zamalek SC", items)
	if !ok || got.ID != 101 {
		t.Fatalf("expected normalized club-designator match (ID 101), got %+v, ok=%v", got, ok)
	}
}

func TestBestTheSportsDBTeamRejectsAmbiguousNormalizedMatches(t *testing.T) {
	items := []thesportsdb.TeamItem{
		{ID: 101, Name: "Example FC"},
		{ID: 202, Name: "Example SC"},
	}

	got, ok := bestTheSportsDBTeam("Example Club", items)
	if ok || got.ID != 0 {
		t.Fatalf("expected ambiguous normalized candidates to stay unresolved, got %+v, ok=%v", got, ok)
	}
}

type teamHistoryTestDB struct {
	execArgs [][]any
}

func (db *teamHistoryTestDB) ExecContext(_ context.Context, _ string, args ...any) (sql.Result, error) {
	db.execArgs = append(db.execArgs, args)
	return noopResult{}, nil
}

func (*teamHistoryTestDB) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, sql.ErrNoRows
}

func (*teamHistoryTestDB) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}

func TestTheSportsDBTeamHistoryUnresolvedMappingIsTerminal(t *testing.T) {
	client := &scriptedTheSportsDBClient{responses: map[string]provider.Response{
		"searchteams.php?t=RC+Alcobendas": {
			Provider:   thesportsdb.ProviderName,
			Endpoint:   "searchteams.php",
			StatusCode: 200,
			Body:       []byte(`{"teams":[{"idTeam":"999","strTeam":"Alcobendas Sport"}]}`),
		},
	}}
	logs := &fakeRequestLogger{}
	db := &teamHistoryTestDB{}
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	handler := NewTheSportsDBTeamHistoryJob(TheSportsDBTeamHistoryJobOptions{
		Client:      client,
		RequestLogs: logs,
		Catalog:     NewCatalogRepository(db),
		EntityMaps:  NewProviderEntityMapRepository(db),
		Now:         func() time.Time { return now },
	})

	err := handler.Run(context.Background(), jobs.Job{
		ID:      42,
		Payload: []byte(`{"team_id":42,"team_name":"RC Alcobendas","competition_id":7,"matches_needed":5}`),
	})
	if err != nil {
		t.Fatalf("unresolved provider mapping should complete without retrying: %v", err)
	}
	if len(db.execArgs) != 1 {
		t.Fatalf("expected one unresolved entity-map write, got %d", len(db.execArgs))
	}
	if got := db.execArgs[0][2]; got != "unresolved:42" {
		t.Fatalf("provider entity ID = %v, want unresolved:42", got)
	}
	if got := db.execArgs[0][7]; got != "unresolved" {
		t.Fatalf("match status = %v, want unresolved", got)
	}
}

type reviewedHistoryDB struct {
	*sql.DB
	mappedIDs []string
}

func (db *reviewedHistoryDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "INSERT INTO provider_entity_maps") {
		db.mappedIDs = append(db.mappedIDs, args[5].(string))
		return noopResult{}, nil
	}
	return db.DB.ExecContext(ctx, query, args...)
}

func TestTheSportsDBHistoryKeepsReviewedAliasAndFinishedScore(t *testing.T) {
	for _, tc := range []struct {
		name        string
		awayScore   int
		teamAliased bool
		wantError   bool
	}{
		{name: "matching score maps to canonical team and fixture", awayScore: 1, teamAliased: true},
		{name: "fixture alias alone stays canonical", awayScore: 1},
		{name: "conflicting score fails closed", awayScore: 2, teamAliased: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sqlDB, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer sqlDB.Close()
			sqlDB.SetMaxOpenConns(1)
			for _, stmt := range []string{
				"CREATE TABLE teams (id INTEGER PRIMARY KEY, country_code TEXT, slug TEXT UNIQUE, name TEXT NOT NULL)",
				"CREATE TABLE team_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL, revoked_at TEXT)",
				"CREATE TABLE fixture_identity_aliases (old_id INTEGER PRIMARY KEY, canonical_id INTEGER NOT NULL, revoked_at TEXT)",
				"CREATE TABLE matches (id INTEGER PRIMARY KEY, league_id INTEGER, home_team_id INTEGER, away_team_id INTEGER, kickoff_at TIMESTAMP, status TEXT, home_score INTEGER, away_score INTEGER)",
				"INSERT INTO teams (id,slug,name) VALUES (3400946,'team-sunrise','Sunrise'),(6743190,'team-marines','Marines'),(3861947,'team-rwanda-marines','Marines')",
				"INSERT INTO fixture_identity_aliases (old_id,canonical_id) VALUES (7816414,1803076)",
			} {
				if _, err := sqlDB.Exec(stmt); err != nil {
					t.Fatal(err)
				}
			}
			if tc.teamAliased {
				if _, err := sqlDB.Exec("INSERT INTO team_identity_aliases (old_id,canonical_id) VALUES (6743190,3861947)"); err != nil {
					t.Fatal(err)
				}
			}
			kickoff := time.Date(2024, 5, 11, 13, 0, 0, 0, time.UTC)
			for _, row := range []struct{ id, awayID int64 }{{7816414, 6743190}, {1803076, 3861947}} {
				if _, err := sqlDB.Exec("INSERT INTO matches (id,league_id,home_team_id,away_team_id,kickoff_at,status,home_score,away_score) VALUES (?,?,?,?,?,?,?,?)",
					row.id, 1213, 3400946, row.awayID, kickoff, "finished", 3, 1); err != nil {
					t.Fatal(err)
				}
			}
			db := &reviewedHistoryDB{DB: sqlDB}
			job := NewTheSportsDBTeamHistoryJob(TheSportsDBTeamHistoryJobOptions{
				Catalog: NewCatalogRepository(db), EntityMaps: NewProviderEntityMapRepository(db),
			})
			err = job.persistEvent(context.Background(), theSportsDBTeamHistoryPayload{TeamID: 3400946, CompetitionID: 1213},
				thesportsdb.TeamItem{ID: 42, Name: "Sunrise"},
				thesportsdb.EventItem{ID: 2020102, Home: "Sunrise", Away: "Marines", HomeScore: 3, AwayScore: tc.awayScore, Date: "2024-05-11", Time: "13:00:00"}, time.Now())
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "score differs") {
					t.Fatalf("expected a score-parity error, got %v", err)
				}
				if len(db.mappedIDs) != 0 {
					t.Fatalf("conflicting score repointed provider map: %v", db.mappedIDs)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if len(db.mappedIDs) != 1 || db.mappedIDs[0] != "1803076" {
					t.Fatalf("provider map targets = %v, want canonical 1803076", db.mappedIDs)
				}
			}
			var count int
			if err := sqlDB.QueryRow("SELECT count(*) FROM matches").Scan(&count); err != nil || count != 2 {
				t.Fatalf("historical match count = %d, err = %v", count, err)
			}
		})
	}
}

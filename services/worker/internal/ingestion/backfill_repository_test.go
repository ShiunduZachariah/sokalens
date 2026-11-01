package ingestion

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/footballdata"
	_ "modernc.org/sqlite"
)

func TestFootballDataBackfillUsesTeamsActualPriorCompetition(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE fixtures (id INTEGER PRIMARY KEY, league_id INTEGER NOT NULL, season_id INTEGER NOT NULL, home_team_id INTEGER NOT NULL, away_team_id INTEGER NOT NULL, kickoff_at TIMESTAMP NOT NULL, status TEXT NOT NULL)`,
		`CREATE TABLE provider_entity_maps (provider TEXT NOT NULL, entity_type TEXT NOT NULL, canonical_entity_type TEXT NOT NULL, canonical_entity_id TEXT NOT NULL, provider_entity_id TEXT NOT NULL, last_seen_at TIMESTAMP NOT NULL, retired_at TIMESTAMP)`,
		`INSERT INTO provider_entity_maps VALUES ('football-data-org', 'league', 'league', '40', 'ELC', CURRENT_TIMESTAMP, NULL)`,
		`INSERT INTO provider_entity_maps VALUES ('football-data-org', 'league', 'league', '39', 'PL', CURRENT_TIMESTAMP, NULL)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("schema/data: %v", err)
		}
	}
	targetKickoff := time.Date(2026, 8, 30, 15, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO fixtures VALUES (10, 40, 2025, 7, 8, ?, 'finished')`, targetKickoff.AddDate(0, -3, 0)); err != nil {
		t.Fatalf("insert Championship history: %v", err)
	}
	repo := NewSQLBackfillRepository(db)
	payloads, err := repo.BuildProviderPayloads(context.Background(), footballdata.ProviderName, DataBackfillRequest{
		TeamID: 7, CompetitionID: 39, FeatureGroup: "results", Season: 2026, TargetKickoffAt: targetKickoff,
	})
	if err != nil {
		t.Fatalf("BuildProviderPayloads: %v", err)
	}
	if len(payloads) != 1 {
		t.Fatalf("payload count=%d, want 1", len(payloads))
	}
	got := string(payloads[0])
	if !strings.Contains(got, `"competition":"ELC"`) || !strings.Contains(got, `"season":"2025"`) {
		t.Fatalf("payload=%s, want actual Championship competition and season", got)
	}
}

func TestStatisticsBackfillCandidatesRemainCrossCompetitionAndBounded(t *testing.T) {
	for _, fragment := range []string{
		"history.season_id IN ($2, $2 - 1)",
		"ORDER BY history.kickoff_at DESC",
		"LIMIT $4",
		"resource.resource_key = 'fixture_stats:' || maps.provider_entity_id",
		"COALESCE(resource.immutable, FALSE) = FALSE",
	} {
		if !strings.Contains(statisticsPayloadsSQL, fragment) {
			t.Fatalf("statistics history SQL missing %q", fragment)
		}
	}
	if strings.Contains(statisticsPayloadsSQL, "history.league_id =") {
		t.Fatal("statistics backfill must not restrict prior history to the target competition")
	}
}

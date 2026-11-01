package ingestion

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestOddsRepositoryMirrorsOneXTwoSnapshotIntoEngineOdds(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	for _, stmt := range []string{
		"CREATE TABLE odds_snapshots (id INTEGER PRIMARY KEY AUTOINCREMENT, match_id INTEGER NOT NULL, market_id INTEGER NOT NULL, provider TEXT NOT NULL, payload TEXT NOT NULL, captured_at TIMESTAMP NOT NULL);",
		"CREATE TABLE odds (id INTEGER PRIMARY KEY AUTOINCREMENT, fixture_id INTEGER NOT NULL, bookmaker TEXT NOT NULL, market TEXT NOT NULL, outcome TEXT NOT NULL, value REAL NOT NULL, implied_prob REAL, fetched_at TIMESTAMP NOT NULL, source_snapshot_id INTEGER, UNIQUE(source_snapshot_id, outcome));",
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}

	repo := NewOddsRepository(db)
	err = repo.InsertOddsSnapshot(context.Background(), OddsSnapshotRecord{
		MatchID:    42,
		MarketID:   9,
		Provider:   "api-football",
		CapturedAt: "2026-07-03T12:00:00+00:00",
		Payload: map[string]any{
			"bookmaker": "Bet365",
			"prices": []oddsPricePayload{
				{Label: "Home", Value: "1.90"},
				{Label: "Draw", Value: "3.40"},
				{Label: "Away", Value: "4.20"},
			},
		},
	})
	if err != nil {
		t.Fatalf("InsertOddsSnapshot returned error: %v", err)
	}

	var rows int
	var totalImplied float64
	if err := db.QueryRowContext(context.Background(), `SELECT count(*), sum(implied_prob) FROM odds WHERE fixture_id = 42 AND market = '1x2'`).Scan(&rows, &totalImplied); err != nil {
		t.Fatalf("scan odds rows: %v", err)
	}
	if rows != 3 {
		t.Fatalf("engine odds rows = %d, want 3", rows)
	}
	if totalImplied < 0.999 || totalImplied > 1.001 {
		t.Fatalf("total implied probability = %f, want about 1.0", totalImplied)
	}

	// A retry of the same provider observation must repair/reuse the same
	// snapshot instead of multiplying both raw and normalized rows.
	if _, err := db.ExecContext(context.Background(), `DELETE FROM odds WHERE outcome = 'Draw'`); err != nil {
		t.Fatalf("remove normalized outcome before retry: %v", err)
	}
	err = repo.InsertOddsSnapshot(context.Background(), OddsSnapshotRecord{
		MatchID:    42,
		MarketID:   9,
		Provider:   "api-football",
		CapturedAt: "2026-07-03T12:00:00+00:00",
		Payload: map[string]any{
			"prices": []oddsPricePayload{
				{Label: "Home", Value: "1.90"},
				{Label: "Draw", Value: "3.40"},
				{Label: "Away", Value: "4.20"},
			},
			"bookmaker": "Bet365",
		},
	})
	if err != nil {
		t.Fatalf("duplicate InsertOddsSnapshot returned error: %v", err)
	}

	var snapshotRows int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM odds_snapshots`).Scan(&snapshotRows); err != nil {
		t.Fatalf("count odds snapshots: %v", err)
	}
	if snapshotRows != 1 {
		t.Fatalf("odds snapshot rows = %d, want 1 after identical retry", snapshotRows)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM odds`).Scan(&rows); err != nil {
		t.Fatalf("count normalized odds after retry: %v", err)
	}
	if rows != 3 {
		t.Fatalf("normalized odds rows = %d, want 3 after identical retry", rows)
	}

	// A genuinely newer observation remains a separate point in the odds
	// history even when its prices have not moved.
	err = repo.InsertOddsSnapshot(context.Background(), OddsSnapshotRecord{
		MatchID:    42,
		MarketID:   9,
		Provider:   "api-football",
		CapturedAt: "2026-07-03T12:05:00+00:00",
		Payload: map[string]any{
			"bookmaker": "Bet365",
			"prices": []oddsPricePayload{
				{Label: "Home", Value: "1.90"},
				{Label: "Draw", Value: "3.40"},
				{Label: "Away", Value: "4.20"},
			},
		},
	})
	if err != nil {
		t.Fatalf("newer InsertOddsSnapshot returned error: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM odds_snapshots`).Scan(&snapshotRows); err != nil {
		t.Fatalf("count newer odds snapshots: %v", err)
	}
	if snapshotRows != 2 {
		t.Fatalf("odds snapshot rows = %d, want 2 for distinct capture times", snapshotRows)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM odds`).Scan(&rows); err != nil {
		t.Fatalf("count newer normalized odds: %v", err)
	}
	if rows != 6 {
		t.Fatalf("normalized odds rows = %d, want 6 for two capture times", rows)
	}
}

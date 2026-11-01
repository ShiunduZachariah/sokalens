package matches

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// The unit tests for this package run on in-memory SQLite, which accepts SQL
// PostgreSQL rejects and, worse, silently tolerates a column that production
// does not have. The tracker-state join was added to a query that SQLite had
// no table for at all; the failure only showed because the table was missing,
// not because the shape was wrong.
//
// This case asks PostgreSQL itself whether the statement is valid against the
// migrated schema. PREPARE resolves every table, column and type without
// needing a single row, so it catches a renamed column or a type mismatch
// that a stubbed database cannot.
//
// Opt-in, following services/api/internal/search/source_db_test.go:
//
//	MATCHES_TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/scratch?sslmode=disable' \
//	  go test ./services/api/internal/matches/
//
// Point it at a database with infra/db/migrations applied. It writes nothing.
func TestMatchPredictionsQueryPreparesAgainstPostgres(t *testing.T) {
	url := os.Getenv("MATCHES_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set MATCHES_TEST_DATABASE_URL to prepare the match queries against PostgreSQL")
	}

	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Running the query is the strongest check available without fixtures:
	// the driver prepares it server-side, so an invalid statement fails here
	// even though no match with this id exists.
	predictions, err := NewSource(db).matchPredictions(ctx, -1)
	if err != nil {
		t.Fatalf("matchPredictions against PostgreSQL: %v", err)
	}
	if len(predictions) != 0 {
		t.Fatalf("a fixture id that cannot exist returned %d predictions", len(predictions))
	}
}

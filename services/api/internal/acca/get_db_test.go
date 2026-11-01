package acca

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// The by-id read against a real inventory schema. None of it can be tested
// otherwise: the leg status is graded inside a jsonb_build_object in a
// correlated subquery, and the three filter decisions (status, the publication
// standard, and dropping the kickoff window) are SQL, not Go.
//
// Opt-in, following TestGenerateAgainstRealInventory:
//
//	ACCA_TEST_DATABASE_URL=postgres://... go test ./services/api/internal/acca/
//
// It writes its own fixtures into the schema and deletes them again. Point it
// at a scratch database anyway.
func TestGetSlipAgainstRealInventory(t *testing.T) {
	url := os.Getenv("ACCA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ACCA_TEST_DATABASE_URL to run the by-id read against PostgreSQL")
	}
	schema := os.Getenv("ACCA_TEST_SCHEMA")
	if schema == "" {
		schema = "public"
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const (
		// prediction_results.prediction_id is a UUID, so the legs must carry
		// UUIDs too -- which is the shape engine_predictions.id has.
		wonPrediction      = "bbbbbbbb-0000-4000-8000-000000000001"
		lostPrediction     = "bbbbbbbb-0000-4000-8000-000000000002"
		voidPrediction     = "bbbbbbbb-0000-4000-8000-000000000003"
		pendingPrediction  = "bbbbbbbb-0000-4000-8000-000000000004"
		pendingPrediction2 = "bbbbbbbb-0000-4000-8000-000000000005"

		activeID   = "aaaaaaaa-0000-4000-8000-000000000001"
		expiredID  = "aaaaaaaa-0000-4000-8000-000000000002"
		invalidID  = "aaaaaaaa-0000-4000-8000-000000000003"
		belowStdID = "aaaaaaaa-0000-4000-8000-000000000004"
		unknownID  = "aaaaaaaa-0000-4000-8000-00000000ffff"
	)
	fixtureIDs := []string{activeID, expiredID, invalidID, belowStdID}
	testPredictionIDs := []string{wonPrediction, lostPrediction, voidPrediction, pendingPrediction, pendingPrediction2}

	cleanup := func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancelCleanup()
		for _, stmt := range []string{
			`DELETE FROM acca_slip_legs WHERE slip_id = ANY($1::uuid[])`,
			`DELETE FROM acca_slips WHERE id = ANY($1::uuid[])`,
		} {
			if _, err := db.ExecContext(cleanupCtx, stmt, pgTextArray(fixtureIDs)); err != nil {
				t.Errorf("clean up fixtures: %v", err)
			}
		}
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM prediction_results WHERE prediction_id = ANY($1::uuid[])`, pgTextArray(testPredictionIDs)); err != nil {
			t.Errorf("clean up results: %v", err)
		}
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM engine_predictions WHERE id = ANY($1::uuid[])`, pgTextArray(testPredictionIDs)); err != nil {
			t.Errorf("clean up engine predictions: %v", err)
		}
		// fixtures and matches cascade from matches, but the delete order is
		// spelled out rather than relied on.
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM fixtures WHERE id BETWEEN 990000001 AND 990000009`); err != nil {
			t.Errorf("clean up fixtures: %v", err)
		}
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM matches WHERE id BETWEEN 990000001 AND 990000009`); err != nil {
			t.Errorf("clean up matches: %v", err)
		}
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM teams WHERE id BETWEEN 990000001 AND 990000009`); err != nil {
			t.Errorf("clean up teams: %v", err)
		}
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM leagues WHERE id = 990000001`); err != nil {
			t.Errorf("clean up leagues: %v", err)
		}
		if _, err := db.ExecContext(cleanupCtx, `DELETE FROM sports WHERE slug = 'acca-get-test-sport'`); err != nil {
			t.Errorf("clean up sports: %v", err)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	// A slip whose matches are already in the past, which is the interesting
	// case: the list endpoint hides it and this endpoint must not.
	played := time.Now().UTC().Add(-3 * time.Hour)
	seed := []struct {
		stmt string
		args []any
	}{
		// A sport row may not exist on a freshly migrated database, so make
		// sure of one rather than assuming the fixture data is there.
		{`INSERT INTO sports (slug, name) VALUES ('acca-get-test-sport', 'ACCA Get Test Sport')
			ON CONFLICT (slug) DO NOTHING`, nil},
		// country_code is a foreign key, so it is left NULL rather than given
		// a plausible-looking value this database may not hold.
		{`INSERT INTO leagues (id, sport_id, slug, name)
			SELECT 990000001, id, 'acca-get-test-league', 'ACCA Get Test League'
			FROM sports WHERE slug='acca-get-test-sport'`, nil},
		{`INSERT INTO teams (id, slug, name) VALUES
			(990000001, 'acca-get-alpha', 'Alpha'), (990000002, 'acca-get-beta', 'Beta'),
			(990000003, 'acca-get-gamma', 'Gamma'), (990000004, 'acca-get-delta', 'Delta')`, nil},
		{`INSERT INTO matches (id, league_id, home_team_id, away_team_id, kickoff_at, status) VALUES
			(990000001, 990000001, 990000001, 990000002, $1, 'finished'),
			(990000002, 990000001, 990000003, 990000004, $1, 'finished')`, []any{played}},
		// fixtures.id REFERENCES matches(id) -- one id per thing, which is why
		// engine_predictions.fixture_id reaches a match through here rather
		// than carrying its own.
		{`INSERT INTO fixtures (id, league_id, season_id, home_team_id, away_team_id, kickoff_at, status) VALUES
			(990000001, 990000001, 1, 990000001, 990000002, $1, 'finished'),
			(990000002, 990000001, 1, 990000003, 990000004, $1, 'finished')`, []any{played}},
		// prediction_results.prediction_id is a FOREIGN KEY to
		// engine_predictions.id, which is the proof the legs' TEXT
		// prediction_id really is that UUID and the cast in the join is
		// comparing the right two things.
		//
		// EACH ROW NEEDS A DISTINCT (fixture_id, market). engine_predictions
		// carries a BEFORE INSERT trigger, guard_engine_prediction_insert,
		// that recomputes a content hash and suppresses an insert that repeats
		// the current row for that fixture and market. Seeding five rows that
		// differed only by id silently stored one of them, and the failure
		// surfaced three statements later as a foreign-key violation on
		// prediction_results -- which is a good argument for seeding against
		// the real schema rather than a hand-written one.
		{`INSERT INTO engine_predictions
			(id, fixture_id, market, prediction, probability, confidence, confidence_tier, lifecycle_stage, data_completeness, gate_tier, model_version, content_hash)
			VALUES
			($1::uuid, 990000001, 'total_goals', 'over_1.5', 0.7, 0.6, 'medium', 'PRE_MATCH', 1.0, 'FULL', 'test', 'acca-get-test-hash-1'),
			($2::uuid, 990000001, 'btts', 'yes', 0.7, 0.6, 'medium', 'PRE_MATCH', 1.0, 'FULL', 'test', 'acca-get-test-hash-2'),
			($3::uuid, 990000001, '1x2', 'home', 0.7, 0.6, 'medium', 'PRE_MATCH', 1.0, 'FULL', 'test', 'acca-get-test-hash-3'),
			($4::uuid, 990000002, 'total_goals', 'over_1.5', 0.7, 0.6, 'medium', 'PRE_MATCH', 1.0, 'FULL', 'test', 'acca-get-test-hash-4'),
			($5::uuid, 990000002, 'btts', 'yes', 0.7, 0.6, 'medium', 'PRE_MATCH', 1.0, 'FULL', 'test', 'acca-get-test-hash-5')`,
			[]any{wonPrediction, lostPrediction, voidPrediction, pendingPrediction, pendingPrediction2}},
		// Three legs' worth of settlement: one won, one lost, one that could
		// not be graded. The two pending predictions deliberately get no row.
		{`INSERT INTO prediction_results
			(prediction_id, fixture_id, market, predicted, probability, confidence, confidence_tier, lifecycle_stage_at_settlement, league_id, settlement_status, is_correct)
			VALUES
			($1::uuid, 990000001, 'total_goals', 'over_1.5', 0.7, 0.6, 'medium', 'PRE_MATCH', 990000001, 'settled', true),
			($2::uuid, 990000001, 'total_goals', 'over_1.5', 0.7, 0.6, 'medium', 'PRE_MATCH', 990000001, 'settled', false),
			($3::uuid, 990000001, 'total_goals', 'over_1.5', 0.7, 0.6, 'medium', 'PRE_MATCH', 990000001, 'unsettled_data_unavailable', NULL)`,
			[]any{wonPrediction, lostPrediction, voidPrediction}},
	}
	for _, s := range seed {
		if _, err := db.ExecContext(ctx, s.stmt, s.args...); err != nil {
			t.Fatalf("seed (%s): %v", s.stmt, err)
		}
	}

	// predictionMarkets mirrors the engine_predictions seed above: a leg must
	// name the same market as the prediction it points at, or the payload
	// would describe a market the settled row never graded.
	predictionMarkets := map[string]string{
		wonPrediction:      "total_goals",
		lostPrediction:     "btts",
		voidPrediction:     "1x2",
		pendingPrediction:  "total_goals",
		pendingPrediction2: "btts",
	}
	predictionFixtures := map[string]int64{
		wonPrediction:      990000001,
		lostPrediction:     990000001,
		voidPrediction:     990000001,
		pendingPrediction:  990000002,
		pendingPrediction2: 990000002,
	}

	insertSlip := func(id, status string, odds float64, legOdds []float64, predictionIDs []string) {
		t.Helper()
		// The id is passed twice, once as uuid and once as text: reusing one
		// placeholder for both makes the driver deduce two types for it.
		if _, err := db.ExecContext(ctx, `
INSERT INTO acca_slips (id, composition_hash, content_hash, market_tab, period, leg_count, combined_fair_odds, ranking_score, earliest_kickoff, latest_kickoff, odds_band, status, generation_intent)
VALUES ($1::uuid, $2, $2, 'mixed', 'today', $3, $4, 0.5, $5, $5, '2-3', $6, 'test')`,
			id, id, len(legOdds), odds, played, status); err != nil {
			t.Fatalf("insert slip %s: %v", id, err)
		}
		for i, legOdd := range legOdds {
			predictionID := predictionIDs[i]
			if _, err := db.ExecContext(ctx, `
INSERT INTO acca_slip_legs (slip_id, position, prediction_source, prediction_id, prediction_version, match_id, market_key, selection, fair_odds_at_generation)
VALUES ($1::uuid, $2, 'engine', $3, 'test', $4, $5, 'over_1.5', $6)`,
				id, i+1, predictionID, predictionFixtures[predictionID],
				predictionMarkets[predictionID], legOdd); err != nil {
				t.Fatalf("insert leg %d of %s: %v", i+1, id, err)
			}
		}
	}

	// 1.5^(3/2) = 1.8371, so 2.4 clears the publication floor for three legs.
	insertSlip(activeID, "active", 2.4, []float64{1.4, 1.3, 1.32},
		[]string{wonPrediction, lostPrediction, voidPrediction})
	insertSlip(expiredID, "expired", 2.4, []float64{1.4, 1.3, 1.32},
		[]string{wonPrediction, pendingPrediction, pendingPrediction2})
	insertSlip(invalidID, "invalid", 2.4, []float64{1.4, 1.3, 1.32},
		[]string{wonPrediction, lostPrediction, voidPrediction})
	// Below the floor for three legs, so the list hides it and so must this.
	insertSlip(belowStdID, "active", 1.60, []float64{1.4, 1.3, 1.32},
		[]string{wonPrediction, lostPrediction, voidPrediction})

	source := NewSource(db, schema)

	t.Run("an active slip comes back with per-leg statuses", func(t *testing.T) {
		slip, err := source.Get(ctx, activeID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if slip.ID != activeID || slip.LegCount != 3 || len(slip.Legs) != 3 {
			t.Fatalf("slip = %+v", slip)
		}
		got := map[string]string{}
		for _, leg := range slip.Legs {
			got[leg.PredictionID] = leg.Status
		}
		// The same mapping the match page uses: a settled correct pick is won,
		// a settled incorrect one is lost, and a market that could not be
		// graded is void rather than lost.
		for predictionID, want := range map[string]string{
			wonPrediction:  "won",
			lostPrediction: "lost",
			voidPrediction: "void",
		} {
			if got[predictionID] != want {
				t.Fatalf("leg %s status = %q, want %q (all: %v)", predictionID, got[predictionID], want, got)
			}
		}
	})

	t.Run("a leg with no result row is pending", func(t *testing.T) {
		slip, err := source.Get(ctx, expiredID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		pending := 0
		for _, leg := range slip.Legs {
			if leg.Status == "pending" {
				pending++
			}
		}
		if pending != 2 {
			t.Fatalf("pending legs = %d, want 2", pending)
		}
	})

	t.Run("an expired slip is still served", func(t *testing.T) {
		// The whole reason the kickoff window is dropped here. Its matches were
		// played three hours ago, so the browse list hides it -- and that is
		// exactly when a reader wants to see how their ACCA did.
		if _, err := source.Get(ctx, expiredID); err != nil {
			t.Fatalf("an expired slip 404ed, which makes leg statuses unreachable: %v", err)
		}
	})

	t.Run("inventory marked invalid is not served", func(t *testing.T) {
		if _, err := source.Get(ctx, invalidID); !errors.Is(err, ErrSlipNotFound) {
			t.Fatalf("err = %v, want ErrSlipNotFound", err)
		}
	})

	t.Run("a slip below the publication standard is not served", func(t *testing.T) {
		// "Tier rules match the list endpoint", and the standard IS that rule.
		if _, err := source.Get(ctx, belowStdID); !errors.Is(err, ErrSlipNotFound) {
			t.Fatalf("err = %v, want ErrSlipNotFound", err)
		}
	})

	t.Run("an unknown id is not found", func(t *testing.T) {
		if _, err := source.Get(ctx, unknownID); !errors.Is(err, ErrSlipNotFound) {
			t.Fatalf("err = %v, want ErrSlipNotFound", err)
		}
	})

	t.Run("a malformed id is not found rather than an error", func(t *testing.T) {
		// Left to PostgreSQL this raises `invalid input syntax for type uuid`,
		// which the handler would turn into a 503. A typo in a URL is not an
		// outage and must not be counted as one.
		for _, id := range []string{"not-a-uuid", "", "   ", "6f1a9c3e-0b2d-4f58-9a7c", "'; DROP TABLE acca_slips; --"} {
			if _, err := source.Get(ctx, id); !errors.Is(err, ErrSlipNotFound) {
				t.Fatalf("id %q gave err = %v, want ErrSlipNotFound", id, err)
			}
		}
	})

	t.Run("the list statement still executes with the status join", func(t *testing.T) {
		// legsQuery is shared, so adding the prediction_results join to it
		// changed the LIST statement as well. That join was broken against the
		// real schema at first -- uuid compared to text, which PostgreSQL
		// refuses outright -- and a broken legs subquery would have taken the
		// browse list down with it, not just this endpoint.
		result, err := source.List(ctx, Query{Tab: "all", Period: "all", Limit: 5, Seed: "integration"})
		if err != nil {
			t.Fatalf("List with the leg-status join: %v", err)
		}
		for _, slip := range result.Items {
			for _, leg := range slip.Legs {
				// Every slip the list serves has yet to kick off, so nothing
				// can be graded. Anything else means the join is matching rows
				// it should not.
				if leg.Status != "" && leg.Status != "pending" {
					t.Fatalf("list served a graded leg (%s) on an unplayed slip %s", leg.Status, slip.ID)
				}
			}
		}
	})

	t.Run("the shape matches a list item", func(t *testing.T) {
		// Same struct, so this asserts the by-id query actually populates the
		// fields a list item has rather than leaving them zero.
		slip, err := source.Get(ctx, activeID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if slip.MarketTab == "" || slip.Period == "" || slip.OddsBand == "" ||
			slip.CombinedFairOdds <= 0 || slip.EarliestKickoff.IsZero() {
			t.Fatalf("by-id read left list-item fields empty: %+v", slip)
		}
		for _, leg := range slip.Legs {
			if leg.Position == 0 || leg.MatchID == 0 || leg.Market == "" ||
				leg.Selection == "" || leg.FairOdds <= 0 || leg.HomeTeam == "" ||
				leg.AwayTeam == "" || leg.League == "" || leg.KickoffAt.IsZero() {
				t.Fatalf("leg is missing list-item fields: %+v", leg)
			}
		}
	})

	t.Run("legs carry the slugs a client needs to link out", func(t *testing.T) {
		// Without these a leg could name a team and a league and still not
		// link to either, which is the gap the client workstream reported.
		// The MATCH link needs no slug -- it is built from the id plus the
		// names, which were already here.
		slip, err := source.Get(ctx, activeID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		for _, leg := range slip.Legs {
			if leg.HomeTeamSlug == "" || leg.AwayTeamSlug == "" || leg.LeagueSlug == "" {
				t.Fatalf("leg has no slugs to link with: %+v", leg)
			}
		}
		if slip.Legs[0].HomeTeamSlug == slip.Legs[0].AwayTeamSlug {
			t.Fatalf("both sides got the same slug, so the join is reading one team twice: %+v", slip.Legs[0])
		}
	})
}

// pgTextArray formats ids for `= ANY($1::uuid[])` without pulling in a driver
// array type, which the rest of this package does not use.
func pgTextArray(values []string) string {
	out := "{"
	for i, v := range values {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out + "}"
}

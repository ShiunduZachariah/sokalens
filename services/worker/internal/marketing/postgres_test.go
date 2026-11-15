package marketing

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/dbtest"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// The queries are where a marketing post could go wrong in public: a best bet
// leaked into a free channel, a superseded pick posted, a result graded
// differently from the record. They run against the real migrated schema.
//
//	MARKETING_TEST_DATABASE_URL='postgres://sports:sports@127.0.0.1:5432/scratch?sslmode=disable' \
//	  go test ./services/worker/internal/marketing/
//
// It inserts rows with a unique suffix into the migrated tables and deletes
// them afterwards. Point it at a scratch database anyway.
func TestQueriesAgainstPostgres(t *testing.T) {
	url := dbtest.URLOrSkip(t, "MARKETING_TEST_DATABASE_URL", "run the marketing queries against a migrated PostgreSQL")
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	// Closed by Cleanup, not defer: cleanups run after the test body returns,
	// and a deferred Close would leave them a closed handle.
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	suffix := fmt.Sprint(time.Now().UnixNano())
	now := time.Now().UTC().Truncate(time.Second)

	mustExec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
	}
	returning := func(query string, args ...any) int64 {
		t.Helper()
		var id int64
		if err := db.QueryRowContext(ctx, query, args...).Scan(&id); err != nil {
			t.Fatalf("%v\n%s", err, query)
		}
		return id
	}

	sportID := returning(`INSERT INTO sports (slug, name) VALUES ($1, 'Football test') RETURNING id`, "football-"+suffix)
	league := func(name string, priority int) int64 {
		return returning(`INSERT INTO leagues (sport_id, slug, name, priority_score) VALUES ($1, $2, $3, $4) RETURNING id`, sportID, name+"-"+suffix, name, priority)
	}
	top := league("Top League", 95)
	minor := league("Minor League", 0)
	chosen := league("Chosen League", 0)
	team := func(name string) int64 {
		return returning(`INSERT INTO teams (slug, name) VALUES ($1, $2) RETURNING id`, strings.ToLower(name)+"-"+suffix, name)
	}
	home, away := team("Homeside"), team("Awayside")

	var fixtureIDs []int64
	fixture := func(leagueID int64, kickoff time.Time) int64 {
		// Same teams and league, so a distinct minute keeps each a distinct
		// canonical match.
		kickoff = kickoff.Add(time.Duration(len(fixtureIDs)) * time.Minute)
		id := returning(`INSERT INTO matches (league_id, home_team_id, away_team_id, kickoff_at) VALUES ($1, $2, $3, $4) RETURNING id`, leagueID, home, away, kickoff)
		mustExec(`INSERT INTO fixtures (id, league_id, season_id, home_team_id, away_team_id, kickoff_at, status) VALUES ($1, $2, 2026, $3, $4, $5, 'NS')`, id, leagueID, home, away, kickoff)
		fixtureIDs = append(fixtureIDs, id)
		return id
	}
	t.Cleanup(func() {
		ids := int64Array(fixtureIDs)
		for _, q := range []string{
			`DELETE FROM prediction_results WHERE fixture_id = ANY($1::bigint[])`,
			`DELETE FROM inplay_snapshots WHERE fixture_id = ANY($1::bigint[])`,
			`DELETE FROM fixture_events WHERE fixture_id = ANY($1::bigint[])`,
			`DELETE FROM matches WHERE id = ANY($1::bigint[])`,
		} {
			if _, err := db.ExecContext(ctx, q, ids); err != nil {
				t.Logf("cleanup: %v", err)
			}
		}
		_, _ = db.ExecContext(ctx, `DELETE FROM marketing_posts WHERE post_key LIKE $1`, "test-"+suffix+"%")
		_, _ = db.ExecContext(ctx, `DELETE FROM teams WHERE id = ANY($1::bigint[])`, int64Array([]int64{home, away}))
		_, _ = db.ExecContext(ctx, `DELETE FROM leagues WHERE id = ANY($1::bigint[])`, int64Array([]int64{top, minor, chosen}))
		_, _ = db.ExecContext(ctx, `DELETE FROM sports WHERE id = $1`, sportID)
	})

	predict := func(fixtureID int64, market, selection string, confidence float64, gate string, bestBet, superseded bool, kickoff time.Time) {
		var supersededAt any
		if superseded {
			supersededAt = kickoff.Add(-2 * time.Hour)
		}
		mustExec(`
INSERT INTO engine_predictions (fixture_id, market, prediction, probability, confidence, confidence_tier, is_best_bet,
  lifecycle_stage, data_completeness, gate_tier, model_version, content_hash, generated_at, superseded_at)
VALUES ($1, $2, $3, 0.6, $4, 'medium', $5, 'PRE_MATCH', 0.9, $6, 'test', $7, $8, $9)`,
			fixtureID, market, selection, confidence, bestBet, gate, strings.Repeat("a", 64), kickoff.Add(-3*time.Hour), supersededAt)
	}

	kickoff := now.Add(5 * time.Hour)
	a := fixture(top, kickoff)
	predict(a, "1x2", "home", 0.80, "FULL", false, false, kickoff)
	predict(a, "btts", "yes", 0.70, "FULL", false, false, kickoff)
	b := fixture(top, kickoff)
	predict(b, "1x2", "home", 0.95, "FULL", true, false, kickoff) // best bet: gated, never public
	predict(b, "btts", "yes", 0.60, "BASIC", false, false, kickoff)
	c := fixture(minor, kickoff)
	predict(c, "1x2", "away", 0.99, "FULL", false, false, kickoff) // league not covered
	d := fixture(top, kickoff)
	predict(d, "1x2", "home", 0.90, "FULL", false, true, kickoff)              // superseded
	predict(d, "total_goals", "over_2.5", 0.65, "SKIP", false, false, kickoff) // gated out
	predict(d, "correct_score", "1-0", 0.99, "FULL", false, false, kickoff)    // unsupported market
	e := fixture(chosen, kickoff)
	predict(e, "double_chance", "home_or_draw", 0.50, "FULL", false, false, kickoff)

	store := NewPostgresStore(db)
	picks, err := store.CandidatePicks(ctx, PickQuery{From: now, To: now.Add(24 * time.Hour), LeagueIDs: []int64{chosen}, MinLeaguePriority: 60, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range picks {
		if p.FixtureID == a || p.FixtureID == b || p.FixtureID == c || p.FixtureID == d || p.FixtureID == e {
			got = append(got, fmt.Sprintf("%d:%s:%s", p.FixtureID, p.Market, p.Selection))
		}
	}
	want := []string{fmt.Sprintf("%d:1x2:home", a), fmt.Sprintf("%d:btts:yes", b), fmt.Sprintf("%d:double_chance:home_or_draw", e)}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("picks = %v, want %v", got, want)
	}
	for _, p := range picks {
		if p.FixtureID == a && (p.Home != "Homeside" || p.HomeTeamID != home || p.League != "Top League" || p.Confidence != 0.8) {
			t.Fatalf("pick details = %+v", p)
		}
	}

	// Grading: the official settlement, the engine's rule on a final score,
	// and a postponed match.
	mustExec(`UPDATE fixtures SET status = 'FT', home_goals_ft = 2, away_goals_ft = 1 WHERE id = $1`, a)
	mustExec(`UPDATE fixtures SET status = 'FT', home_goals_ft = 0, away_goals_ft = 0 WHERE id = $1`, b)
	mustExec(`UPDATE fixtures SET status = 'PST' WHERE id = $1`, e)
	mustExec(`
INSERT INTO prediction_results (prediction_id, fixture_id, market, predicted, actual, is_correct, probability, confidence,
  confidence_tier, lifecycle_stage_at_settlement, league_id)
SELECT id, fixture_id, market, prediction, 'home', true, 0.6, 0.8, 'medium', 'PRE_MATCH', $2
FROM engine_predictions WHERE fixture_id = $1 AND market = '1x2' AND superseded_at IS NULL`, a, top)
	graded, err := store.GradePicks(ctx, picks[:0:0])
	if err != nil || len(graded) != 0 {
		t.Fatalf("empty grade = %v, %v", graded, err)
	}
	var mine []Pick
	for _, p := range picks {
		if p.FixtureID == a || p.FixtureID == b || p.FixtureID == e {
			mine = append(mine, p)
		}
	}
	graded, err = store.GradePicks(ctx, mine)
	if err != nil {
		t.Fatal(err)
	}
	results := []string{}
	for _, g := range graded {
		results = append(results, g.Result+":"+g.Score())
	}
	if strings.Join(results, ",") != "won:2-1,lost:0-0,void:" {
		t.Fatalf("grades = %v", results)
	}

	// Goals: two snapshots of the same scoreline are one goal.
	mustExec(`INSERT INTO fixture_events (fixture_id, provider, observed_at, elapsed_minute, event_type, event_detail, player_name, identity_key)
VALUES ($1, 'api-football', $2, 23, 'Goal', 'Normal Goal', 'A. Striker', $3)`, a, now.Add(-4*time.Minute), "goal-"+suffix)
	for i, health := range []string{"ok", "ok"} {
		mustExec(`INSERT INTO inplay_snapshots (fixture_id, captured_at, trigger, status, elapsed_minute, home_goals, away_goals, model_health, markets, model_version)
VALUES ($1, $2, 'post_goal', '1H', 23, 1, 0, $3, '{"match_result_in_play": {"home": 6000, "draw": 2500, "away": 1500}}', 'test')`,
			a, now.Add(time.Duration(i-5)*time.Minute), health)
	}
	goals, err := store.RecentGoals(ctx, GoalQuery{Since: now.Add(-20 * time.Minute), FixtureIDs: []int64{a}, MinLeaguePriority: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(goals) != 1 || goals[0].Scorer != "A. Striker" || goals[0].Probs == nil || goals[0].Probs.Home != 6000 || goals[0].Minute != 23 {
		t.Fatalf("goals = %+v", goals)
	}
	scores, err := store.LatestScores(ctx, []int64{a})
	if err != nil || scores[a] != [2]int{1, 0} {
		t.Fatalf("latest scores = %v, %v", scores, err)
	}

	// The ledger: the first claim's content wins, and a key is seen once.
	key := "test-" + suffix + ":picks"
	first, err := store.ClaimPost(ctx, Post{Key: key, Channel: ChannelTelegram, Kind: KindPicks, Caption: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ClaimPost(ctx, Post{Key: key, Channel: ChannelTelegram, Kind: KindPicks, Caption: "second"})
	if err != nil || second.ID != first.ID || second.Caption != "first" {
		t.Fatalf("second claim = %+v, %v", second, err)
	}
	if err := store.BeginAttempt(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkFailed(ctx, first.ID, "down"); err != nil {
		t.Fatal(err)
	}
	retry, err := store.RetryablePosts(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range retry {
		found = found || p.ID == first.ID
	}
	if !found {
		t.Fatal("a failed post with attempts left must be retryable")
	}
	if exists, err := store.KeyExists(ctx, key); err != nil || !exists {
		t.Fatalf("key exists = %v, %v", exists, err)
	}
	if _, err := store.WeeklyNumbers(ctx, now.AddDate(0, 0, -7)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarketRecord(ctx, RecordQuery{Market: "1x2", Since: now.AddDate(0, 0, -90), MinLeaguePriority: 60}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecentForm(ctx, home, now.Add(time.Hour), 5); err != nil {
		t.Fatal(err)
	}
}

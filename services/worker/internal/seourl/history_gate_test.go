package seourl

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"
)

func gatedFixture(mutate func(*Entity)) Entity {
	e := Entity{
		Path:          "/predictions/premier-league/arsenal-vs-chelsea-1",
		Type:          TypeFixture,
		ID:            1,
		Status:        "scheduled",
		KickoffAt:     time.Now().Add(24 * time.Hour),
		HasPrediction: true,
		HomeHistory:   10,
		AwayHistory:   10,
		HistoryKnown:  true,
	}
	if mutate != nil {
		mutate(&e)
	}
	return e
}

func TestHistoryGateWithholdsThinFixtures(t *testing.T) {
	now := time.Now()

	cases := []struct {
		name       string
		home, away int
		indexable  bool
	}{
		{"both well above the bar", 10, 10, true},
		{"both exactly at the bar", MinimumFixtureHistory, MinimumFixtureHistory, true},
		{"home one short", MinimumFixtureHistory - 1, MinimumFixtureHistory, false},
		{"away one short", MinimumFixtureHistory, MinimumFixtureHistory - 1, false},
		{"both empty", 0, 0, false},
		// The averaging trap: a lopsided pair must fail. A preview cannot say
		// anything about the side with no record, however deep the other is.
		{"one deep, one empty", 40, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(gatedFixture(func(e *Entity) {
				e.HomeHistory, e.AwayHistory = tc.home, tc.away
			}), now)

			if got.Indexable != tc.indexable {
				t.Fatalf("indexable = %v, want %v (home=%d away=%d)",
					got.Indexable, tc.indexable, tc.home, tc.away)
			}
			if !tc.indexable && got.Reason != ReasonInsufficientHistory {
				t.Fatalf("reason = %q, want %q", got.Reason, ReasonInsufficientHistory)
			}
		})
	}
}

// An unsupplied count must never gate.
//
// This is the whole reason HistoryKnown exists. Without it, a source query that
// forgot the two columns would report zero for every fixture and the sweep
// would withhold the entire catalogue -- roughly 7,300 pages -- reporting no
// error at all. The failure has to land on the side of advertising a few thin
// pages for one sweep, never on the side of de-indexing the property.
func TestUnknownHistoryNeverGates(t *testing.T) {
	got := Decide(gatedFixture(func(e *Entity) {
		e.HistoryKnown = false
		e.HomeHistory, e.AwayHistory = 0, 0
	}), time.Now())

	if !got.Indexable {
		t.Fatalf("a fixture with unsupplied history must stay indexable, got %+v", got)
	}
}

// Archived is reported ahead of thin history, and the order is load-bearing.
//
// Both exclude, but only one is recoverable: thin history resolves itself as
// the teams play, while an archived fixture never comes back. Reporting the
// recoverable reason on a fixture that is really just old would inflate the
// population the gate is meant to measure, and that population is the number
// the rollout decision is made on.
func TestArchivedBeatsInsufficientHistory(t *testing.T) {
	now := time.Now()
	got := Decide(gatedFixture(func(e *Entity) {
		e.Status = "ft"
		e.KickoffAt = now.Add(-time.Duration(SettledIndexableDays+1) * 24 * time.Hour)
		e.HomeHistory, e.AwayHistory = 0, 0
	}), now)

	if got.Indexable {
		t.Fatal("an archived fixture must not be indexable")
	}
	if got.Reason != ReasonArchived {
		t.Fatalf("reason = %q, want %q", got.Reason, ReasonArchived)
	}
}

// A missing prediction still outranks every other reason: it is by far the
// largest excluded population, and moving it would make the counts
// incomparable with every measurement taken before this change.
func TestNoPredictionBeatsInsufficientHistory(t *testing.T) {
	got := Decide(gatedFixture(func(e *Entity) {
		e.HasPrediction = false
		e.HomeHistory, e.AwayHistory = 0, 0
	}), time.Now())

	if got.Reason != ReasonNoPrediction {
		t.Fatalf("reason = %q, want %q", got.Reason, ReasonNoPrediction)
	}
}

// The gate applies to fixtures only. A team page's bar is its own
// (isIndexableTeam), and a static or hub URL has no history to speak of.
func TestHistoryGateDoesNotTouchOtherEntityTypes(t *testing.T) {
	for _, entityType := range []EntityType{TypeTeam, TypeLeague, TypeCountry} {
		got := Decide(Entity{
			Path:       "/teams/arsenal",
			Type:       entityType,
			HasContent: true,
		}, time.Now())
		if !got.Indexable {
			t.Fatalf("%s with content must stay indexable, got %+v", entityType, got)
		}
	}
}

// TestGoAndTypeScriptFixtureHistoryAgree pins MinimumFixtureHistory to the
// web app's MINIMUM_FIXTURE_HISTORY.
//
// The sweep decides what the sitemap advertises; the page decides its own
// robots tag. If these two numbers drift, the sitemap advertises pages that
// noindex themselves -- silently, and reported back weeks later as crawl
// errors nobody attributes to a constant. Same hazard, and same remedy, as
// TestGoAndTypeScriptSlugsAgree.
func TestGoAndTypeScriptFixtureHistoryAgree(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	file := filepath.Join(repo, "apps", "web", "app", "lib", "fixtureLifecycle.ts")
	source, err := os.ReadFile(file)
	if err != nil {
		t.Skipf("fixtureLifecycle.ts not found at %s", file)
	}

	match := regexp.MustCompile(
		`export const MINIMUM_FIXTURE_HISTORY\s*=\s*(\d+)`,
	).FindSubmatch(source)
	if match == nil {
		// Loud rather than silent: if the declaration moved or was renamed this
		// test would otherwise pass while checking nothing at all.
		t.Fatal("MINIMUM_FIXTURE_HISTORY not found in fixtureLifecycle.ts; " +
			"if it moved, update this test rather than deleting it")
	}

	web, err := strconv.Atoi(string(match[1]))
	if err != nil {
		t.Fatalf("parse MINIMUM_FIXTURE_HISTORY: %v", err)
	}
	if web != MinimumFixtureHistory {
		t.Fatalf("MINIMUM_FIXTURE_HISTORY = %d but MinimumFixtureHistory = %d; "+
			"the page and the sitemap would disagree about the same fixture",
			web, MinimumFixtureHistory)
	}
}

// The gate's bar is the preview builder's bar. If MIN_MATCHES_FOR_RATES moves,
// the gate is withholding pages that would have had content, or advertising
// pages that cannot state a rate -- either way the two have to move together.
func TestGateMatchesThePreviewsOwnBar(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	file := filepath.Join(repo, "apps", "web", "app", "lib", "marketReads.ts")
	source, err := os.ReadFile(file)
	if err != nil {
		t.Skipf("marketReads.ts not found at %s", file)
	}

	match := regexp.MustCompile(
		`export const MIN_MATCHES_FOR_RATES\s*=\s*(\d+)`,
	).FindSubmatch(source)
	if match == nil {
		t.Fatal("MIN_MATCHES_FOR_RATES not found in marketReads.ts")
	}
	rates, err := strconv.Atoi(string(match[1]))
	if err != nil {
		t.Fatalf("parse MIN_MATCHES_FOR_RATES: %v", err)
	}
	if rates != MinimumFixtureHistory {
		t.Fatalf("MIN_MATCHES_FOR_RATES = %d but the SEO gate uses %d; "+
			"the gate must withhold exactly the pages the preview cannot fill",
			rates, MinimumFixtureHistory)
	}
}

// The history subqueries must never be scoped to a competition.
//
// This guards a defect that was caught on 2026-09-28 with the gate still
// disabled. Scoping the counts to m.league_id made a promoted club read as
// zero in its new division, and made BOTH sides of every cup and continental
// tie read zero -- a club's league record is not in the FA Cup or the
// Champions League. Verified against a seeded Postgres at the time: an FA Cup
// tie between two clubs with 30 and 40 settled matches counted 0 and 0.
//
// The gate consumes these counts, so re-adding the predicate would withhold
// precisely the highest-demand fixtures on the property. It is asserted on the
// SQL text because the scoping lives in the query, not in Decide, and nothing
// in the Go logic would notice.
func TestHistorySubqueriesAreNotScopedToALeague(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	for _, file := range []string{
		filepath.Join(repo, "services", "worker", "internal", "seourl", "postgres.go"),
		filepath.Join(repo, "services", "api", "internal", "matches", "source.go"),
	} {
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}

		// Isolate each history subquery by its alias, so an unrelated
		// league_id join elsewhere in these files cannot trip the assertion.
		for _, alias := range []string{"hh", "ah", "history_home", "history_away"} {
			pattern := regexp.MustCompile(
				`(?s)FROM matches ` + alias + `\b(.*?)\) AS \w+_history_count`)
			for _, match := range pattern.FindAllSubmatch(source, -1) {
				if regexp.MustCompile(`\.league_id\s*=\s*m\.league_id`).Match(match[1]) {
					t.Errorf("%s: the %s history subquery is scoped to m.league_id; "+
						"this withholds every cup and continental fixture",
						filepath.Base(file), alias)
				}
			}
		}
	}
}

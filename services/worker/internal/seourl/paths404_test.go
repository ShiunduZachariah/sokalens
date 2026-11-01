package seourl

import (
	"strings"
	"testing"
)

// Everything that can put a 404 in a sitemap.
//
// A sitemap URL that 404s is worse than a missing one. Missing costs the reach
// of one page; advertising a dead URL teaches the crawler the file is
// unreliable, and this property is rebuilding that trust after impressions
// fell from 928 a day to 6. At roughly 275 crawls a day, a wasted crawl is
// also a page that could have ranked and was not fetched.
//
// The first version of these paths got every entity type wrong and nothing
// failed, because nothing read the table yet. These are the cases that were
// missed, plus the ones adjacent to them.

func TestNonAsciiSlugsAreRefused(t *testing.T) {
	// The catalogue stores 186 team slugs with the original diacritics; the
	// route's guard is ASCII-only, so each is a page that does not exist.
	// These are real values from production.
	for _, slug := range []string{
		"team-denmark-nørresundby",
		"team-norway-lillestrøm-ii",
		"team-poland-akademia-piłkarska-lg-w",
		"team-germany-münchen",
		"équipe",
	} {
		if got := TeamPath(slug); got != "" {
			t.Errorf("TeamPath(%q) = %q; that URL 404s", slug, got)
		}
		if got := LeaguePath(slug); got != "" {
			t.Errorf("LeaguePath(%q) = %q; that URL 404s", slug, got)
		}
	}
}

func TestSlugShapesTheRouteRejects(t *testing.T) {
	cases := map[string]string{
		"empty":             "",
		"whitespace only":   "   ",
		"leading dash":      "-team-a",
		"trailing dash":     "team-a-",
		"double dash":       "team--a",
		"uppercase":         "Team-A",
		"underscore":        "team_a",
		"dot":               "team.a",
		"slash":             "team/a",
		"space":             "team a",
		"literal null":      "null",
		"literal undefined": "undefined",
		"literal nan":       "nan",
		"query string":      "team?a=1",
		"fragment":          "team#a",
		"percent encoded":   "team%20a",
	}
	for name, slug := range cases {
		if got := TeamPath(slug); got != "" {
			t.Errorf("%s: TeamPath(%q) = %q, want refused", name, slug, got)
		}
	}
}

func TestValidSlugsStillPass(t *testing.T) {
	// The guard must not be so strict it empties the sitemap. These are real
	// production slugs that serve 200.
	for _, slug := range []string{
		"team-albania-basania",
		"league-world-uefa-champions-league",
		"arsenal",
		"a1",
		"team-1-fc-koln",
	} {
		if TeamPath(slug) == "" {
			t.Errorf("TeamPath(%q) refused a valid slug", slug)
		}
		if LeaguePath(slug) == "" {
			t.Errorf("LeaguePath(%q) refused a valid slug", slug)
		}
	}
}

func TestEveryEmittedPathSurvivesValidPath(t *testing.T) {
	// The two guards must agree. A path the builders emit but ValidPath
	// rejects would be dropped by the sweep with a warning nobody reads; the
	// reverse would be a malformed row in the table.
	emitted := []string{
		TeamPath("team-albania-basania"),
		LeaguePath("league-world-uefa-champions-league"),
		FixturePath("Serie C Girone A", "Trento", "Pro Vercelli", 3957145),
		FixturePath("Copa Federación", "San Fernando CD", "Orihuela", 3052312),
	}
	for _, path := range emitted {
		if path == "" {
			t.Fatal("a valid input produced no path")
		}
		if !ValidPath(path) {
			t.Errorf("ValidPath rejected an emitted path: %q", path)
		}
	}
}

func TestDiacriticNamesProduceAsciiFixturePaths(t *testing.T) {
	// Names are slugified rather than guarded, so a diacritic in a team name
	// must be folded, not passed through. "Copa Federación" reaching the URL
	// as-is would 404 the same way the team slugs do.
	got := FixturePath("Copa Federación", "Bröndby IF", "FC Bayern München", 42)
	for _, r := range got {
		if r > 127 {
			t.Fatalf("path %q contains a non-ASCII rune %q", got, r)
		}
	}
	if !strings.Contains(got, "federacion") || !strings.Contains(got, "brondby") || !strings.Contains(got, "munchen") {
		t.Fatalf("diacritics were not folded: %q", got)
	}
}

func TestNoPathEverContainsAnEmptySegment(t *testing.T) {
	// "/predictions//x-vs-y-1" resolves nowhere. The seoSlug fallback exists
	// for this, and the team and league guards refuse rather than fall back.
	candidates := []string{
		FixturePath("···", "···", "···", 1),
		FixturePath("A", "B", "C", 1),
		TeamPath("team-a"),
		LeaguePath("league-a"),
	}
	for _, path := range candidates {
		if path == "" {
			continue
		}
		if strings.Contains(path, "//") {
			t.Errorf("path %q has an empty segment", path)
		}
		if strings.HasSuffix(path, "/") {
			t.Errorf("path %q has a trailing slash the route does not serve", path)
		}
	}
}

func TestAFixtureIdIsAlwaysPresentAndPositive(t *testing.T) {
	// The route parses the trailing id. A path without one, or with a
	// nonsensical one, is a 404 that looks like a real URL.
	if got := FixturePath("L", "A", "B", -5); got != "" {
		t.Errorf("negative id produced %q", got)
	}
	if got := FixturePath("L", "A", "B", 0); got != "" {
		t.Errorf("zero id produced %q", got)
	}
	got := FixturePath("L", "A", "B", 987654321)
	if !strings.HasSuffix(got, "-987654321") {
		t.Errorf("path %q lost its fixture id", got)
	}
}

func TestLocalePrefixedPathsAreNeverEmitted(t *testing.T) {
	// The /en/ tree was half of why Google concluded the property duplicates
	// itself. Nothing in this package should be able to reintroduce it.
	for _, path := range []string{
		TeamPath("team-a"),
		LeaguePath("league-a"),
		FixturePath("L", "A", "B", 1),
	} {
		for _, locale := range []string{"/en/", "/es/", "/fr/", "/de/", "/it/", "/pt/", "/ar/"} {
			if strings.HasPrefix(path, locale) {
				t.Errorf("path %q carries a locale prefix", path)
			}
		}
	}
}

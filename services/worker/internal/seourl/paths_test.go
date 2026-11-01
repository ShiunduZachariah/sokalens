package seourl

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// slugCases are deliberately nasty: diacritics, punctuation, casing, runs of
// separators, and a name that slugifies to nothing at all.
var slugCases = []string{
	"Serie C Girone A",
	"UEFA Champions League",
	"Copa Federación",
	"Bröndby IF",
	"FC Bayern München",
	"Ñuñoa  --  Unidos",
	"1. FC Köln",
	"Real Madrid C.F.",
	"  leading and trailing  ",
	"···",
	"Ⅻ roman numeral",
	"AFC Wimbledon & Co.",
}

// The paths seo_urls stores must be the paths the site actually serves.
//
// The first version got all three wrong -- /football/teams instead of /teams,
// a missing /leagues/ segment, and fixture paths built from slug columns
// rather than slugified display names. Every one of 21,377 rows pointed at a
// 404 or a redirect. Nothing broke only because nothing read the table yet.

func TestTeamAndLeaguePathsMatchTheRoutes(t *testing.T) {
	// Verified against production: /teams/team-albania-basania answers 200
	// and /football/teams/team-aalen answers 404.
	if got := TeamPath("team-albania-basania"); got != "/teams/team-albania-basania" {
		t.Fatalf("team path = %q; /football/teams/... is a 404", got)
	}
	// And /predictions/leagues/league-world-uefa-champions-league answers 200
	// while /predictions/league-world-... does not.
	if got := LeaguePath("league-world-uefa-champions-league"); got != "/predictions/leagues/league-world-uefa-champions-league" {
		t.Fatalf("league path = %q; the /leagues/ segment is required", got)
	}
}

func TestFixturePathUsesNamesNotSlugColumns(t *testing.T) {
	// The route slugifies display names. Passing the slug column produced a
	// 308 to the real URL -- not a 404, so a status check would have called
	// it healthy while every fixture in the sitemap cost a redirect.
	got := FixturePath("Serie C Girone A", "Trento", "Pro Vercelli", 3957145)
	want := "/predictions/serie-c-girone-a/trento-vs-pro-vercelli-3957145"
	if got != want {
		t.Fatalf("fixture path =\n  %q\nwant\n  %q", got, want)
	}
}

func TestIncompleteFixtureDataYieldsNoPathAtAll(t *testing.T) {
	// Better an empty path that ValidPath rejects than a plausible-looking one
	// built from the "football" fallback, which would point at no fixture and
	// be advertised as though it did.
	for _, tc := range []struct {
		league, home, away string
		id                 int64
	}{
		{"", "A", "B", 1},
		{"L", "", "B", 1},
		{"L", "A", "", 1},
		{"L", "A", "B", 0},
		{"   ", "A", "B", 1},
	} {
		if got := FixturePath(tc.league, tc.home, tc.away, tc.id); got != "" {
			t.Fatalf("built %q from incomplete data %+v", got, tc)
		}
	}
}

func TestAPathThatSlugifiesToNothingStillHasASegment(t *testing.T) {
	// "···" has no alphanumerics. Without the fallback the path would be
	// "/predictions//x-vs-y-1", which resolves nowhere.
	got := FixturePath("···", "A", "B", 7)
	if strings.Contains(got, "//") {
		t.Fatalf("path %q has an empty segment", got)
	}
	if !strings.HasPrefix(got, "/predictions/football/") {
		t.Fatalf("path = %q, want the football fallback segment", got)
	}
}

// TestGoAndTypeScriptSlugsAgree runs the real seoSlug from the web app and
// compares it to this package's port, input by input.
//
// This is the only thing standing between the two implementations and a
// silent divergence. A Go slug that drifts from the TypeScript one does not
// error: it writes a row for a URL that does not exist, the sitemap
// advertises it, and the failure surfaces weeks later as crawl errors nobody
// attributes to a slug function.
func TestGoAndTypeScriptSlugsAgree(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not available; the cross-language slug check needs it")
	}
	repo, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	seoFile := filepath.Join(repo, "apps", "web", "app", "lib", "seo.ts")
	if _, err := os.Stat(seoFile); err != nil {
		t.Skipf("seo.ts not found at %s", seoFile)
	}

	// The function is small and pure, so the test extracts and runs it rather
	// than standing up a TypeScript toolchain. If the source no longer matches
	// this shape the extraction fails loudly instead of silently testing
	// nothing.
	source, err := os.ReadFile(seoFile)
	if err != nil {
		t.Fatalf("read seo.ts: %v", err)
	}
	start := strings.Index(string(source), "export function seoSlug")
	if start < 0 {
		t.Fatal("seoSlug is no longer declared in seo.ts; this test is not checking anything")
	}
	body := string(source)[start:]
	end := strings.Index(body, "\n}")
	if end < 0 {
		t.Fatal("could not delimit seoSlug in seo.ts")
	}
	fn := strings.Replace(body[:end+2], "export function", "function", 1)
	// Strip the TypeScript type annotation so node can parse the function as
	// plain JavaScript. Doing this by hand rather than adding a TS toolchain
	// to a Go test keeps the check cheap enough that it always runs.
	fn = strings.Replace(fn, "(value: string)", "(value)", 1)

	script := fn + "\nconst inputs = " + jsArray(slugCases) + ";\n" +
		"console.log(JSON.stringify(inputs.map(seoSlug)));\n"

	cmd := exec.Command(node, "--input-type=module", "-e", script)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run seoSlug via node: %v | stderr: %s | script: %s", err, stderr.String(), script)
	}
	var want []string
	if err := decodeJSONStrings(out, &want); err != nil {
		t.Fatalf("decode node output %q: %v", string(out), err)
	}
	if len(want) != len(slugCases) {
		t.Fatalf("node returned %d slugs for %d inputs", len(want), len(slugCases))
	}
	for i, input := range slugCases {
		if got := seoSlug(input); got != want[i] {
			t.Errorf("seoSlug(%q):\n  go = %q\n  ts = %q", input, got, want[i])
		}
	}
}

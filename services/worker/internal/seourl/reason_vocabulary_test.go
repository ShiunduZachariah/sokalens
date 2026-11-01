package seourl

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEveryReasonIsAcceptedByTheDatabase pins the Go reason vocabulary to the
// CHECK constraint that stores it.
//
// seo_urls.excluded_reason is a closed vocabulary, which is right: an operator
// reads it to answer "why is this page not in Google?", and a free-text column
// becomes decoration the first time a typo lands. The cost is that the two
// lists can drift, and drift is silent in the worst possible way.
//
// It already happened. ReasonInsufficientHistory was added in Go without being
// added to the constraint, and nothing failed -- because the gate shipped
// disabled and report-only mode rewrites a gated decision back to indexable
// with no reason, so the new value never reached a write. It surfaced only when
// the flag was turned on in QA, as the whole sweep failing on its first batch:
//
//	upsert seo urls: ERROR: new row for relation "seo_urls"
//	violates check constraint "seo_urls_excluded_reason_check"
//
// Nothing else re-derives seo_urls, so that is not one lost row: it freezes the
// crawlable set while the job retries and gives up. This test fails in CI
// instead, before a flag flip finds it in production.
func TestEveryReasonIsAcceptedByTheDatabase(t *testing.T) {
	accepted := acceptedReasons(t)

	// Every reason the package can actually write. ReasonNone is excluded: it
	// is the absence of a reason, stored as NULL, which the constraint permits
	// separately.
	written := []Reason{
		ReasonNoPrediction,
		ReasonArchived,
		ReasonNotPlayed,
		ReasonThin,
		ReasonDuplicate,
		ReasonRetired,
		ReasonInsufficientHistory,
	}

	for _, reason := range written {
		if !accepted[string(reason)] {
			t.Errorf("Decide can write excluded_reason %q, but no migration permits it; "+
				"the sweep will fail its whole batch with a check constraint violation. "+
				"Add it to seo_urls_excluded_reason_check in a new migration.", reason)
		}
	}
}

// acceptedReasons reads the vocabulary from the latest migration that defines
// seo_urls_excluded_reason_check.
//
// Reading the migrations rather than querying a database on purpose: this has
// to fail in a unit test run, on a machine with no PostgreSQL, or it is not a
// guard that runs often enough to catch the drift it exists for.
func acceptedReasons(t *testing.T) map[string]bool {
	t.Helper()

	dir := filepath.Join("..", "..", "..", "..", "infra", "db", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations dir %s: %v", dir, err)
	}

	// Sorted by name, so the last up-migration mentioning the constraint wins.
	// That is what the database ends up with.
	var latest string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read migration %s: %v", name, err)
		}
		if strings.Contains(string(body), "excluded_reason IS NULL OR excluded_reason IN") {
			latest = string(body)
		}
	}
	if latest == "" {
		t.Fatal("no migration defines the excluded_reason vocabulary; this test cannot guard anything")
	}

	block := regexp.MustCompile(`excluded_reason IS NULL OR excluded_reason IN \(([^)]*)\)`).FindStringSubmatch(latest)
	if block == nil {
		t.Fatal("found the constraint but could not parse its value list")
	}

	accepted := map[string]bool{}
	for _, value := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(block[1], -1) {
		accepted[value[1]] = true
	}
	if len(accepted) == 0 {
		t.Fatal("parsed an empty vocabulary; the regex no longer matches the migration")
	}
	return accepted
}

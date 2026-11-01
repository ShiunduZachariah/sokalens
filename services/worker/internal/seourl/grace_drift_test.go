package seourl

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// unresolvedGrace is a copy of fixturepolicy.UnresolvedGrace in services/api,
// which cannot be imported: different Go module, and the original is under
// internal/. A copy with no check is a copy that drifts.
//
// The consequence of drift is not subtle. This window decides how long an
// unresolved earlier fixture withholds a prediction. If the sitemap prices it
// shorter than the API does, the sitemap advertises fixtures whose pages
// render noindex -- which failed the deploy on 2026-09-22 and is the reason
// this mirroring exists at all.
func TestUnresolvedGraceMatchesTheAPI(t *testing.T) {
	source := filepath.Join("..", "..", "..", "api", "internal", "fixturepolicy", "sql.go")
	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("cannot read the API's fixture policy at %s: %v", source, err)
	}

	match := regexp.MustCompile(`UnresolvedGrace\s*=\s*(\d+)\s*\*\s*time\.Hour`).FindSubmatch(body)
	if match == nil {
		t.Fatal("could not find UnresolvedGrace in the API's fixture policy; " +
			"if it was renamed or restructured, this mirror needs revisiting")
	}
	hours, err := strconv.Atoi(string(match[1]))
	if err != nil {
		t.Fatalf("UnresolvedGrace is not a plain hour count: %v", err)
	}

	if want := time.Duration(hours) * time.Hour; unresolvedGrace != want {
		t.Fatalf("seourl prices the unresolved-fixture window at %s, the API at %s -- "+
			"the sitemap will advertise pages that render noindex", unresolvedGrace, want)
	}
}

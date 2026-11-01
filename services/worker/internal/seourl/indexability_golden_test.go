package seourl

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// goldenPath is the contract the web page is tested against.
//
// Deliberately outside this module, in packages/contracts, because it is not a
// fixture for this package's own tests: it is a published artifact that another
// runtime consumes. Keeping it next to openapi.yaml says what it is.
var goldenPath = filepath.Join("..", "..", "..", "..", "packages", "contracts", "seo-indexability.golden.json")

type goldenFile struct {
	Comment []string      `json:"$comment"`
	Now     string        `json:"now"`
	Cases   []goldenCase  `json:"cases"`
}

type goldenCase struct {
	Name    string        `json:"name"`
	Fixture goldenFixture `json:"fixture"`
	GoOnly  bool          `json:"goOnly,omitempty"`
	// Emitted by Decide. Pointers so a missing value is distinguishable from
	// false/"" -- a golden file that silently read as "not indexable, no reason"
	// would pass against a broken generator.
	Indexable *bool   `json:"indexable"`
	Reason    *string `json:"reason"`
}

type goldenFixture struct {
	Status        string `json:"status"`
	KickoffAt     string `json:"kickoffAt"`
	HasPrediction bool   `json:"hasPrediction"`
	HomeHistory   int    `json:"homeHistory"`
	AwayHistory   int    `json:"awayHistory"`
	DuplicateOf   int64  `json:"duplicateOf,omitempty"`
}

// entity builds the Decide input for one case.
func (c goldenCase) entity(t *testing.T) Entity {
	t.Helper()
	kickoff, err := time.Parse(time.RFC3339, c.Fixture.KickoffAt)
	if err != nil {
		t.Fatalf("case %q: bad kickoffAt: %v", c.Name, err)
	}
	return Entity{
		Type:          TypeFixture,
		ID:            1,
		Status:        c.Fixture.Status,
		KickoffAt:     kickoff.UTC(),
		HasPrediction: c.Fixture.HasPrediction,
		HomeHistory:   c.Fixture.HomeHistory,
		AwayHistory:   c.Fixture.AwayHistory,
		HistoryKnown:  true,
		DuplicateOf:   c.Fixture.DuplicateOf,
	}
}

// TestIndexabilityGolden makes Go the source of truth for indexability.
//
// The fixture page decides its own robots tag and the sweep decides what the
// sitemap advertises. They must agree, they cannot call each other, and on
// 2026-09-28 they did not: 136 URLs were advertised that the page served as
// noindex. Pinning the shared CONSTANTS was not enough, because the divergence
// was in the DECISION.
//
// So the decision is published. This test regenerates the golden file from
// Decide and fails if the committed copy has drifted, which means a change to
// Decide is not mergeable until the file is regenerated -- and regenerating it
// is what fails the TypeScript test until the page is brought back into line.
//
// Run with SEO_GOLDEN_UPDATE=1 to accept the new output.
func TestIndexabilityGolden(t *testing.T) {
	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden goldenFile
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	if len(golden.Cases) == 0 {
		t.Fatal("golden file has no cases; it cannot guard anything")
	}
	now, err := time.Parse(time.RFC3339, golden.Now)
	if err != nil {
		t.Fatalf("bad now: %v", err)
	}

	// Recompute every expectation from the authority.
	updated := golden
	updated.Cases = make([]goldenCase, len(golden.Cases))
	for i, c := range golden.Cases {
		decision := Decide(c.entity(t), now.UTC())
		indexable := decision.Indexable
		reason := string(decision.Reason)
		c.Indexable = &indexable
		c.Reason = &reason
		updated.Cases[i] = c
	}

	want, err := json.MarshalIndent(updated, "", "  ")
	if err != nil {
		t.Fatalf("encode golden: %v", err)
	}
	want = append(want, '\n')

	if os.Getenv("SEO_GOLDEN_UPDATE") == "1" {
		if err := os.WriteFile(goldenPath, want, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Log("golden regenerated from Decide; run the web tests to see whether the page still agrees")
		return
	}

	if !bytes.Equal(bytes.TrimSpace(raw), bytes.TrimSpace(want)) {
		// Report the first differing case by name rather than a whole-file diff:
		// the useful question is which decision moved.
		for i, c := range golden.Cases {
			got := updated.Cases[i]
			if c.Indexable == nil || c.Reason == nil ||
				*c.Indexable != *got.Indexable || *c.Reason != *got.Reason {
				t.Errorf("case %q: golden says indexable=%v reason=%q, Decide now says indexable=%v reason=%q",
					c.Name, deref(c.Indexable), derefStr(c.Reason), *got.Indexable, *got.Reason)
			}
		}
		t.Fatalf("%s is stale. Regenerate with:\n\n"+
			"    SEO_GOLDEN_UPDATE=1 go test ./internal/seourl/ -run TestIndexabilityGolden\n\n"+
			"then run the web tests -- the page is held to this file and will fail until it agrees.",
			goldenPath)
	}
}

func deref(b *bool) any {
	if b == nil {
		return "<unset>"
	}
	return *b
}

func derefStr(s *string) string {
	if s == nil {
		return "<unset>"
	}
	return *s
}

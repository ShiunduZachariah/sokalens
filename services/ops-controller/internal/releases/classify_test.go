package releases

import (
	"strings"
	"testing"
)

// TestUnlabelledPullRequestIsBatched is the default, and the default is the
// whole point. Nothing was said about this change, so it waits for the train.
func TestUnlabelledPullRequestIsBatched(t *testing.T) {
	got := Classify(PullRequest{Number: 1, Title: "Tidy the fixture table renderer"})
	if got.Class != ClassBatched {
		t.Fatalf("unlabelled PR classified %s, want %s", got.Class, ClassBatched)
	}
	if got.Rule != RuleDefault {
		t.Fatalf("rule = %s, want %s", got.Rule, RuleDefault)
	}
	if got.Sensitive {
		t.Fatal("an unlabelled PR touching nothing sensitive must not be flagged sensitive")
	}
}

// TestImmediateRequiresAnExplicitLabel covers every label that may elect
// IMMEDIATE. Each one is a human claim about urgency.
func TestImmediateRequiresAnExplicitLabel(t *testing.T) {
	for label := range immediateLabels {
		t.Run(label, func(t *testing.T) {
			got := Classify(PullRequest{Number: 2, Labels: []string{label}})
			if got.Class != ClassImmediate {
				t.Fatalf("label %q classified %s, want %s", label, got.Class, ClassImmediate)
			}
			if got.Rule != RuleLabel {
				t.Fatalf("label %q decided by rule %s, want %s", label, got.Rule, RuleLabel)
			}
			if !got.Immediate() {
				t.Fatal("Immediate() disagrees with Class")
			}
		})
	}
}

func TestBatchedLabelsStateTheDefault(t *testing.T) {
	for label := range batchedLabels {
		t.Run(label, func(t *testing.T) {
			got := Classify(PullRequest{Number: 3, Labels: []string{label}})
			if got.Class != ClassBatched {
				t.Fatalf("label %q classified %s, want %s", label, got.Class, ClassBatched)
			}
			if got.Rule != RuleLabel {
				t.Fatalf("rule = %s, want %s", got.Rule, RuleLabel)
			}
		})
	}
}

// TestPathsAloneNeverElectImmediate is the executable form of the asymmetry
// comment in classify.go. Every one of these PRs touches an area the plan
// calls out as IMMEDIATE-eligible, and none of them carries a label. Every one
// of them must be BATCHED.
//
// If this test is ever "fixed" by making it expect IMMEDIATE, the release
// train is finished: path fragments like "auth" and "middleware" match most of
// the codebase, so path-elected IMMEDIATE means everything is immediate and
// the train is permanently empty.
func TestPathsAloneNeverElectImmediate(t *testing.T) {
	cases := map[string][]string{
		"auth":                {"services/api/internal/auth/token.go"},
		"sessions":            {"services/api/internal/session/store.go"},
		"middleware":          {"services/api/internal/middleware/ratelimit.go"},
		"RBAC":                {"services/api/internal/rbac/roles.go"},
		"admin authorization": {"services/admin/authz/policy.go"},
		"prediction":          {"services/prediction-engine/predictor/model.go"},
		"freshness":           {"services/prediction-engine/freshness/check.go"},
		"live collection":     {"services/collector/live/poller.go"},
		"data integrity":      {"infra/db/migrations/000058_add_index.up.sql"},
		"everything at once": {
			"services/api/internal/auth/token.go",
			"services/prediction-engine/predictor/model.go",
			"services/collector/live/poller.go",
		},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			got := Classify(PullRequest{Number: 4, Files: files})
			if got.Class != ClassBatched {
				t.Fatalf("path %v elected %s; only an explicit label may elect IMMEDIATE", files, got.Class)
			}
			if got.Rule != RulePath {
				t.Fatalf("rule = %s, want %s", got.Rule, RulePath)
			}
			if !got.Sensitive {
				t.Fatal("a sensitive path must still be flagged for review")
			}
			if len(got.Areas) == 0 {
				t.Fatal("no areas reported for a sensitive path")
			}
			if !strings.Contains(got.Reason, "no IMMEDIATE label") {
				t.Fatalf("reason does not explain the decision: %q", got.Reason)
			}
		})
	}
}

// TestSensitivePathWithImmediateLabelIsImmediate is the intended route for an
// urgent security fix: the path flags it, and a human labels it.
func TestSensitivePathWithImmediateLabelIsImmediate(t *testing.T) {
	got := Classify(PullRequest{
		Number: 5,
		Labels: []string{"security"},
		Files:  []string{"services/api/internal/auth/token.go"},
	})
	if got.Class != ClassImmediate {
		t.Fatalf("labelled security fix classified %s, want %s", got.Class, ClassImmediate)
	}
	if got.Reason != "security fix" {
		t.Fatalf("reason = %q", got.Reason)
	}
}

// TestConflictingLabelsResolveToImmediate: labelling a change both urgent and
// routine is a human contradiction. Believing the urgent claim costs one extra
// deploy; disbelieving it costs an outage.
func TestConflictingLabelsResolveToImmediate(t *testing.T) {
	got := Classify(PullRequest{Number: 6, Labels: []string{"refactor", "p0"}})
	if got.Class != ClassImmediate {
		t.Fatalf("conflict resolved to %s, want %s", got.Class, ClassImmediate)
	}
	if !strings.Contains(got.Reason, "urgency wins") {
		t.Fatalf("reason does not record the conflict: %q", got.Reason)
	}
}

func TestLabelNormalization(t *testing.T) {
	cases := []struct {
		label string
		want  Class
	}{
		{"Deploy/Immediate", ClassImmediate},
		{"deploy:immediate", ClassImmediate},
		{"DEPLOY IMMEDIATE", ClassImmediate},
		{"data_integrity", ClassImmediate},
		{"  P0  ", ClassImmediate},
		{"Deploy/Batched", ClassBatched},
		{"SEO", ClassBatched},
		{"unrelated-label", ClassBatched},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			if got := Classify(PullRequest{Number: 7, Labels: []string{tc.label}}); got.Class != tc.want {
				t.Fatalf("Classify(%q) = %s, want %s", tc.label, got.Class, tc.want)
			}
		})
	}
}

// TestBatchedLabelStillFlagsASensitivePath: an explicitly batched change that
// touches auth is still worth a second look, it just is not urgent.
func TestBatchedLabelStillFlagsASensitivePath(t *testing.T) {
	got := Classify(PullRequest{
		Number: 8,
		Labels: []string{"refactor"},
		Files:  []string{"services/api/internal/middleware/logging.go"},
	})
	if got.Class != ClassBatched || got.Rule != RuleLabel {
		t.Fatalf("got %s via %s, want BATCHED via label", got.Class, got.Rule)
	}
	if !got.Sensitive {
		t.Fatal("sensitive path not flagged on an explicitly batched PR")
	}
}

func TestSensitiveAreasAreDedupedAndSorted(t *testing.T) {
	areas := sensitiveAreas([]string{
		"services/api/internal/auth/a.go",
		"services/api/internal/auth/b.go",
		"services/collector/ingest/c.go",
	})
	if len(areas) != 2 || areas[0] != "auth" || areas[1] != "live data collection" {
		t.Fatalf("areas = %v, want [auth, live data collection]", areas)
	}
}

func TestNoFilesYieldsNoAreas(t *testing.T) {
	if areas := sensitiveAreas(nil); areas != nil {
		t.Fatalf("areas = %v, want nil", areas)
	}
	if areas := sensitiveAreas([]string{"", "   "}); areas != nil {
		t.Fatalf("areas = %v, want nil", areas)
	}
}

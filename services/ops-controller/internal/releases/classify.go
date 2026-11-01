// Package releases implements the batched release train (plan phase T4).
//
// Two concerns live here and they are deliberately separate:
//
//   - classify.go decides, at merge, whether a pull request deploys on its own
//     (IMMEDIATE) or waits for the train (BATCHED).
//   - train.go is the state machine over ops_release_trains that accumulates
//     BATCHED work and reports when it is due.
//
// Nothing in this package deploys anything. It records train state and answers
// questions about it; the actual deploy remains the existing CI path. There is
// no exec, no command string, no deploy script invocation anywhere in this
// package, and adding one would move production control into a chat bot.
package releases

import (
	"sort"
	"strings"
)

// Class is the deployment classification stored in ops_deployments.deploy_class.
// The column's CHECK constrains it to exactly these two values, plus NULL for
// pre-bridge rows, which mean "unclassified", never "misclassified".
type Class string

const (
	// ClassImmediate deploys on merge and bypasses the train.
	ClassImmediate Class = "IMMEDIATE"
	// ClassBatched holds for the window. This is the default.
	ClassBatched Class = "BATCHED"
)

// ---------------------------------------------------------------------------
// The asymmetry. Read this before changing anything below.
//
// BATCHED is the default and IMMEDIATE requires an explicit label. The rule is
// not symmetric and must never be made symmetric.
//
// The failure mode of the symmetric design is well understood and it is not
// hypothetical: if paths alone could elect IMMEDIATE, then within about a
// month every change would qualify. "auth" appears in middleware that every
// request passes through; "model" appears in half the prediction tree;
// "collector" appears in anything that touches ingestion. Path rules are broad
// by construction, so a path rule that can *decide* IMMEDIATE decides it for
// nearly everything, the train empties, and the batching discipline that the
// train exists to enforce quietly stops meaning anything. Nobody notices,
// because nothing fails: the trains just get smaller until they are all of
// size one.
//
// So the two tiers do different jobs:
//
//	Tier 1  labels  — authoritative. Only an explicit IMMEDIATE label
//	                  produces ClassImmediate. A human typed it, and that is
//	                  the whole point: urgency is a claim someone makes, not a
//	                  property a regex infers.
//	Tier 2  paths   — advisory fallback, and only when no label decided. A
//	                  sensitive path sets Sensitive/Reason so the classification
//	                  says "this touches auth" out loud. It stays BATCHED.
//
// TestPathsAloneNeverElectImmediate in classify_test.go is the executable form
// of this paragraph. If you find yourself editing that test to pass, you are
// about to delete the release train.
// ---------------------------------------------------------------------------

// PullRequest is the merge-time input to classification. It is a value type
// with no repository behaviour: whoever owns the GitHub client fills it in.
type PullRequest struct {
	Number int
	Title  string
	// Labels are the PR's labels as GitHub reports them. Matching is
	// case-insensitive and tolerant of `deploy/immediate` vs `deploy:immediate`
	// vs `Deploy Immediate`.
	Labels []string
	// Files are the changed paths, used only by the advisory path fallback.
	Files []string
	// HeadSHA and BaseSHA bound the change; the train records the range.
	HeadSHA string
	BaseSHA string
	Author  string
}

// Rule names which tier decided the classification. It is stored in the
// classification, not recomputed, so an operator reading a train can tell
// "somebody labelled this" apart from "nothing said otherwise".
type Rule string

const (
	// RuleLabel: an explicit label decided it (either direction).
	RuleLabel Rule = "label"
	// RulePath: no label decided, and a path rule matched. The class is still
	// BATCHED; the path only supplies the reason and the Sensitive flag.
	RulePath Rule = "path"
	// RuleDefault: nothing matched. BATCHED, because that is the default.
	RuleDefault Rule = "default"
)

// Classification is the decision plus its provenance.
type Classification struct {
	Class Class
	Rule  Rule
	// Reason is one short human sentence for the audit row and the /deploys
	// line. It never invents urgency it did not find.
	Reason string
	// Sensitive reports that the change touches a high-risk area (auth,
	// prediction correctness, live data collection) without an explicit
	// classification label. It is a review signal, NOT an escalation: the
	// class stays BATCHED. Escalating on this flag is the drift described
	// above.
	Sensitive bool
	// Areas are the matched sensitive areas, sorted, for the reason text.
	Areas []string
}

// Immediate reports whether this classification bypasses the train.
func (c Classification) Immediate() bool { return c.Class == ClassImmediate }

// immediateLabels are the only inputs that can produce ClassImmediate. The set
// is intentionally small and intentionally explicit. Every entry maps to one
// of the five IMMEDIATE categories in the plan.
var immediateLabels = map[string]string{
	// Explicit override, no category claim.
	"deploy:immediate": "labelled deploy:immediate",
	"immediate":        "labelled immediate",
	"hotfix":           "labelled hotfix",

	// Security fixes in auth, sessions, middleware, RBAC, admin authorization.
	"security":     "security fix",
	"security-fix": "security fix",

	// Causing or about to cause a P0/P1.
	"p0":       "P0",
	"p1":       "P1",
	"sev0":     "P0",
	"sev1":     "P1",
	"incident": "incident mitigation",

	// Prediction correctness or freshness.
	"prediction-correctness": "prediction correctness",
	"prediction-freshness":   "prediction freshness",

	// Data integrity, including live data collection.
	"data-integrity":  "data integrity",
	"data-collection": "live data collection",

	// Active user-facing breakage.
	"user-facing-breakage": "user-facing breakage",
	"breakage":             "user-facing breakage",
}

// batchedLabels state the default out loud. They exist so a PR can be marked
// "definitely not urgent" and so the reason line can name the category, not so
// that anything can be *forced* batched: batched is where everything already
// lands.
var batchedLabels = map[string]string{
	"deploy:batched": "labelled deploy:batched",
	"batched":        "labelled deploy:batched",
	"feature":        "feature",
	"enhancement":    "feature",
	"refactor":       "refactor",
	"seo":            "SEO",
	"docs":           "documentation",
	"documentation":  "documentation",
	"dependencies":   "dependencies",
	"deps":           "dependencies",
	"performance":    "performance",
	"perf":           "performance",
	"chore":          "chore",
}

// pathRule maps a path fragment to the sensitive area it belongs to. These are
// substring matches on the lowercased path, which is exactly why they may not
// decide IMMEDIATE: "auth" matches half the middleware tree.
type pathRule struct {
	fragment string
	area     string
}

var sensitivePaths = []pathRule{
	{"auth", "auth"},
	{"session", "sessions"},
	{"middleware", "middleware"},
	{"rbac", "RBAC"},
	{"authz", "admin authorization"},
	{"permission", "admin authorization"},
	{"admin/", "admin authorization"},
	{"/admin", "admin authorization"},

	{"prediction", "prediction correctness"},
	{"predictor", "prediction correctness"},
	{"forecast", "prediction correctness"},
	{"freshness", "prediction freshness"},

	{"collector", "live data collection"},
	{"collection", "live data collection"},
	{"ingest", "live data collection"},
	{"scraper", "live data collection"},
	{"migrations/", "data integrity"},
	{"schema", "data integrity"},
}

// normalizeLabel folds the spellings a human or a GitHub label picker will
// actually produce onto one key: case, separators, and the deploy/ vs deploy:
// prefix split.
func normalizeLabel(label string) string {
	s := strings.ToLower(strings.TrimSpace(label))
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "/", ":")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-:")
}

// labelKeys returns every spelling one label could match.
//
// A space and a slash are the same separator to a human ("deploy immediate",
// "Deploy/Immediate") but normalizeLabel folds them differently: space becomes
// "-", slash becomes ":". Some map keys use one and some the other
// ("deploy:immediate" but "security-fix"), so a single normalized form misses
// half the spellings. Getting this wrong is not cosmetic — an explicit
// "DEPLOY IMMEDIATE" silently fell through to BATCHED, which is the exact
// failure the classifier exists to prevent.
func labelKeys(raw string) []string {
	base := normalizeLabel(raw)
	if base == "" {
		return nil
	}
	keys := []string{base}
	// Swap the first separator for the other spelling, if there is one.
	if i := strings.IndexAny(base, "-:"); i >= 0 {
		alt := []byte(base)
		if alt[i] == '-' {
			alt[i] = ':'
		} else {
			alt[i] = '-'
		}
		if variant := string(alt); variant != base {
			keys = append(keys, variant)
		}
	}
	return keys
}

// Classify assigns a deploy class to a merged pull request.
//
// Order of decision:
//
//  1. An explicit IMMEDIATE label wins. It beats a BATCHED label on the same
//     PR: labelling something both urgent and routine is a human contradiction,
//     and the safe reading of a contradiction about urgency is to believe the
//     urgent claim, which at worst costs one extra deploy.
//  2. An explicit BATCHED label states the default.
//  3. Otherwise BATCHED, with the path fallback supplying the reason and the
//     Sensitive flag. The path fallback cannot change the class.
func Classify(pr PullRequest) Classification {
	var immediateReason, batchedReason string
	for _, raw := range pr.Labels {
		for _, key := range labelKeys(raw) {
			if reason, ok := immediateLabels[key]; ok && immediateReason == "" {
				immediateReason = reason
			}
			if reason, ok := batchedLabels[key]; ok && batchedReason == "" {
				batchedReason = reason
			}
		}
	}

	areas := sensitiveAreas(pr.Files)

	if immediateReason != "" {
		reason := immediateReason
		if batchedReason != "" {
			reason += " (also labelled " + batchedReason + "; urgency wins)"
		}
		return Classification{
			Class:  ClassImmediate,
			Rule:   RuleLabel,
			Reason: reason,
			// Sensitive is a "needs a label" signal. It has already got one.
			Areas: areas,
		}
	}

	if batchedReason != "" {
		return Classification{
			Class:     ClassBatched,
			Rule:      RuleLabel,
			Reason:    batchedReason,
			Sensitive: len(areas) > 0,
			Areas:     areas,
		}
	}

	if len(areas) > 0 {
		return Classification{
			Class:     ClassBatched,
			Rule:      RulePath,
			Reason:    "touches " + strings.Join(areas, ", ") + "; batched because no IMMEDIATE label was applied",
			Sensitive: true,
			Areas:     areas,
		}
	}

	return Classification{
		Class:  ClassBatched,
		Rule:   RuleDefault,
		Reason: "no classification label; batched by default",
	}
}

// sensitiveAreas returns the distinct, sorted sensitive areas the changed
// paths touch.
func sensitiveAreas(files []string) []string {
	if len(files) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	for _, file := range files {
		path := strings.ToLower(strings.TrimSpace(file))
		if path == "" {
			continue
		}
		for _, rule := range sensitivePaths {
			if strings.Contains(path, rule.fragment) {
				seen[rule.area] = struct{}{}
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	areas := make([]string, 0, len(seen))
	for area := range seen {
		areas = append(areas, area)
	}
	sort.Strings(areas)
	return areas
}

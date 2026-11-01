package predictions

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every prediction job key must be BOTH scheduled and handled.
//
// WHY THIS TEST EXISTS. The same defect has now landed three times in this
// workstream, and it is the only one that cannot fail loudly:
//
//   - `refresh-trackers` was written, tested, and never scheduled, so
//     prediction_tracker_state stayed empty and tracker_state read as dead
//     rather than absent-until-judged;
//   - `resettle-retracted` and `recount-reversed` were written, tested, and
//     never invoked at all, so a goal withdrawn by VAR left its settled result
//     standing and its accuracy figures counting it.
//
// In every case the unit tests passed, because each piece was correct in
// isolation and nobody owned the seam. A missing wire is not a late job or a
// failing job: it is a job that does not exist, and there is nothing to alert
// on. The only guard is asserting the wire.
//
// It reads the SOURCE rather than importing the planner or main. Importing
// main is impossible and importing the planner from here would be a cycle;
// more usefully, this way the test names the file and symbol a reader has to
// go and edit, which is the whole point when it fails.
func TestEveryPredictionJobKeyIsScheduledAndHandled(t *testing.T) {
	keys := declaredJobKeys(t)
	if len(keys) < 6 {
		t.Fatalf("found %d job keys in jobs.go, which is fewer than expected: %v", len(keys), keys)
	}

	planner := readRepoFile(t, "services/worker/internal/scheduler/planner.go")
	main := readRepoFile(t, "services/worker/cmd/worker/main.go")

	for _, key := range keys {
		reference := "predictions." + key
		if !strings.Contains(planner, reference) {
			t.Errorf(
				"%s is never scheduled: add a planner entry in "+
					"services/worker/internal/scheduler/planner.go, or the job will "+
					"simply never run and nothing will report it",
				reference,
			)
		}
		if !strings.Contains(main, reference+":") {
			t.Errorf(
				"%s has no handler: register one in the predictionProcessor "+
					"handler map in services/worker/cmd/worker/main.go, or every "+
					"scheduled copy will fail as an unknown job key",
				reference,
			)
		}
	}
}

// declaredJobKeys returns the *JobKey constants this package declares.
//
// Parsed from the AST rather than matched with a regex so that a key added in
// any shape -- a new const block, a grouped declaration, a differently
// formatted line -- is still found. A guard against forgetting a wire is
// worthless if it can be bypassed by forgetting it in an unusual way.
func declaredJobKeys(t *testing.T) []string {
	t.Helper()
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, "jobs.go", nil, 0)
	if err != nil {
		t.Fatalf("parse jobs.go: %v", err)
	}

	keys := []string{}
	for _, declaration := range parsed.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, spec := range general.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range value.Names {
				if strings.HasSuffix(name.Name, "JobKey") {
					keys = append(keys, name.Name)
				}
			}
		}
	}
	return keys
}

func readRepoFile(t *testing.T, relative string) string {
	t.Helper()
	// This package sits at services/worker/internal/predictions.
	path := filepath.Join("..", "..", "..", "..", relative)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", relative, err)
	}
	return string(content)
}

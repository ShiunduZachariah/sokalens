// Package dbtest holds the one decision every PostgreSQL-gated worker test has
// to make: what to do when no database is configured.
//
// Each of those tests used to make it alone, and all of them made it the same
// way -- t.Skip. That is right on a laptop and wrong in CI, and because
// .github/workflows/ci.yml set none of the DSN variables, every one of them
// skipped on every run from the day it was written. The suite looked green
// while the only coverage of the partition maintainer, the reaper's
// compare-and-set and the in-play statistics writer never executed; one of
// those tests had meanwhile rotted and nothing said so.
//
// So the decision lives here instead, and the CI job sets
// WORKER_REQUIRE_DB_TESTS=1. The point is not this one fix: a gated test added
// next month calls URLOrSkip and is covered by CI without anyone remembering
// to wire it up.
package dbtest

import (
	"os"
	"testing"
)

// RequireEnv names the switch that turns a skip into a failure. It is one
// variable for the whole service because CI runs the worker's suite as one
// job: the job either has a database or it does not.
const RequireEnv = "WORKER_REQUIRE_DB_TESTS"

// URLOrSkip returns the DSN in the named environment variable.
//
// With nothing set it skips, so `go test ./...` still works on a machine with
// no PostgreSQL. With WORKER_REQUIRE_DB_TESTS set -- as CI sets it -- it fails
// instead: a skipped database test is not a passing one, and an unreachable or
// unmigrated database has to fail the job rather than quietly shrink the suite.
func URLOrSkip(t *testing.T, envVar, reason string) string {
	t.Helper()
	if url := os.Getenv(envVar); url != "" {
		return url
	}
	message := "set " + envVar + " to " + reason
	if os.Getenv(RequireEnv) != "" {
		t.Fatal(message + " (" + RequireEnv + " is set, so a skip is a failure)")
	}
	t.Skip(message)
	return ""
}

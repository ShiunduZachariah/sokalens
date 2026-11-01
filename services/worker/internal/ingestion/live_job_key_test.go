package ingestion

import "testing"

// The API's /healthz reports how long ago the bulk live poll last completed,
// and it finds that row by job key. It cannot import this package -- internal
// to another module -- so it pins the same literal in its own test.
//
// This is the other half of that pin. If the generated key changes here, the
// health endpoint would silently start reporting "never observed" for a poll
// that is running fine, which is worse than a missing feature: it would send an
// operator looking for an outage that is not happening.
func TestLiveFixturesJobKeyIsStableForTheHealthEndpoint(t *testing.T) {
	const pinnedInOpsHealth = "provider.api-football.live"
	if got := LiveFixturesJobKey(); got != pinnedInOpsHealth {
		t.Fatalf("LiveFixturesJobKey() = %q; services/api/internal/opshealth pins %q", got, pinnedInOpsHealth)
	}
}

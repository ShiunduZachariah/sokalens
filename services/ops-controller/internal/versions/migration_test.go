package versions

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMigrationEnforcesImmutableScopedDigestPinnedRegistry(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test path")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "..", "..", "infra", "db", "migrations", "000042_component_version_registry.up.sql"))
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(contents))
	for _, required := range []string{
		"ops_component_versions", "ops_component_version_heads", "ops_component_version_transitions",
		"current_version_id", "previous_version_id", "last_known_good_version_id", "candidate_version_id",
		"source_sha", "image_digest", "manifest_digest", "retain_until", "retention_reason",
		"expected_revision", "resulting_revision = expected_revision + 1",
		"before update or delete on ops_component_versions",
		"before update or delete on ops_component_version_transitions",
		"before delete on ops_component_version_heads",
		"deferrable initially deferred", "component version head mutation requires fresh matching transition evidence",
		"on delete restrict", "latest", "set_candidate", "rollback_to_lkg",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing contract %q", required)
		}
	}
}

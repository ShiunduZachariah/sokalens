package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validRelease() Release {
	return Release{
		SchemaVersion:       CurrentSchemaVersion,
		ReleaseID:           "release-2026-08-14.1",
		GitSHA:              strings.Repeat("a", 40),
		Images:              []Image{{Name: "worker", Digest: "sha256:" + strings.Repeat("b", 64)}, {Name: "api", Digest: "sha256:" + strings.Repeat("c", 64)}},
		MigrationVersion:    "000032",
		ConfigSchemaVersion: "2026-08-14",
		BuiltAt:             time.Date(2026, time.August, 14, 8, 0, 0, 0, time.UTC),
		PreviousLKGRelease:  "release-2026-08-13.1",
	}
}

func TestCanonicalSortsImages(t *testing.T) {
	body, err := validRelease().Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(string(body), `"name": "api"`) > strings.Index(string(body), `"name": "worker"`) {
		t.Fatal("images are not sorted canonically")
	}
}

func TestSealCreatesManifestAndRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "release.json")
	hash, err := Seal(path, validRelease())
	if err != nil {
		t.Fatal(err)
	}
	if len(hash) != 64 {
		t.Fatalf("unexpected hash %q", hash)
	}
	if _, err := os.Stat(path + ".sha256"); err != nil {
		t.Fatalf("checksum was not created: %v", err)
	}
	if _, err := Seal(path, validRelease()); err == nil {
		t.Fatal("Seal allowed an immutable manifest overwrite")
	}
}

func TestValidateRejectsMutableReferences(t *testing.T) {
	release := validRelease()
	release.Images[0].Digest = "latest"
	if err := release.Validate(); err == nil {
		t.Fatal("Validate accepted a mutable image reference")
	}
}

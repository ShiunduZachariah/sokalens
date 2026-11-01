package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFileSetsMissingValuesWithoutOverridingProcessEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("DBTOOL_NEW=value\nDBTOOL_EXISTING=file-value\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	t.Setenv("DBTOOL_EXISTING", "process-value")
	_ = os.Unsetenv("DBTOOL_NEW")
	t.Cleanup(func() { _ = os.Unsetenv("DBTOOL_NEW") })

	if err := loadEnvFile(path); err != nil {
		t.Fatalf("loadEnvFile returned error: %v", err)
	}
	if got := os.Getenv("DBTOOL_NEW"); got != "value" {
		t.Fatalf("DBTOOL_NEW = %q, want value", got)
	}
	if got := os.Getenv("DBTOOL_EXISTING"); got != "process-value" {
		t.Fatalf("DBTOOL_EXISTING = %q, want process-value", got)
	}
}

func TestLoadEnvFileIgnoresMissingOptionalFile(t *testing.T) {
	if err := loadEnvFile(filepath.Join(t.TempDir(), "missing.env")); err != nil {
		t.Fatalf("loadEnvFile missing optional file returned error: %v", err)
	}
}

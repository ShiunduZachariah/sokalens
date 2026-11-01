package logging

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestNewProducesStructuredContextualLogs(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, "info", "ops-controller", "test")
	logger.Info("started", "autonomy_mode", "observe")

	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatalf("log is not JSON: %v", err)
	}
	if event["service"] != "ops-controller" || event["environment"] != "test" || event["autonomy_mode"] != "observe" {
		t.Fatalf("missing structured context: %v", event)
	}
}

func TestNewHonorsLogLevel(t *testing.T) {
	var output bytes.Buffer
	logger := New(&output, "warn", "ops-controller", "test")
	logger.Info("hidden")
	if output.Len() != 0 {
		t.Fatalf("info log was emitted at warn level: %s", output.String())
	}
}

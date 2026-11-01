package main

import (
	"os"
	"strings"
	"testing"
)

func TestRunPredictionsEntrypointUsesPythonPredictionEngineOnly(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	source := string(body)
	if strings.Contains(source, "/internal/predictionengine\"") {
		t.Fatalf("runpredictions entrypoint must not import the old Go prediction engine")
	}
	if !strings.Contains(source, "/internal/predictionenginepython\"") {
		t.Fatalf("runpredictions entrypoint must keep the Python prediction engine runner wired")
	}
}

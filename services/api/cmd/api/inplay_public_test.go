package main

import (
	"os"
	"regexp"
	"testing"
)

// Keep the real executable composition under test: exercising an opt-in
// ServerOptions in a handler test alone does not enable it in deployments.
func TestProductionCompositionEnablesPublicInPlay(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`InPlayPublicRead:\s*true\s*,`).Match(source) {
		t.Fatal("API composition must explicitly enable public in-play reads")
	}
}

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/ShiunduZachariah/sports-prediction-platform/tools/release-manifest/internal/manifest"
)

func main() {
	input := flag.String("input", "", "release manifest JSON input")
	output := flag.String("output", "", "new immutable manifest path")
	flag.Parse()
	if *input == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "-input and -output are required")
		os.Exit(2)
	}
	body, err := os.ReadFile(*input)
	if err != nil {
		fatal(err)
	}
	var release manifest.Release
	if err := json.Unmarshal(body, &release); err != nil {
		fatal(err)
	}
	hash, err := manifest.Seal(*output, release)
	if err != nil {
		fatal(err)
	}
	fmt.Println(hash)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

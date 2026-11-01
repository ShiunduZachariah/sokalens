// Command capturefixtureids captures one real `fixtures?ids=` response.
//
// WHY THIS EXISTS. `fixtures?ids=` is documented to return events, lineups,
// statistics and player statistics for up to 20 fixtures at once. If it does,
// live statistics for 200 concurrent matches cost ~3,000 requests a day instead
// of ~72,000, and corners, shots, fouls and possession become affordable in-play.
// Every part of that is built except the confirmation: no response carrying the
// embedded objects has ever been held in this repository, so
// `fixtureStatisticsBlock` is an assumption written from documentation.
//
// This takes the capture. One request, against a live fixture, written to
// `testdata/fixtures-ids-embedded.captured.json`, which the parser tests prefer
// over the synthetic file the moment it exists -- so the tests start asserting
// against evidence with no edit. If they then fail, the assumption was wrong and
// the failure says exactly how.
//
// WHY A COMMAND AND NOT A SCRIPT. Provider calls belong inside the worker (see
// AGENTS.md), and running through the real client means this inherits the quota
// accounting, rate-limit headers and request shaping every other call gets. A
// curl would bypass all of it.
//
// It will be run again: for the same confirmation after a provider change, and
// for any other endpoint whose embedded shape needs pinning. Hence a home in the
// repository rather than a throwaway.
//
//	API_FOOTBALL_KEY=... go run ./services/worker/cmd/capturefixtureids -ids 1208021-1208022
//
// Pick ids that are LIVE RIGHT NOW. A finished fixture answers with final
// statistics, which confirms the shape but not that it updates in play; a
// fixture that has not kicked off carries no statistics at all and would
// "confirm" an empty assumption.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/provider/apifootball"
)

const defaultOutput = "services/worker/internal/provider/apifootball/testdata/fixtures-ids-embedded.captured.json"

func main() {
	ids := flag.String("ids", "", "hyphen-separated fixture ids, up to 20 (e.g. 1208021-1208022)")
	out := flag.String("out", defaultOutput, "where to write the captured response")
	baseURL := flag.String("base-url", "https://v3.football.api-sports.io", "provider base URL")
	timeout := flag.Duration("timeout", 30*time.Second, "request timeout")
	flag.Parse()

	if err := run(*ids, *out, *baseURL, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "capture failed: %v\n", err)
		os.Exit(1)
	}
}

func run(ids, out, baseURL string, timeout time.Duration) error {
	ids = strings.TrimSpace(ids)
	if ids == "" {
		return fmt.Errorf("-ids is required; pass fixtures that are live right now")
	}
	if count := len(strings.Split(ids, "-")); count > apifootball.MaxCaptureIDs {
		return fmt.Errorf("%d ids exceeds the provider cap of %d", count, apifootball.MaxCaptureIDs)
	}
	apiKey := strings.TrimSpace(os.Getenv("API_FOOTBALL_KEY"))
	if apiKey == "" {
		return fmt.Errorf("API_FOOTBALL_KEY is not set; this command makes a real, metered provider call")
	}

	client := apifootball.NewClient(apifootball.Config{BaseURL: baseURL, APIKey: apiKey})
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	response, err := client.Get(ctx, "fixtures", url.Values{"ids": []string{ids}})
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}

	// Pretty-printed, because this file is read by a person deciding whether the
	// assumed shape matches. A single line of minified JSON answers the question
	// much less well.
	var pretty any
	if err := json.Unmarshal(response.Body, &pretty); err != nil {
		return fmt.Errorf("provider did not return JSON: %w", err)
	}
	encoded, err := json.MarshalIndent(pretty, "", "  ")
	if err != nil {
		return fmt.Errorf("re-encode: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(out, append(encoded, '\n'), 0o644); err != nil {
		return err
	}

	// Say straight away whether the assumption held, because that is the entire
	// question this command exists to answer.
	parsed, parseErr := apifootball.ParseFixturesWithStatistics(response.Body)
	fmt.Printf("wrote %s (%d bytes)\n", out, len(encoded))
	if parseErr != nil {
		fmt.Printf("PARSE FAILED: %v\n", parseErr)
		fmt.Println("The assumed shape is wrong. The captured file shows the real one; fix fixtureStatisticsBlock.")
		return nil
	}
	withStats := 0
	for _, fixture := range parsed {
		if len(fixture.Statistics) > 0 {
			withStats++
		}
	}
	fmt.Printf("parsed %d fixtures, %d carrying embedded statistics\n", len(parsed), withStats)
	switch {
	case len(parsed) == 0:
		fmt.Println("No fixtures returned. Check the ids are real and the season is current.")
	case withStats == 0:
		fmt.Println("CONFIRMED NOTHING: the response carried no embedded statistics.")
		fmt.Println("Either the fixtures were not live, or ids= does not embed them -- and the")
		fmt.Println("batched-statistics plan depends on which. Retry against a fixture in play")
		fmt.Println("before concluding the endpoint does not supply them.")
	default:
		fmt.Println("CONFIRMED: ids= embeds statistics. Rename nothing -- the tests prefer the")
		fmt.Println("captured file automatically. Delete the synthetic fixture and its provenance test.")
	}
	return nil
}

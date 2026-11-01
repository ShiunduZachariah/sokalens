package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	accadomain "github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/acca"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/config"
)

type stubAccaSlipSource struct {
	slip     accadomain.Slip
	err      error
	askedFor string
}

func (s *stubAccaSlipSource) Get(_ context.Context, id string) (accadomain.Slip, error) {
	s.askedFor = id
	if s.err != nil {
		return accadomain.Slip{}, s.err
	}
	return s.slip, nil
}

func sampleSlip() accadomain.Slip {
	kickoff := time.Date(2026, 10, 2, 14, 0, 0, 0, time.UTC)
	return accadomain.Slip{
		ID:               "6f1a9c3e-0b2d-4f58-9a7c-2d1e8b4f0a11",
		MarketTab:        "goals",
		Period:           "today",
		LegCount:         2,
		CombinedFairOdds: 2.4,
		RankingScore:     0.61,
		OddsBand:         "2-3",
		EarliestKickoff:  kickoff,
		LatestKickoff:    kickoff.Add(2 * time.Hour),
		Legs: []accadomain.Leg{
			{Position: 1, Source: "engine", PredictionID: "uuid-1", MatchID: 4411, Market: "total_goals", Selection: "over_1.5", FairOdds: 1.4, KickoffAt: kickoff, HomeTeam: "Brentford", AwayTeam: "Fulham", League: "Premier League", Country: "GB", HomeTeamSlug: "brentford", AwayTeamSlug: "fulham", LeagueSlug: "premier-league", Status: "won"},
			{Position: 2, Source: "engine", PredictionID: "uuid-2", MatchID: 4412, Market: "btts", Selection: "yes", FairOdds: 1.7, KickoffAt: kickoff.Add(2 * time.Hour), HomeTeam: "Leeds", AwayTeam: "Everton", League: "Premier League", Country: "GB", HomeTeamSlug: "leeds", AwayTeamSlug: "everton", LeagueSlug: "premier-league", Status: "pending"},
		},
	}
}

func accaDetailRequest(t *testing.T, path string, source AccaSlipSource) *httptest.ResponseRecorder {
	t.Helper()
	handler := New(ServerOptions{
		Config:    config.Config{Environment: "test", Port: 8080},
		AccaSlips: source,
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// The body is one slip in the list endpoint's own shape, with each leg's
// status alongside. A client that renders a list item renders this unchanged,
// which is the point of sharing the struct rather than designing the payload
// twice.
func TestAccaDetailReturnsOneSlipInTheListItemShape(t *testing.T) {
	source := &stubAccaSlipSource{slip: sampleSlip()}
	rec := accaDetailRequest(t, "/v1/accas/6f1a9c3e-0b2d-4f58-9a7c-2d1e8b4f0a11", source)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if source.askedFor != "6f1a9c3e-0b2d-4f58-9a7c-2d1e8b4f0a11" {
		t.Fatalf("source was asked for %q", source.askedFor)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	// Not wrapped in an envelope: the slip IS the body.
	for _, key := range []string{"id", "market_tab", "combined_fair_odds", "leg_count", "legs"} {
		if _, ok := body[key]; !ok {
			t.Fatalf("body is missing %q, so it is not a list-item slip: %v", key, body)
		}
	}
	legs, ok := body["legs"].([]any)
	if !ok || len(legs) != 2 {
		t.Fatalf("legs = %v, want 2", body["legs"])
	}
	first := legs[0].(map[string]any)
	if first["status"] != "won" {
		t.Fatalf("first leg status = %v, want won", first["status"])
	}
	second := legs[1].(map[string]any)
	if second["status"] != "pending" {
		t.Fatalf("second leg status = %v, want pending", second["status"])
	}
	// The identifying fields a client needs to link to the match must survive.
	if first["match_id"] == nil || first["home_team"] != "Brentford" {
		t.Fatalf("leg lost its match identity: %v", first)
	}
	// And the slugs it needs to link to the team and league pages. The MATCH
	// link needs no slug -- the clients build it from the id plus the names.
	for key, want := range map[string]any{
		"home_team_slug": "brentford",
		"away_team_slug": "fulham",
		"league_slug":    "premier-league",
	} {
		if first[key] != want {
			t.Fatalf("leg %s = %v, want %v", key, first[key], want)
		}
	}
}

// An unknown id is a 404, and it must not be reported as an outage: a typo in
// a URL that answers 503 puts a healthy service on the wrong side of every
// availability check that watches status codes.
func TestAccaDetailReturns404ForAnUnknownID(t *testing.T) {
	source := &stubAccaSlipSource{err: accadomain.ErrSlipNotFound}
	rec := accaDetailRequest(t, "/v1/accas/6f1a9c3e-0b2d-4f58-9a7c-2d1e8b4f0a11", source)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// A genuine failure stays a 503. Collapsing it into the 404 above would report
// a broken database as an empty catalogue, and the endpoint would look healthy
// while serving nothing.
func TestAccaDetailKeepsAFailureApartFromAMissingSlip(t *testing.T) {
	source := &stubAccaSlipSource{err: errors.New("connection refused")}
	rec := accaDetailRequest(t, "/v1/accas/6f1a9c3e-0b2d-4f58-9a7c-2d1e8b4f0a11", source)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
}

// The list routes must keep winning over the id prefix. `featured` is a real
// endpoint, not an id, and routing it here would break it.
func TestAccaDetailDoesNotSwallowTheListRoutes(t *testing.T) {
	source := &stubAccaSlipSource{slip: sampleSlip()}
	for _, path := range []string{"/v1/accas", "/v1/accas/featured"} {
		rec := accaDetailRequest(t, path, source)
		if source.askedFor != "" {
			t.Fatalf("%s was routed to the detail handler as id %q", path, source.askedFor)
		}
		if rec.Code == http.StatusOK {
			t.Fatalf("%s returned a slip; it must stay a list route", path)
		}
	}
}

// A deeper path is not a slip id. Answering /v1/accas/{id}/legs with the slip
// would make a route that does not exist look as though it did.
func TestAccaDetailRejectsADeeperPath(t *testing.T) {
	source := &stubAccaSlipSource{slip: sampleSlip()}
	rec := accaDetailRequest(t, "/v1/accas/6f1a9c3e-0b2d-4f58-9a7c-2d1e8b4f0a11/legs", source)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if source.askedFor != "" {
		t.Fatalf("a deeper path reached the source as id %q", source.askedFor)
	}
}

func TestAccaDetailRejectsNonGET(t *testing.T) {
	handler := New(ServerOptions{
		Config:    config.Config{Environment: "test", Port: 8080},
		AccaSlips: &stubAccaSlipSource{slip: sampleSlip()},
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/accas/6f1a9c3e-0b2d-4f58-9a7c-2d1e8b4f0a11", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

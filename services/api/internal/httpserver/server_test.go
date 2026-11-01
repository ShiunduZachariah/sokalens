package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/admin"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/emailsettings"
)

type staticChecker struct {
	name string
	err  error
}

func (s staticChecker) Name() string { return s.name }
func (s staticChecker) Check() error { return s.err }

type fakeAdminAuth struct {
	loginResult admin.LoginResult
	loginErr    error
	sessionUser admin.User
	sessionErr  error
	logoutErr   error
}

type fakeEmailProviderSettings struct {
	status emailsettings.Status
	set    string
}

func (f *fakeEmailProviderSettings) Status(context.Context) (emailsettings.Status, error) {
	return f.status, nil
}
func (f *fakeEmailProviderSettings) Set(_ context.Context, provider string, _ int64) error {
	f.set = provider
	return nil
}

func TestAdminEmailProviderCanBeReadAndUpdatedWithoutSecrets(t *testing.T) {
	source := &fakeEmailProviderSettings{status: emailsettings.Status{Provider: "cloudflare", Providers: []string{"cloudflare", "resend"}}}
	handler := New(ServerOptions{Config: config.Config{Environment: "test", Port: 8080}, AdminAuth: fakeAdminAuth{sessionUser: admin.User{ID: 7, Role: admin.RoleAdmin}}, EmailProvider: source})
	get := httptest.NewRequest(http.MethodGet, "/v1/admin/email/provider", nil)
	get.Header.Set("Authorization", "Bearer session-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, get)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"provider":"cloudflare"`) || strings.Contains(rec.Body.String(), "token") {
		t.Fatalf("GET status=%d body=%s", rec.Code, rec.Body.String())
	}
	put := httptest.NewRequest(http.MethodPut, "/v1/admin/email/provider", strings.NewReader(`{"provider":"resend"}`))
	put.Header.Set("Authorization", "Bearer session-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, put)
	if rec.Code != http.StatusOK || source.set != "resend" {
		t.Fatalf("PUT status=%d body=%s set=%q", rec.Code, rec.Body.String(), source.set)
	}
}

func (f fakeAdminAuth) Login(context.Context, string, string) (admin.LoginResult, error) {
	return f.loginResult, f.loginErr
}

func (f fakeAdminAuth) ValidateSession(context.Context, string) (admin.User, error) {
	return f.sessionUser, f.sessionErr
}

func (f fakeAdminAuth) Logout(context.Context, string) error {
	return f.logoutErr
}

type fakeProviderStatusSource struct {
	statuses []ProviderStatus
	err      error
}

func (f fakeProviderStatusSource) ListProviderStatuses(context.Context) ([]ProviderStatus, error) {
	return f.statuses, f.err
}

type fakePredictionSource struct {
	summaries         []PredictionSummary
	providerSummaries []ProviderPredictionSummary
	detail            PredictionDetail
	hitRate           PredictionHitRateReport
	results           []PredictionResultSummary
	err               error
}

type fakeMatchSource struct {
	matches []MatchSummary
	detail  MatchSummary
	err     error
	query   MatchQuery
	id      int64
}

type blockingMatchSource struct {
	delay time.Duration
}

func (s blockingMatchSource) ListMatches(context.Context, MatchQuery) ([]MatchSummary, error) {
	time.Sleep(s.delay)
	return nil, nil
}

func (s blockingMatchSource) CountMatches(context.Context, MatchQuery) (int, error) {
	time.Sleep(s.delay)
	return 0, nil
}

func (s blockingMatchSource) GetMatch(context.Context, int64) (MatchSummary, error) {
	time.Sleep(s.delay)
	return MatchSummary{}, nil
}

func (f *fakeMatchSource) ListMatches(_ context.Context, query MatchQuery) ([]MatchSummary, error) {
	f.query = query
	return f.matches, f.err
}

func (f *fakeMatchSource) CountMatches(_ context.Context, query MatchQuery) (int, error) {
	f.query = query
	return len(f.matches), f.err
}

func (f *fakeMatchSource) GetMatch(_ context.Context, id int64) (MatchSummary, error) {
	f.id = id
	return f.detail, f.err
}

func (f fakePredictionSource) ListTodayPredictions(context.Context, PredictionFilter) ([]PredictionSummary, error) {
	return f.summaries, f.err
}

func (f fakePredictionSource) CountTodayPredictions(context.Context, PredictionFilter) (int, error) {
	return len(f.summaries), f.err
}

func (f fakePredictionSource) ListTodayProviderPredictions(context.Context, PredictionFilter) ([]ProviderPredictionSummary, error) {
	return f.providerSummaries, f.err
}

func (f fakePredictionSource) CountTodayProviderPredictions(context.Context, PredictionFilter) (int, error) {
	return len(f.providerSummaries), f.err
}

func (f fakePredictionSource) GetPrediction(context.Context, int64) (PredictionDetail, error) {
	return f.detail, f.err
}

func (f fakePredictionSource) GetHitRateReport(context.Context) (PredictionHitRateReport, error) {
	return f.hitRate, f.err
}

func (f fakePredictionSource) ListPredictionResults(context.Context, PredictionFilter) ([]PredictionResultSummary, error) {
	return f.results, f.err
}

func (f fakePredictionSource) CountPredictionResults(context.Context, PredictionFilter) (int, error) {
	return len(f.results), f.err
}

func TestPredictionResultsReturnsEverySettledMarket(t *testing.T) {
	source := fakePredictionSource{results: []PredictionResultSummary{{
		PredictionID: 7, MatchID: 42, MatchTitle: "Arsenal vs Chelsea", MarketKey: "total_goals",
		Selection: "over_2.5", Actual: "over_2.5", Outcome: "won", KickoffAt: time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC),
	}}}
	handler := New(ServerOptions{Config: config.Config{Environment: "test", Port: 8080}, Predictions: source})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/predictions/results?date=2026-08-22&timezone=Africa%2FNairobi", nil))

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"outcome":"won"`) || !strings.Contains(recorder.Body.String(), `"actual":"over_2.5"`) {
		t.Fatalf("status/body = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestHealthzReturnsOK(t *testing.T) {
	handler := New(ServerOptions{Config: config.Config{Environment: "test", Port: 8080}})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); !strings.Contains(got, `"status":"ok"`) {
		t.Fatalf("body = %s, want status ok", got)
	}
}

func TestReadyzReturnsOKWhenDependenciesPass(t *testing.T) {
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		ReadinessChecks: []ReadinessChecker{
			staticChecker{name: "postgres"},
			staticChecker{name: "redis"},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); !strings.Contains(got, `"status":"ready"`) {
		t.Fatalf("body = %s, want status ready", got)
	}
}

func TestReadyzReturnsUnavailableWhenDependencyFails(t *testing.T) {
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		ReadinessChecks: []ReadinessChecker{
			staticChecker{name: "postgres", err: errDependencyUnavailable("postgres unavailable")},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := rec.Body.String(); !strings.Contains(got, `"postgres":"unavailable"`) {
		t.Fatalf("body = %s, want postgres unavailable", got)
	}
}

func TestMobileConfigReturnsFoundationFeatureFlags(t *testing.T) {
	handler := New(ServerOptions{Config: config.Config{Environment: "test", Port: 8080}})

	req := httptest.NewRequest(http.MethodGet, "/v1/mobile/config", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, fragment := range []string{
		`"public_content":true`,
		`"predictions":true`,
		`"partner_ctas":false`,
		`"analysis_watchlist":"Analysis Watchlist"`,
		`"analysis_alerts":"Analysis Alerts"`,
		`"multi_match_builder":"Multi-Match Builder"`,
		`"min_matches":2`,
		`"max_matches":6`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("body = %s, want fragment %s", body, fragment)
		}
	}
	for _, blocked := range []string{"bookmaker_ctas", "accumulator", "acca", "stake", "returns", "payout", "wager", "sportsbook", "gambling"} {
		if strings.Contains(strings.ToLower(body), blocked) {
			t.Fatalf("body = %s, want mobile config without blocked term %q", body, blocked)
		}
	}
}

func TestTodayPredictionsReturnsInternalSummaries(t *testing.T) {
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		Predictions: fakePredictionSource{
			summaries: []PredictionSummary{
				{
					ID:               11,
					MatchID:          21,
					MatchTitle:       "Gor Mahia vs AFC Leopards",
					HomeTeam:         "Gor Mahia",
					AwayTeam:         "AFC Leopards",
					LeagueName:       "Kenyan Premier League",
					KickoffAt:        time.Date(2026, 5, 1, 15, 0, 0, 0, time.UTC),
					MatchStatus:      "scheduled",
					MarketKey:        "1x2",
					Selection:        "home",
					Confidence:       61.25,
					PublishedVersion: 1,
					PublishedAt:      time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
					UpdatedAt:        time.Date(2026, 5, 1, 12, 5, 0, 0, time.UTC),
					LastEvaluatedAt:  time.Date(2026, 5, 1, 12, 4, 0, 0, time.UTC),
					Markets: []PredictionMarketOption{
						{MarketKey: "1x2", Selection: "home", Confidence: 61.25},
						{MarketKey: "total_goals", Selection: "over_2.5", Confidence: 68.5},
					},
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/predictions/today?market=1x2&minimum_confidence=60", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Cache-Control"); got != publicCanonicalCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, publicCanonicalCacheControl)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag is empty")
	}
	body := rec.Body.String()
	for _, fragment := range []string{
		`"predictions":[`,
		`"id":11`,
		`"match_id":21`,
		`"match_title":"Gor Mahia vs AFC Leopards"`,
		`"home_team":"Gor Mahia"`,
		`"away_team":"AFC Leopards"`,
		`"league_name":"Kenyan Premier League"`,
		`"kickoff_at":"2026-05-01T15:00:00Z"`,
		`"match_status":"scheduled"`,
		`"market_key":"1x2"`,
		`"selection":"home"`,
		`"confidence":61.25`,
		`"published_version":1`,
		`"updated_at":"2026-05-01T12:05:00Z"`,
		`"last_evaluated_at":"2026-05-01T12:04:00Z"`,
		`"markets":[{"confidence":61.25,"is_best_bet":false,"market_key":"1x2","probability":0,"selection":"home"},{"confidence":68.5,"is_best_bet":false,"market_key":"total_goals","probability":0,"selection":"over_2.5"}]`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("body = %s, want fragment %s", body, fragment)
		}
	}

	revalidation := httptest.NewRequest(http.MethodGet, "/v1/predictions/today?market=1x2&minimum_confidence=60", nil)
	revalidation.Header.Set("If-None-Match", etag)
	revalidated := httptest.NewRecorder()
	handler.ServeHTTP(revalidated, revalidation)
	if revalidated.Code != http.StatusNotModified || revalidated.Body.Len() != 0 {
		t.Fatalf("revalidated response = %d %q, want 304 with no body", revalidated.Code, revalidated.Body.String())
	}
}

func TestPredictionFilterParsesBoundedLimit(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/predictions/today?market=1x2&minimum_confidence=60&limit=4", nil)
	filter, err := predictionFilterFromRequest(req)
	if err != nil {
		t.Fatalf("predictionFilterFromRequest returned error: %v", err)
	}
	if filter.MarketKey != "1x2" || filter.MinimumConfidence == nil || *filter.MinimumConfidence != 60 || filter.Limit != 4 {
		t.Fatalf("filter = %+v, want market 1x2 confidence 60 limit 4", filter)
	}

	for _, limit := range []string{"0", "101", "not-a-number"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/predictions/today?limit="+limit, nil)
		if _, err := predictionFilterFromRequest(req); err == nil {
			t.Fatalf("limit %q returned nil error", limit)
		}
	}
}

func TestTodayMatchesReturnsInternalSummaries(t *testing.T) {
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		Matches: &fakeMatchSource{
			matches: []MatchSummary{
				{
					ID:                    1538375,
					LeagueName:            "Primera División",
					HomeTeam:              "Aurora",
					AwayTeam:              "Real Oruro",
					KickoffAt:             time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC),
					Status:                "finished",
					RawStatus:             "FT",
					RawStatusLabel:        "Match Finished",
					VenueName:             "Estadio Felix Capriles",
					VenueCity:             "Cochabamba",
					HomeScore:             intPtr(2),
					AwayScore:             intPtr(1),
					PredictionState:       "published",
					PredictionStateReason: "published",
					Odds: []OddsPrice{
						{Label: "Home", Value: "1.91", Trend: "flat"},
					},
					Prediction: &MatchPredictionSummary{
						ID:               7,
						MarketKey:        "1x2",
						Selection:        "home",
						Confidence:       61.25,
						PublishedVersion: 2,
						PublishedAt:      time.Date(2026, 5, 12, 18, 0, 0, 0, time.UTC),
					},
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/matches/today?date=2026-05-13&status=finished&timezone=Africa/Nairobi", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, fragment := range []string{
		`"matches":[`,
		`"id":1538375`,
		`"league_name":"Primera División"`,
		`"home_team":"Aurora"`,
		`"away_team":"Real Oruro"`,
		`"kickoff_at":"2026-05-13T00:00:00Z"`,
		`"status":"finished"`,
		`"raw_status":"FT"`,
		`"raw_status_label":"Match Finished"`,
		`"venue_name":"Estadio Felix Capriles"`,
		`"venue_city":"Cochabamba"`,
		`"prediction_state":"published"`,
		`"odds":[{"label":"Home","trend":"flat","value":"1.91"}]`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("body = %s, want fragment %s", body, fragment)
		}
	}
}

func TestTodayMatchesDoesNotExposeInternalPredictionGateReasons(t *testing.T) {
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		Matches: &fakeMatchSource{
			matches: []MatchSummary{
				{
					ID:                    29151,
					LeagueName:            "Eliteserien",
					HomeTeam:              "Fredrikstad",
					AwayTeam:              "Lillestrom",
					KickoffAt:             time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC),
					Status:                "upcoming",
					PredictionState:       "insufficient_history",
					PredictionStateReason: "insufficient_history",
					Odds: []OddsPrice{
						{Label: "Home", Value: "2.88", Trend: "flat"},
					},
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/matches/today?date=2026-07-11&timezone=Africa/Nairobi&include_unavailable=true", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if strings.Contains(body, "insufficient_history") || strings.Contains(body, "prediction_state_reason") {
		t.Fatalf("body = %s, want no internal prediction gate reason", body)
	}
}

func TestTodayMatchesReturnsUnavailableWithoutSource(t *testing.T) {
	handler := New(ServerOptions{Config: config.Config{Environment: "test", Port: 8080}})

	req := httptest.NewRequest(http.MethodGet, "/v1/matches/today", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestTodayMatchesPassesParsedQueryToSource(t *testing.T) {
	source := &fakeMatchSource{}
	handler := New(ServerOptions{
		Config:  config.Config{Environment: "test", Port: 8080},
		Matches: source,
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/matches/today?day=tomorrow&status=live&status=halftime&timezone=Africa/Nairobi&limit=30&include_unavailable=true", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if source.query.Day != "tomorrow" {
		t.Fatalf("query day = %q, want tomorrow", source.query.Day)
	}
	if source.query.Timezone != "Africa/Nairobi" {
		t.Fatalf("query timezone = %q, want Africa/Nairobi", source.query.Timezone)
	}
	if len(source.query.Statuses) != 2 || source.query.Statuses[0] != "live" || source.query.Statuses[1] != "halftime" {
		t.Fatalf("query statuses = %#v, want [live halftime]", source.query.Statuses)
	}
	if source.query.Limit != 30 {
		t.Fatalf("query limit = %d, want 30", source.query.Limit)
	}
	if !source.query.IncludeUnavailable {
		t.Fatal("query include unavailable = false, want true")
	}
}

func TestTodayMatchesRejectsInvalidTimezone(t *testing.T) {
	handler := New(ServerOptions{
		Config:  config.Config{Environment: "test", Port: 8080},
		Matches: &fakeMatchSource{},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/matches/today?timezone=Not/AZone", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestHistoricalMatchesDefaultsToFinishedMatchesForDate(t *testing.T) {
	source := &fakeMatchSource{
		matches: []MatchSummary{
			{
				ID:              1538375,
				LeagueName:      "Premier League",
				HomeTeam:        "Arsenal",
				AwayTeam:        "Chelsea",
				KickoffAt:       time.Date(2025, 5, 18, 15, 0, 0, 0, time.UTC),
				Status:          "finished",
				HomeScore:       intPtr(2),
				AwayScore:       intPtr(1),
				PredictionState: "published",
			},
		},
	}
	handler := New(ServerOptions{
		Config:  config.Config{Environment: "test", Port: 8080},
		Matches: source,
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/matches/history?date=2025-05-18&timezone=UTC&limit=50", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if source.query.Date != "2025-05-18" {
		t.Fatalf("query date = %q, want 2025-05-18", source.query.Date)
	}
	if len(source.query.Statuses) != 1 || source.query.Statuses[0] != "finished" {
		t.Fatalf("query statuses = %#v, want [finished]", source.query.Statuses)
	}
	if source.query.ResultDetailWindow != 24*time.Hour {
		t.Fatalf("result detail window = %s, want 24h", source.query.ResultDetailWindow)
	}
	if got := rec.Body.String(); !strings.Contains(got, `"matches":[`) || !strings.Contains(got, `"home_score":2`) {
		t.Fatalf("body = %s, want historical match response", got)
	}
}

func TestMatchDetailReturnsFixtureDetail(t *testing.T) {
	source := &fakeMatchSource{
		detail: MatchSummary{
			ID:              1538375,
			LeagueName:      "Premier League",
			HomeTeam:        "Arsenal",
			AwayTeam:        "Chelsea",
			KickoffAt:       time.Date(2025, 5, 18, 15, 0, 0, 0, time.UTC),
			Status:          "finished",
			HomeScore:       intPtr(2),
			AwayScore:       intPtr(1),
			PredictionState: "published",
			Prediction: &MatchPredictionSummary{
				ID:               7,
				MarketKey:        "1x2",
				Selection:        "home",
				Confidence:       63.5,
				PublishedVersion: 1,
				PublishedAt:      time.Date(2025, 5, 18, 12, 0, 0, 0, time.UTC),
				UpdatedAt:        time.Date(2025, 5, 18, 12, 5, 0, 0, time.UTC),
				LastEvaluatedAt:  time.Date(2025, 5, 18, 12, 4, 0, 0, time.UTC),
			},
		},
	}
	handler := New(ServerOptions{
		Config:  config.Config{Environment: "test", Port: 8080},
		Matches: source,
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/matches/1538375", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if source.id != 1538375 {
		t.Fatalf("source id = %d, want 1538375", source.id)
	}
	body := rec.Body.String()
	for _, fragment := range []string{
		`"id":1538375`,
		`"home_team":"Arsenal"`,
		`"away_team":"Chelsea"`,
		`"prediction":{"confidence":63.5`,
		`"updated_at":"2025-05-18T12:05:00Z"`,
		`"last_evaluated_at":"2025-05-18T12:04:00Z"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("body = %s, want fragment %s", body, fragment)
		}
	}
}

func TestPredictionDetailReturnsAuditableVersionState(t *testing.T) {
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		Predictions: fakePredictionSource{
			detail: PredictionDetail{
				PredictionSummary: PredictionSummary{
					ID:               11,
					MatchID:          21,
					MarketKey:        "1x2",
					Selection:        "home",
					Confidence:       61.25,
					PublishedVersion: 1,
					PublishedAt:      time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
					UpdatedAt:        time.Date(2026, 5, 1, 12, 5, 0, 0, time.UTC),
					LastEvaluatedAt:  time.Date(2026, 5, 1, 12, 4, 0, 0, time.UTC),
				},
				Probabilities:     map[string]float64{"home": 0.6125, "draw": 0.2225, "away": 0.165},
				InputSnapshotHash: "foundation-seed-1x2-gor-afc",
				Outcome:           "pending",
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/predictions/11", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, fragment := range []string{
		`"id":11`,
		`"probabilities":{"away":0.165,"draw":0.2225,"home":0.6125}`,
		`"input_snapshot_hash":"foundation-seed-1x2-gor-afc"`,
		`"outcome":"pending"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("body = %s, want fragment %s", body, fragment)
		}
	}
}

func TestAdminPredictionHitRateRequiresBearerToken(t *testing.T) {
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		AdminAuth: fakeAdminAuth{
			sessionUser: admin.User{ID: 7, Email: "admin@example.com", Role: admin.RoleViewer},
		},
		Predictions: fakePredictionSource{},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/predictions/hit-rate", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAdminPredictionHitRateReturnsSettledPerformance(t *testing.T) {
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		AdminAuth: fakeAdminAuth{
			sessionUser: admin.User{ID: 7, Email: "admin@example.com", Role: admin.RoleAdmin},
		},
		Predictions: fakePredictionSource{
			hitRate: PredictionHitRateReport{
				Settled:     10,
				Won:         6,
				Lost:        3,
				Void:        1,
				Denominator: 9,
				Markets: []PredictionMarketHitRate{
					{MarketKey: "1x2", Settled: 5, Won: 3, Lost: 2, Void: 0, HitRate: 60, Denominator: 5},
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/predictions/hit-rate", nil)
	req.Header.Set("Authorization", "Bearer session-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, fragment := range []string{
		`"settled":10`,
		`"won":6`,
		`"lost":3`,
		`"void":1`,
		`"hit_rate":60`,
		`"denominator":9`,
		`"denominator":5`,
		`"market_key":"1x2"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("body = %s, want fragment %s", body, fragment)
		}
	}
}

func TestAdminCatalogueCoverageReturnsReferenceCounts(t *testing.T) {
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		AdminAuth: fakeAdminAuth{
			sessionUser: admin.User{ID: 7, Email: "admin@example.com", Role: admin.RoleAdmin},
		},
		Coverage: fakeCoverageSource{
			report: CatalogueCoverage{
				Countries:      166,
				CountriesFlags: 120,
				Teams:          7990,
				TeamLogos:      926,
				Leagues:        1234,
				LeagueLogos:    84,
				SearchIndexed:  20790,
				LiveLastSeen:   time.Date(2026, 8, 18, 19, 30, 0, 0, time.UTC),
				WorkerLastSeen: time.Date(2026, 8, 18, 19, 31, 0, 0, time.UTC),
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/catalogue/coverage", nil)
	req.Header.Set("Authorization", "Bearer session-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, fragment := range []string{
		`"countries_total":166`,
		`"countries_with_flag":120`,
		`"teams_total":7990`,
		`"teams_with_logo":926`,
		`"leagues_total":1234`,
		`"leagues_with_logo":84`,
		`"search_indexed":20790`,
		`"live_last_seen":"2026-08-18T19:30:00Z"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("body = %s, want fragment %s", body, fragment)
		}
	}
}

func TestAdminCatalogueCoverageRequiresBearerToken(t *testing.T) {
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		AdminAuth: fakeAdminAuth{
			sessionUser: admin.User{ID: 7, Email: "admin@example.com", Role: admin.RoleViewer},
		},
		Coverage: fakeCoverageSource{},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/catalogue/coverage", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

type fakeCoverageSource struct {
	report CatalogueCoverage
	err    error
}

func (f fakeCoverageSource) Coverage(context.Context) (CatalogueCoverage, error) {
	return f.report, f.err
}

func TestTodayPredictionsReturnsUnavailableWithoutSource(t *testing.T) {
	handler := New(ServerOptions{Config: config.Config{Environment: "test", Port: 8080}})

	req := httptest.NewRequest(http.MethodGet, "/v1/predictions/today", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestUnknownRouteReturnsJSON404(t *testing.T) {
	handler := New(ServerOptions{Config: config.Config{Environment: "test", Port: 8080}})

	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if got := rec.Body.String(); !strings.Contains(got, `"error":"not_found"`) {
		t.Fatalf("body = %s, want not_found error", got)
	}
}

func TestOpenAPIYAMLServesConfiguredContract(t *testing.T) {
	dir := t.TempDir()
	contractPath := filepath.Join(dir, "openapi.yaml")
	if err := os.WriteFile(contractPath, []byte("openapi: 3.1.0\ninfo:\n  title: Test API\n"), 0o644); err != nil {
		t.Fatalf("write contract: %v", err)
	}
	handler := New(ServerOptions{
		Config:      config.Config{Environment: "test", Port: 8080},
		OpenAPIPath: contractPath,
	})

	req := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/yaml") {
		t.Fatalf("Content-Type = %q, want application/yaml", got)
	}
	if got := rec.Body.String(); !strings.Contains(got, "openapi: 3.1.0") {
		t.Fatalf("body = %s, want OpenAPI contract", got)
	}
}

func TestDocsServesScalarReferenceForOpenAPIContract(t *testing.T) {
	handler := New(ServerOptions{Config: config.Config{Environment: "test", Port: 8080}})

	req := httptest.NewRequest(http.MethodGet, "/docs", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "@scalar/api-reference") {
		t.Fatalf("body = %s, want Scalar API Reference script", body)
	}
	if !strings.Contains(body, "/openapi.yaml") {
		t.Fatalf("body = %s, want /openapi.yaml reference", body)
	}
}

func TestAdminLoginReturnsSessionToken(t *testing.T) {
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		AdminAuth: fakeAdminAuth{
			loginResult: admin.LoginResult{
				Token:     "session-token",
				ExpiresAt: time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC),
				User:      admin.User{ID: 7, Email: "admin@example.com", Role: admin.RoleSuperAdmin},
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/auth/login", strings.NewReader(`{"email":"admin@example.com","password":"local-admin-password"}`))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, fragment := range []string{`"token":"session-token"`, `"email":"admin@example.com"`, `"role":"super_admin"`} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("body = %s, want fragment %s", body, fragment)
		}
	}
}

func TestAdminLoginRejectsInvalidCredentials(t *testing.T) {
	handler := New(ServerOptions{
		Config:    config.Config{Environment: "test", Port: 8080},
		AdminAuth: fakeAdminAuth{loginErr: admin.ErrInvalidCredentials},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/auth/login", strings.NewReader(`{"email":"admin@example.com","password":"wrong"}`))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAdminSessionRequiresBearerToken(t *testing.T) {
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		AdminAuth: fakeAdminAuth{
			sessionUser: admin.User{ID: 7, Email: "admin@example.com", Role: admin.RoleAdmin},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/auth/session", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAdminSessionReturnsCurrentUser(t *testing.T) {
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		AdminAuth: fakeAdminAuth{
			sessionUser: admin.User{ID: 7, Email: "admin@example.com", Role: admin.RoleAdmin},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/auth/session", nil)
	req.Header.Set("Authorization", "Bearer session-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); !strings.Contains(got, `"email":"admin@example.com"`) {
		t.Fatalf("body = %s, want admin email", got)
	}
}

func TestAdminProviderStatusRequiresBearerToken(t *testing.T) {
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		AdminAuth: fakeAdminAuth{
			sessionUser: admin.User{ID: 7, Email: "admin@example.com", Role: admin.RoleViewer},
		},
		ProviderStatus: fakeProviderStatusSource{},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/providers/status", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestAdminProviderStatusReturnsProviderQuota(t *testing.T) {
	throttleUntil := time.Date(2026, 4, 29, 12, 30, 0, 0, time.UTC)
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		AdminAuth: fakeAdminAuth{
			sessionUser: admin.User{ID: 7, Email: "admin@example.com", Role: admin.RoleAdmin},
		},
		ProviderStatus: fakeProviderStatusSource{
			statuses: []ProviderStatus{
				{
					Provider:      "api-football",
					DisplayName:   "API-Football",
					Enabled:       true,
					Status:        "ready",
					ThrottleUntil: &throttleUntil,
					Quota: ProviderQuota{
						DailyLimit:      100,
						DailyRemaining:  82,
						DailyReserve:    20,
						MinuteLimit:     10,
						MinuteRemaining: 8,
						MinuteReserve:   2,
						UpdatedAt:       time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC),
					},
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/providers/status", nil)
	req.Header.Set("Authorization", "Bearer session-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, fragment := range []string{
		`"providers":[`,
		`"provider":"api-football"`,
		`"display_name":"API-Football"`,
		`"enabled":true`,
		`"status":"ready"`,
		`"daily_limit":100`,
		`"daily_remaining":82`,
		`"daily_reserve":20`,
		`"minute_limit":10`,
		`"minute_remaining":8`,
		`"minute_reserve":2`,
		`"throttle_until":"2026-04-29T12:30:00Z"`,
		`"updated_at":"2026-04-29T12:00:00Z"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("body = %s, want fragment %s", body, fragment)
		}
	}
}

func TestPublicProviderStatusReturnsCoarseHealthWithoutAdminQuota(t *testing.T) {
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		ProviderStatus: fakeProviderStatusSource{
			statuses: []ProviderStatus{
				{
					Provider:    "football-data-org",
					DisplayName: "football-data.org",
					Enabled:     true,
					Status:      "ready",
					Quota: ProviderQuota{
						DailyLimit:      0,
						DailyRemaining:  0,
						DailyReserve:    0,
						MinuteLimit:     10,
						MinuteRemaining: 9,
						MinuteReserve:   1,
						UpdatedAt:       time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC),
					},
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/providers/status", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, fragment := range []string{
		`"providers":[`,
		`"provider":"football-data-org"`,
		`"display_name":"football-data.org"`,
		`"status":"ready"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("body = %s, want fragment %s", body, fragment)
		}
	}
	if strings.Contains(body, `"minute_limit"`) {
		t.Fatalf("public body exposes operational quota: %s", body)
	}
}

func TestPublicPredictionHitRateReturnsSettledPerformanceWithoutAdminAuth(t *testing.T) {
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		Predictions: fakePredictionSource{
			hitRate: PredictionHitRateReport{
				Settled:     10,
				Won:         6,
				Lost:        3,
				Void:        1,
				HitRate:     66.67,
				Denominator: 9,
				Markets: []PredictionMarketHitRate{
					{MarketKey: "1x2", Settled: 5, Won: 3, Lost: 2, Void: 0, HitRate: 60, Denominator: 5},
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/predictions/hit-rate", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Cache-Control"); got != publicReportCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, publicReportCacheControl)
	}
	body := rec.Body.String()
	for _, fragment := range []string{
		`"settled":10`,
		`"hit_rate":66.67`,
		`"denominator":9`,
		`"denominator":5`,
		`"market_key":"1x2"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("body = %s, want fragment %s", body, fragment)
		}
	}
}

func TestAdminLoginHandlesMalformedJSON(t *testing.T) {
	handler := New(ServerOptions{
		Config:    config.Config{Environment: "test", Port: 8080},
		AdminAuth: fakeAdminAuth{loginErr: errors.New("should not be called")},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/auth/login", strings.NewReader(`{`))
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestLeagueSummaryResponseExposesContentEligibility(t *testing.T) {
	withContent := leagueSummaryResponse(LeagueSummary{ID: 7, Slug: "premier-league", Name: "Premier League", HasContent: true})
	withoutContent := leagueSummaryResponse(LeagueSummary{ID: 8, Slug: "empty-league", Name: "Empty League"})
	if got, ok := withContent["has_content"].(bool); !ok || !got {
		t.Fatalf("has_content = %#v, want true", withContent["has_content"])
	}
	if got, ok := withoutContent["has_content"].(bool); !ok || got {
		t.Fatalf("has_content = %#v, want false", withoutContent["has_content"])
	}
}

func TestServerAddsRequestIDHeader(t *testing.T) {
	handler := New(ServerOptions{Config: config.Config{Environment: "test", Port: 8080}})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-ID"); got == "" {
		t.Fatal("X-Request-ID header is empty")
	}
}

func TestServerPreservesIncomingRequestID(t *testing.T) {
	handler := New(ServerOptions{Config: config.Config{Environment: "test", Port: 8080}})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", "req_existing")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-ID"); got != "req_existing" {
		t.Fatalf("X-Request-ID = %q, want req_existing", got)
	}
}

func TestServerReturnsJSONWhenRequestDeadlineExpires(t *testing.T) {
	handler := New(ServerOptions{
		Config:  config.Config{Environment: "test", Port: 8080, APIRequestTimeout: 15 * time.Millisecond},
		Matches: blockingMatchSource{delay: 100 * time.Millisecond},
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/matches/today?include_unavailable=true", nil)
	rec := httptest.NewRecorder()

	started := time.Now()
	handler.ServeHTTP(rec, req)
	elapsed := time.Since(started)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if elapsed >= 75*time.Millisecond {
		t.Fatalf("request elapsed = %s, want timeout before 75ms", elapsed)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := rec.Body.String(); got != "{\"error\":\"request_timeout\",\"code\":\"request_timeout\",\"message\":\"The request timed out.\"}\n" {
		t.Fatalf("body = %q, want canonical JSON timeout error", got)
	}
}

func TestPasswordHashingRoutesReceiveOnlyTheBoundedAuthDeadline(t *testing.T) {
	for _, path := range []string{"/v1/auth/register", "/v1/auth/login", "/v1/auth/password-reset/confirm", "/v1/me/password"} {
		if !passwordHashingRoute(path) {
			t.Errorf("passwordHashingRoute(%q)=false", path)
		}
	}
	for _, path := range []string{"/v1/auth/password-reset/request", "/v1/access", "/v1/predictions/today", "/v1/me/profile"} {
		if passwordHashingRoute(path) {
			t.Errorf("passwordHashingRoute(%q)=true", path)
		}
	}
}

func TestTodayPredictionsReturnsProviderFallbackWithCanonicalPrimitives(t *testing.T) {
	handler := New(ServerOptions{
		Config: config.Config{Environment: "test", Port: 8080},
		Predictions: fakePredictionSource{
			providerSummaries: []ProviderPredictionSummary{
				{
					MatchID:          21,
					MatchTitle:       "Gor Mahia vs AFC Leopards",
					HomeTeam:         "Gor Mahia",
					AwayTeam:         "AFC Leopards",
					LeagueName:       "Kenyan Premier League",
					KickoffAt:        time.Date(2026, 5, 1, 15, 0, 0, 0, time.UTC),
					MatchStatus:      "scheduled",
					PredictedWinner:  "Gor Mahia",
					WinOrDraw:        false,
					UnderOver:        "Over 2.5",
					PercentHome:      45.2,
					PercentDraw:      28.1,
					PercentAway:      26.7,
					Advice:           "Gor Mahia strong at home.",
					MarketKey:        "1x2",
					Selection:        "Home",
					Confidence:       45.2,
					PublishedVersion: 0,
					PublishedAt:      time.Date(2026, 5, 1, 9, 30, 0, 0, time.UTC),
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/predictions/today", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, fragment := range []string{
		`"provider_predictions":[{`,
		`"provider_total":1`,
		`"total":1`,
		`"match_id":21`,
		`"predicted_winner":"Gor Mahia"`,
		`"percent_home":45.2`,
		`"advice":"Gor Mahia strong at home."`,
		`"source":"provider"`,
		`"market_key":"1x2"`,
		`"selection":"Home"`,
		`"confidence":45.2`,
		`"published_version":0`,
		`"published_at":"2026-05-01T09:30:00Z"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("body = %s, want fragment %s", body, fragment)
		}
	}
}

func TestErrorResponsesUseCanonicalErrorShape(t *testing.T) {
	handler := New(ServerOptions{
		Config:      config.Config{Environment: "test", Port: 8080},
		Predictions: fakePredictionSource{err: ErrPredictionNotFound},
	})

	notFound := httptest.NewRequest(http.MethodGet, "/v1/predictions/11", nil)
	notFoundRec := httptest.NewRecorder()
	handler.ServeHTTP(notFoundRec, notFound)
	if notFoundRec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", notFoundRec.Code, http.StatusNotFound)
	}
	assertCanonicalError(t, notFoundRec.Body.String(), "prediction_not_found")

	badID := httptest.NewRequest(http.MethodGet, "/v1/predictions/not-a-number", nil)
	badIDRec := httptest.NewRecorder()
	handler.ServeHTTP(badIDRec, badID)
	if badIDRec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", badIDRec.Code, http.StatusBadRequest)
	}
	assertCanonicalError(t, badIDRec.Body.String(), "invalid_prediction_id")
}

func assertCanonicalError(t *testing.T, body, wantCode string) {
	t.Helper()
	var payload struct {
		Error   string `json:"error"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("error body is not valid JSON: %v (body %q)", err, body)
	}
	if payload.Error != wantCode || payload.Code != wantCode {
		t.Fatalf("error = %q code = %q, want %q (body %q)", payload.Error, payload.Code, wantCode, body)
	}
	if strings.TrimSpace(payload.Message) == "" {
		t.Fatalf("error message is empty (body %q)", body)
	}
}

func TestRequestLogIncludesResponseStatus(t *testing.T) {
	var logs bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	handler := New(ServerOptions{Config: config.Config{Environment: "test", Port: 8080}})
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := logs.String(); !strings.Contains(got, "status=404") {
		t.Fatalf("request log = %q, want status=404", got)
	}
}

func intPtr(value int) *int {
	return &value
}

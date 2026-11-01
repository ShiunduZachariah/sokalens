package securityinvariants_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/access"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/accounts"
	analysisdomain "github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/analysis"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/httpserver"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/predictions/entitlement"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/usernotifications"
)

// The in-play entitlement invariant.
//
// In-play predictions are the paid feature. If a probability reaches an
// anonymous response the feature has been given away, and it cannot be taken
// back -- the trick circulates and the signup incentive is gone. This battery
// is the CI gate named in the architecture's failure-mode table: "no
// probability fields in anonymous responses".
//
// It works two ways at once, because each catches a leak the other misses:
//
//   - Forbidden keys. A handler that serializes the engine struct, or builds
//     its own map with "probability" in it, is caught by name.
//   - Sentinel values. A handler that renames the field, nests it, encodes it
//     as a string, or smuggles it in a debug blob is caught because the
//     numbers and strings below appear nowhere else in the universe.
//
// The stub source deliberately returns a fully-populated prediction for every
// market. A gate that passes because there was nothing to leak has proved
// nothing.

const (
	sentinelSelection  = "HOME_SENTINEL_SELECTION"
	sentinelConfidence = "MEDIUM_SENTINEL_CONFIDENCE"
	sentinelCutoff     = "2026-09-07T18:43:10Z"
	// The snapshot series serves basis points under a `markets` key. This string
	// appears nowhere else, so a hit on an anonymous payload is a leak of the
	// series however it was spelled.
	sentinelSnapshotMarkets = `"MATCH_RESULT":{"away":1868,"draw":2000,"home":6132}`
)

// Chosen to be unmistakable in a body: no rounding of a real probability
// produces these digits, so a substring hit is a leak and never a coincidence.
var sentinelNumbers = []string{"0.613245", "0.729431"}

// forbiddenAnonymousKeys are JSON keys that must never appear anywhere in an
// anonymous in-play payload, at any depth.
//
// "confidence" is on the list even though pre-match surfaces publish it: the
// in-play confidence is derived from the same gated model output as the
// probability, and a confidence band alone is enough to rebuild most of the
// signal. The list is about the in-play surface only.
var forbiddenAnonymousKeys = []string{
	"prediction",
	"probability",
	"confidence",
	"confidence_score",
	"selection",
	"data_cutoff_at",
	"expected_goals",
	"lambda",
	"fair_odds",
	"model_version",
}

type stubInPlaySource struct {
	err error
}

func (s stubInPlaySource) state(fixtureID int64) entitlement.FixtureState {
	cutoff, _ := time.Parse(time.RFC3339, sentinelCutoff)
	return entitlement.FixtureState{
		FixtureID: fixtureID,
		Status:    "LIVE_2H",
		Minute:    63,
		HomeGoals: 1,
		AwayGoals: 1,
		Markets: []entitlement.MarketState{
			{
				Key:   "1X2",
				Label: "Match Result",
				Prediction: &entitlement.Prediction{
					Selection:       sentinelSelection,
					Probability:     0.613245,
					Confidence:      sentinelConfidence,
					ConfidenceScore: 0.729431,
				},
				DataCutoffAt: cutoff,
				MatchMinute:  63,
			},
			{
				Key:   "NEXT_GOAL",
				Label: "Next Goal",
				Prediction: &entitlement.Prediction{
					Selection:       sentinelSelection,
					Probability:     0.613245,
					Confidence:      sentinelConfidence,
					ConfidenceScore: 0.729431,
				},
				DataCutoffAt: cutoff,
				MatchMinute:  63,
			},
			// A market the engine has not published. An entitled viewer sees
			// it unlocked and empty; it must not be counted as locked, or the
			// CTA fires for a viewer who has nothing to unlock.
			{Key: "OVER_UNDER_2_5", Label: "Over/Under 2.5"},
		},
	}
}

func (s stubInPlaySource) ListInPlayFixtures(context.Context, int) ([]entitlement.FixtureState, error) {
	if s.err != nil {
		return nil, s.err
	}
	return []entitlement.FixtureState{s.state(1234567), s.state(7654321)}, nil
}

func (s stubInPlaySource) FixtureSeries(context.Context, int64) ([]entitlement.SeriesPoint, error) {
	if s.err != nil {
		return nil, s.err
	}
	return nil, nil
}

func (s stubInPlaySource) GetInPlayFixture(_ context.Context, fixtureID int64) (entitlement.FixtureState, error) {
	if s.err != nil {
		return entitlement.FixtureState{}, s.err
	}
	return s.state(fixtureID), nil
}

// stubInPlayStreamSource carries the same sentinels through the transport
// routes. The snapshot series is the paid asset over time -- strictly more than
// the point-in-time payload -- so the battery has to probe it with real
// probability values in it, or the new routes would be "covered" by a 503.
type stubInPlayStreamSource struct{ err error }

func (s stubInPlayStreamSource) snapshot(fixtureID int64, sequence int64) entitlement.SnapshotState {
	cutoff, _ := time.Parse(time.RFC3339, sentinelCutoff)
	return entitlement.SnapshotState{
		FixtureID:      fixtureID,
		Sequence:       sequence,
		CapturedAt:     cutoff,
		DataCutoffAt:   cutoff,
		Trigger:        "post_goal",
		TriggeredByKey: "sentinel-event-key",
		ModelHealth:    "ok",
		Minute:         63,
		Period:         "2H",
		HomeGoals:      1,
		AwayGoals:      1,
		Markets: map[string]map[string]int{
			// Basis points are integers, so the float sentinels cannot appear
			// here. These are the gated values for this surface, and the
			// key-level assertion is what catches them.
			"MATCH_RESULT": {"home": 6132, "draw": 2000, "away": 1868},
		},
	}
}

func (s stubInPlayStreamSource) Overview(context.Context, int, int) (entitlement.OverviewState, error) {
	if s.err != nil {
		return entitlement.OverviewState{}, s.err
	}
	stub := stubInPlaySource{}
	return entitlement.OverviewState{
		Fixtures:  []entitlement.FixtureState{stub.state(1234567)},
		Snapshots: []entitlement.SnapshotState{s.snapshot(1234567, 41), s.snapshot(1234567, 42)},
		Events: []entitlement.Event{
			{Sequence: 40, FixtureID: 1234567, IdentityKey: "sentinel-event-key", Kind: "goal", Minute: 62},
		},
		Cursor: 42,
	}, nil
}

func (s stubInPlayStreamSource) ChangesSince(_ context.Context, cursor int64, _ int) (entitlement.ChangeSetState, error) {
	if s.err != nil {
		return entitlement.ChangeSetState{}, s.err
	}
	return entitlement.ChangeSetState{
		Snapshots: []entitlement.SnapshotState{s.snapshot(1234567, cursor+1)},
		Events: []entitlement.Event{
			{Sequence: cursor + 2, FixtureID: 1234567, IdentityKey: "sentinel-event-key", Kind: "goal", Minute: 62},
		},
		Cursor: cursor + 2,
	}, nil
}

func newInPlayHandler(t *testing.T) (http.Handler, *accounts.Service, func(string) accounts.Account) {
	t.Helper()
	db := openSecurityDB(t)
	service := accounts.NewService(accounts.NewSQLRepository(db, accounts.DialectSQLite))
	handler := httpserver.New(httpserver.ServerOptions{
		Config:        config.Config{Environment: "production", PublicWebURL: "https://sokalens.example"},
		Accounts:      service,
		Access:        access.NewSQLRepository(db, access.DialectSQLite),
		Notifications: usernotifications.NewService(db, "test-notification-secret"),
		InPlay:        stubInPlaySource{},
		InPlayStream:  stubInPlayStreamSource{},
		// A one-shot probe must terminate. The production defaults hold a
		// connection for half an hour, which is correct for a client and makes a
		// test look like a hang.
		InPlayStreamPacing: httpserver.InPlayStreamPacing{
			Poll:      time.Millisecond,
			Heartbeat: time.Hour,
			Lifetime:  20 * time.Millisecond,
		},
	})
	return handler, service, func(email string) accounts.Account {
		return registerVerified(t, db, service, email)
	}
}

// inPlayProbePaths is every in-play path the battery exercises: the routes the
// router itself publishes, plus a detail path under the same prefix.
func inPlayProbePaths() []string {
	paths := append([]string(nil), httpserver.InPlayRoutes...)
	return append(paths, "/v1/inplay/fixtures/1234567")
}

// gatedValuesFor is what an entitled viewer must receive on a given surface, and
// an anonymous viewer must never receive.
//
// Per surface, because the surfaces carry different shapes of the same asset: the
// point-in-time payload serves float probabilities and a selection, while the
// snapshot series serves integer basis points under a `markets` key. Requiring
// every sentinel everywhere would assert a shape neither surface has, and the
// resulting "fix" would be to weaken the assertion rather than the gate.
// probeBody returns a path's response body as JSON.
//
// The stream serves text/event-stream, so its payload arrives inside `data:`
// frames rather than as a bare document. Extracting them keeps the stream under
// exactly the same key-level and value-level assertions as every other route --
// which matters more here than anywhere, since a leak on a stream repeats every
// fifteen seconds for as long as the client stays connected.
func probeBody(t *testing.T, path, raw string) []byte {
	t.Helper()
	if path != httpserver.InPlayStreamPath {
		return []byte(raw)
	}
	frames := make([]json.RawMessage, 0, 4)
	for _, line := range strings.Split(raw, "\n") {
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		frames = append(frames, json.RawMessage(payload))
	}
	if len(frames) == 0 {
		t.Fatalf("stream emitted no data frames; nothing was asserted\nbody: %s", raw)
	}
	wrapped, err := json.Marshal(frames)
	if err != nil {
		t.Fatalf("stream frames are not JSON: %v\nbody: %s", err, raw)
	}
	return wrapped
}

func gatedValuesFor(path string) []string {
	switch path {
	case httpserver.InPlayOverviewPath:
		// The overview carries both: the board's markets and the series.
		return append(append([]string(nil), sentinelNumbers...),
			sentinelSelection, sentinelConfidence, sentinelCutoff, sentinelSnapshotMarkets)
	case httpserver.InPlayChangesPath, httpserver.InPlayStreamPath:
		return []string{sentinelCutoff, sentinelSnapshotMarkets}
	default:
		return append(append([]string(nil), sentinelNumbers...),
			sentinelSelection, sentinelConfidence, sentinelCutoff)
	}
}

func TestAnonymousInPlayResponsesCarryNoProbabilityField(t *testing.T) {
	handler, _, _ := newInPlayHandler(t)

	for _, path := range inPlayProbePaths() {
		t.Run(path, func(t *testing.T) {
			response := serve(handler, http.MethodGet, path, nil, nil)
			if response.Code != http.StatusOK {
				t.Fatalf("anonymous %s = %d, want 200; body=%s", path, response.Code, response.Body.String())
			}
			body := response.Body.String()

			// Value-level: the sentinels exist nowhere else, so any hit is a
			// leak however it was spelled.
			for _, sentinel := range gatedValuesFor(path) {
				if strings.Contains(body, sentinel) {
					t.Fatalf("anonymous %s leaked gated value %q\nbody: %s", path, sentinel, body)
				}
			}

			// Key-level: catches a leak that happens to carry different
			// numbers than the stub produced.
			var decoded any
			if err := json.Unmarshal(probeBody(t, path, body), &decoded); err != nil {
				t.Fatalf("anonymous %s body is not JSON: %v", path, err)
			}
			if leaked := findForbiddenKey(decoded, ""); leaked != "" {
				t.Fatalf("anonymous %s exposed gated key %q\nbody: %s", path, leaked, body)
			}

			// Shape: the client still gets a complete board, so a locked cell
			// is a conversion surface rather than a hole in the page.
			if !strings.Contains(body, `"locked":true`) {
				t.Fatalf("anonymous %s did not mark markets locked\nbody: %s", path, body)
			}
			if !strings.Contains(body, entitlement.ReasonAuthRequired) || !strings.Contains(body, `"cta"`) {
				t.Fatalf("anonymous %s omitted the unlock affordance\nbody: %s", path, body)
			}
		})
	}
}

func TestAuthenticatedInPlayResponsesCarryPredictionsAndMatchTheAnonymousSchema(t *testing.T) {
	handler, service, register := newInPlayHandler(t)
	account := register("inplay@example.test")
	credential, err := service.CreateSession(context.Background(), account.ID, "browser", "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: "sokalens_session", Value: credential.Token}

	for _, path := range inPlayProbePaths() {
		t.Run(path, func(t *testing.T) {
			response := requestWithCookies(handler, http.MethodGet, path, nil, []*http.Cookie{cookie}, nil)
			if response.Code != http.StatusOK {
				t.Fatalf("authenticated %s = %d, want 200; body=%s", path, response.Code, response.Body.String())
			}
			body := response.Body.String()

			// The negative test above is only meaningful if the positive one
			// holds: the filter must actually be capable of emitting the
			// gated payload, or it is passing by being broken.
			for _, sentinel := range gatedValuesFor(path) {
				if !strings.Contains(body, sentinel) {
					t.Fatalf("authenticated %s is missing gated value %q -- the gate may be denying everyone\nbody: %s", path, sentinel, body)
				}
			}
			if strings.Contains(body, `"locked":true`) {
				t.Fatalf("authenticated %s locked a market\nbody: %s", path, body)
			}
			if strings.Contains(body, entitlement.ReasonAuthRequired) {
				t.Fatalf("authenticated %s carried an unlock CTA\nbody: %s", path, body)
			}
		})
	}
}

// A guest session is a free-view allowance on pre-match analysis. It is not an
// account, and it must not unlock in-play.
func TestGuestSessionDoesNotUnlockInPlayPredictions(t *testing.T) {
	handler, _, _ := newInPlayHandler(t)
	response := requestWithCookies(handler, http.MethodGet, "/v1/inplay/fixtures", nil,
		[]*http.Cookie{{Name: "sokalens_guest", Value: "any-guest-token"}}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("guest request = %d, want 200; body=%s", response.Code, response.Body.String())
	}
	for _, sentinel := range append(append([]string(nil), sentinelNumbers...), sentinelSelection) {
		if strings.Contains(response.Body.String(), sentinel) {
			t.Fatalf("guest session leaked gated value %q\nbody: %s", sentinel, response.Body.String())
		}
	}
	if access.GuestContext(7).Allows(access.CapabilityInPlayPredictions) {
		t.Fatal("guest access context grants the in-play capability")
	}
}

// An edge cache holding one authenticated in-play payload answers every
// anonymous request behind it. The leak is silent, so the headers are an
// invariant and not a nicety.
func TestInPlayResponsesAreNeverCacheableAtTheEdge(t *testing.T) {
	handler, _, _ := newInPlayHandler(t)
	for _, path := range inPlayProbePaths() {
		response := serve(handler, http.MethodGet, path, nil, nil)
		cacheControl := response.Header().Get("Cache-Control")
		if !strings.Contains(cacheControl, "private") || !strings.Contains(cacheControl, "no-store") {
			t.Errorf("%s Cache-Control = %q, want private and no-store", path, cacheControl)
		}
		if vary := response.Header().Get("Vary"); !strings.Contains(vary, "Cookie") {
			t.Errorf("%s Vary = %q, want it to include Cookie", path, vary)
		}
		if robots := response.Header().Get("X-Robots-Tag"); !strings.Contains(robots, "noindex") {
			t.Errorf("%s X-Robots-Tag = %q, want noindex", path, robots)
		}
	}
}

// The gate is only as wide as the surface it covers. Routing the whole
// /v1/inplay/ prefix to one handler is what makes "audit one place" true, so
// the battery asserts that an unlisted path under the prefix is a 404 rather
// than a second, ungated handler someone wired in beside it.
func TestInPlayPrefixHasNoRouteOutsideTheGate(t *testing.T) {
	handler, _, _ := newInPlayHandler(t)
	if len(httpserver.InPlayRoutes) == 0 {
		t.Fatal("httpserver.InPlayRoutes is empty: the battery would assert nothing")
	}
	for _, route := range httpserver.InPlayRoutes {
		if !strings.HasPrefix(route, httpserver.InPlayPathPrefix) {
			t.Fatalf("route %q is outside %q and escapes this battery", route, httpserver.InPlayPathPrefix)
		}
	}
	for _, path := range []string{"/v1/inplay/predictions", "/v1/inplay/", "/v1/inplay/fixtures/1234567/markets"} {
		response := serve(handler, http.MethodGet, path, nil, nil)
		if response.Code != http.StatusNotFound && response.Code != http.StatusBadRequest {
			t.Fatalf("unlisted in-play path %s = %d, want 404/400; an ungated route may have been added\nbody: %s",
				path, response.Code, response.Body.String())
		}
	}
}

// §8.1 at the type level: the shaped market's prediction fields are
// unexported, so no package outside the filter can construct an entitled
// payload. This asserts the wire consequence -- that the anonymous shape of an
// arbitrary engine state is clean -- without going through HTTP, which catches
// a leak in a future non-HTTP caller such as the SSE broadcaster.
func TestEntitlementFilterAnonymousShapeIsCleanForArbitraryState(t *testing.T) {
	filter := entitlement.NewFilter()
	source := stubInPlaySource{}
	states, err := source.ListInPlayFixtures(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(filter.ShapeAll(entitlement.Anonymous(), states))
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if leaked := findForbiddenKey(decoded, ""); leaked != "" {
		t.Fatalf("anonymous shape exposed gated key %q\npayload: %s", leaked, encoded)
	}
	for _, sentinel := range append(append([]string(nil), sentinelNumbers...), sentinelSelection, sentinelConfidence, sentinelCutoff) {
		if strings.Contains(string(encoded), sentinel) {
			t.Fatalf("anonymous shape leaked gated value %q\npayload: %s", sentinel, encoded)
		}
	}
}

// The public pre-match reads must stay pre-match.
//
// /v1/analysis/{id} is readable by guests -- three free views, no account --
// and it takes the newest current prediction for the fixture. Once the
// in-play engine writes post-kickoff rows with `superseded_at IS NULL`,
// "newest" is an in-play prediction. The `generated_at <= kickoff_at` guard
// is the only thing standing between that and an anonymous reader, so this
// asserts the behaviour rather than the text of the query: two rows on one
// live fixture, and the pre-match one has to win.
func TestPublicAnalysisNeverServesAnInPlayPredictionToAnonymousReaders(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	mustExec(t, db, `
CREATE TABLE fixtures (id INTEGER PRIMARY KEY, kickoff_at DATETIME NOT NULL);
CREATE TABLE engine_predictions (
  id INTEGER PRIMARY KEY AUTOINCREMENT, fixture_id INTEGER NOT NULL, market TEXT NOT NULL,
  prediction TEXT NOT NULL, confidence REAL NOT NULL, confidence_tier TEXT NOT NULL DEFAULT '',
  explanation TEXT NOT NULL DEFAULT '', gate_tier TEXT NOT NULL DEFAULT 'FULL',
  is_best_bet BOOLEAN NOT NULL DEFAULT false, superseded_at DATETIME, generated_at DATETIME NOT NULL
);`)
	kickoff := time.Now().UTC().Add(-63 * time.Minute)
	mustExec(t, db, `INSERT INTO fixtures(id,kickoff_at) VALUES (1234567,?)`, kickoff)
	mustExec(t, db, `INSERT INTO engine_predictions(fixture_id,market,prediction,confidence,generated_at) VALUES (1234567,'1x2','pre_match_pick',0.51,?)`,
		kickoff.Add(-2*time.Hour))
	mustExec(t, db, `INSERT INTO engine_predictions(fixture_id,market,prediction,confidence,generated_at) VALUES (1234567,'1x2',?,0.99,?)`,
		sentinelSelection, kickoff.Add(time.Hour))

	result, err := analysisdomain.NewService(db).Get(context.Background(), 1234567, false)
	if err != nil {
		t.Fatalf("prepared analysis: %v", err)
	}
	if result.Pick == sentinelSelection {
		t.Fatalf("public analysis served the in-play prediction to an anonymous reader: %+v", result)
	}
	if result.Pick != "pre_match_pick" {
		t.Fatalf("public analysis pick = %q, want the pre-match row", result.Pick)
	}
}

// findForbiddenKey walks decoded JSON and returns the first forbidden key it
// finds, with its path, or "" when the payload is clean.
func findForbiddenKey(node any, path string) string {
	switch typed := node.(type) {
	case map[string]any:
		for key, value := range typed {
			for _, forbidden := range forbiddenAnonymousKeys {
				if strings.EqualFold(key, forbidden) {
					return path + "." + key
				}
			}
			if found := findForbiddenKey(value, path+"."+key); found != "" {
				return found
			}
		}
	case []any:
		for index, value := range typed {
			if found := findForbiddenKey(value, path+"[]"); found != "" {
				_ = index
				return found
			}
		}
	}
	return ""
}

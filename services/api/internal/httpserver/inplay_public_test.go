package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/predictions/entitlement"
)

type publicInPlaySource struct{ suppressed bool }

func (s publicInPlaySource) fixture() entitlement.FixtureState {
	prediction := &entitlement.Prediction{Selection: "PUBLIC_HOME", Probability: 0.61}
	if s.suppressed {
		prediction = nil
	}
	return entitlement.FixtureState{FixtureID: 42, Status: "LIVE_2H", Stale: s.suppressed,
		Markets: []entitlement.MarketState{{Key: "1X2", Prediction: prediction}}}
}
func (s publicInPlaySource) ListInPlayFixtures(context.Context, int) ([]entitlement.FixtureState, error) {
	return []entitlement.FixtureState{s.fixture()}, nil
}
func (s publicInPlaySource) GetInPlayFixture(context.Context, int64) (entitlement.FixtureState, error) {
	return s.fixture(), nil
}
func (s publicInPlaySource) FixtureSeries(context.Context, int64) ([]entitlement.SeriesPoint, error) {
	return nil, nil
}
func (s publicInPlaySource) snapshots() []entitlement.SnapshotState {
	var markets map[string]map[string]int
	if !s.suppressed {
		markets = map[string]map[string]int{"PUBLIC_MARKET": {"home": 6100, "away": 3900}}
	}
	return []entitlement.SnapshotState{{FixtureID: 42, Sequence: 1, Markets: markets}}
}
func (s publicInPlaySource) Overview(context.Context, int, int) (entitlement.OverviewState, error) {
	return entitlement.OverviewState{Fixtures: []entitlement.FixtureState{s.fixture()}, Snapshots: s.snapshots()}, nil
}
func (s publicInPlaySource) ChangesSince(context.Context, int64, int) (entitlement.ChangeSetState, error) {
	return entitlement.ChangeSetState{Snapshots: s.snapshots(), Cursor: 1}, nil
}

func publicInPlayHandler(suppressed bool) http.Handler {
	source := publicInPlaySource{suppressed: suppressed}
	return New(ServerOptions{InPlayPublicRead: true, InPlay: source, InPlayStream: source,
		AdminAuth:          fakeAdminAuth{sessionErr: errors.New("invalid session")},
		InPlayStreamPacing: InPlayStreamPacing{Poll: time.Hour, Heartbeat: time.Hour, Lifetime: time.Millisecond}})
}

// The permanent securityinvariants exercise the legacy gated policy. These
// tests exercise the explicitly enabled public policy through the real router.
func TestPublicInPlayAnonymousReadsEverySurface(t *testing.T) {
	for _, path := range append(append([]string{}, InPlayRoutes...), "/v1/inplay/fixtures/42") {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			publicInPlayHandler(false).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			body := rec.Body.String()
			if rec.Code != http.StatusOK || (!strings.Contains(body, "PUBLIC_HOME") && !strings.Contains(body, "PUBLIC_MARKET")) {
				t.Fatalf("status=%d body=%s", rec.Code, body)
			}
			if strings.Contains(body, `"locked":true`) || strings.Contains(body, "AUTH_REQUIRED") {
				t.Fatalf("public read locked: %s", body)
			}
			if !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
				t.Fatal("live data cache protection lost")
			}
		})
	}
}

func TestPublicInPlayDoesNotInventSuppressedPredictions(t *testing.T) {
	for _, path := range append(append([]string{}, InPlayRoutes...), "/v1/inplay/fixtures/42") {
		rec := httptest.NewRecorder()
		publicInPlayHandler(true).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "PUBLIC_") {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestPublicInPlayKeepsMethodsAndPrivateRoutesProtected(t *testing.T) {
	handler := publicInPlayHandler(false)
	for _, path := range append(append([]string{}, InPlayRoutes...), "/v1/inplay/fixtures/42") {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s POST status=%d", path, rec.Code)
		}
	}
	for _, path := range []string{"/v1/me", "/v1/me/profile", "/v1/me/favorites", "/v1/admin/providers/status"} {
		rec := httptest.NewRecorder()
		method := http.MethodGet
		if path == "/v1/me/profile" {
			method = http.MethodPatch
		}
		handler.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("private route %s status=%d, want 401", path, rec.Code)
		}
	}
}

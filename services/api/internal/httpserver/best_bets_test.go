package httpserver

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/access"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/accounts"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/config"
	_ "modernc.org/sqlite"
)

type bestBetsPredictionSource struct {
	items  []PredictionSummary
	filter PredictionFilter
	calls  int
}

func (s *bestBetsPredictionSource) ListTodayPredictions(_ context.Context, filter PredictionFilter) ([]PredictionSummary, error) {
	s.calls++
	s.filter = filter
	return s.items, nil
}

func (*bestBetsPredictionSource) CountTodayPredictions(context.Context, PredictionFilter) (int, error) {
	return 0, nil
}

func (*bestBetsPredictionSource) ListTodayProviderPredictions(context.Context, PredictionFilter) ([]ProviderPredictionSummary, error) {
	return nil, nil
}

func (*bestBetsPredictionSource) CountTodayProviderPredictions(context.Context, PredictionFilter) (int, error) {
	return 0, nil
}

func (*bestBetsPredictionSource) GetPrediction(context.Context, int64) (PredictionDetail, error) {
	return PredictionDetail{}, nil
}

func (*bestBetsPredictionSource) GetHitRateReport(context.Context) (PredictionHitRateReport, error) {
	return PredictionHitRateReport{}, nil
}

func TestBestBetsRejectsGuestWithoutQueryingPredictions(t *testing.T) {
	source := &bestBetsPredictionSource{}
	handler := New(ServerOptions{Config: config.Config{Environment: "test"}, Predictions: source})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/best-bets", nil))

	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), `"code":"authentication_required"`) {
		t.Fatalf("guest response = %d %s", response.Code, response.Body.String())
	}
	if source.calls != 0 {
		t.Fatalf("prediction source calls = %d, want 0", source.calls)
	}
}

func TestBestBetsReturnsPreparedPredictionsForEntitledAccount(t *testing.T) {
	db, token := bestBetsAccountDB(t, `{"analysis.standard":true}`)
	source := &bestBetsPredictionSource{items: []PredictionSummary{{ID: 11, MatchID: 82, MatchTitle: "Home vs Away", Confidence: 74}}}
	handler := New(ServerOptions{
		Config:      config.Config{Environment: "test"},
		Predictions: source,
		Accounts:    accounts.NewService(accounts.NewSQLRepository(db, accounts.DialectSQLite)),
		Access:      access.NewSQLRepository(db, access.DialectSQLite),
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/best-bets?confidence=72&limit=999", nil)
	request.AddCookie(&http.Cookie{Name: userSessionCookie, Value: token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"tier":"basic"`) || !strings.Contains(response.Body.String(), `"predictions":[`) {
		t.Fatalf("account response = %d %s", response.Code, response.Body.String())
	}
	if source.calls != 1 || source.filter.MinimumConfidence == nil || *source.filter.MinimumConfidence != 72 || !source.filter.BestBetsOnly || source.filter.Limit != 50 {
		t.Fatalf("prediction filter = %+v, calls = %d", source.filter, source.calls)
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "private, no-store" {
		t.Fatalf("Cache-Control = %q", cacheControl)
	}
}

func TestBestBetsRejectsAccountWithoutStandardAnalysisCapability(t *testing.T) {
	db, token := bestBetsAccountDB(t, `{}`)
	source := &bestBetsPredictionSource{}
	handler := New(ServerOptions{
		Config:      config.Config{Environment: "test"},
		Predictions: source,
		Accounts:    accounts.NewService(accounts.NewSQLRepository(db, accounts.DialectSQLite)),
		Access:      access.NewSQLRepository(db, access.DialectSQLite),
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/best-bets", nil)
	request.AddCookie(&http.Cookie{Name: userSessionCookie, Value: token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"standard_analysis_required"`) {
		t.Fatalf("capability response = %d %s", response.Code, response.Body.String())
	}
	if source.calls != 0 {
		t.Fatalf("prediction source calls = %d, want 0", source.calls)
	}
}

func bestBetsAccountDB(t *testing.T, entitlements string) (*sql.DB, string) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	schema := `
CREATE TABLE users (id INTEGER PRIMARY KEY, public_id TEXT NOT NULL, email TEXT NOT NULL, password_hash TEXT NOT NULL, email_verified_at DATETIME, account_status TEXT NOT NULL, country_code TEXT, deleted_at DATETIME, created_at DATETIME NOT NULL);
CREATE TABLE user_sessions (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL, token_hash TEXT NOT NULL, public_id TEXT NOT NULL, device_name TEXT NOT NULL, user_agent TEXT NOT NULL, expires_at DATETIME NOT NULL, absolute_expires_at DATETIME, last_seen_at DATETIME NOT NULL, revoked_at DATETIME, created_at DATETIME NOT NULL);
CREATE TABLE user_entitlement_snapshots (user_id INTEGER PRIMARY KEY, tier TEXT NOT NULL, entitlements TEXT NOT NULL, ads_allowed BOOLEAN NOT NULL, next_change_at DATETIME);
CREATE TABLE user_roles (user_id INTEGER NOT NULL, role_key TEXT NOT NULL, revoked_at DATETIME);
CREATE TABLE role_permissions (role_key TEXT NOT NULL, permission_key TEXT);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	token := "best-bets-session-token"
	digest := sha256.Sum256([]byte(token))
	if _, err := db.Exec(`INSERT INTO users(id,public_id,email,password_hash,email_verified_at,account_status,created_at) VALUES(1,'00000000-0000-4000-8000-000000000099','fan@example.com','unused',?,'active',?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_sessions(id,user_id,token_hash,public_id,device_name,user_agent,expires_at,last_seen_at,created_at) VALUES(1,1,?,'00000000-0000-4000-8000-000000000001','test','test',?,?,?)`, hex.EncodeToString(digest[:]), now.Add(time.Hour), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_entitlement_snapshots(user_id,tier,entitlements,ads_allowed,next_change_at) VALUES(1,'basic',?,true,?)`, entitlements, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return db, token
}

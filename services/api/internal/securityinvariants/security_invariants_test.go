package securityinvariants_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/access"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/accounts"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/admin"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/billing"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/httpserver"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/usernotifications"
	_ "modernc.org/sqlite"
)

// This package is deliberately a consumer of the production packages. It is a
// compact, permanent invariant battery rather than a duplicate implementation
// of authentication or authorization policy.

func TestUnauthenticatedAccountEndpointIsDenied(t *testing.T) {
	handler := httpserver.New(httpserver.ServerOptions{Config: config.Config{Environment: "production"}})
	recorder := serve(handler, http.MethodGet, "/v1/me", nil, nil)
	assertResponse(t, recorder, http.StatusUnauthorized, `"authentication_required"`)
}

func TestRegistrationCreatesPendingSessionAndOnlyVerificationOutbox(t *testing.T) {
	db := openSecurityDB(t)
	service := accounts.NewService(accounts.NewSQLRepository(db, accounts.DialectSQLite))
	handler := newAccountHandler(db, service)
	response := serve(handler, http.MethodPost, "/v1/auth/register", []byte(`{"email":"launch@example.test","password":"Correct-Pass-8","country_code":"KE","accept_terms":true,"utm_source":"launch","referrer_host":"first-touch.example","landing_page":"/predictions/today","signup_page":"/register"}`), map[string]string{"Content-Type": "application/json", "User-Agent": "Mozilla/5.0 Mobile", "Referer": "https://sokalens.com/register"})
	assertResponse(t, response, http.StatusCreated, `"pending_verification"`)
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		switch cookie.Name {
		case "sokalens_session":
			sessionCookie = cookie
		case "sokalens_csrf":
			csrfCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || !sessionCookie.Secure || csrfCookie == nil {
		t.Fatalf("pending registration cookies session=%+v csrf=%+v", sessionCookie, csrfCookie)
	}
	var status, country, referrer string
	if err := db.QueryRow(`SELECT account_status,country_code FROM users WHERE email_normalized='launch@example.test'`).Scan(&status, &country); err != nil || status != "pending_verification" || country != "KE" {
		t.Fatalf("account status=%q country=%q err=%v", status, country, err)
	}
	if err := db.QueryRow(`SELECT COALESCE(first_referrer_host,'') FROM user_acquisition ua JOIN users u ON u.id=ua.user_id WHERE u.email_normalized='launch@example.test'`).Scan(&referrer); err != nil || referrer != "first-touch.example" {
		t.Fatalf("referrer=%q err=%v", referrer, err)
	}
	var total, verification int
	if err := db.QueryRow(`SELECT COUNT(*),COUNT(*) FILTER (WHERE category='email_verification') FROM notification_outbox`).Scan(&total, &verification); err != nil || total != 1 || verification != 1 {
		t.Fatalf("outbox total=%d verification=%d err=%v", total, verification, err)
	}
	me := requestWithCookies(handler, http.MethodGet, "/v1/me", nil, []*http.Cookie{sessionCookie}, nil)
	assertResponse(t, me, http.StatusOK, `"email_verified":false`)
}

func TestRegistrationReportsQueueFailureAndVerificationResendRecovers(t *testing.T) {
	db := openSecurityDB(t)
	service := accounts.NewService(accounts.NewSQLRepository(db, accounts.DialectSQLite))
	handler := newAccountHandler(db, service)
	mustExec(t, db, `DROP TABLE notification_outbox`)
	registration := serve(handler, http.MethodPost, "/v1/auth/register", []byte(`{"email":"recover@example.test","password":"Correct-Pass-8","country_code":"KE","accept_terms":true}`), jsonHeaders())
	assertResponse(t, registration, http.StatusServiceUnavailable, `"verification_delivery_unavailable"`)
	var users int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE email_normalized='recover@example.test' AND account_status='pending_verification'`).Scan(&users); err != nil || users != 1 {
		t.Fatalf("recoverable account count=%d err=%v", users, err)
	}
	mustExec(t, db, `CREATE TABLE notification_outbox (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, channel TEXT NOT NULL, category TEXT NOT NULL, dedupe_key TEXT NOT NULL UNIQUE, payload TEXT NOT NULL, priority INTEGER NOT NULL DEFAULT 100)`)
	resend := serve(handler, http.MethodPost, "/v1/auth/email-verification/request", []byte(`{"email":"recover@example.test"}`), jsonHeaders())
	assertResponse(t, resend, http.StatusAccepted, `"accepted"`)
	var queued int
	if err := db.QueryRow(`SELECT COUNT(*) FROM notification_outbox WHERE category='email_verification'`).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("resend queue count=%d err=%v", queued, err)
	}
}

func TestPasswordResetEmailIsEnumerationSafeOneTimeAndRevokesSessions(t *testing.T) {
	db := openSecurityDB(t)
	service := accounts.NewService(accounts.NewSQLRepository(db, accounts.DialectSQLite))
	account := registerVerified(t, db, service, "reset@example.test")
	handler := newAccountHandler(db, service)

	known := serve(handler, http.MethodPost, "/v1/auth/password-reset/request", []byte(`{"email":"reset@example.test"}`), jsonHeaders())
	unknown := serve(handler, http.MethodPost, "/v1/auth/password-reset/request", []byte(`{"email":"unknown@example.test"}`), jsonHeaders())
	if known.Code != http.StatusAccepted || unknown.Code != http.StatusAccepted || known.Body.String() != unknown.Body.String() {
		t.Fatalf("enumeration-safe responses known=%d %q unknown=%d %q", known.Code, known.Body.String(), unknown.Code, unknown.Body.String())
	}
	var resets, other int
	if err := db.QueryRow(`SELECT COUNT(*) FILTER (WHERE category='password_reset'),COUNT(*) FILTER (WHERE category NOT IN ('email_verification','password_reset')) FROM notification_outbox`).Scan(&resets, &other); err != nil || resets != 1 || other != 0 {
		t.Fatalf("reset outbox=%d other=%d err=%v", resets, other, err)
	}

	credential, err := service.CreateSession(context.Background(), account.ID, "reset-test", "test")
	if err != nil {
		t.Fatal(err)
	}
	issued, _, err := service.IssuePasswordReset(context.Background(), account.Email)
	if err != nil {
		t.Fatal(err)
	}
	confirmBody := []byte(`{"token":"` + issued.Token + `","password":"NewPass8"}`)
	confirm := serve(handler, http.MethodPost, "/v1/auth/password-reset/confirm", confirmBody, jsonHeaders())
	assertResponse(t, confirm, http.StatusOK, `"password_reset"`)
	if _, _, err := service.ValidateSession(context.Background(), credential.Token); !errors.Is(err, accounts.ErrInvalidSession) {
		t.Fatalf("pre-reset session validation error=%v, want invalid session", err)
	}
	if reused := serve(handler, http.MethodPost, "/v1/auth/password-reset/confirm", confirmBody, jsonHeaders()); reused.Code != http.StatusBadRequest {
		t.Fatalf("reused reset status=%d body=%s", reused.Code, reused.Body.String())
	}
	if _, err := service.Authenticate(context.Background(), account.Email, "NewPass8"); err != nil {
		t.Fatalf("new password authentication: %v", err)
	}
}

func TestPreparedEntitlementsEnforceGuestBasicAndProBoundaries(t *testing.T) {
	db := openSecurityDB(t)
	now := time.Now().UTC()
	mustExec(t, db, `INSERT INTO user_entitlement_snapshots(user_id,tier,entitlements,ads_allowed,next_change_at) VALUES
		(1,'basic','{"analysis.standard":true,"analysis.advanced":false}',true,?),
		(2,'pro','{"analysis.standard":true,"analysis.advanced":true,"ads.hidden":true}',false,?)`, now.Add(time.Hour), now.Add(time.Hour))
	repo := access.NewSQLRepository(db, access.DialectSQLite)

	guest := access.GuestContext(7)
	if guest.Allows(access.CapabilityAnalysisStandard) || !guest.Allows(access.CapabilityPublicRead) {
		t.Fatalf("guest capabilities = %v, want public-only access", guest.CapabilityNames())
	}
	basic, err := repo.ResolveUser(context.Background(), 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if !basic.Allows(access.CapabilityAnalysisStandard) || basic.Allows(access.CapabilityAnalysisAdvanced) || !basic.AdsAllowed {
		t.Fatalf("basic context = tier %q capabilities %v ads=%t", basic.Principal.Tier, basic.CapabilityNames(), basic.AdsAllowed)
	}
	pro, err := repo.ResolveUser(context.Background(), 2, now)
	if err != nil {
		t.Fatal(err)
	}
	if !pro.Allows(access.CapabilityAnalysisAdvanced) || pro.AdsAllowed {
		t.Fatalf("pro context = tier %q capabilities %v ads=%t", pro.Principal.Tier, pro.CapabilityNames(), pro.AdsAllowed)
	}
	if err := basic.Require(access.CapabilityAnalysisAdvanced); !errors.Is(err, access.ErrCapabilityDenied) {
		t.Fatalf("basic advanced-analysis error = %v, want capability denied", err)
	}
}

func TestSessionsRejectCrossUserRevocationSuspensionExpiryAndRotation(t *testing.T) {
	db := openSecurityDB(t)
	service := accounts.NewService(accounts.NewSQLRepository(db, accounts.DialectSQLite))
	ctx := context.Background()
	userOne := registerVerified(t, db, service, "one@example.test")
	userTwo := registerVerified(t, db, service, "two@example.test")
	one, err := service.CreateSession(ctx, userOne.ID, "phone", "test-agent")
	if err != nil {
		t.Fatal(err)
	}

	if err := service.RevokeSession(ctx, userTwo.ID, one.Session.ID); !errors.Is(err, accounts.ErrInvalidSession) {
		t.Fatalf("cross-user revoke error = %v, want invalid session", err)
	}
	if _, _, err := service.ValidateSession(ctx, one.Token); err != nil {
		t.Fatalf("cross-user attempt affected owner session: %v", err)
	}

	for _, status := range []string{"disabled", "suspended"} {
		mustExec(t, db, `UPDATE users SET account_status=? WHERE id=?`, status, userOne.ID)
		if _, _, err := service.ValidateSession(ctx, one.Token); !errors.Is(err, accounts.ErrInvalidSession) {
			t.Fatalf("%s account session error = %v, want invalid session", status, err)
		}
	}
	mustExec(t, db, `UPDATE users SET account_status='active' WHERE id=?`, userOne.ID)
	mustExec(t, db, `UPDATE user_sessions SET expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Minute), one.Session.ID)
	if _, _, err := service.ValidateSession(ctx, one.Token); !errors.Is(err, accounts.ErrInvalidSession) {
		t.Fatalf("expired session error = %v, want invalid session", err)
	}

	revoked, err := service.CreateSession(ctx, userOne.ID, "tablet", "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RevokeSession(ctx, userOne.ID, revoked.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ValidateSession(ctx, revoked.Token); !errors.Is(err, accounts.ErrInvalidSession) {
		t.Fatalf("revoked session error = %v, want invalid session", err)
	}

	rotated, err := service.CreateSession(ctx, userOne.ID, "browser", "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ChangePassword(ctx, userOne.ID, testPassword, "Another-Good-Password-9"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ValidateSession(ctx, rotated.Token); !errors.Is(err, accounts.ErrInvalidSession) {
		t.Fatalf("password-rotated session error = %v, want invalid session", err)
	}
}

func TestOneTimeAccountTokensRejectReplacementReuseAndExpiry(t *testing.T) {
	db := openSecurityDB(t)
	service := accounts.NewService(accounts.NewSQLRepository(db, accounts.DialectSQLite))
	ctx := context.Background()
	account, err := service.Register(ctx, "tokens@example.test", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.IssueEmailVerification(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.IssueEmailVerification(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.VerifyEmail(ctx, first.Token); !errors.Is(err, accounts.ErrInvalidToken) {
		t.Fatalf("replaced token error = %v, want invalid token", err)
	}
	if err := service.VerifyEmail(ctx, second.Token); err != nil {
		t.Fatal(err)
	}
	if err := service.VerifyEmail(ctx, second.Token); !errors.Is(err, accounts.ErrInvalidToken) {
		t.Fatalf("reused token error = %v, want invalid token", err)
	}

	reset, _, err := service.IssuePasswordReset(ctx, account.Email)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE auth_tokens SET expires_at=? WHERE user_id=? AND purpose='password_reset'`, time.Now().UTC().Add(-time.Minute), account.ID)
	if err := service.ResetPassword(ctx, reset.Token, "New-Password-For-Expiry-9"); !errors.Is(err, accounts.ErrInvalidToken) {
		t.Fatalf("expired reset token error = %v, want invalid token", err)
	}
}

func TestAdminRBACAndPasswordOnlyMFABypassAreDenied(t *testing.T) {
	checks := []struct {
		role       admin.Role
		permission admin.Permission
		allowed    bool
	}{
		{admin.RoleSupportAdmin, admin.PermissionManageUsers, true},
		{admin.RoleSupportAdmin, admin.PermissionManageBilling, false},
		{admin.RoleBillingAdmin, admin.PermissionManageBilling, true},
		{admin.RoleBillingAdmin, admin.PermissionManageUsers, false},
		{admin.RoleAnalyst, admin.PermissionReadAnalytics, true},
		{admin.RoleAnalyst, admin.PermissionManagePolicy, false},
	}
	for _, check := range checks {
		if got := admin.Can(check.role, check.permission); got != check.allowed {
			t.Errorf("Can(%q, %q) = %t, want %t", check.role, check.permission, got, check.allowed)
		}
	}

	mfa := admin.NewMFAAuthService(admin.NewSQLAuthStore(nil, admin.DialectSQLite), time.Hour, "test-only-encryption-secret")
	if _, err := mfa.Login(context.Background(), "admin@example.test", "password"); !errors.Is(err, admin.ErrMFARequired) {
		t.Fatalf("password-only MFA login error = %v, want MFA required", err)
	}

	db := openSecurityDB(t)
	accountsService := accounts.NewService(accounts.NewSQLRepository(db, accounts.DialectSQLite))
	account := registerVerified(t, db, accountsService, "support-admin@example.test")
	mustExec(t, db, `UPDATE users SET role='support_admin' WHERE id=?`, account.ID)
	auth := admin.NewAuthService(admin.NewSQLAuthStore(db, admin.DialectSQLite), time.Hour)
	rawToken := "support-admin-invariant-session-token"
	tokenDigest := sha256.Sum256([]byte(rawToken))
	mustExec(t, db, `INSERT INTO user_sessions(user_id,token_hash,public_id,session_type,expires_at,last_seen_at) VALUES(?,?,?,'admin',?,?)`,
		account.ID, hex.EncodeToString(tokenDigest[:]), "support-admin-invariant-session", time.Now().UTC().Add(time.Hour), time.Now().UTC())
	mustExec(t, db, `UPDATE users SET account_status='suspended' WHERE id=?`, account.ID)
	if _, err := auth.ValidateSession(context.Background(), rawToken); !errors.Is(err, admin.ErrInvalidSession) {
		t.Fatalf("suspended admin session error = %v, want invalid session", err)
	}
}

func TestAuthEndpointsAreRateLimitedAndResetDoesNotEnumerateAccounts(t *testing.T) {
	db := openSecurityDB(t)
	service := accounts.NewService(accounts.NewSQLRepository(db, accounts.DialectSQLite))
	registerVerified(t, db, service, "known@example.test")

	resetHandler := newAccountHandler(db, service)
	known := serve(resetHandler, http.MethodPost, "/v1/auth/password-reset/request", []byte(`{"email":"known@example.test"}`), jsonHeaders())
	unknown := serve(resetHandler, http.MethodPost, "/v1/auth/password-reset/request", []byte(`{"email":"unknown@example.test"}`), jsonHeaders())
	if known.Code != http.StatusAccepted || unknown.Code != known.Code || known.Body.String() != unknown.Body.String() {
		t.Fatalf("reset responses differ: known=%d %q unknown=%d %q", known.Code, known.Body.String(), unknown.Code, unknown.Body.String())
	}
	var resetTokens int
	if err := db.QueryRow(`SELECT COUNT(*) FROM auth_tokens WHERE purpose='password_reset'`).Scan(&resetTokens); err != nil || resetTokens != 1 {
		t.Fatalf("known-account reset tokens: count=%d err=%v", resetTokens, err)
	}

	cases := []struct {
		name  string
		path  string
		body  []byte
		limit int
	}{
		{"login", "/v1/auth/login", []byte(`{}`), 10},
		{"reset", "/v1/auth/password-reset/request", []byte(`{"email":"nobody@example.test"}`), 2},
		{"register", "/v1/auth/register", []byte(`{"website":"bot","accept_terms":true}`), 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := newAccountHandler(db, service)
			for attempt := 1; attempt <= tc.limit+1; attempt++ {
				response := serve(handler, http.MethodPost, tc.path, tc.body, jsonHeaders())
				if attempt <= tc.limit && response.Code == http.StatusTooManyRequests {
					t.Fatalf("attempt %d was limited before configured boundary", attempt)
				}
				if attempt == tc.limit+1 {
					assertResponse(t, response, http.StatusTooManyRequests, `"rate_limited"`)
				}
			}
		})
	}

	loginHandler := newAccountHandler(db, service)
	for attempt := 1; attempt <= 10; attempt++ {
		response := serveFrom(loginHandler, http.MethodPost, "/v1/auth/login", []byte(`{"email":"known@example.test","password":"wrong-password"}`), jsonHeaders(), "198.51.100.20:43210")
		if response.Code == http.StatusTooManyRequests {
			t.Fatalf("targeted failure %d was limited before the configured boundary", attempt)
		}
	}
	valid := serveFrom(loginHandler, http.MethodPost, "/v1/auth/login", []byte(`{"email":"known@example.test","password":"`+testPassword+`"}`), jsonHeaders(), "203.0.113.30:43210")
	assertResponse(t, valid, http.StatusOK, `"authenticated"`)
}

func TestProductionCookiesCSRFAndCrossOriginResponsePolicy(t *testing.T) {
	db := openSecurityDB(t)
	service := accounts.NewService(accounts.NewSQLRepository(db, accounts.DialectSQLite))
	registerVerified(t, db, service, "cookie@example.test")
	handler := newAccountHandler(db, service)
	login := serve(handler, http.MethodPost, "/v1/auth/login", []byte(`{"email":"cookie@example.test","password":"`+testPassword+`"}`), map[string]string{"Content-Type": "application/json", "Origin": "https://hostile.example"})
	assertResponse(t, login, http.StatusOK, `"authenticated"`)
	if value := login.Header().Get("Access-Control-Allow-Origin"); value != "" {
		t.Fatalf("hostile origin was allowed: %q", value)
	}
	var sessionCookie, csrfCookie *http.Cookie
	for _, cookie := range login.Result().Cookies() {
		switch cookie.Name {
		case "sokalens_session":
			sessionCookie = cookie
		case "sokalens_csrf":
			csrfCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.Secure || !sessionCookie.HttpOnly || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie is not production-safe: %+v", sessionCookie)
	}
	if csrfCookie == nil || !csrfCookie.Secure || csrfCookie.HttpOnly || csrfCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("CSRF cookie has unexpected flags: %+v", csrfCookie)
	}

	missing := requestWithCookies(handler, http.MethodPost, "/v1/auth/logout", nil, []*http.Cookie{sessionCookie}, nil)
	assertResponse(t, missing, http.StatusForbidden, `"csrf_invalid"`)
	mismatch := requestWithCookies(handler, http.MethodPost, "/v1/auth/logout", nil, []*http.Cookie{sessionCookie, csrfCookie}, map[string]string{"X-CSRF-Token": "wrong"})
	assertResponse(t, mismatch, http.StatusForbidden, `"csrf_invalid"`)
	valid := requestWithCookies(handler, http.MethodPost, "/v1/auth/logout", nil, []*http.Cookie{sessionCookie, csrfCookie}, map[string]string{"X-CSRF-Token": csrfCookie.Value})
	if valid.Code != http.StatusNoContent {
		t.Fatalf("valid CSRF logout status = %d, want 204; body=%s", valid.Code, valid.Body.String())
	}
}

func TestWebhookBoundaryRequiresSignatureFreshnessAndRepositoryIdempotency(t *testing.T) {
	repo := &webhookRepository{events: map[string]int64{}}
	service := billing.NewService(billing.Options{Repository: repo, Provider: "testpay", Environment: "test", Mode: "sandbox", WebhookSecret: "test-webhook-secret"})
	payload := []byte(`{"type":"subscription.active"}`)
	now := time.Now().UTC()
	signature := webhookSignature("test-webhook-secret", payload)

	if _, err := service.AcceptWebhook(context.Background(), "event-1", "subscription.active", "bad", now, payload); !errors.Is(err, billing.ErrInvalidWebhook) {
		t.Fatalf("bad signature error = %v, want invalid webhook", err)
	}
	if _, err := service.AcceptWebhook(context.Background(), "event-old", "subscription.active", signature, now.Add(-25*time.Hour), payload); !errors.Is(err, billing.ErrInvalidWebhook) {
		t.Fatalf("stale webhook error = %v, want invalid webhook", err)
	}
	first, err := service.AcceptWebhook(context.Background(), "event-1", "subscription.active", signature, now, payload)
	if err != nil || first.Duplicate {
		t.Fatalf("first webhook = %+v, %v", first, err)
	}
	duplicate, err := service.AcceptWebhook(context.Background(), "event-1", "subscription.active", signature, now, payload)
	if err != nil || !duplicate.Duplicate || duplicate.ID != first.ID {
		t.Fatalf("duplicate webhook = %+v, %v; want duplicate receipt for %d", duplicate, err, first.ID)
	}
}

type webhookRepository struct {
	events map[string]int64
}

func (r *webhookRepository) ListPlans(context.Context, string, string) ([]billing.Plan, error) {
	return nil, nil
}
func (r *webhookRepository) SubscriptionForUser(context.Context, int64) (billing.Subscription, error) {
	return billing.Subscription{}, billing.ErrSubscriptionNotFound
}
func (r *webhookRepository) ScheduleCancellation(context.Context, int64, time.Time) (billing.Subscription, error) {
	return billing.Subscription{}, billing.ErrSubscriptionNotFound
}
func (r *webhookRepository) StoreVerifiedWebhook(_ context.Context, provider, environment, eventID, _ string, _ time.Time, _ []byte) (billing.WebhookReceipt, error) {
	key := provider + ":" + environment + ":" + eventID
	if id, ok := r.events[key]; ok {
		return billing.WebhookReceipt{ID: id, Duplicate: true}, nil
	}
	id := int64(len(r.events) + 1)
	r.events[key] = id
	return billing.WebhookReceipt{ID: id}, nil
}

func webhookSignature(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func newAccountHandler(db *sql.DB, service *accounts.Service) http.Handler {
	return httpserver.New(httpserver.ServerOptions{
		Config:        config.Config{Environment: "production", PublicWebURL: "https://sokalens.example"},
		Accounts:      service,
		Access:        access.NewSQLRepository(db, access.DialectSQLite),
		Notifications: usernotifications.NewService(db, "test-notification-secret"),
	})
}

func serve(handler http.Handler, method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	return serveFrom(handler, method, path, body, headers, "192.0.2.10:43210")
}

func serveFrom(handler http.Handler, method, path string, body []byte, headers map[string]string, remoteAddr string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.RemoteAddr = remoteAddr
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func requestWithCookies(handler http.Handler, method, path string, body []byte, cookies []*http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.RemoteAddr = "192.0.2.11:43210"
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func jsonHeaders() map[string]string { return map[string]string{"Content-Type": "application/json"} }

func assertResponse(t *testing.T, response *httptest.ResponseRecorder, status int, fragment string) {
	t.Helper()
	if response.Code != status || !strings.Contains(response.Body.String(), fragment) {
		t.Fatalf("response = %d %q, want %d containing %q", response.Code, response.Body.String(), status, fragment)
	}
}

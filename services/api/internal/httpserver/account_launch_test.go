package httpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/accounts"
)

func TestFavoriteResponseLinksEverySupportedEntityType(t *testing.T) {
	tests := map[string]string{
		"match": "/matches/42", "team": "/teams/arsenal", "league": "/leagues/premier-league",
		"competition": "/leagues/champions-league", "country": "/leagues", "market": "/predictions/markets/over-2-5",
	}
	for entityType, want := range tests {
		got := favoriteResponse(accounts.Favorite{EntityType: entityType, EntityID: map[string]string{
			"match": "42", "team": "arsenal", "league": "premier-league", "competition": "champions-league", "country": "ke", "market": "over-2-5",
		}[entityType]})["href"]
		if got != want {
			t.Fatalf("%s favorite href=%q want=%q", entityType, got, want)
		}
	}
}

func TestTrustedClientIPOnlyHonorsInternalProxyHeader(t *testing.T) {
	internal := httptest.NewRequest(http.MethodPost, "/v1/auth/register", nil)
	internal.RemoteAddr = "172.20.0.4:4567"
	internal.Header.Set("X-SokaLens-Client-IP", "203.0.113.42")
	if got := trustedClientIP(internal); got != "203.0.113.42" {
		t.Fatalf("internal forwarded IP=%q", got)
	}
	public := httptest.NewRequest(http.MethodPost, "/v1/auth/register", nil)
	public.RemoteAddr = "198.51.100.12:4567"
	public.Header.Set("X-SokaLens-Client-IP", "203.0.113.99")
	if got := trustedClientIP(public); got != "198.51.100.12" {
		t.Fatalf("public spoofed IP was trusted: %q", got)
	}
}

func TestAuthLimiterDoesNotGrowIdentityBucketsAfterClientDenial(t *testing.T) {
	deps := accountDependencies{authLimiter: newFixedWindowLimiter(1, time.Minute)}
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/register", nil)
	request.RemoteAddr = "198.51.100.12:4567"
	if !deps.allowAuth(request, "register", "first@example.test") {
		t.Fatal("first request was unexpectedly denied")
	}
	if deps.allowAuth(request, "register", "second@example.test") {
		t.Fatal("second client request was unexpectedly allowed")
	}
	if got := len(deps.authLimiter.entries); got != 2 {
		t.Fatalf("rate limiter entries=%d want=2", got)
	}
}

func TestLoginFailuresDoNotLockOutValidCredentialsFromAnotherClient(t *testing.T) {
	deps := accountDependencies{authLimiter: newFixedWindowLimiter(1, time.Minute)}
	attacker := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
	attacker.RemoteAddr = "198.51.100.12:4567"
	if !deps.allowAuthClient(attacker, "login") || !deps.allowAuthIdentity("login", "Victim <victim@example.test>") {
		t.Fatal("first failed login was unexpectedly denied")
	}
	victim := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
	victim.RemoteAddr = "203.0.113.44:4567"
	if !deps.allowAuthClient(victim, "login") {
		t.Fatal("valid login from another client was locked out")
	}
	if deps.allowAuthIdentity("login", "victim@example.test") {
		t.Fatal("canonical identity failure bucket did not reach its limit")
	}
}

func TestAuthLimitEmailCanonicalizationMatchesLogin(t *testing.T) {
	for _, input := range []string{"Victim@Example.Test", " Victim@example.test ", "Person <victim@example.test>"} {
		if got := normalizedAuthLimitIdentity("login", input); got != "victim@example.test" {
			t.Fatalf("normalized identity for %q=%q", input, got)
		}
	}
}

func TestFixedWindowLimiterHasHardEntryCap(t *testing.T) {
	limiter := newFixedWindowLimiter(1, time.Minute)
	now := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 4096; index++ {
		if !limiter.allow(fmt.Sprintf("key-%d", index), now) {
			t.Fatalf("entry %d was denied before cap", index)
		}
	}
	if limiter.allow("overflow", now) || len(limiter.entries) != 4096 {
		t.Fatalf("hard cap failed: entries=%d", len(limiter.entries))
	}
}

func TestTurnstileRequiresExpectedActionAndHostname(t *testing.T) {
	tests := []struct {
		name           string
		payload        string
		expectedAction string
		want           bool
	}{
		{"valid registration", `{"success":true,"action":"register","hostname":"sokalens.com"}`, "register", true},
		{"valid login", `{"success":true,"action":"login","hostname":"sokalens.com"}`, "login", true},
		{"wrong action", `{"success":true,"action":"login","hostname":"sokalens.com"}`, "register", false},
		{"wrong hostname", `{"success":true,"action":"register","hostname":"evil.example"}`, "register", false},
		{"provider rejected", `{"success":false,"action":"register","hostname":"sokalens.com"}`, "register", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, tc.payload)
			}))
			defer server.Close()
			deps := accountDependencies{turnstileSecret: "test-secret", turnstileRequired: true, turnstileURL: server.URL, turnstileHostname: "sokalens.com"}
			request := httptest.NewRequest(http.MethodPost, "/v1/auth/register", nil)
			request.RemoteAddr = "192.0.2.1:1234"
			got, err := deps.verifyTurnstile(request, "client-token", tc.expectedAction)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("verified=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestTurnstileOmitsPrivateProxyIP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.Form.Get("remoteip"); got != "" {
			t.Fatalf("private proxy IP was sent to Siteverify: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"success":true,"action":"login","hostname":"sokalens.com"}`)
	}))
	defer server.Close()
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
	request.RemoteAddr = "172.20.0.4:4567"
	deps := accountDependencies{turnstileSecret: "test-secret", turnstileRequired: true, turnstileURL: server.URL, turnstileHostname: "sokalens.com"}
	if ok, err := deps.verifyTurnstile(request, "client-token", "login"); err != nil || !ok {
		t.Fatalf("verified=%v err=%v", ok, err)
	}
}

func TestTurnstileOptionalAndRequiredConfiguration(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/register", nil)
	if ok, err := (accountDependencies{}).verifyTurnstile(request, "", "register"); err != nil || !ok {
		t.Fatalf("unconfigured optional Turnstile ok=%v err=%v", ok, err)
	}
	if ok, err := (accountDependencies{turnstileRequired: true}).verifyTurnstile(request, "", "register"); err != nil || ok {
		t.Fatalf("required unconfigured Turnstile ok=%v err=%v", ok, err)
	}
}

func TestPreferredReferrerHostAcceptsOnlyBoundedHostnames(t *testing.T) {
	if got := preferredReferrerHost("News.Example.COM", "https://fallback.example/register"); got != "news.example.com" {
		t.Fatalf("valid first-touch hostname=%q", got)
	}
	for _, invalid := range []string{"https://evil.example/path", "evil.example/register", "evil.example:443", "-evil.example", "evil..example"} {
		if got := preferredReferrerHost(invalid, "https://fallback.example/register"); got != "fallback.example" {
			t.Fatalf("invalid input %q yielded %q", invalid, got)
		}
	}
	if got := preferredReferrerHost(strings.Repeat("a", 254), "https://fallback.example/register"); got != "fallback.example" {
		t.Fatalf("oversized input yielded %q", got)
	}
}

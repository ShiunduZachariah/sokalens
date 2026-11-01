package httpserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/access"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/accounts"
	analysisdomain "github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/analysis"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/billing"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/emaillinks"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/locales"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/recommendations"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/usernotifications"
)

const (
	userSessionCookie = "sokalens_session"
	csrfCookie        = "sokalens_csrf"
	guestCookie       = "sokalens_guest"
	visitorCookie     = "sokalens_visitor"
	localeCookie      = "sokalens_locale"
	legalVersion      = "2026-08-24"
)

type accountDependencies struct {
	accounts             *accounts.Service
	access               *access.SQLRepository
	guest                *access.GuestService
	recommendations      *recommendations.Service
	billing              billing.Service
	notifications        usernotifications.Service
	analysis             analysisdomain.Service
	predictions          PredictionSource
	matches              MatchSource
	catalog              CatalogSource
	ads                  AdsSource
	adRouting            AdRoutingService
	publicWebURL         string
	secureCookies        bool
	guestLimit           int
	authLimiter          *fixedWindowLimiter
	verificationLimiter  *fixedWindowLimiter
	passwordResetLimiter *fixedWindowLimiter
	shareLimiter         *fixedWindowLimiter
	turnstileSecret      string
	turnstileRequired    bool
	turnstileURL         string
	turnstileHostname    string
}

type savedPredictionSource interface {
	GetSavedPrediction(context.Context, int64) (PredictionDetail, error)
}

func (d accountDependencies) bestBetsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		principal, ok := d.requireUser(w, r, false)
		if !ok {
			return
		}
		if !principal.access.Allows(access.CapabilityAnalysisStandard) {
			writeJSON(w, http.StatusForbidden, apiError("standard_analysis_required", "A verified SokaLens account is required."))
			return
		}
		if d.predictions == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("best_bets_unavailable", "Recommendations are temporarily unavailable."))
			return
		}
		minimum := 65.0
		if raw := r.URL.Query().Get("confidence"); raw != "" {
			if parsed, err := strconv.ParseFloat(raw, 64); err == nil {
				minimum = min(90, max(55, parsed))
			}
		}
		items, err := d.predictions.ListTodayPredictions(r.Context(), PredictionFilter{MinimumConfidence: &minimum, BestBetsOnly: true, Limit: boundedQueryLimit(r, 25, 50)})
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("best_bets_unavailable", "Recommendations are temporarily unavailable."))
			return
		}
		predictions := make([]map[string]any, 0, len(items))
		for _, item := range items {
			predictions = append(predictions, predictionSummaryResponse(item))
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{"predictions": predictions, "tier": principal.access.Principal.Tier})
	}
}

type userPrincipal struct {
	account accounts.Account
	session accounts.Session
	access  access.Context
}

type fixedWindowLimiter struct {
	mu      sync.Mutex
	entries map[string]limitEntry
	limit   int
	window  time.Duration
}

type limitEntry struct {
	count int
	start time.Time
}

func newFixedWindowLimiter(limit int, window time.Duration) *fixedWindowLimiter {
	return &fixedWindowLimiter{entries: make(map[string]limitEntry), limit: limit, window: window}
}

func (l *fixedWindowLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[key]
	if entry.start.IsZero() || now.Sub(entry.start) >= l.window {
		if len(l.entries) >= 4096 {
			for existingKey, existing := range l.entries {
				if now.Sub(existing.start) >= l.window {
					delete(l.entries, existingKey)
				}
			}
		}
		if entry.start.IsZero() && len(l.entries) >= 4096 {
			return false
		}
		l.entries[key] = limitEntry{count: 1, start: now}
		return true
	}
	if entry.count >= l.limit {
		return false
	}
	entry.count++
	l.entries[key] = entry
	return true
}

func (d accountDependencies) accessHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if principal, ok := d.optionalUser(r); ok {
			csrf := ensureCSRFCookie(w, r, d.secureCookies)
			writeJSON(w, http.StatusOK, accessResponse(principal, csrf))
			return
		}
		token, _, views, err := d.ensureGuest(w, r)
		if err != nil {
			slog.Error("guest access unavailable", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, apiError("guest_access_unavailable", "Guest access is temporarily unavailable."))
			return
		}
		_ = token
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": false, "tier": "guest", "ads_allowed": true,
			"email_verified": false, "entitlements": []string{access.CapabilityPublicRead},
			"guest_analysis_limit": d.guestLimit, "guest_analysis_remaining": views.Remaining,
			"principal": map[string]string{"type": "guest"},
		})
	}
}

func (d accountDependencies) registerHandler() http.HandlerFunc {
	type request struct {
		Email             string `json:"email"`
		Password          string `json:"password"`
		DisplayName       string `json:"display_name"`
		AcceptTerms       bool   `json:"accept_terms"`
		MarketingConsent  bool   `json:"marketing_consent"`
		CountryCode       string `json:"country_code"`
		PreferredLanguage string `json:"preferred_language"`
		GuestLanguage     string `json:"guest_language"`
		UTMSource         string `json:"utm_source"`
		UTMMedium         string `json:"utm_medium"`
		UTMCampaign       string `json:"utm_campaign"`
		UTMContent        string `json:"utm_content"`
		LandingPage       string `json:"landing_page"`
		SignupPage        string `json:"signup_page"`
		ReferrerHost      string `json:"referrer_host"`
		TurnstileToken    string `json:"turnstile_token"`
		Website           string `json:"website"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var input request
		if err := decodeJSON(w, r, &input); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_request", "Check the registration details and try again."))
			return
		}
		if input.Website != "" {
			if !d.allowAuth(r, "register", input.Email) {
				writeJSON(w, http.StatusTooManyRequests, apiError("rate_limited", "Please wait before trying again."))
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "verification_required"})
			return
		}
		if !d.allowAuth(r, "register", input.Email) {
			writeJSON(w, http.StatusTooManyRequests, apiError("rate_limited", "Please wait before trying again."))
			return
		}
		ok, err := d.verifyTurnstile(r, input.TurnstileToken, "register")
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("bot_verification_unavailable", "Security verification is temporarily unavailable. Please try again."))
			return
		}
		if !ok {
			writeJSON(w, http.StatusForbidden, apiError("bot_challenge_failed", "Complete the security check and try again."))
			return
		}
		if !input.AcceptTerms {
			writeJSON(w, http.StatusBadRequest, apiError("terms_required", "Terms and privacy acceptance is required."))
			return
		}
		country := registrationCountry(input.CountryCode, r)
		if country == "" {
			writeJSON(w, http.StatusBadRequest, apiError("country_required", "Choose your country to continue."))
			return
		}
		if strings.TrimSpace(input.PreferredLanguage) != "" {
			if _, ok := locales.Normalize(input.PreferredLanguage); !ok {
				writeJSON(w, http.StatusBadRequest, apiError("unsupported_language", "Choose a supported language."))
				return
			}
		}
		guestLanguage := input.GuestLanguage
		if guestLanguage == "" {
			if cookie, err := r.Cookie(localeCookie); err == nil {
				guestLanguage = cookie.Value
			}
		}
		language := locales.Resolve(locales.Inputs{
			Selected: input.PreferredLanguage, Guest: guestLanguage,
			Browser: r.Header.Get("Accept-Language"), CountryCode: country,
		})
		visitorID := ""
		if cookie, err := r.Cookie(visitorCookie); err == nil {
			visitorID = cookie.Value
		}
		result, err := d.accounts.RegisterPending(r.Context(), accounts.Registration{
			Email: input.Email, Password: input.Password, DisplayName: input.DisplayName,
			CountryCode: country, PreferredLanguage: language.Locale, LanguageSource: language.Source,
			MarketingConsent: false, LegalVersion: legalVersion,
			DeviceName: deviceSummary(r.UserAgent()), UserAgent: boundedText(r.UserAgent(), 512),
			Acquisition: accounts.Acquisition{
				VisitorID: visitorID, CountryCode: country, ReferrerHost: preferredReferrerHost(input.ReferrerHost, r.Referer()),
				UTMSource: boundedText(input.UTMSource, 120), UTMMedium: boundedText(input.UTMMedium, 120),
				UTMCampaign: boundedText(input.UTMCampaign, 160), UTMContent: boundedText(input.UTMContent, 160),
				LandingPage: safePath(input.LandingPage), SignupPage: safePath(input.SignupPage),
				DeviceType: deviceType(r.UserAgent()), Platform: "web",
			},
		})
		if errors.Is(err, accounts.ErrEmailExists) {
			writeJSON(w, http.StatusConflict, apiError("email_already_registered", "An account already exists with this email. Sign in or reset your password."))
			return
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("registration_failed", err.Error()))
			return
		}
		setCookie(w, userSessionCookie, result.Credential.Token, result.Credential.Session.ExpiresAt, true, d.secureCookies)
		csrf := ensureCSRFCookie(w, r, d.secureCookies)
		if err := d.queueAuthEmail(r, result.Verification.UserID, "email_verification", result.Verification.Token, result.Account.Email); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("verification_delivery_unavailable", "Your account is ready, but the verification email could not be queued. Request a fresh link shortly."))
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"status": "pending_verification", "authenticated": true, "email": result.Account.Email, "email_verified": false, "preferred_language": language.Locale, "csrf_token": csrf})
	}
}

func (d accountDependencies) growthEventsHandler() http.HandlerFunc {
	type request struct {
		EventType  string `json:"event_type"`
		Source     string `json:"source"`
		Locale     string `json:"locale"`
		Step       string `json:"step"`
		ReasonCode string `json:"reason_code"`
		Website    string `json:"website"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if d.accounts == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("growth_event_unavailable", "Event tracking is temporarily unavailable."))
			return
		}
		if !d.allowAuth(r, "growth-event") {
			// Funnel telemetry is best-effort. Keep the write limiter, but drop
			// excess events without turning analytics noise into a user-visible
			// 429 or an operational error-rate signal.
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var input request
		if err := decodeJSON(w, r, &input); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_growth_event", "Event details are invalid."))
			return
		}
		if input.Website != "" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		visitorID := ""
		if visitor, err := r.Cookie(visitorCookie); err == nil {
			visitorID = visitor.Value
		}
		if visitorID == "" {
			visitorID = randomHex(24)
			setCookie(w, visitorCookie, visitorID, time.Now().UTC().Add(30*24*time.Hour), true, d.secureCookies)
		}
		metadata, _ := json.Marshal(map[string]string{
			"locale":      boundedText(input.Locale, 16),
			"step":        boundedText(input.Step, 64),
			"reason_code": boundedText(input.ReasonCode, 64),
		})
		err := d.accounts.RecordGrowthEvent(r.Context(), accounts.GrowthEvent{
			VisitorID: visitorID, EventType: input.EventType,
			CountryCode: registrationCountry("", r), Platform: "web",
			Source: boundedText(input.Source, 64), Metadata: metadata,
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_growth_event", "Event details are invalid."))
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

func (d accountDependencies) localesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		savedLanguage := ""
		if principal, ok := d.optionalUser(r); ok {
			if profile, err := d.accounts.GetProfile(r.Context(), principal.account.ID); err == nil {
				savedLanguage = profile.Language
			}
		}
		guestLanguage := ""
		if cookie, err := r.Cookie(localeCookie); err == nil {
			guestLanguage = cookie.Value
		}
		country := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("country")))
		if country == "" {
			country = strings.ToUpper(strings.TrimSpace(r.Header.Get("CF-IPCountry")))
		}
		resolution := locales.Resolve(locales.Inputs{
			Selected: r.URL.Query().Get("selected"), Saved: savedLanguage,
			Guest: guestLanguage, Browser: r.Header.Get("Accept-Language"), CountryCode: country,
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"supported": locales.Supported(), "resolved": resolution,
			"fallback": locales.Default, "country_is_hint": true,
		})
	}
}

func (d accountDependencies) capabilitiesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		principal, ok := d.requireUser(w, r, false)
		if !ok {
			return
		}
		productKeys := map[string]bool{
			access.CapabilityPublicRead: true, access.CapabilityAnalysisStandard: true,
			access.CapabilityAnalysisAdvanced: true, access.CapabilityAdsHidden: true,
			access.CapabilityPersonalRecommendations: true, access.CapabilityNotificationDigest: true,
			access.CapabilityNotificationRealtime: true, access.CapabilityExpandedHistory: true,
		}
		roles := make([]string, 0, len(principal.access.Principal.Roles))
		for _, role := range principal.access.Principal.Roles {
			if role != "basic" && role != "pro" {
				roles = append(roles, role)
			}
		}
		entitlements := []string{}
		permissions := []string{}
		for _, capability := range principal.access.CapabilityNames() {
			if productKeys[capability] {
				entitlements = append(entitlements, capability)
			} else {
				permissions = append(permissions, capability)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"roles": roles, "permissions": permissions, "entitlements": entitlements})
	}
}

func (d accountDependencies) loginHandler() http.HandlerFunc {
	type request struct {
		Email          string `json:"email"`
		Password       string `json:"password"`
		DeviceName     string `json:"device_name"`
		TurnstileToken string `json:"turnstile_token"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var input request
		if err := decodeJSON(w, r, &input); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_request", "Check your credentials and try again."))
			return
		}
		if !d.allowAuthClient(r, "login") {
			writeJSON(w, http.StatusTooManyRequests, apiError("rate_limited", "Please wait before trying again."))
			return
		}
		ok, err := d.verifyTurnstile(r, input.TurnstileToken, "login")
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("bot_verification_unavailable", "Security verification is temporarily unavailable. Please try again."))
			return
		}
		if !ok {
			writeJSON(w, http.StatusForbidden, apiError("bot_challenge_failed", "Complete the security check and try again."))
			return
		}
		account, err := d.accounts.Authenticate(r.Context(), input.Email, input.Password)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, apiError("invalid_credentials", "Email or password is incorrect."))
			return
		}
		credential, err := d.accounts.CreateSession(r.Context(), account.ID, input.DeviceName, r.UserAgent())
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("session_unavailable", "A secure session could not be created."))
			return
		}
		setCookie(w, userSessionCookie, credential.Token, credential.Session.ExpiresAt, true, d.secureCookies)
		csrf := ensureCSRFCookie(w, r, d.secureCookies)
		writeJSON(w, http.StatusOK, map[string]any{"status": "authenticated", "csrf_token": csrf})
	}
}

func (d accountDependencies) logoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		principal, ok := d.requireUser(w, r, true)
		if !ok {
			return
		}
		_ = d.accounts.RevokeSession(r.Context(), principal.account.ID, principal.session.ID)
		clearCookie(w, userSessionCookie, true, d.secureCookies)
		clearCookie(w, csrfCookie, false, d.secureCookies)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d accountDependencies) verificationRequestHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var input struct {
			Email string `json:"email"`
		}
		if decodeJSON(w, r, &input) != nil {
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
			return
		}
		if !d.allowAuth(r, "verify", input.Email) {
			writeJSON(w, http.StatusTooManyRequests, apiError("rate_limited", "Please wait before trying again."))
			return
		}
		if d.verificationLimiter != nil && !d.verificationLimiter.allow("email:"+hashTokenForLimit(strings.ToLower(strings.TrimSpace(input.Email))), time.Now().UTC()) {
			writeJSON(w, http.StatusTooManyRequests, apiError("rate_limited", "Please wait before requesting another link."))
			return
		}
		if input.Email != "" {
			if issued, account, err := d.accounts.IssueEmailVerificationForEmail(r.Context(), input.Email); err == nil && issued.Token != "" {
				_ = d.queueAuthEmail(r, account.ID, "email_verification", issued.Token, account.Email)
			}
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	}
}

func (d accountDependencies) verificationConfirmHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var input struct {
			Token string `json:"token"`
		}
		if decodeJSON(w, r, &input) != nil || d.accounts.VerifyEmail(r.Context(), input.Token) != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_token", "This verification link is invalid or expired."))
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "verified"})
	}
}

func (d accountDependencies) passwordResetRequestHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var input struct {
			Email string `json:"email"`
		}
		if decodeJSON(w, r, &input) != nil {
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
			return
		}
		if !d.allowAuth(r, "reset", input.Email) {
			writeJSON(w, http.StatusTooManyRequests, apiError("rate_limited", "Please wait before trying again."))
			return
		}
		if d.passwordResetLimiter != nil && !d.passwordResetLimiter.allow("email:"+hashTokenForLimit(strings.ToLower(strings.TrimSpace(input.Email))), time.Now().UTC()) {
			writeJSON(w, http.StatusTooManyRequests, apiError("rate_limited", "Please wait before requesting another reset link."))
			return
		}
		if issued, account, err := d.accounts.IssuePasswordReset(r.Context(), input.Email); err == nil && issued.Token != "" {
			_ = d.queueAuthEmail(r, account.ID, "password_reset", issued.Token, account.Email)
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
	}
}

func (d accountDependencies) passwordResetConfirmHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		var input struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}
		if decodeJSON(w, r, &input) != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_reset", "This reset link or password is invalid."))
			return
		}
		if !d.allowAuth(r, "reset_confirm", input.Token) {
			writeJSON(w, http.StatusTooManyRequests, apiError("rate_limited", "Please wait before trying again."))
			return
		}
		if err := d.accounts.ResetPassword(r.Context(), input.Token, input.Password); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_reset", "This reset link or password is invalid."))
			return
		}
		clearCookie(w, userSessionCookie, true, d.secureCookies)
		clearCookie(w, csrfCookie, false, d.secureCookies)
		writeJSON(w, http.StatusOK, map[string]string{"status": "password_reset"})
	}
}

func (d accountDependencies) meHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := d.requireUser(w, r, r.Method != http.MethodGet)
		if !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			profile, err := d.accounts.GetProfile(r.Context(), principal.account.ID)
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError("profile_unavailable", "Profile is temporarily unavailable."))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"user": accountResponse(principal.account), "profile": profileResponse(profile, principal.account)})
		case http.MethodDelete:
			if err := d.accounts.RequestDeletion(r.Context(), principal.account.ID); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError("deletion_unavailable", "The deletion request could not be recorded."))
				return
			}
			clearCookie(w, userSessionCookie, true, d.secureCookies)
			clearCookie(w, csrfCookie, false, d.secureCookies)
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "deletion_pending"})
		default:
			methodNotAllowed(w)
		}
	}
}

func (d accountDependencies) profileHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			methodNotAllowed(w)
			return
		}
		principal, ok := d.requireUser(w, r, true)
		if !ok {
			return
		}
		current, err := d.accounts.GetProfile(r.Context(), principal.account.ID)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("profile_unavailable", "Profile is temporarily unavailable."))
			return
		}
		var input struct {
			DisplayName            *string `json:"display_name"`
			Timezone               *string `json:"timezone"`
			CountryCode            *string `json:"country_code"`
			OddsFormat             *string `json:"odds_format"`
			Language               *string `json:"language"`
			PreferredLanguage      *string `json:"preferred_language"`
			PersonalizationEnabled *bool   `json:"personalization_enabled"`
			MarketingConsent       *bool   `json:"marketing_consent"`
		}
		if decodeJSON(w, r, &input) != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_request", "Profile settings are invalid."))
			return
		}
		if input.DisplayName != nil {
			current.DisplayName = *input.DisplayName
		}
		if input.Timezone != nil {
			current.Timezone = *input.Timezone
		}
		if input.CountryCode != nil {
			current.CountryCode = *input.CountryCode
		}
		if input.OddsFormat != nil {
			current.OddsFormat = *input.OddsFormat
			selectedAt := time.Now().UTC()
			current.OddsFormatSelectedAt = &selectedAt
		}
		if input.Language != nil {
			current.Language = *input.Language
		}
		if input.PreferredLanguage != nil {
			if input.Language != nil {
				legacy, legacyOK := locales.Normalize(*input.Language)
				preferred, preferredOK := locales.Normalize(*input.PreferredLanguage)
				if !legacyOK || !preferredOK || legacy != preferred {
					writeJSON(w, http.StatusBadRequest, apiError("conflicting_language", "Use preferred_language for one language choice."))
					return
				}
			}
			current.Language = *input.PreferredLanguage
		}
		if normalized, ok := locales.Normalize(current.Language); ok {
			current.Language = normalized
		}
		if input.PersonalizationEnabled != nil {
			current.PersonalizationEnabled = *input.PersonalizationEnabled
		}
		// Product and marketing notifications are dormant for launch. Keep this
		// compatibility field parseable for rolling clients, but never opt a user in.
		current.MarketingConsent = false
		if _, err := time.LoadLocation(current.Timezone); err != nil && current.Timezone != "UTC" {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_timezone", "Use a valid IANA timezone."))
			return
		}
		if err := d.accounts.UpdateProfile(r.Context(), current); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_profile", err.Error()))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"profile": profileResponse(current, principal.account)})
	}
}

func (d accountDependencies) passwordChangeHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		principal, ok := d.requireUser(w, r, true)
		if !ok {
			return
		}
		var input struct {
			CurrentPassword string `json:"current_password"`
			NewPassword     string `json:"new_password"`
		}
		if decodeJSON(w, r, &input) != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_request", "Current and new passwords are required."))
			return
		}
		if err := d.accounts.ChangePassword(r.Context(), principal.account.ID, input.CurrentPassword, input.NewPassword); err != nil {
			if errors.Is(err, accounts.ErrInvalidCredential) {
				writeJSON(w, http.StatusUnauthorized, apiError("invalid_credentials", "The current password is incorrect."))
				return
			}
			writeJSON(w, http.StatusBadRequest, apiError("password_rejected", err.Error()))
			return
		}
		clearCookie(w, userSessionCookie, true, d.secureCookies)
		clearCookie(w, csrfCookie, false, d.secureCookies)
		writeJSON(w, http.StatusOK, map[string]string{"status": "password_changed"})
	}
}

func (d accountDependencies) sessionsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := d.requireUser(w, r, r.Method != http.MethodGet)
		if !ok {
			return
		}
		if r.Method == http.MethodDelete {
			if err := d.accounts.RevokeAllSessions(r.Context(), principal.account.ID); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError("sessions_unavailable", "Sessions could not be revoked."))
				return
			}
			clearCookie(w, userSessionCookie, true, d.secureCookies)
			clearCookie(w, csrfCookie, false, d.secureCookies)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		sessions, err := d.accounts.ListSessions(r.Context(), principal.account.ID)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("sessions_unavailable", "Sessions are temporarily unavailable."))
			return
		}
		items := make([]map[string]any, 0, len(sessions))
		for _, session := range sessions {
			items = append(items, sessionResponse(session, session.ID == principal.session.ID))
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": items})
	}
}

func (d accountDependencies) sessionActionHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			methodNotAllowed(w)
			return
		}
		principal, ok := d.requireUser(w, r, true)
		if !ok {
			return
		}
		idText := strings.TrimPrefix(r.URL.Path, "/v1/me/sessions/")
		var target int64
		for _, session := range mustSessions(d.accounts.ListSessions(r.Context(), principal.account.ID)) {
			if session.PublicID == idText {
				target = session.ID
				break
			}
		}
		if target == 0 {
			writeJSON(w, http.StatusNotFound, apiError("session_not_found", "Session not found."))
			return
		}
		if err := d.accounts.RevokeSession(r.Context(), principal.account.ID, target); err != nil {
			writeJSON(w, http.StatusNotFound, apiError("session_not_found", "Session not found."))
			return
		}
		if target == principal.session.ID {
			clearCookie(w, userSessionCookie, true, d.secureCookies)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d accountDependencies) favoritesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := d.requireUser(w, r, r.Method != http.MethodGet)
		if !ok {
			return
		}
		var input struct {
			EntityType           string `json:"entity_type"`
			EntityID             string `json:"entity_id"`
			Name                 string `json:"name"`
			NotificationsEnabled *bool  `json:"notifications_enabled"`
		}
		switch r.Method {
		case http.MethodGet:
			items, err := d.accounts.ListFavorites(r.Context(), principal.account.ID)
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError("favorites_unavailable", "Favorites are temporarily unavailable."))
				return
			}
			// `view=keys` answers the only question a save button has -- "is
			// this one already saved?" -- and skips the per-favorite catalog
			// fan-out below. Every page that renders save buttons renders
			// hundreds of them, so this runs on ordinary page loads; the
			// context build is for /favorites, which shows match previews.
			if r.URL.Query().Get("view") == "keys" {
				writeJSON(w, http.StatusOK, map[string]any{"favorites": favoriteKeyResponses(items)})
				return
			}
			type favoriteContextResult struct {
				response map[string]any
				expired  bool
			}
			results := make([]favoriteContextResult, len(items))
			sem := make(chan struct{}, 6)
			var wg sync.WaitGroup
			now := time.Now()
			for index, item := range items {
				wg.Add(1)
				go func() {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					response, expired := d.favoriteContextResponse(r.Context(), item, now)
					results[index] = favoriteContextResult{response: response, expired: expired}
				}()
			}
			wg.Wait()
			responses := make([]map[string]any, 0, len(items))
			for index, result := range results {
				if result.expired {
					item := items[index]
					_ = d.accounts.RemoveFavorite(r.Context(), principal.account.ID, item.EntityType, item.EntityID)
				} else {
					responses = append(responses, result.response)
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"favorites": responses})
		case http.MethodPost:
			if decodeJSON(w, r, &input) != nil || d.accounts.AddFavorite(r.Context(), principal.account.ID, input.EntityType, input.EntityID, input.Name) != nil {
				writeJSON(w, http.StatusBadRequest, apiError("invalid_favorite", "This favorite is invalid."))
				return
			}
			writeJSON(w, http.StatusCreated, map[string]string{"status": "saved"})
		case http.MethodPatch:
			if decodeJSON(w, r, &input) != nil || input.NotificationsEnabled == nil {
				writeJSON(w, http.StatusBadRequest, apiError("invalid_favorite", "This favorite could not be updated."))
				return
			}
			if err := d.accounts.UpdateFavoriteNotifications(r.Context(), principal.account.ID, input.EntityType, input.EntityID, *input.NotificationsEnabled); errors.Is(err, accounts.ErrNotificationsDormant) {
				writeJSON(w, http.StatusConflict, apiError("notifications_dormant", "Favorite notifications are not available yet."))
				return
			} else if err != nil {
				writeJSON(w, http.StatusBadRequest, apiError("invalid_favorite", "This favorite could not be updated."))
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
		case http.MethodDelete:
			if decodeJSON(w, r, &input) != nil || d.accounts.RemoveFavorite(r.Context(), principal.account.ID, input.EntityType, input.EntityID) != nil {
				writeJSON(w, http.StatusBadRequest, apiError("invalid_favorite", "This favorite could not be removed."))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			methodNotAllowed(w)
		}
	}
}

const (
	savedMatchResultRetention = 6 * time.Hour
	legacyMatchDuration       = 2 * time.Hour
)

var terminalFavoriteMatchStatuses = map[string]bool{
	"finished": true, "ft": true, "aet": true, "pen": true, "awarded": true,
	"walkover": true, "cancelled": true, "canceled": true, "abandoned": true,
}

var settledFavoriteMatchStatuses = map[string]bool{
	"finished": true, "ft": true, "aet": true, "pen": true, "awarded": true, "walkover": true,
}

func favoriteMatchPreview(match MatchSummary) map[string]any {
	preview := map[string]any{
		"id": match.ID, "href": "/matches/" + strconv.FormatInt(match.ID, 10),
		"home_team": match.HomeTeam, "away_team": match.AwayTeam, "league_name": match.LeagueName,
		"kickoff_at": match.KickoffAt.UTC().Format(time.RFC3339), "status": match.Status,
	}
	if match.HomeScore != nil {
		preview["home_score"] = *match.HomeScore
	}
	if match.AwayScore != nil {
		preview["away_score"] = *match.AwayScore
	}
	return preview
}

func addFavoriteMatchContext(response map[string]any, label string, matches []MatchSummary, limit int) {
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	if len(matches) == 0 {
		return
	}
	previews := make([]map[string]any, 0, len(matches))
	for _, match := range matches {
		previews = append(previews, favoriteMatchPreview(match))
	}
	response["context_label"] = label
	response["matches"] = previews
}

func savedFixtureExpired(status string, kickoffAt time.Time, finishedAt *time.Time, now time.Time) bool {
	if !terminalFavoriteMatchStatuses[strings.ToLower(strings.TrimSpace(status))] {
		return false
	}
	completedAt := finishedAt
	if completedAt == nil && !kickoffAt.IsZero() {
		// Legacy terminal fixtures predate the write-once finished_at field.
		fallback := kickoffAt.Add(legacyMatchDuration)
		completedAt = &fallback
	}
	return completedAt != nil && !now.Before(completedAt.Add(savedMatchResultRetention))
}

func (d accountDependencies) favoriteContextResponse(ctx context.Context, item accounts.Favorite, now time.Time) (map[string]any, bool) {
	response := favoriteResponse(item)
	switch item.EntityType {
	case "match":
		if d.matches == nil {
			return response, false
		}
		matchID, err := strconv.ParseInt(item.EntityID, 10, 64)
		if err != nil {
			return response, false
		}
		match, err := d.matches.GetMatch(ctx, matchID)
		if err != nil {
			return response, false
		}
		status := strings.ToLower(strings.TrimSpace(match.Status))
		terminal := terminalFavoriteMatchStatuses[status]
		if savedFixtureExpired(status, match.KickoffAt, match.FinishedAt, now) {
			return response, true
		}
		label := "Saved match"
		if settledFavoriteMatchStatuses[status] {
			label = "Recent result"
		} else if terminal {
			label = "Final status"
		}
		addFavoriteMatchContext(response, label, []MatchSummary{match}, 1)
	case "prediction":
		if d.predictions == nil {
			return response, false
		}
		predictionID, err := strconv.ParseInt(item.EntityID, 10, 64)
		if err != nil {
			return response, false
		}
		prediction, err := d.predictions.GetPrediction(ctx, predictionID)
		if source, ok := d.predictions.(savedPredictionSource); ok {
			prediction, err = source.GetSavedPrediction(ctx, predictionID)
		}
		if err != nil {
			if errors.Is(err, ErrPredictionNotFound) {
				return response, true
			}
			return response, false
		}
		if savedFixtureExpired(prediction.MatchStatus, prediction.KickoffAt, prediction.FinishedAt, now) {
			return response, true
		}
		response["context_label"] = "Saved prediction"
		if settledFavoriteMatchStatuses[strings.ToLower(strings.TrimSpace(prediction.MatchStatus))] {
			response["context_label"] = "Recent prediction"
		}
		response["href"] = "/matches/" + strconv.FormatInt(prediction.MatchID, 10)
		response["match_id"] = prediction.MatchID
		response["kickoff_at"] = prediction.KickoffAt.UTC().Format(time.RFC3339)
		response["status"] = prediction.MatchStatus
		response["market_key"] = prediction.MarketKey
		response["selection"] = prediction.Selection
	case "team":
		if d.catalog == nil {
			return response, false
		}
		if catalog, ok := d.catalog.(FavoriteCatalogSource); ok {
			next, err := catalog.GetTeamNextMatch(ctx, item.EntityID)
			if err == nil && next != nil {
				addFavoriteMatchContext(response, "Next match", []MatchSummary{*next}, 1)
			}
		} else if team, err := d.catalog.GetTeam(ctx, item.EntityID); err == nil && team.NextMatch != nil {
			addFavoriteMatchContext(response, "Next match", []MatchSummary{*team.NextMatch}, 1)
		}
	case "league", "competition":
		if d.catalog == nil {
			return response, false
		}
		if catalog, ok := d.catalog.(FavoriteCatalogSource); ok {
			matches, err := catalog.ListLeagueMatches(ctx, item.EntityID, 6)
			if err == nil {
				addFavoriteMatchContext(response, "Next matches", matches, 6)
			}
		} else if league, err := d.catalog.GetLeague(ctx, item.EntityID); err == nil {
			addFavoriteMatchContext(response, "Next matches", league.Upcoming, 6)
		}
	case "country":
		catalog, ok := d.catalog.(FavoriteCatalogSource)
		if !ok {
			return response, false
		}
		matches, err := catalog.ListCountryMatches(ctx, item.EntityID, 8)
		if err == nil {
			addFavoriteMatchContext(response, "Next matches", matches, 8)
		}
	}
	return response, false
}

func (d accountDependencies) interestsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := d.requireUser(w, r, r.Method != http.MethodGet)
		if !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			items, err := d.accounts.ListInterests(r.Context(), principal.account.ID, true)
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError("interests_unavailable", "Interests are temporarily unavailable."))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"interests": items})
		case http.MethodPut:
			var input struct {
				Interests []struct {
					Type         string  `json:"type"`
					InterestType string  `json:"interest_type"`
					Key          string  `json:"key"`
					InterestKey  string  `json:"interest_key"`
					Weight       float64 `json:"weight"`
					Disabled     bool    `json:"disabled"`
				} `json:"interests"`
			}
			if decodeJSON(w, r, &input) != nil {
				writeJSON(w, http.StatusBadRequest, apiError("invalid_interests", "Interests are invalid."))
				return
			}
			items := make([]accounts.Interest, 0, len(input.Interests))
			for _, item := range input.Interests {
				if item.Type == "" {
					item.Type = item.InterestType
				}
				if item.Key == "" {
					item.Key = item.InterestKey
				}
				switch item.Type {
				case "markets":
					item.Type = "market"
				case "competitions":
					item.Type = "league"
				case "signals":
					item.Type = "signal"
				}
				weight := item.Weight
				if weight == 0 {
					weight = 1
				}
				items = append(items, accounts.Interest{Type: item.Type, Key: item.Key, Weight: weight, Disabled: item.Disabled})
			}
			if err := d.accounts.ReplaceInterests(r.Context(), principal.account.ID, items); err != nil {
				writeJSON(w, http.StatusBadRequest, apiError("invalid_interests", err.Error()))
				return
			}
			if profile, err := d.accounts.GetProfile(r.Context(), principal.account.ID); err == nil {
				now := time.Now().UTC()
				profile.OnboardingCompletedAt = &now
				_ = d.accounts.UpdateProfile(r.Context(), profile)
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
		case http.MethodDelete:
			if err := d.accounts.ResetPersonalization(r.Context(), principal.account.ID); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError("reset_unavailable", "The recommendation profile could not be reset."))
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			methodNotAllowed(w)
		}
	}
}

func (d accountDependencies) recommendationsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		principal, ok := d.requireUser(w, r, false)
		if !ok {
			return
		}
		limit := boundedQueryLimit(r, 24, 100)
		items, err := d.recommendations.Feed(r.Context(), principal.account.ID, limit, false)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("recommendations_unavailable", "Recommendations are temporarily unavailable."))
			return
		}
		responses := make([]map[string]any, 0, len(items))
		for _, item := range items {
			responses = append(responses, recommendationResponse(item))
		}
		w.Header().Set("Cache-Control", "private, max-age=60")
		writeJSON(w, http.StatusOK, map[string]any{"recommendations": responses})
	}
}

func (d accountDependencies) recommendationEventsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		principal, ok := d.requireUser(w, r, true)
		if !ok {
			return
		}
		var input struct {
			EventType        string      `json:"event_type"`
			RecommendationID json.Number `json:"recommendation_id"`
			Reason           string      `json:"reason"`
			EntityType       string      `json:"entity_type"`
			EntityID         string      `json:"entity_id"`
		}
		if decodeJSON(w, r, &input) != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_event", "Recommendation feedback is invalid."))
			return
		}
		id, _ := strconv.ParseInt(string(input.RecommendationID), 10, 64)
		if input.EventType == "dismissed" || input.EventType == "recommendation_dismissed" {
			itemType, itemID := input.EntityType, input.EntityID
			if itemType == "" && id > 0 {
				candidate, err := d.recommendations.DismissCandidate(r.Context(), principal.account.ID, id, input.Reason, nil)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, apiError("invalid_event", "Recommendation feedback is invalid."))
					return
				}
				itemType, itemID = candidate.ItemType, candidate.ItemID
			} else if err := d.recommendations.Dismiss(r.Context(), principal.account.ID, itemType, itemID, input.Reason, nil); err != nil {
				writeJSON(w, http.StatusBadRequest, apiError("invalid_event", "Recommendation feedback is invalid."))
				return
			}
			input.EntityType, input.EntityID = itemType, itemID
		}
		eventType := input.EventType
		if eventType == "dismissed" {
			eventType = "recommendation_dismissed"
		}
		var recID *int64
		if id > 0 {
			recID = &id
		}
		uid := principal.account.ID
		if err := d.recommendations.IngestEvents(r.Context(), []recommendations.Event{{UserID: &uid, SessionPublicID: principal.session.PublicID, EventType: eventType, EntityType: input.EntityType, EntityID: input.EntityID, RecommendationID: recID, Source: "web"}}); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_event", err.Error()))
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

func (d accountDependencies) shareEventsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if d.recommendations == nil || d.shareLimiter == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("sharing_unavailable", "Sharing analytics are temporarily unavailable."))
			return
		}
		if !d.shareLimiter.allow("share:client:"+trustedClientIP(r), time.Now().UTC()) {
			w.Header().Set("Retry-After", "60")
			writeJSON(w, http.StatusTooManyRequests, apiError("share_rate_limited", "Too many share events. Try again shortly."))
			return
		}
		var input struct {
			Channel    string `json:"channel"`
			EntityType string `json:"entity_type"`
			EntityID   string `json:"entity_id"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if decodeJSON(w, r, &input) != nil {
			return
		}
		input.Channel = strings.ToLower(strings.TrimSpace(input.Channel))
		input.EntityType = strings.ToLower(strings.TrimSpace(input.EntityType))
		input.EntityID = strings.TrimSpace(input.EntityID)
		allowedChannels := map[string]bool{"native": true, "copy": true, "whatsapp": true, "facebook": true, "instagram": true, "x": true, "telegram": true}
		allowedEntities := map[string]bool{"match": true, "prediction": true, "acca": true, "builder": true}
		if !allowedChannels[input.Channel] || !allowedEntities[input.EntityType] || len(input.EntityID) == 0 || len(input.EntityID) > 240 {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_share_event", "Share event is invalid."))
			return
		}
		for _, char := range input.EntityID {
			if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && !strings.ContainsRune(":._~-", char) {
				writeJSON(w, http.StatusBadRequest, apiError("invalid_share_event", "Share event is invalid."))
				return
			}
		}
		metadata, _ := json.Marshal(map[string]string{"channel": input.Channel})
		event := recommendations.Event{EventType: "content_shared", EntityType: input.EntityType, EntityID: input.EntityID, Source: "web", Metadata: metadata}
		if principal, ok := d.optionalUser(r); ok {
			event.UserID = &principal.account.ID
			event.SessionPublicID = principal.session.PublicID
		} else {
			_, guest, _, err := d.ensureGuest(w, r)
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError("sharing_unavailable", "Sharing analytics are temporarily unavailable."))
				return
			}
			event.GuestSessionID = &guest.ID
		}
		if err := d.recommendations.IngestEvents(r.Context(), []recommendations.Event{event}); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_share_event", "Share event is invalid."))
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

func (d accountDependencies) notificationsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := d.requireUser(w, r, r.Method != http.MethodGet)
		if !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			items, prefs, err := d.notifications.List(r.Context(), principal.account.ID, boundedQueryLimit(r, 30, 100))
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError("notifications_unavailable", "Notifications are temporarily unavailable."))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"notifications": items, "preferences": prefs})
		case http.MethodPatch:
			var prefs usernotifications.Preferences
			if decodeJSON(w, r, &prefs) != nil {
				writeJSON(w, http.StatusBadRequest, apiError("invalid_preferences", "Notification preferences are invalid."))
				return
			}
			if err := d.notifications.SavePreferences(r.Context(), principal.account.ID, prefs); errors.Is(err, usernotifications.ErrDeliveryDormant) {
				writeJSON(w, http.StatusConflict, apiError("notifications_dormant", "Notifications are not available during the initial launch."))
				return
			} else if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError("notifications_unavailable", "Notification preferences could not be saved."))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"preferences": prefs})
		default:
			methodNotAllowed(w)
		}
	}
}

func (d accountDependencies) notificationActionHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			methodNotAllowed(w)
			return
		}
		principal, ok := d.requireUser(w, r, true)
		if !ok {
			return
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/v1/me/notifications/"), 10, 64)
		if err != nil || id < 1 {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_notification", "Notification identifier is invalid."))
			return
		}
		var input struct {
			Read bool `json:"read"`
		}
		if decodeJSON(w, r, &input) != nil || !input.Read {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_notification", "Only marking notifications read is supported."))
			return
		}
		if err := d.notifications.MarkRead(r.Context(), principal.account.ID, id); err != nil {
			writeJSON(w, http.StatusNotFound, apiError("notification_not_found", "Notification not found."))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d accountDependencies) plansHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		plans, err := d.billing.Plans(r.Context())
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("plans_unavailable", "Plans are temporarily unavailable."))
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=300")
		writeJSON(w, http.StatusOK, map[string]any{"plans": plans})
	}
}

func (d accountDependencies) checkoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		principal, ok := d.requireUser(w, r, true)
		if !ok {
			return
		}
		var input struct {
			PlanKey string `json:"plan_key"`
		}
		if decodeJSON(w, r, &input) != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_checkout", "Choose a valid plan."))
			return
		}
		checkout, err := d.billing.BeginCheckout(r.Context(), principal.account.ID, input.PlanKey)
		if errors.Is(err, billing.ErrBillingDisabled) || errors.Is(err, billing.ErrPlanUnavailable) {
			writeJSON(w, http.StatusConflict, apiError("checkout_unavailable", "Checkout is not configured for this environment."))
			return
		}
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("checkout_unavailable", "Checkout is temporarily unavailable."))
			return
		}
		writeJSON(w, http.StatusCreated, checkout)
	}
}

func (d accountDependencies) subscriptionHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := d.requireUser(w, r, r.Method != http.MethodGet)
		if !ok {
			return
		}
		if r.Method == http.MethodGet {
			subscription, err := d.billing.Status(r.Context(), principal.account.ID)
			if errors.Is(err, billing.ErrSubscriptionNotFound) {
				writeJSON(w, http.StatusOK, map[string]any{"subscription": map[string]any{"plan_name": "Basic", "tier": "basic", "status": "active", "manage_available": false}, "access": accessResponse(principal, "")})
				return
			}
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError("subscription_unavailable", "Subscription status is temporarily unavailable."))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"subscription": subscription, "access": accessResponse(principal, "")})
			return
		}
		if r.Method == http.MethodDelete || r.Method == http.MethodPost {
			subscription, err := d.billing.CancelAtPeriodEnd(r.Context(), principal.account.ID)
			if err != nil {
				writeJSON(w, http.StatusConflict, apiError("cancellation_unavailable", "There is no renewable subscription to cancel."))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"subscription": subscription})
			return
		}
		methodNotAllowed(w)
	}
}

func (d accountDependencies) billingPortalHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		if _, ok := d.requireUser(w, r, true); !ok {
			return
		}
		writeJSON(w, http.StatusConflict, apiError("billing_portal_unavailable", "The billing portal is not configured for this environment."))
	}
}

func (d accountDependencies) billingWebhookHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024*1024)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_webhook", "Webhook payload is invalid."))
			return
		}
		occurredAt, err := time.Parse(time.RFC3339, r.Header.Get("X-Event-Time"))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_webhook", "Webhook timestamp is invalid."))
			return
		}
		receipt, err := d.billing.AcceptWebhook(r.Context(), r.Header.Get("X-Event-ID"), r.Header.Get("X-Event-Type"), r.Header.Get("X-Signature"), occurredAt, body)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, apiError("invalid_webhook", "Webhook could not be verified."))
			return
		}
		writeJSON(w, http.StatusAccepted, receipt)
	}
}

func (d accountDependencies) analysisHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		matchID, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/v1/analysis/"), 10, 64)
		if err != nil || matchID < 1 {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_match", "Match identifier is invalid."))
			return
		}
		advanced := false
		projection := "guest"
		if principal, ok := d.optionalUser(r); ok {
			advanced = principal.access.Allows(access.CapabilityAnalysisAdvanced)
			projection = string(principal.access.Principal.Tier)
		} else {
			token, _, _, guestErr := d.ensureGuest(w, r)
			if guestErr != nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError("guest_access_unavailable", "Guest access is temporarily unavailable."))
				return
			}
			view, observeErr := d.guest.ObserveAnalysis(r.Context(), token, matchID, "match_analysis")
			if errors.Is(observeErr, access.ErrViewLimitReached) {
				writeJSON(w, http.StatusForbidden, map[string]any{"code": "guest_limit_reached", "message": "Create a free account to continue with full analysis.", "remaining_views": view.Remaining})
				return
			}
			if observeErr != nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError("guest_access_unavailable", "Guest access is temporarily unavailable."))
				return
			}
		}
		result, err := d.analysis.Get(r.Context(), matchID, advanced)
		if errors.Is(err, analysisdomain.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, apiError("analysis_not_found", "Prepared analysis is not available for this match."))
			return
		}
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("analysis_unavailable", "Prepared analysis is temporarily unavailable."))
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{"analysis": result, "projection_tier": projection})
	}
}

func (d accountDependencies) requireUser(w http.ResponseWriter, r *http.Request, csrf bool) (userPrincipal, bool) {
	principal, ok := d.optionalUser(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, apiError("authentication_required", "Sign in to continue."))
		return userPrincipal{}, false
	}
	if csrf && !validCSRF(r) {
		writeJSON(w, http.StatusForbidden, apiError("csrf_invalid", "Refresh the page and try again."))
		return userPrincipal{}, false
	}
	return principal, true
}

func (d accountDependencies) optionalUser(r *http.Request) (userPrincipal, bool) {
	cookie, err := r.Cookie(userSessionCookie)
	if err != nil || cookie.Value == "" {
		return userPrincipal{}, false
	}
	account, session, err := d.accounts.ValidateSession(r.Context(), cookie.Value)
	if err != nil {
		return userPrincipal{}, false
	}
	ctx, err := d.access.ResolveUser(r.Context(), account.ID, time.Now().UTC())
	if err != nil {
		return userPrincipal{}, false
	}
	return userPrincipal{account: account, session: session, access: ctx}, true
}

func (d accountDependencies) ensureGuest(w http.ResponseWriter, r *http.Request) (string, access.GuestSession, access.ViewResult, error) {
	if cookie, err := r.Cookie(guestCookie); err == nil {
		if session, views, err := d.guest.Status(r.Context(), cookie.Value); err == nil {
			return cookie.Value, session, views, nil
		}
	}
	visitor := ""
	if cookie, err := r.Cookie(visitorCookie); err == nil {
		visitor = cookie.Value
	}
	if visitor == "" {
		visitor = randomHex(24)
	}
	token, session, err := d.guest.CreateSession(r.Context(), visitor)
	if err != nil {
		return "", access.GuestSession{}, access.ViewResult{}, err
	}
	setCookie(w, visitorCookie, visitor, session.ExpiresAt, true, d.secureCookies)
	setCookie(w, guestCookie, token, session.ExpiresAt, true, d.secureCookies)
	return token, session, access.ViewResult{Allowed: true, Remaining: d.guestLimit}, nil
}

func (d accountDependencies) allowAuth(r *http.Request, action string, identities ...string) bool {
	if !d.allowAuthClient(r, action) {
		return false
	}
	for _, value := range identities {
		if strings.TrimSpace(value) != "" {
			return d.allowAuthIdentity(action, value)
		}
	}
	if cookie, err := r.Cookie(visitorCookie); err == nil && cookie.Value != "" {
		return d.allowAuthIdentity(action, cookie.Value)
	}
	return true
}

func (d accountDependencies) allowAuthClient(r *http.Request, action string) bool {
	now := time.Now().UTC()
	return d.authLimiter.allow(strings.Join([]string{action, "client", trustedClientIP(r)}, ":"), now)
}

func (d accountDependencies) allowAuthIdentity(action, value string) bool {
	identity := normalizedAuthLimitIdentity(action, value)
	if identity == "" {
		return true
	}
	return d.authLimiter.allow(strings.Join([]string{action, "identity", hashTokenForLimit(identity)}, ":"), time.Now().UTC())
}

func normalizedAuthLimitIdentity(action, value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return ""
	}
	switch action {
	case "login", "register", "reset", "verify":
		if parsed, err := mail.ParseAddress(normalized); err == nil {
			return strings.ToLower(parsed.Address)
		}
	}
	return normalized
}

func (d accountDependencies) verifyTurnstile(r *http.Request, token, expectedAction string) (bool, error) {
	token = strings.TrimSpace(token)
	if d.turnstileSecret == "" {
		return !d.turnstileRequired, nil
	}
	if token == "" {
		return !d.turnstileRequired, nil
	}
	endpoint := d.turnstileURL
	if endpoint == "" {
		endpoint = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	}
	form := url.Values{"secret": {d.turnstileSecret}, "response": {token}}
	// Siteverify's remoteip is optional and must be a visitor address. When the
	// request arrives through the internal BFF/container network, trustedClientIP
	// can legitimately be a private proxy address; forwarding that address can
	// make an otherwise valid token fail validation. Omit it in that case.
	if clientIP := net.ParseIP(trustedClientIP(r)); clientIP != nil && !clientIP.IsPrivate() && !clientIP.IsLoopback() {
		form.Set("remoteip", clientIP.String())
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("turnstile returned status %d", response.StatusCode)
	}
	var result struct {
		Success  bool   `json:"success"`
		Action   string `json:"action"`
		Hostname string `json:"hostname"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 32*1024)).Decode(&result); err != nil {
		return false, err
	}
	if !result.Success || result.Action != expectedAction {
		return false, nil
	}
	if d.turnstileHostname != "" && !strings.EqualFold(result.Hostname, d.turnstileHostname) {
		return false, nil
	}
	return true, nil
}

func (d accountDependencies) queueAuthEmail(r *http.Request, userID int64, category, token, email string) error {
	var page, subject, message, action string
	switch category {
	case "email_verification":
		page = "verify_email"
		subject = "Verify your SokaLens email"
		message = "Use the secure button below to continue. This link expires and can be used once."
		action = "Continue securely"
	case "password_reset":
		page = "password_reset"
		subject = "Reset your SokaLens password"
		message = "Use the secure button below to choose a new password. This link expires and can be used once."
		action = "Reset password"
	default:
		slog.Warn("suppressed disallowed account email", "category", category, "user_id", userID)
		return nil
	}
	// Every URL that leaves the building in an email is built by emaillinks,
	// which owns the canonical shapes and cannot emit a retired URL tree. The
	// seo-invariants gate fails the build if an email path is spelled out
	// here instead.
	link := emaillinks.Builder{BaseURL: d.publicWebURL}.Account(page, token)
	textBody := "Open this one-time SokaLens link: " + link + "\n\nThis link expires and can be used once. If you did not request it, ignore this message."
	htmlBody := brandedEmailHTML(subject, message, link, action)
	if err := d.notifications.QueueEmailHTML(r.Context(), userID, category, randomHex(12), email, subject, textBody, htmlBody, requestIDFromContext(r.Context())); err != nil {
		slog.Error("queue account email", "category", category, "user_id", userID, "error", err)
		return err
	}
	return nil
}

func brandedEmailHTML(title, message, link, action string) string {
	return `<!doctype html><html><body style="margin:0;background:#f3f7f5;font-family:Arial,sans-serif;color:#08251c"><div style="max-width:600px;margin:0 auto;padding:32px 20px"><div style="background:#06271f;color:#fff;padding:18px 22px;border-radius:14px 14px 0 0;font-size:22px;font-weight:800">SokaLens</div><div style="background:#fff;padding:28px 22px;border:1px solid #d7e4de;border-top:0;border-radius:0 0 14px 14px"><h1 style="font-size:24px;margin:0 0 14px">` + html.EscapeString(title) + `</h1><p style="line-height:1.6">` + html.EscapeString(message) + `</p><p style="margin:24px 0"><a href="` + html.EscapeString(link) + `" style="background:#0b7d5b;color:#fff;text-decoration:none;padding:12px 18px;border-radius:8px;font-weight:700">` + html.EscapeString(action) + `</a></p><p style="font-size:12px;color:#60756c">Predictions are statistical estimates, never guaranteed outcomes.</p></div></div></body></html>`
}

func accessResponse(principal userPrincipal, csrf string) map[string]any {
	return map[string]any{"authenticated": true, "tier": principal.access.Principal.Tier, "ads_allowed": principal.access.AdsAllowed, "email_verified": principal.account.EmailVerifiedAt != nil, "entitlements": principal.access.CapabilityNames(), "csrf_token": csrf, "principal": map[string]any{"type": "user", "id": principal.account.PublicID, "email": principal.account.Email, "email_verified": principal.account.EmailVerifiedAt != nil}}
}
func accountResponse(account accounts.Account) map[string]any {
	return map[string]any{"id": account.PublicID, "email": account.Email, "email_verified": account.EmailVerifiedAt != nil, "account_status": account.AccountStatus, "created_at": account.CreatedAt}
}
func profileResponse(profile accounts.Profile, account accounts.Account) map[string]any {
	return map[string]any{"display_name": profile.DisplayName, "email": account.Email, "email_verified": account.EmailVerifiedAt != nil, "timezone": profile.Timezone, "country_code": profile.CountryCode, "odds_format": profile.OddsFormat, "odds_format_selected": profile.OddsFormatSelectedAt != nil, "language": profile.Language, "preferred_language": profile.Language, "personalization_enabled": profile.PersonalizationEnabled, "marketing_consent": profile.MarketingConsent, "onboarding_completed_at": profile.OnboardingCompletedAt, "updated_at": profile.UpdatedAt}
}

// sessionResponse carries the user agent as well as the device name.
//
// device_name is set only by clients that send one, and most do not: of 14
// live sessions two had a device name and four had a user agent. The session
// list therefore read "Unknown device" for every row, which makes it useless
// for the one thing it exists for -- spotting a session you do not recognise.
//
// The user agent is the fallback the reader can actually act on, and the
// frontend already looks for it. It was simply never sent.
func sessionResponse(session accounts.Session, current bool) map[string]any {
	return map[string]any{"id": session.PublicID, "public_id": session.PublicID, "device_name": session.DeviceName, "user_agent": session.UserAgent, "signed_in_at": session.CreatedAt, "created_at": session.CreatedAt, "expires_at": session.ExpiresAt, "revoked_at": session.RevokedAt, "current": current}
}

// favoriteKeyResponses answers the only question a save button asks -- "is
// this one already saved?" -- and deliberately carries nothing else. Building
// the full favoriteResponse means a catalog lookup per favorite, which is
// affordable on /favorites and not on every page that draws save buttons.
func favoriteKeyResponses(items []accounts.Favorite) []map[string]any {
	keys := make([]map[string]any, 0, len(items))
	for _, item := range items {
		keys = append(keys, map[string]any{"entity_type": item.EntityType, "entity_id": item.EntityID})
	}
	return keys
}

func favoriteResponse(item accounts.Favorite) map[string]any {
	name := item.DisplayName
	if name == "" {
		name = item.EntityID
	}
	href := ""
	switch item.EntityType {
	case "match":
		href = "/matches/" + url.PathEscape(item.EntityID)
	case "team":
		href = "/teams/" + url.PathEscape(item.EntityID)
	case "league":
		href = "/leagues/" + url.PathEscape(item.EntityID)
	case "competition":
		href = "/leagues/" + url.PathEscape(item.EntityID)
	case "country":
		href = "/leagues"
	case "market":
		href = "/predictions/markets/" + url.PathEscape(item.EntityID)
	}
	return map[string]any{"id": item.ID, "entity_type": item.EntityType, "entity_id": item.EntityID, "name": name, "href": href, "notifications_enabled": item.NotificationsEnabled, "created_at": item.CreatedAt}
}
func recommendationResponse(item recommendations.Candidate) map[string]any {
	return map[string]any{"id": item.ID, "item_type": item.ItemType, "item_id": item.ItemID, "title": item.Title, "subtitle": item.Subtitle, "reason": item.ReasonText, "reason_text": item.ReasonText, "interest_key": item.InterestKey, "prediction_confidence": item.PredictionConfidence, "starts_at": item.StartsAt, "freshness_at": item.FreshnessAt, "sponsored": item.Sponsored, "editorial": item.Editorial, "href": item.Href}
}
func mustSessions(items []accounts.Session, err error) []accounts.Session {
	if err != nil {
		return nil
	}
	return items
}
func apiError(code, message string) map[string]string {
	return map[string]string{"error": code, "code": code, "message": message}
}
func methodNotAllowed(w http.ResponseWriter) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
}
func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) == nil {
		return errors.New("multiple JSON values")
	}
	return nil
}
func randomHex(size int) string {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		panic(fmt.Sprintf("secure randomness unavailable: %v", err))
	}
	return hex.EncodeToString(value)
}
func ensureCSRFCookie(w http.ResponseWriter, r *http.Request, secure bool) string {
	if cookie, err := r.Cookie(csrfCookie); err == nil && len(cookie.Value) >= 32 {
		return cookie.Value
	}
	value := randomHex(24)
	setCookie(w, csrfCookie, value, time.Now().UTC().Add(24*time.Hour), false, secure)
	return value
}
func validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookie)
	if err != nil {
		return false
	}
	header := r.Header.Get("X-CSRF-Token")
	return len(header) == len(cookie.Value) && subtle.ConstantTimeCompare([]byte(header), []byte(cookie.Value)) == 1
}
func setCookie(w http.ResponseWriter, name, value string, expires time.Time, httpOnly, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Expires: expires, MaxAge: int(time.Until(expires).Seconds()), HttpOnly: httpOnly, Secure: secure, SameSite: http.SameSiteLaxMode})
}
func clearCookie(w http.ResponseWriter, name string, httpOnly, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", Expires: time.Unix(1, 0), MaxAge: -1, HttpOnly: httpOnly, Secure: secure, SameSite: http.SameSiteLaxMode})
}

// X-SokaLens-Client-IP is an internal BFF-to-API contract. It is honored only
// when the direct peer is loopback or a private container address, preventing
// public clients from spoofing the rate-limit identity.
func trustedClientIP(r *http.Request) string {
	remote := r.RemoteAddr
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	parsedRemote := net.ParseIP(strings.TrimSpace(remote))
	if parsedRemote != nil && (parsedRemote.IsPrivate() || parsedRemote.IsLoopback()) {
		if forwarded := net.ParseIP(strings.TrimSpace(r.Header.Get("X-SokaLens-Client-IP"))); forwarded != nil {
			return forwarded.String()
		}
	}
	if parsedRemote != nil {
		return parsedRemote.String()
	}
	return "unknown"
}

func hashTokenForLimit(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func registrationCountry(input string, r *http.Request) string {
	for _, candidate := range []string{input, r.Header.Get("X-SokaLens-Country"), r.Header.Get("CF-IPCountry")} {
		candidate = strings.ToUpper(strings.TrimSpace(candidate))
		if len(candidate) == 2 && candidate != "XX" && candidate != "T1" {
			return candidate
		}
	}
	return ""
}

func boundedText(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) > maximum {
		value = value[:maximum]
	}
	return value
}

func safePath(value string) string {
	value = boundedText(value, 512)
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") {
		return ""
	}
	return value
}

func referrerHost(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	return boundedText(strings.ToLower(parsed.Hostname()), 253)
}

func preferredReferrerHost(input, fallbackURL string) string {
	input = boundedText(strings.ToLower(strings.TrimSpace(input)), 253)
	if input != "" && !strings.ContainsAny(input, "/:@?#[]\\") {
		if parsed := net.ParseIP(input); parsed != nil {
			return parsed.String()
		}
		if strings.Contains(input, ".") {
			valid := true
			for _, label := range strings.Split(input, ".") {
				if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
					valid = false
					break
				}
				for _, character := range label {
					if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
						valid = false
						break
					}
				}
			}
			if valid {
				return input
			}
		}
	}
	return referrerHost(fallbackURL)
}

func deviceType(userAgent string) string {
	lower := strings.ToLower(userAgent)
	if strings.Contains(lower, "mobile") || strings.Contains(lower, "android") || strings.Contains(lower, "iphone") {
		return "mobile"
	}
	if strings.Contains(lower, "tablet") || strings.Contains(lower, "ipad") {
		return "tablet"
	}
	return "desktop"
}

func deviceSummary(userAgent string) string {
	return deviceType(userAgent) + " web browser"
}

func publicHostname(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

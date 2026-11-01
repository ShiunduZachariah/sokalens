package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/admin"
)

func adminUsersHandler(auth AdminAuthenticator, management AdminManagement) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if management == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin_management_unavailable"})
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionReadUsers); !ok {
			return
		}
		limit := boundedQueryLimit(r, 50, 100)
		users, err := management.ListUsers(r.Context(), r.URL.Query().Get("search"), r.URL.Query().Get("status"), limit)
		if err != nil {
			slog.Error("admin users unavailable", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin_users_unavailable"})
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{"users": users})
	}
}

func adminUserGrowthHandler(auth AdminAuthenticator, management AdminManagement) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if management == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin_management_unavailable"})
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionReadAnalytics); !ok {
			return
		}
		summary, err := management.UserGrowth(r.Context())
		if err != nil {
			slog.Error("admin user growth unavailable", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin_user_growth_unavailable"})
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{"growth": summary})
	}
}

func adminUserActivityHandler(auth AdminAuthenticator, management AdminManagement) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		if management == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin_management_unavailable"})
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionReadAnalytics); !ok {
			return
		}
		items, err := management.UserActivity(r.Context(), boundedQueryLimit(r, 100, 200))
		if err != nil {
			slog.Error("admin user activity unavailable", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin_user_activity_unavailable"})
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{"events": items})
	}
}

func adminAuditLogsHandler(auth AdminAuthenticator, management AdminManagement) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if management == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin_management_unavailable"})
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionReadAuditLogs); !ok {
			return
		}
		logs, err := management.AuditLogs(r.Context(), boundedQueryLimit(r, 100, 200))
		if err != nil {
			slog.Error("admin audit unavailable", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin_audit_unavailable"})
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{"events": logs})
	}
}

func adminJobsHandler(auth AdminAuthenticator, source OpsSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin_ops_unavailable"})
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionReadAnalytics); !ok {
			return
		}
		jobs, err := source.ListJobs(r.Context(), boundedQueryLimit(r, 100, 200))
		if err != nil {
			slog.Error("admin jobs unavailable", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin_jobs_unavailable"})
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
	}
}

func adminProviderRequestsHandler(auth AdminAuthenticator, source OpsSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin_ops_unavailable"})
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionReadAnalytics); !ok {
			return
		}
		requests, err := source.ListProviderRequests(r.Context(), boundedQueryLimit(r, 100, 200))
		if err != nil {
			slog.Error("admin provider requests unavailable", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin_provider_requests_unavailable"})
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{"requests": requests})
	}
}

// adminCatalogueCoverageHandler reports canonical catalogue + search index
// coverage (reference entities, media coverage, live pipeline freshness).
func adminCatalogueCoverageHandler(auth AdminAuthenticator, source CoverageSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin_catalogue_unavailable"})
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionReadAnalytics); !ok {
			return
		}
		report, err := source.Coverage(r.Context())
		if err != nil {
			slog.Error("admin catalogue coverage unavailable", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin_catalogue_unavailable"})
			return
		}
		response := map[string]any{
			"countries_total":     report.Countries,
			"countries_with_flag": report.CountriesFlags,
			"teams_total":         report.Teams,
			"teams_with_logo":     report.TeamLogos,
			"leagues_total":       report.Leagues,
			"leagues_with_logo":   report.LeagueLogos,
			"search_indexed":      report.SearchIndexed,
		}
		if !report.LiveLastSeen.IsZero() {
			response["live_last_seen"] = report.LiveLastSeen.Format(time.RFC3339)
		}
		if !report.WorkerLastSeen.IsZero() {
			response["worker_last_seen"] = report.WorkerLastSeen.Format(time.RFC3339)
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, response)
	}
}

func adminSubscriptionsHandler(auth AdminAuthenticator, management AdminManagement) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if management == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin_management_unavailable"})
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionReadBilling); !ok {
			return
		}
		items, err := management.ListSubscriptions(r.Context(), boundedQueryLimit(r, 100, 200))
		if err != nil {
			slog.Error("admin subscriptions unavailable", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin_subscriptions_unavailable"})
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{"subscriptions": items})
	}
}

func adminPlansHandler(auth AdminAuthenticator, management AdminManagement) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		if _, ok := requireAdmin(w, r, auth, admin.PermissionReadBilling); !ok {
			return
		}
		items, err := management.ListPlanConfigurations(r.Context())
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("plans_unavailable", "Pricing configuration is temporarily unavailable."))
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, map[string]any{"plans": items})
	}
}

func adminPlanActionHandler(auth AdminAuthenticator, management AdminManagement) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			methodNotAllowed(w)
			return
		}
		actor, ok := requireAdmin(w, r, auth, admin.PermissionManageBilling)
		if !ok {
			return
		}
		planKey := strings.TrimPrefix(r.URL.Path, "/v1/admin/plans/")
		if planKey != "pro_monthly" && planKey != "pro_yearly" {
			writeJSON(w, http.StatusNotFound, apiError("plan_not_found", "Plan not found."))
			return
		}
		var input admin.PlanConfiguration
		if decodeJSON(w, r, &input) != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_plan", "Pricing configuration is invalid."))
			return
		}
		input.PlanKey = planKey
		if err := management.UpdatePlanConfiguration(r.Context(), input, actor); err != nil {
			if errors.Is(err, admin.ErrAdminForbidden) {
				writeJSON(w, http.StatusForbidden, apiError("forbidden", "Billing administrator access is required."))
				return
			}
			writeJSON(w, http.StatusBadRequest, apiError("invalid_plan", "Pricing configuration could not be saved."))
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
	}
}

func adminUserActionHandler(auth AdminAuthenticator, management AdminManagement) http.HandlerFunc {
	type actionRequest struct {
		Reason   string         `json:"reason"`
		Evidence map[string]any `json:"evidence"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
			return
		}
		if management == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin_management_unavailable"})
			return
		}
		actor, ok := requireAdmin(w, r, auth, admin.PermissionManageUsers)
		if !ok {
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 5 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		userID, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil || userID < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_user_id"})
			return
		}
		action := parts[4]
		if len(parts) == 6 && parts[4] == "sessions" && parts[5] == "revoke" {
			action = "sessions/revoke"
		}
		r.Body = http.MaxBytesReader(w, r.Body, 32*1024)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var payload actionRequest
		if err := decoder.Decode(&payload); err != nil || strings.TrimSpace(payload.Reason) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "reason_required"})
			return
		}
		switch action {
		case "suspend":
			err = management.Suspend(r.Context(), admin.SuspensionCommand{UserID: userID, Reason: payload.Reason, Evidence: payload.Evidence, Actor: actor})
		case "reactivate":
			err = management.Reactivate(r.Context(), userID, payload.Reason, actor)
		case "sessions/revoke":
			err = management.RevokeSessions(r.Context(), userID, payload.Reason, actor)
		default:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
			return
		}
		if errors.Is(err, admin.ErrAdminForbidden) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
			return
		}
		if errors.Is(err, admin.ErrUserNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "user_not_found"})
			return
		}
		if err != nil {
			slog.Error("admin user action failed", "action", action, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "admin_action_failed"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func requireAdmin(w http.ResponseWriter, r *http.Request, auth AdminAuthenticator, permission admin.Permission) (admin.User, bool) {
	if auth == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin_auth_unavailable"})
		return admin.User{}, false
	}
	token, ok := bearerToken(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing_bearer_token"})
		return admin.User{}, false
	}
	user, err := auth.ValidateSession(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_admin_session"})
		return admin.User{}, false
	}
	allowed := false
	if authorizer, ok := auth.(interface {
		Authorize(context.Context, int64, admin.Permission) (bool, error)
	}); ok {
		var authorizeErr error
		allowed, authorizeErr = authorizer.Authorize(r.Context(), user.ID, permission)
		if authorizeErr != nil {
			slog.Error("admin authorization failed", "user_id", user.ID, "permission", permission, "error", authorizeErr)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin_authorization_unavailable"})
			return admin.User{}, false
		}
	} else {
		// Compatibility for narrow test doubles. Production authenticators always
		// implement database-backed authorization.
		allowed = admin.Can(user.Role, permission)
	}
	if !allowed {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return admin.User{}, false
	}
	return user, true
}

func boundedQueryLimit(r *http.Request, fallback, maximum int) int {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit < 1 {
		return fallback
	}
	if limit > maximum {
		return maximum
	}
	return limit
}

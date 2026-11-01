package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/admin"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/emailsettings"
)

type EmailProviderSettings interface {
	Status(context.Context) (emailsettings.Status, error)
	Set(context.Context, string, int64) error
}

func adminEmailProviderHandler(auth AdminAuthenticator, source EmailProviderSettings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireAdmin(w, r, auth, admin.PermissionManagePolicy)
		if !ok {
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "email_settings_unavailable"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			status, err := source.Status(r.Context())
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "email_settings_unavailable"})
				return
			}
			w.Header().Set("Cache-Control", "private, no-store")
			writeJSON(w, http.StatusOK, status)
		case http.MethodPut:
			var input struct {
				Provider string `json:"provider"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			if decoder.Decode(&input) != nil || strings.TrimSpace(input.Provider) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_email_provider"})
				return
			}
			if err := source.Set(r.Context(), input.Provider, actor.ID); err != nil {
				if errors.Is(err, emailsettings.ErrInvalidProvider) {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_email_provider"})
					return
				}
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "email_provider_update_failed"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
		default:
			methodNotAllowed(w)
		}
	}
}

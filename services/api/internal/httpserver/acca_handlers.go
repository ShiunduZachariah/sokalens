package httpserver

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	accadomain "github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/acca"
)

func accaHandler(source AccaSource, featured bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("acca_source_unavailable", "ACCA inventory is temporarily unavailable."))
			return
		}
		qv := r.URL.Query()
		q := accadomain.Query{Tab: qv.Get("tab"), Period: qv.Get("period"), Country: qv.Get("country"), League: qv.Get("league"), Selection: qv.Get("selection"), Cursor: qv.Get("cursor"), Seed: qv.Get("seed"), Limit: 10}
		// Only an explicit generate=1 search may compose at request time, and
		// never while paging: a cursor describes stored inventory.
		q.Generate = qv.Get("generate") == "1" && q.Cursor == "" && !featured
		if err := accadomain.ValidateCursor(q.Cursor); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_cursor", err.Error()))
			return
		}
		if featured {
			q.Limit = 3
		}
		if raw := qv.Get("limit"); raw != "" {
			n, e := strconv.Atoi(raw)
			if e != nil || n < 1 || n > 50 {
				writeJSON(w, http.StatusBadRequest, apiError("invalid_limit", "limit must be between 1 and 50."))
				return
			}
			q.Limit = n
			if featured && n > 3 {
				q.Limit = 3
			}
		}
		var err error
		if q.Legs, err = accadomain.ParseInt(qv.Get("legs")); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_legs", err.Error()))
			return
		}
		if q.MinFairOdds, err = accadomain.ParseOdds(qv.Get("min_fair_odds"), 1000); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_odds", err.Error()))
			return
		}
		if q.MaxFairOdds, err = accadomain.ParseOdds(qv.Get("max_fair_odds"), 1000); err != nil {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_odds", err.Error()))
			return
		}
		if q.MinFairOdds != nil && q.MaxFairOdds != nil && *q.MinFairOdds > *q.MaxFairOdds {
			writeJSON(w, http.StatusBadRequest, apiError("invalid_odds", "minimum odds must not exceed maximum odds"))
			return
		}
		result, err := source.List(r.Context(), q)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("acca_source_unavailable", "ACCA inventory is temporarily unavailable."))
			return
		}
		writeCanonicalJSON(w, r, result)
	}
}

// accaDetailHandler serves GET /v1/accas/{id}.
//
// The body is one Slip, exactly as it appears inside `items` on the list
// endpoint, plus each leg's `status`. Not a wrapper object: a client that has
// rendered a list item can render this with the same code, which is the whole
// reason the shape is shared rather than designed twice.
//
// No entitlement gate, matching the list endpoint, which has none either --
// ACCA inventory is public. If that ever changes it has to change in both
// places, and the handlers sitting side by side in one file is the cheapest
// way to make that obvious.
func accaDetailHandler(source AccaSlipSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, apiError("method_not_allowed", "Method not allowed."))
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("acca_source_unavailable", "ACCA inventory is temporarily unavailable."))
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/accas/"), "/")
		if id == "" || strings.Contains(id, "/") {
			// A deeper path is not a slip id. 404 rather than treating the
			// first segment as one: /v1/accas/{id}/legs does not exist, and
			// answering it with the slip would make it look as though it did.
			writeJSON(w, http.StatusNotFound, apiError("acca_not_found", "No ACCA with that id."))
			return
		}
		slip, err := source.Get(r.Context(), id)
		if errors.Is(err, accadomain.ErrSlipNotFound) {
			writeJSON(w, http.StatusNotFound, apiError("acca_not_found", "No ACCA with that id."))
			return
		}
		if err != nil {
			// A real failure, kept apart from "no such slip". Collapsing the
			// two would report an outage as an empty catalogue, and the
			// endpoint would look healthy while serving nothing.
			writeJSON(w, http.StatusServiceUnavailable, apiError("acca_source_unavailable", "ACCA inventory is temporarily unavailable."))
			return
		}
		writeCanonicalJSON(w, r, slip)
	}
}

package httpserver

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/seo"
)

// SEOURLSource supplies the crawlable URL set to the sitemap builders.
type SEOURLSource interface {
	List(ctx context.Context, entityType string, offset, limit int) ([]seo.URL, error)
	Count(ctx context.Context, entityType string) (int, error)
}

// seoURLsHandler serves indexable paths for one entity type.
//
// Public and cacheable: it contains nothing but URLs the site wants crawled,
// which are by definition not secret. It is also the one endpoint whose
// consumer is a machine building a document for another machine, so it
// carries no locale, no entitlement and no personalisation.
func seoURLsHandler(source SEOURLSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("seo_urls_unavailable", "Crawlable URL set is unavailable."))
			return
		}

		entityType := r.URL.Query().Get("type")
		if !seo.ValidType(entityType) {
			// A 400 rather than an empty list. An empty sitemap and a
			// misspelled parameter look identical to the builder, and the
			// failure would surface weeks later as pages missing from the
			// index rather than as an error anyone sees.
			writeJSON(w, http.StatusBadRequest, apiError("invalid_type", "Unknown entity type."))
			return
		}

		limit := seo.MaxPerPage
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= seo.MaxPerPage {
				limit = parsed
			}
		}
		offset := 0
		if raw := r.URL.Query().Get("offset"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 {
				offset = parsed
			}
		}

		total, err := source.Count(r.Context(), entityType)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("seo_urls_unavailable", "Crawlable URL set is unavailable."))
			return
		}
		urls, err := source.List(r.Context(), entityType, offset, limit)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("seo_urls_unavailable", "Crawlable URL set is unavailable."))
			return
		}

		items := make([]map[string]any, 0, len(urls))
		for _, url := range urls {
			item := map[string]any{"path": url.Path}
			// Omitted rather than nulled when absent. A lastmod that appears
			// and disappears between crawls teaches Google to stop trusting
			// the field across the whole property.
			if url.LastModified != nil {
				item["last_modified"] = url.LastModified.UTC().Format(time.RFC3339)
			}
			items = append(items, item)
		}

		// Five minutes, matching the sitemap loaders' own cache. Longer would
		// delay a newly published fixture reaching the sitemap; shorter would
		// rebuild a 12,906-row document for no reader.
		w.Header().Set("Cache-Control", "public, max-age=0, s-maxage=300, must-revalidate")
		writeJSON(w, http.StatusOK, map[string]any{
			"type":   entityType,
			"total":  total,
			"offset": offset,
			"limit":  limit,
			"urls":   items,
		})
	}
}

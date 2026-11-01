package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCanonicalJSONRevalidatesUnchangedPayloadAndRefreshesChangedPayload(t *testing.T) {
	firstRequest := httptest.NewRequest(http.MethodGet, "/v1/predictions/today", nil)
	firstResponse := httptest.NewRecorder()
	writeCanonicalJSON(firstResponse, firstRequest, map[string]any{"prediction_id": 10})
	firstETag := firstResponse.Header().Get("ETag")
	if firstResponse.Code != http.StatusOK || firstETag == "" {
		t.Fatalf("first response = %d etag=%q body=%s", firstResponse.Code, firstETag, firstResponse.Body.String())
	}
	if got := firstResponse.Header().Get("Cache-Control"); got != publicCanonicalCacheControl {
		t.Fatalf("Cache-Control = %q", got)
	}

	unchangedRequest := httptest.NewRequest(http.MethodGet, "/v1/predictions/today", nil)
	unchangedRequest.Header.Set("If-None-Match", "W/"+firstETag)
	unchangedResponse := httptest.NewRecorder()
	writeCanonicalJSON(unchangedResponse, unchangedRequest, map[string]any{"prediction_id": 10})
	if unchangedResponse.Code != http.StatusNotModified || unchangedResponse.Body.Len() != 0 {
		t.Fatalf("unchanged response = %d %q", unchangedResponse.Code, unchangedResponse.Body.String())
	}

	changedRequest := httptest.NewRequest(http.MethodGet, "/v1/predictions/today", nil)
	changedRequest.Header.Set("If-None-Match", firstETag)
	changedResponse := httptest.NewRecorder()
	writeCanonicalJSON(changedResponse, changedRequest, map[string]any{"prediction_id": 11})
	if changedResponse.Code != http.StatusOK || changedResponse.Header().Get("ETag") == firstETag || !strings.Contains(changedResponse.Body.String(), `"prediction_id":11`) {
		t.Fatalf("changed response = %d etag=%q body=%s", changedResponse.Code, changedResponse.Header().Get("ETag"), changedResponse.Body.String())
	}
}

package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestTimeoutHandlerCannotFlush pins the reason /v1/inplay/stream bypasses
// http.TimeoutHandler.
//
// Every other test of the stream calls the handler directly, so the handler was
// correct and the deployed endpoint still answered 501 streaming_unsupported on
// every request: the defect lived in the middleware stack, not the handler, and
// nothing exercised the two together.
//
// http.TimeoutHandler swaps the ResponseWriter for an unexported timeoutWriter
// that buffers the whole body and implements neither Flush nor Unwrap. So
// http.NewResponseController finds no flusher in the chain, Flush returns
// ErrNotSupported, and the stream handler correctly reports that it cannot
// stream here.
//
// If this test ever fails, Go has changed timeoutWriter and the bypass can be
// reconsidered. Until then it is load-bearing.
func TestTimeoutHandlerCannotFlush(t *testing.T) {
	var flushErr error
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flushErr = http.NewResponseController(w).Flush()
	})

	rec := httptest.NewRecorder()
	http.TimeoutHandler(inner, time.Second, "timeout").
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/inplay/stream", nil))

	if flushErr == nil {
		t.Fatal("http.TimeoutHandler now supports flushing; the streaming bypass may no longer be needed")
	}

	// And the same handler flushes fine without the wrapper, so the wrapper is
	// the cause rather than the recorder or the controller.
	flushErr = nil
	inner.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/inplay/stream", nil))
	if flushErr != nil {
		t.Fatalf("flushing failed without TimeoutHandler too, so the bypass is not the fix: %v", flushErr)
	}
}

// TestStreamingRouteCoversTheStreamAndNothingElse pins the bypass list.
//
// Too narrow and the stream goes back to 501. Too wide and ordinary bounded
// reads silently lose their request timeout, which is the protection that keeps
// a slow query from holding a connection open indefinitely.
func TestStreamingRouteCoversTheStreamAndNothingElse(t *testing.T) {
	if !streamingRoute("/v1/inplay/stream") {
		t.Error("/v1/inplay/stream is not exempt from the timeout wrapper; it will answer 501")
	}
	for _, path := range []string{
		"/v1/inplay/overview",
		"/v1/inplay/changes",
		"/v1/inplay",
		"/v1/matches/today",
		"/v1/predictions/results",
	} {
		if streamingRoute(path) {
			t.Errorf("%s lost its request timeout; only long-lived responses belong in this list", path)
		}
	}
}

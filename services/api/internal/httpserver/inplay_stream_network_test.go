package httpserver

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/config"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/predictions/entitlement"
)

func TestStreamRealHTTPPreservesHeadersAndOutlivesServerWriteTimeout(t *testing.T) {
	source := &recordingStreamSource{}
	server := httptest.NewUnstartedServer(New(ServerOptions{
		Config: config.Config{APIRequestTimeout: 10 * time.Millisecond}, InPlayStream: source,
		InPlayStreamPacing: InPlayStreamPacing{Poll: 60 * time.Millisecond, Heartbeat: time.Hour, Lifetime: 180 * time.Millisecond},
	}))
	server.Config.WriteTimeout = 25 * time.Millisecond
	server.Start()
	defer server.Close()
	client := server.Client()
	client.Timeout = time.Second
	response, err := client.Get(server.URL + InPlayStreamPath)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("Content-Type") != "text/event-stream" || response.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("committed headers: %v", response.Header)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("stream broke after server write timeout: %v, %s", err, body)
	}
	if strings.Count(string(body), "event: changes") < 2 {
		t.Fatalf("expected multiple frames beyond write timeout: %s", body)
	}
	if !strings.Contains(string(body), "event: reconnect") {
		t.Fatalf("missing bounded close: %s", body)
	}
}

type blockingStreamSource struct{ cancelled chan struct{} }

func (*blockingStreamSource) Overview(context.Context, int, int) (entitlement.OverviewState, error) {
	return entitlement.OverviewState{}, nil
}
func (s *blockingStreamSource) ChangesSince(ctx context.Context, _ int64, _ int) (entitlement.ChangeSetState, error) {
	<-ctx.Done()
	close(s.cancelled)
	return entitlement.ChangeSetState{}, ctx.Err()
}

func TestStreamLifetimeCancelsBlockedQuery(t *testing.T) {
	source := &blockingStreamSource{cancelled: make(chan struct{})}
	server := httptest.NewServer(New(ServerOptions{InPlayStream: source,
		InPlayStreamPacing: InPlayStreamPacing{Lifetime: 30 * time.Millisecond}}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 300 * time.Millisecond
	response, err := client.Get(server.URL + InPlayStreamPath)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	started := time.Now()
	_, err = io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("stream did not close itself: %v", err)
	}
	if time.Since(started) > 200*time.Millisecond {
		t.Fatal("lifetime did not bound query")
	}
	select {
	case <-source.cancelled:
	default:
		t.Fatal("query context not cancelled")
	}
}

func TestStreamQueryTimeoutIsShorterThanConnectionLifetime(t *testing.T) {
	source := &blockingStreamSource{cancelled: make(chan struct{})}
	server := httptest.NewServer(New(ServerOptions{InPlayStream: source,
		InPlayStreamPacing: InPlayStreamPacing{Lifetime: time.Second, QueryTimeout: 20 * time.Millisecond}}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 300 * time.Millisecond
	response, err := client.Get(server.URL + InPlayStreamPath)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || !strings.Contains(string(body), "event: error") {
		t.Fatalf("bounded query must signal failure: %v %s", err, body)
	}
	select {
	case <-source.cancelled:
	default:
		t.Fatal("query context not cancelled")
	}
}

type brokenStreamWriter struct {
	header    http.Header
	flushes   int
	failWrite bool
}

func (w *brokenStreamWriter) Header() http.Header { return w.header }
func (*brokenStreamWriter) WriteHeader(int)       {}
func (w *brokenStreamWriter) Write(p []byte) (int, error) {
	if w.failWrite {
		return 0, errors.New("disconnected")
	}
	return len(p), nil
}
func (w *brokenStreamWriter) FlushError() error {
	w.flushes++
	if w.flushes > 1 {
		return errors.New("flush disconnected")
	}
	return nil
}

func TestStreamStopsOnWriteOrFlushFailureBeforeQuery(t *testing.T) {
	for _, failWrite := range []bool{true, false} {
		source := &recordingStreamSource{}
		writer := &brokenStreamWriter{header: make(http.Header), failWrite: failWrite}
		streamHandler(source, InPlayStreamPacing{})(writer, httptest.NewRequest(http.MethodGet, InPlayStreamPath, nil))
		if len(source.cursors) != 0 {
			t.Fatal("queried after write/flush failure")
		}
	}
}

func TestStreamRealHTTPReconnectUsesLastEventID(t *testing.T) {
	source := &recordingStreamSource{}
	server := httptest.NewServer(New(ServerOptions{InPlayStream: source,
		InPlayStreamPacing: InPlayStreamPacing{Poll: time.Hour, Heartbeat: time.Hour, Lifetime: 10 * time.Millisecond}}))
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+InPlayStreamPath+"?since=2", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Last-Event-ID", "50")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || !strings.Contains(string(body), "id: 51\n") {
		t.Fatalf("did not resume from applied event: %v %s", err, body)
	}
}

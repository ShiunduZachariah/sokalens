package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/predictions/entitlement"
)

// recordingStreamSource hands out one snapshot per call, advancing the cursor, so
// a test can watch resumption without a database.
type recordingStreamSource struct {
	cursors []int64
	more    int
	err     error
}

func (s *recordingStreamSource) Overview(context.Context, int, int) (entitlement.OverviewState, error) {
	if s.err != nil {
		return entitlement.OverviewState{}, s.err
	}
	return entitlement.OverviewState{
		Snapshots: []entitlement.SnapshotState{{FixtureID: 1, Sequence: 7, Markets: map[string]map[string]int{"m": {"home": 10000}}}},
		Events:    []entitlement.Event{{Sequence: 6, FixtureID: 1, Kind: "goal"}},
		Cursor:    7,
	}, nil
}

func (s *recordingStreamSource) ChangesSince(_ context.Context, cursor int64, _ int) (entitlement.ChangeSetState, error) {
	if s.err != nil {
		return entitlement.ChangeSetState{}, s.err
	}
	s.cursors = append(s.cursors, cursor)
	more := s.more > 0
	if more {
		s.more--
	}
	return entitlement.ChangeSetState{
		Snapshots: []entitlement.SnapshotState{{FixtureID: 1, Sequence: cursor + 1}},
		Cursor:    cursor + 1,
		More:      more,
	}, nil
}

func streamHandler(source InPlayStreamSource, pacing InPlayStreamPacing) http.HandlerFunc {
	return accountDependencies{}.inPlayHandlerWithStream(nil, source, pacing)
}

func TestStreamResumesFromTheSuppliedCursor(t *testing.T) {
	source := &recordingStreamSource{}
	request := httptest.NewRequest(http.MethodGet, InPlayStreamPath+"?since=41", nil)
	recorder := httptest.NewRecorder()
	streamHandler(source, InPlayStreamPacing{Poll: time.Hour, Heartbeat: time.Hour, Lifetime: 5 * time.Millisecond})(recorder, request)

	if len(source.cursors) == 0 || source.cursors[0] != 41 {
		t.Fatalf("first query cursor = %v, want 41 first", source.cursors)
	}
	body := recorder.Body.String()
	// The SSE id IS the cursor: a reconnecting EventSource sends it back as
	// Last-Event-ID and resumes exactly where it stopped.
	if !strings.Contains(body, "id: 42") {
		t.Fatalf("stream did not emit the advanced cursor as an id\nbody: %s", body)
	}
	if !strings.Contains(body, "event: changes") {
		t.Fatalf("stream emitted no changes event\nbody: %s", body)
	}
}

func TestStreamPrefersLastEventIDOverInitialSince(t *testing.T) {
	// EventSource retains its initial URL during automatic reconnect. The
	// last delivered event must win over that URL's now-stale since parameter.
	source := &recordingStreamSource{}
	request := httptest.NewRequest(http.MethodGet, InPlayStreamPath+"?since=100", nil)
	request.Header.Set("Last-Event-ID", "5")
	streamHandler(source, InPlayStreamPacing{Poll: time.Hour, Heartbeat: time.Hour, Lifetime: 5 * time.Millisecond})(
		httptest.NewRecorder(), request)
	if source.cursors[0] != 5 {
		t.Fatalf("cursor = %d, want Last-Event-ID 5 rather than initial URL", source.cursors[0])
	}
}

func TestStreamFallsBackToLastEventIDForBrowserReconnects(t *testing.T) {
	source := &recordingStreamSource{}
	request := httptest.NewRequest(http.MethodGet, InPlayStreamPath, nil)
	request.Header.Set("Last-Event-ID", "88")
	streamHandler(source, InPlayStreamPacing{Poll: time.Hour, Heartbeat: time.Hour, Lifetime: 5 * time.Millisecond})(
		httptest.NewRecorder(), request)
	if source.cursors[0] != 88 {
		t.Fatalf("cursor = %d, want 88 from Last-Event-ID", source.cursors[0])
	}
}

func TestStreamIgnoresAnUnparseableCursorRatherThanFailing(t *testing.T) {
	// A malformed cursor replays from the start, which is recoverable. Rejecting
	// the connection would leave a client with a corrupted cursor permanently
	// unable to reconnect.
	source := &recordingStreamSource{}
	request := httptest.NewRequest(http.MethodGet, InPlayStreamPath+"?since=not-a-number", nil)
	streamHandler(source, InPlayStreamPacing{Poll: time.Hour, Heartbeat: time.Hour, Lifetime: 5 * time.Millisecond})(
		httptest.NewRecorder(), request)
	if source.cursors[0] != 0 {
		t.Fatalf("cursor = %d, want 0", source.cursors[0])
	}
}

func TestStreamDrainsATruncatedSetBeforeWaiting(t *testing.T) {
	// A client far behind must catch up within one tick rather than one limit
	// per tick, or it never converges on a busy fixture.
	source := &recordingStreamSource{more: 2}
	request := httptest.NewRequest(http.MethodGet, InPlayStreamPath, nil)
	streamHandler(source, InPlayStreamPacing{Poll: time.Hour, Heartbeat: time.Hour, Lifetime: 20 * time.Millisecond})(
		httptest.NewRecorder(), request)
	if len(source.cursors) < 3 {
		t.Fatalf("queried %d times, want at least 3 to drain two truncated sets", len(source.cursors))
	}
	if source.cursors[0] != 0 || source.cursors[1] != 1 || source.cursors[2] != 2 {
		t.Fatalf("cursors did not advance while draining: %v", source.cursors)
	}
}

func TestStreamAnnouncesADeliberateClose(t *testing.T) {
	// Named so a client can tell a bounded lifetime apart from a network drop.
	// It resumes from its cursor either way, but the distinction is the
	// difference between an expected reconnect and an error to report.
	source := &recordingStreamSource{}
	recorder := httptest.NewRecorder()
	streamHandler(source, InPlayStreamPacing{Poll: time.Hour, Heartbeat: time.Hour, Lifetime: 5 * time.Millisecond})(
		recorder, httptest.NewRequest(http.MethodGet, InPlayStreamPath, nil))
	body := recorder.Body.String()
	if !strings.Contains(body, "event: reconnect") {
		t.Fatalf("stream closed without announcing it\nbody: %s", body)
	}
	if !strings.Contains(body, `"cursor":1`) {
		t.Fatalf("reconnect frame omitted the cursor to resume from\nbody: %s", body)
	}
}

func TestStreamSetsTheHeadersThatMakeStreamingWork(t *testing.T) {
	source := &recordingStreamSource{}
	recorder := httptest.NewRecorder()
	streamHandler(source, InPlayStreamPacing{Poll: time.Hour, Heartbeat: time.Hour, Lifetime: 5 * time.Millisecond})(
		recorder, httptest.NewRequest(http.MethodGet, InPlayStreamPath, nil))
	header := recorder.Header()
	if got := header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	// Proxy buffering defeats streaming as thoroughly as a missing flush.
	if got := header.Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no", got)
	}
	// The gate's cache headers must survive onto the stream: a cached
	// event-stream is the same leak as a cached payload.
	if got := header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if !strings.Contains(recorder.Body.String(), "retry: ") {
		t.Fatal("stream did not advertise a retry interval")
	}
}

func TestStreamReportsASourceFailureInBand(t *testing.T) {
	// The status line is long gone by the time a mid-stream query fails, so the
	// error has to arrive as a frame rather than as a 503.
	source := &recordingStreamSource{err: errors.New("database down")}
	recorder := httptest.NewRecorder()
	streamHandler(source, InPlayStreamPacing{Poll: time.Hour, Heartbeat: time.Hour, Lifetime: 5 * time.Millisecond})(
		recorder, httptest.NewRequest(http.MethodGet, InPlayStreamPath, nil))
	if !strings.Contains(recorder.Body.String(), "event: error") {
		t.Fatalf("no error frame\nbody: %s", recorder.Body.String())
	}
}

func TestChangesSharesTheStreamQueryAndAdvertisesItsPace(t *testing.T) {
	// One query, two transports: a client that drops the stream and polls must
	// end in the same state, which only holds if both read the same query.
	source := &recordingStreamSource{}
	recorder := httptest.NewRecorder()
	streamHandler(source, InPlayStreamPacing{})(
		recorder, httptest.NewRequest(http.MethodGet, InPlayChangesPath+"?since=12", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if source.cursors[0] != 12 {
		t.Fatalf("cursor = %d, want 12", source.cursors[0])
	}
	if got := recorder.Header().Get("X-Poll-After-Seconds"); got != "15" {
		t.Fatalf("X-Poll-After-Seconds = %q, want 15 to match the stream poll", got)
	}
	var decoded entitlement.ChangeSet
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Cursor != 13 {
		t.Fatalf("cursor = %d, want 13", decoded.Cursor)
	}
}

func TestOverviewServesTheCombinedFirstLoad(t *testing.T) {
	source := &recordingStreamSource{}
	recorder := httptest.NewRecorder()
	streamHandler(source, InPlayStreamPacing{})(
		recorder, httptest.NewRequest(http.MethodGet, InPlayOverviewPath, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var decoded entitlement.Overview
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	// A cold client gets the series, the timeline and the cursor in one call --
	// the whole point of the route is that it does not have to fan out.
	if len(decoded.Snapshots) == 0 || len(decoded.Events) == 0 || decoded.Cursor != 7 {
		t.Fatalf("overview is not complete: %+v", decoded)
	}
}

func TestTransportRoutesAreServiceUnavailableWithoutASource(t *testing.T) {
	for _, path := range []string{InPlayOverviewPath, InPlayChangesPath, InPlayStreamPath} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			streamHandler(nil, InPlayStreamPacing{})(recorder, httptest.NewRequest(http.MethodGet, path, nil))
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", recorder.Code)
			}
		})
	}
}

func TestEveryTransportRouteIsRegisteredForTheInvariantBattery(t *testing.T) {
	// A route the battery does not know about is a route nobody checks for
	// leaks. The prefix check lives in the battery; this asserts the list.
	for _, path := range []string{InPlayOverviewPath, InPlayChangesPath, InPlayStreamPath} {
		found := false
		for _, route := range InPlayRoutes {
			if route == path {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("%s is served but missing from InPlayRoutes", path)
		}
	}
}

package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/predictions/entitlement"
)

// The in-play transport: one combined first load, a resumable stream, and a
// polling fallback.
//
// ONE QUERY, TWO TRANSPORTS. The stream and the fallback both call
// `InPlayStreamSource.ChangesSince`. That is deliberate and load-bearing: a
// client that drops the stream and falls back to polling must end up in the same
// state, and the only way to guarantee that is for both to read the same query
// rather than two that drift apart.
//
// THE CURSOR IS THE ONLY RESUME TOKEN. Not a timestamp -- wall-clock times are
// not totally ordered across a restart, and two snapshots can share a
// `captured_at`. `inplay_stream_sequence` is a single monotonic counter shared by
// snapshots and events, so a cursor orders the whole stream.
//
// ALL OF THIS LIVES UNDER /v1/inplay/. Point-in-time reads and historical
// snapshots share one explicit public-or-gated read policy and serializer.
// Keeping the entire prefix together prevents route-specific policy drift.

// InPlayStreamSource supplies the series and the change feed.
//
// Returns pre-gate domain values, never response types: the implementation loads
// what it likes and the gate between that and the client is not its concern.
type InPlayStreamSource interface {
	// Overview is the combined first load: live fixtures, their recent series,
	// the event timeline and the cursor to resume from.
	Overview(ctx context.Context, fixtureLimit int, seriesLimit int) (entitlement.OverviewState, error)
	// ChangesSince returns everything after `cursor`, in sequence order.
	ChangesSince(ctx context.Context, cursor int64, limit int) (entitlement.ChangeSetState, error)
}

const (
	// InPlayOverviewPath is the combined first load.
	InPlayOverviewPath = "/v1/inplay/overview"
	// InPlayChangesPath is the polling fallback.
	InPlayChangesPath = "/v1/inplay/changes"
	// InPlayStreamPath is the resumable server-sent event stream.
	InPlayStreamPath = "/v1/inplay/stream"
)

const (
	// defaultStreamPoll is how often the stream checks for new changes.
	//
	// Matched to the provider's own 15-second refresh for fixtures and events:
	// polling the database faster cannot surface data the provider has not sent
	// yet, so it would burn queries to deliver the same bytes.
	defaultStreamPoll = 15 * time.Second
	// defaultStreamHeartbeat keeps an idle connection alive. Proxies and mobile
	// networks close a silent stream; a comment frame costs nothing and stops a
	// quiet half-time from being read as a dead connection.
	defaultStreamHeartbeat = 20 * time.Second
	// defaultStreamLifetime bounds one connection. A stream that never ends
	// pins a goroutine and a database connection for as long as the client
	// lives; ending it deliberately lets the client resume from its cursor,
	// which is the mechanism that already exists for a dropped connection.
	defaultStreamLifetime = 30 * time.Minute

	defaultChangeLimit = 200
	maxChangeLimit     = 1000
)

// streamCursor reads the resume point.
//
// Last-Event-ID wins on reconnect: EventSource keeps its original URL, whose
// since parameter describes the initial load rather than the last applied frame.
func streamCursor(r *http.Request) int64 {
	if raw := strings.TrimSpace(r.Header.Get("Last-Event-ID")); raw != "" {
		if cursor, err := strconv.ParseInt(raw, 10, 64); err == nil && cursor >= 0 {
			return cursor
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		if cursor, err := strconv.ParseInt(raw, 10, 64); err == nil && cursor >= 0 {
			return cursor
		}
	}
	return 0
}

func changeLimit(r *http.Request) int {
	return boundedQueryLimit(r, defaultChangeLimit, maxChangeLimit)
}

// serveInPlayOverview answers the combined first load.
func serveInPlayOverview(
	w http.ResponseWriter,
	r *http.Request,
	filter entitlement.Filter,
	viewer entitlement.Viewer,
	source InPlayStreamSource,
) {
	state, err := source.Overview(r.Context(), boundedQueryLimit(r, 50, 200), changeLimit(r))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError("inplay_unavailable", "In-play data is temporarily unavailable."))
		return
	}
	writeJSON(w, http.StatusOK, filter.ShapeOverview(viewer, state))
}

// serveInPlayChanges answers the polling fallback from the same query the stream
// uses.
func serveInPlayChanges(
	w http.ResponseWriter,
	r *http.Request,
	filter entitlement.Filter,
	viewer entitlement.Viewer,
	source InPlayStreamSource,
) {
	cursor := streamCursor(r)
	state, err := source.ChangesSince(r.Context(), cursor, changeLimit(r))
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, apiError("inplay_unavailable", "In-play data is temporarily unavailable."))
		return
	}
	// How long a client should wait before asking again. Advisory, and it matches
	// the stream's own poll so the two transports pace identically.
	w.Header().Set("X-Poll-After-Seconds", strconv.Itoa(int(defaultStreamPoll.Seconds())))
	writeJSON(w, http.StatusOK, filter.ShapeChanges(viewer, state))
}

// InPlayStreamPacing is how a stream connection paces itself.
//
// Exposed on ServerOptions because the defaults are tuned for a real client on a
// real network and are wrong for anything else: a 30-minute lifetime makes a
// one-shot probe -- a test, a curl, a health check -- appear to hang. Zero values
// take the defaults.
type InPlayStreamPacing struct {
	// Poll is how often the connection checks for new changes.
	Poll time.Duration
	// Heartbeat is how often an idle connection emits a keep-alive comment.
	Heartbeat time.Duration
	// Lifetime bounds one connection, after which the client resumes from its
	// cursor through the mechanism that already handles a dropped connection.
	Lifetime time.Duration
	// QueryTimeout bounds each database read, independently of connection lifetime.
	QueryTimeout time.Duration
	// WriteTimeout bounds a frame write/flush without expiring idle connections.
	WriteTimeout time.Duration
}

// streamClock is the resolved pacing.
type streamClock = InPlayStreamPacing

func (c streamClock) resolved() streamClock {
	if c.Poll <= 0 {
		c.Poll = defaultStreamPoll
	}
	if c.Heartbeat <= 0 {
		c.Heartbeat = defaultStreamHeartbeat
	}
	if c.Lifetime <= 0 {
		c.Lifetime = defaultStreamLifetime
	}
	if c.QueryTimeout <= 0 {
		c.QueryTimeout = 2 * time.Second
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = 5 * time.Second
	}
	return c
}

// serveInPlayStream holds the connection open and emits changes as they appear.
func serveInPlayStream(
	w http.ResponseWriter,
	r *http.Request,
	filter entitlement.Filter,
	viewer entitlement.Viewer,
	source InPlayStreamSource,
	clock streamClock,
) {
	// http.NewResponseController, not a w.(http.Flusher) assertion.
	//
	// Every response in this service passes through `statusResponseWriter`, which
	// embeds the http.ResponseWriter INTERFACE -- and that interface has no
	// Flush, so embedding promotes none. A direct type assertion therefore fails
	// on every real request, not just in tests: the first version of this handler
	// returned 501 in production. The controller walks the Unwrap() chain the
	// wrapper already implements, which is what that method is for.
	controller := http.NewResponseController(w)
	clock = clock.resolved()
	ctx, cancel := context.WithTimeout(r.Context(), clock.Lifetime)
	defer cancel()
	// Commit SSE headers before the first flush, which itself sends HTTP 200.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	setDeadline := func(deadline time.Time) bool {
		err := controller.SetWriteDeadline(deadline)
		// Recorders and non-network adapters can legitimately lack deadlines.
		return err == nil || errors.Is(err, http.ErrNotSupported)
	}
	if !setDeadline(time.Now().Add(clock.WriteTimeout)) {
		return
	}
	if err := controller.Flush(); err != nil {
		// Without flushing, every frame sits in a buffer until the connection
		// closes -- a slow download, not a stream. Saying so beats looking like
		// a hang.
		if errors.Is(err, http.ErrNotSupported) {
			writeJSON(w, http.StatusNotImplemented, apiError("streaming_unsupported", "Streaming is not supported on this connection."))
		}
		return
	}
	if !setDeadline(time.Time{}) {
		return
	}
	// Replace net/http's whole-response deadline with a bounded deadline for
	// each frame. Clear it between frames so quiet matches can stay connected.
	writeFrame := func(frame string) bool {
		if !setDeadline(time.Now().Add(clock.WriteTimeout)) {
			return false
		}
		if _, err := fmt.Fprint(w, frame); err != nil {
			return false
		}
		if err := controller.Flush(); err != nil {
			return false
		}
		return setDeadline(time.Time{})
	}
	closeForLifetime := func(cursor int64) {
		if ctx.Err() != nil && r.Context().Err() == nil {
			writeFrame(fmt.Sprintf("event: reconnect\ndata: {\"cursor\":%d}\n\n", cursor))
		}
	}

	cursor := streamCursor(r)
	// Tell the client the retry interval so the browser's own reconnect paces
	// itself rather than hammering after a drop.
	if !writeFrame(fmt.Sprintf("retry: %d\n\n", clock.Poll.Milliseconds())) {
		return
	}
	poll := time.NewTicker(clock.Poll)
	defer poll.Stop()
	heartbeat := time.NewTicker(clock.Heartbeat)
	defer heartbeat.Stop()

	// Emit immediately, then on each tick: a client connecting mid-match wants
	// what it missed now, not after one poll interval.
	emit := func() bool {
		for {
			if ctx.Err() != nil {
				return false
			}
			queryCtx, queryCancel := context.WithTimeout(ctx, clock.QueryTimeout)
			state, err := source.ChangesSince(queryCtx, cursor, defaultChangeLimit)
			queryCancel()
			if err != nil {
				if ctx.Err() != nil {
					return false
				}
				writeFrame("event: error\ndata: {\"error\":\"inplay_unavailable\"}\n\n")
				return false
			}
			if len(state.Snapshots) == 0 && len(state.Events) == 0 {
				return true
			}
			payload, err := json.Marshal(filter.ShapeChanges(viewer, state))
			if err != nil {
				return false
			}
			if state.Cursor <= cursor {
				return false
			}
			// The id IS the cursor: a client that reconnects sends it back as
			// Last-Event-ID and resumes exactly where it stopped.
			if !writeFrame(fmt.Sprintf("id: %d\nevent: changes\ndata: %s\n\n", state.Cursor, payload)) {
				return false
			}
			cursor = state.Cursor
			// Drain a truncated set before waiting, or a client that fell a long
			// way behind would catch up one limit per tick.
			if !state.More {
				return true
			}
		}
	}

	if !emit() {
		closeForLifetime(cursor)
		return
	}
	for {
		select {
		case <-ctx.Done():
			// A deliberate close, named so a client can tell it apart from a
			// network drop. It resumes from its cursor either way.
			closeForLifetime(cursor)
			return
		case <-poll.C:
			if !emit() {
				closeForLifetime(cursor)
				return
			}
		case <-heartbeat.C:
			// A comment frame: ignored by EventSource, enough to keep a proxy
			// from deciding the connection is idle.
			if !writeFrame(": keep-alive\n\n") {
				return
			}
		}
	}
}

// ErrInPlayStreamUnavailable marks a source that cannot serve the stream.
var ErrInPlayStreamUnavailable = errors.New("in-play stream unavailable")

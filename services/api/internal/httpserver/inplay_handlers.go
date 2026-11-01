package httpserver

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/predictions/entitlement"
)

// InPlayPathPrefix is the whole in-play HTTP surface.
//
// Routing the prefix to one handler is deliberate. It means there is exactly
// one place where an in-play payload becomes bytes, so the entitlement filter
// cannot be skipped by adding a route somewhere else in the switch: a new
// in-play path either lands in inPlayHandler and goes through the gate, or it
// 404s. The CI invariant battery probes this prefix rather than a hand-kept
// list of paths for the same reason.
const InPlayPathPrefix = "/v1/inplay/"

// InPlayRoutes is the set of in-play paths that serve a payload, exported so
// the security-invariant battery iterates the same list the router does. A
// route added here without a matching case below serves 404; a case added
// below without an entry here is unreachable from the battery, and the
// coverage assertion in the battery fails.
var InPlayRoutes = []string{
	"/v1/inplay/fixtures",
	InPlayOverviewPath,
	InPlayChangesPath,
	InPlayStreamPath,
}

// ErrInPlayFixtureNotFound is returned when a fixture is not currently in
// play. Kept distinct from a transport error so a finished match reads as 404
// rather than as an outage.
var ErrInPlayFixtureNotFound = errors.New("in-play fixture not found")

// InPlaySource supplies in-play state to the HTTP layer.
//
// It returns entitlement.FixtureState -- the pre-gate domain value carrying
// predictions -- and never a response type. The implementation is free to
// load whatever it likes; the gate between that and the client is not its
// concern and not its decision.
type InPlaySource interface {
	ListInPlayFixtures(ctx context.Context, limit int) ([]entitlement.FixtureState, error)
	GetInPlayFixture(ctx context.Context, fixtureID int64) (entitlement.FixtureState, error)
	FixtureSeries(ctx context.Context, fixtureID int64) ([]entitlement.SeriesPoint, error)
}

// inPlayHandler serves the whole in-play surface behind the entitlement gate.
func (d accountDependencies) inPlayHandler(source InPlaySource) http.HandlerFunc {
	return d.inPlayHandlerWithStream(source, nil, streamClock{})
}

// inPlayHandlerWithStream serves the whole prefix, including the transport
// routes. The stream source is separate from the point-in-time one because they
// load different things, and optional because an environment without it must
// still serve the board rather than 500.
func (d accountDependencies) inPlayHandlerWithStream(
	source InPlaySource,
	stream InPlayStreamSource,
	clock streamClock,
) http.HandlerFunc {
	return d.inPlayHandlerWithPolicy(source, stream, clock, false)
}

// inPlayHandlerWithPolicy retains one serialization path for public and
// legacy gated deployments. Public read does not create an account principal.
func (d accountDependencies) inPlayHandlerWithPolicy(
	source InPlaySource, stream InPlayStreamSource, clock streamClock, publicRead bool,
) http.HandlerFunc {
	filter := entitlement.NewFilter()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}

		// Headers first, and before any branch that can return early.
		//
		// A gated response that reaches a shared cache is the same leak as
		// serializing the prediction in the first place, and it is silent:
		// one authenticated payload stored at the edge answers every
		// anonymous request behind it until it expires. `private, no-store`
		// stops that, and Vary stops a proxy that ignores it from keying an
		// anonymous and an authenticated request to the same entry.
		w.Header().Set("Cache-Control", "private, no-store, max-age=0, must-revalidate")
		w.Header().Set("Vary", "Cookie, Authorization")
		// Keep rapidly changing API payloads out of search indexes. Public
		// page indexing is a separate decision from this API read policy.
		w.Header().Set("X-Robots-Tag", "noindex")

		viewer := entitlement.Anonymous()
		if publicRead {
			viewer = entitlement.PublicRead()
		} else if principal, ok := d.optionalUser(r); ok {
			viewer = entitlement.ForContext(principal.access)
		}

		// The transport routes are checked before the nil-source guard: they are
		// served by a different source, and a missing point-in-time source must
		// not take the stream down with it.
		switch r.URL.Path {
		case InPlayOverviewPath, InPlayChangesPath, InPlayStreamPath:
			if stream == nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError("inplay_unavailable", "In-play data is temporarily unavailable."))
				return
			}
			switch r.URL.Path {
			case InPlayOverviewPath:
				serveInPlayOverview(w, r, filter, viewer, stream)
			case InPlayChangesPath:
				serveInPlayChanges(w, r, filter, viewer, stream)
			default:
				// Written last, because it takes over the response writer: a
				// header set after the first flush never reaches the client.
				serveInPlayStream(w, r, filter, viewer, stream, clock)
			}
			return
		}

		if source == nil {
			writeJSON(w, http.StatusServiceUnavailable, apiError("inplay_unavailable", "In-play data is temporarily unavailable."))
			return
		}

		switch {
		case r.URL.Path == "/v1/inplay/fixtures":
			states, err := source.ListInPlayFixtures(r.Context(), boundedQueryLimit(r, 50, 200))
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError("inplay_unavailable", "In-play data is temporarily unavailable."))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"fixtures":  filter.ShapeAll(viewer, states),
				"entitled":  viewer.Entitled(),
				"generated": true,
			})
		case strings.HasPrefix(r.URL.Path, "/v1/inplay/fixtures/") && strings.HasSuffix(r.URL.Path, "/series"):
			// Checked before the plain per-fixture case, which parses the whole
			// remainder as an id and would reject "123/series" as malformed.
			raw := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/inplay/fixtures/"), "/series")
			fixtureID, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || fixtureID < 1 {
				writeJSON(w, http.StatusBadRequest, apiError("invalid_fixture", "Fixture identifier is invalid."))
				return
			}
			points, err := source.FixtureSeries(r.Context(), fixtureID)
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError("inplay_unavailable", "In-play data is temporarily unavailable."))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"fixture_id": fixtureID,
				"entitled":   viewer.Entitled(),
				"series":     inPlaySeriesResponse(points, viewer.Entitled()),
			})
		case strings.HasPrefix(r.URL.Path, "/v1/inplay/fixtures/"):
			raw := strings.TrimPrefix(r.URL.Path, "/v1/inplay/fixtures/")
			fixtureID, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || fixtureID < 1 {
				writeJSON(w, http.StatusBadRequest, apiError("invalid_fixture", "Fixture identifier is invalid."))
				return
			}
			state, err := source.GetInPlayFixture(r.Context(), fixtureID)
			if errors.Is(err, ErrInPlayFixtureNotFound) {
				writeJSON(w, http.StatusNotFound, apiError("inplay_fixture_not_found", "No in-play record exists for that match."))
				return
			}
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, apiError("inplay_unavailable", "In-play data is temporarily unavailable."))
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"fixture":  filter.Shape(viewer, state),
				"entitled": viewer.Entitled(),
			})
		default:
			writeJSON(w, http.StatusNotFound, apiError("not_found", "Endpoint not found."))
		}
	}
}

// inPlaySeriesResponse renders a fixture's graph for one viewer.
//
// The shape is the same either way -- minute, trigger, score, and the event key
// that caused the point -- and only the probabilities are withheld. That mirrors
// how a locked market still appears on the board: an anonymous reader sees the
// line's shape and when it moved, which is the argument for signing up, rather
// than an empty panel that reads as a broken feature.
func inPlaySeriesResponse(points []entitlement.SeriesPoint, entitled bool) []map[string]any {
	out := make([]map[string]any, 0, len(points))
	for _, point := range points {
		item := map[string]any{
			"sequence": point.Sequence,
			"minute":   point.Minute,
			"trigger":  point.Trigger,
			"score":    map[string]any{"home": point.HomeGoals, "away": point.AwayGoals},
		}
		if point.EventKey != "" {
			item["event_key"] = point.EventKey
		}
		// Named incidents are not gated. The markets behind the line are the
		// paid product; who scored in the 22nd minute is public record, and a
		// marker a guest cannot read is just an unexplained vertical rule.
		if point.Event != nil {
			event := map[string]any{"type": point.Event.Type, "side": point.Event.Side}
			if point.Event.Detail != "" {
				event["detail"] = point.Event.Detail
			}
			if point.Event.Player != "" {
				event["player"] = point.Event.Player
			}
			item["event"] = event
		}
		if entitled && len(point.Markets) > 0 {
			item["markets"] = point.Markets
		} else if !entitled {
			item["locked"] = true
		}
		out = append(out, item)
	}
	return out
}

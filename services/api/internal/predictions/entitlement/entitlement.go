// Package entitlement shapes in-play predictions for an explicit read policy.
// Public deployments use PublicRead; legacy gated deployments resolve account
// capabilities with ForContext and withhold prediction fields from Anonymous.
// Both policies share the same serialization boundary and basis-point format.
//
// The package is deliberately a leaf: it imports the access package, the
// basispoints package -- itself a leaf with no service dependencies -- and
// nothing else from this service. Callers hand it domain values and receive a
// value whose JSON form is already correct for the viewer. There is no second
// path, and no way to build the entitled payload from outside -- see the
// unexported `prediction` field on Market, which only Filter.Shape can set.
//
// Basis-point conversion belongs here rather than upstream because this is the
// one place an in-play probability becomes bytes. Converting earlier would mean
// a probability could reach the wire without passing through the gate.
//
// It lives under internal/predictions/ as a subpackage rather than in
// predictions itself because internal/predictions imports internal/httpserver
// for its shared DTOs, and the HTTP layer has to import the filter. A leaf
// package keeps that dependency acyclic without splitting the gate in two.
package entitlement

import (
	"encoding/json"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/access"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/predictions/basispoints"
)

// Unlock reasons. A response carries at most one, and it exists to tell the
// client what to offer -- never to describe what was withheld.
const (
	// ReasonAuthRequired applies only to the legacy gated policy.
	ReasonAuthRequired = "AUTH_REQUIRED"
)

// RegisterCTA is the destination an anonymous viewer is sent to. Kept here so
// the gate owns the whole anonymous payload rather than leaving one field to a
// caller that might forget it.
const RegisterCTA = "/register"

// Prediction is what the engine produced for one market at one instant. It is
// an input to the filter, never a response type: reaching this struct from a
// handler means the handler is holding gated data, which is exactly why the
// shaped output does not expose it.
type Prediction struct {
	Selection       string
	Probability     float64
	Confidence      string
	ConfidenceScore float64
}

// MarketState is one market on one in-play fixture, with the prediction
// attached when the engine has a current one.
type MarketState struct {
	Key   string
	Label string

	// Prediction is nil when the engine has published nothing for this market
	// yet. An absent prediction is not a locked one: the market is unlocked
	// and simply empty, which is how an entitled viewer learns the difference
	// between "not for you" and "not computed".
	Prediction *Prediction

	// SettlementState is how this market was graded, using the in-play
	// contract's vocabulary: "won", "lost", "void" or
	// "unsettled_data_unavailable". Empty means not graded yet, which is the
	// normal state while a match is in play.
	//
	// Gated with the prediction, and that is forced rather than chosen. The
	// score is public, so "this market won" next to a visible 1-1 tells an
	// anonymous reader that the BTTS pick was Yes -- the gate would leak the
	// very selection it exists to protect. Same reasoning as DataCutoffAt
	// below, with a sharper edge.
	//
	// Keyed on the CURRENTLY PUBLISHED prediction's own id, so a grade is only
	// ever shown beside the pick it graded. See the source query: if the
	// graded generation was later superseded, this reads empty rather than
	// attributing an older generation's result to the pick now on display.
	// Under-reporting is safe; mis-attributing is not.
	SettlementState string

	// DataCutoffAt is the snapshot instant the prediction was derived from.
	// It answers "what did SokaLens know when it published this?" and is
	// gated with the prediction -- a cutoff timestamp on its own tells an
	// anonymous scraper the regeneration cadence for free.
	DataCutoffAt time.Time
	MatchMinute  int
}

// FixtureState is the in-play state of one fixture plus its markets.
type FixtureState struct {
	FixtureID int64
	Status    string
	Minute    int
	HomeGoals int
	AwayGoals int

	// HomeTeamLogo/AwayTeamLogo are crest URLs for the fixture's two sides.
	//
	// Decoration, never identity: a fixture is resolved by FixtureID and a
	// side by the fixture's own team ids, exactly as before. Nothing keys on
	// these, and a fixture whose teams have no crest on record is still served
	// -- the clients draw an initials shield from the name they already hold.
	HomeTeamLogo string
	AwayTeamLogo string

	// Stale mirrors the server-side clock freeze: when the feed has stopped,
	// the minute is the last known one and must not be presented as live.
	Stale   bool
	Markets []MarketState
}

// Viewer is the entitlement decision for one request, resolved once at the
// edge of the handler and carried into the filter. It is a value, not an
// interface, so there is nothing to stub into an entitled state by accident.
type Viewer struct {
	entitled bool
}

// Anonymous is the viewer for a request with no usable session. Guests with a
// guest-session cookie are anonymous for this purpose: a guest session is a
// free-view allowance on pre-match analysis, not an account.
func Anonymous() Viewer { return Viewer{} }

// PublicRead permits publication on an explicitly public in-play surface.
// It carries no account identity or capabilities outside this serializer.
// Legacy gated callers continue to use Anonymous or ForContext.
func PublicRead() Viewer { return Viewer{entitled: true} }

// ForContext derives the viewer from a resolved access context. It asks the
// existing capability machinery rather than re-deriving entitlement from a
// tier string, so a future per-account revocation (an abusive scraper, a
// chargeback) takes effect here with no change to this package.
func ForContext(ctx access.Context) Viewer {
	return Viewer{entitled: ctx.Allows(access.CapabilityInPlayPredictions)}
}

// Entitled reports whether this viewer may receive predictions. Exposed for
// handlers that need to vary a header or a metric, never to let a caller
// assemble the entitled payload itself.
func (v Viewer) Entitled() bool { return v.entitled }

// Filter shapes engine output for a viewer. It holds no state; it is a named
// type rather than a bare function so that the one auditable place §8.4 asks
// for is a thing you can point at.
type Filter struct{}

// NewFilter returns the entitlement filter.
func NewFilter() Filter { return Filter{} }

// Score is the public part of an in-play fixture that never gets gated.
type Score struct {
	Home int `json:"home"`
	Away int `json:"away"`
}

// Unlock tells an anonymous client what to offer. Present only when something
// was withheld.
type Unlock struct {
	Reason string `json:"reason"`
	CTA    string `json:"cta"`
}

// Market is the shaped form of one market.
//
// Prediction and the audit timestamps are unexported and have no setter. The
// only code that can populate them is Filter.Shape, in this file, which
// populates them only for an entitled viewer. A handler in another package
// physically cannot construct a Market carrying a probability, which is what
// makes §8.1 a property of the type system instead of a rule people remember.
type Market struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Locked bool   `json:"locked"`

	prediction      *shapedPrediction
	dataCutoffAt    time.Time
	matchMinute     int
	settlementState string
}

type shapedPrediction struct {
	Selection string `json:"selection"`
	// Retained for existing clients. New clients read ProbabilityBp.
	Probability float64 `json:"probability"`
	// Integer basis points out of 10,000, so web and Flutter cannot round the
	// same market differently. See internal/predictions/basispoints.
	ProbabilityBp   int     `json:"probability_bp"`
	Confidence      string  `json:"confidence"`
	ConfidenceScore float64 `json:"confidence_score"`
}

// MarshalJSON emits the gated fields only when the filter attached them.
//
// Written by hand rather than with struct tags because the gated fields have
// to be unexported to be unforgeable, and encoding/json will not emit an
// unexported field. The wire shape is identical either way -- locked markets
// simply stop after "locked".
func (m Market) MarshalJSON() ([]byte, error) {
	payload := map[string]any{
		"key":    m.Key,
		"label":  m.Label,
		"locked": m.Locked,
	}
	if m.prediction != nil {
		payload["prediction"] = m.prediction
		if !m.dataCutoffAt.IsZero() {
			payload["data_cutoff_at"] = m.dataCutoffAt.UTC().Format(time.RFC3339)
		}
		payload["match_minute"] = m.matchMinute
		// Omitted rather than sent empty while a market is still in play: an
		// absent key says "not graded", which is the truth, where "" would
		// make every client invent its own meaning for the empty string.
		if m.settlementState != "" {
			payload["settlement"] = m.settlementState
		}
	}
	return json.Marshal(payload)
}

// Fixture is the shaped in-play payload: identical schema for both viewers,
// differing only in `locked` and the presence of `prediction`.
type Fixture struct {
	FixtureID int64  `json:"fixture_id"`
	Status    string `json:"status"`
	Minute    int    `json:"minute"`
	// Crest URLs, exported and ungated: a logo is not model output, so it is
	// served to both tiers and carries no entitlement decision. Always emitted,
	// empty when the team has no crest on record -- the same convention
	// acca.Leg uses, because the clients key on the empty string to decide
	// between an <img> and a drawn shield and an absent key would make them
	// handle the same gap a second way.
	HomeTeamLogo string   `json:"home_team_logo"`
	AwayTeamLogo string   `json:"away_team_logo"`
	Stale        bool     `json:"stale"`
	Score        Score    `json:"score"`
	Markets      []Market `json:"markets"`
	LockedCount  int      `json:"locked_count"`
	Unlock       *Unlock  `json:"unlock,omitempty"`
}

// Shape converts engine state into the payload this viewer may see.
//
// Both branches walk the same market list and emit the same keys, so an
// anonymous client renders a complete board with locked cells rather than a
// page with holes in it -- and a regression that forgets to lock a market
// shows up as a present `prediction` key, which the CI invariant looks for.
func (Filter) Shape(viewer Viewer, state FixtureState) Fixture {
	shaped := Fixture{
		FixtureID: state.FixtureID,
		Status:    state.Status,
		Minute:    state.Minute,
		// Copied before the gate branches, and identically for both viewers:
		// the crest is public and the schema must not vary by tier.
		HomeTeamLogo: state.HomeTeamLogo,
		AwayTeamLogo: state.AwayTeamLogo,
		Stale:        state.Stale,
		Score:        Score{Home: state.HomeGoals, Away: state.AwayGoals},
		Markets:      make([]Market, 0, len(state.Markets)),
	}
	for _, market := range state.Markets {
		shapedMarket := Market{Key: market.Key, Label: market.Label}
		switch {
		case !viewer.entitled:
			// The gated branch. Nothing from `market.Prediction` is read here,
			// so there is no value to accidentally carry through.
			shapedMarket.Locked = true
			shaped.LockedCount++
		case market.Prediction == nil:
			// Entitled, but the engine has not published this market yet.
			// Unlocked and empty: not a lock, and not a reason to show a CTA.
		default:
			shapedMarket.prediction = &shapedPrediction{
				Selection:       market.Prediction.Selection,
				Probability:     market.Prediction.Probability,
				ProbabilityBp:   basispoints.FromProbability(market.Prediction.Probability),
				Confidence:      market.Prediction.Confidence,
				ConfidenceScore: market.Prediction.ConfidenceScore,
			}
			shapedMarket.dataCutoffAt = market.DataCutoffAt
			shapedMarket.matchMinute = market.MatchMinute
			shapedMarket.settlementState = market.SettlementState
		}
		shaped.Markets = append(shaped.Markets, shapedMarket)
	}
	if shaped.LockedCount > 0 {
		shaped.Unlock = &Unlock{Reason: ReasonAuthRequired, CTA: RegisterCTA}
	}
	return shaped
}

// ShapeAll applies Shape across a feed.
func (f Filter) ShapeAll(viewer Viewer, states []FixtureState) []Fixture {
	shaped := make([]Fixture, 0, len(states))
	for _, state := range states {
		shaped = append(shaped, f.Shape(viewer, state))
	}
	return shaped
}

// SeriesPoint is one recorded point on a fixture's probability graph.
//
// Markets is the model's own output, already in basis points, exactly as the
// engine wrote it: {"match_result_in_play": {"home": 4800, "draw": 2700,
// "away": 2500}}. Kept as a map rather than a typed struct because the market
// set is the engine's to decide -- it already publishes goals, cards, corners
// and next-goal lines, and a struct here would have to be edited every time the
// model gains one.
type SeriesPoint struct {
	Sequence   int64
	Minute     int
	Trigger    string
	EventKey   string
	HomeGoals  int
	AwayGoals  int
	CapturedAt time.Time
	Markets    map[string]map[string]int
	// What happened at this minute, when the snapshot was event-triggered and a
	// matching record exists. Absent for a timer tick.
	Event *SeriesEvent
}

// SeriesEvent names the incident behind a marker on the probability graph.
//
// It is resolved by joining fixture_events on minute rather than by following
// inplay_snapshots.triggered_by_event_key, which is empty on every row in
// production -- the snapshot writer decides "a goal happened" by comparing this
// scoreline to the previous one, so it never holds an event record to take a
// key from. The join is the only link that actually exists.
type SeriesEvent struct {
	Type   string
	Detail string
	// "home" or "away", resolved from the fixture's own team ids.
	Side   string
	Player string
}

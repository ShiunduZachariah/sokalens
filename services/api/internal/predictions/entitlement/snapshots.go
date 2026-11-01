package entitlement

import (
	"encoding/json"
	"time"
)

// The stream and the snapshot series are the paid asset over time.
//
// A snapshot carries every market the model produced, as basis points. That is
// strictly MORE than `/v1/inplay/fixtures` hands out -- the current numbers plus
// their whole history -- so it has to be gated at least as tightly. Gating the
// point-in-time payload and then streaming the same probabilities would be the
// same leak with extra steps.
//
// So the gated fields here are unexported with a hand-written MarshalJSON, the
// same construction as Market: no package outside this one can build a Snapshot
// that carries probabilities, whatever it passes. §8.1 stays a property of the
// type system rather than a rule a future handler has to remember.

// SnapshotState is one recorded point, pre-gate. Callers load whatever they like
// and hand it over; what reaches the client is not their decision.
type SnapshotState struct {
	FixtureID      int64
	Sequence       int64
	CapturedAt     time.Time
	DataCutoffAt   time.Time
	Trigger        string
	TriggeredByKey string
	ModelHealth    string
	Minute         int
	Period         string
	HomeGoals      int
	AwayGoals      int
	HomeRedCards   int
	AwayRedCards   int
	// Markets per key, each selection in basis points out of 10,000.
	Markets map[string]map[string]int
}

// Snapshot is the shaped point. Identical schema for both viewers, differing
// only in `locked` and the presence of `markets`.
type Snapshot struct {
	FixtureID    int64  `json:"fixture_id"`
	Sequence     int64  `json:"sequence"`
	Trigger      string `json:"trigger"`
	ModelHealth  string `json:"model_health"`
	Minute       int    `json:"minute"`
	Period       string `json:"period"`
	Score        Score  `json:"score"`
	RedCards     Score  `json:"red_cards"`
	Locked       bool   `json:"locked"`
	capturedAt   time.Time
	dataCutoffAt time.Time
	triggeredBy  string
	markets      map[string]map[string]int
}

// MarshalJSON emits the gated fields only when the filter attached them.
//
// Written by hand for the same reason Market's is: the gated fields must be
// unexported to be unforgeable, and encoding/json will not emit an unexported
// field. A locked snapshot still carries its clock, score and trigger, so a
// client can draw the shape of the match and the markers on it -- it simply has
// no numbers on the lines.
func (s Snapshot) MarshalJSON() ([]byte, error) {
	payload := map[string]any{
		"fixture_id":   s.FixtureID,
		"sequence":     s.Sequence,
		"trigger":      s.Trigger,
		"model_health": s.ModelHealth,
		"minute":       s.Minute,
		"period":       s.Period,
		"score":        s.Score,
		"red_cards":    s.RedCards,
		"locked":       s.Locked,
	}
	if s.triggeredBy != "" {
		payload["triggered_by_event_key"] = s.triggeredBy
	}
	if s.markets != nil {
		payload["markets"] = s.markets
		// The audit envelope travels with the numbers it describes, and only
		// with them. `Market` exposes no timestamp to an anonymous viewer, so
		// neither does this -- a locked point is positioned by its sequence and
		// match minute, which is all a client needs to place it on a line.
		if !s.capturedAt.IsZero() {
			payload["captured_at"] = s.capturedAt.UTC().Format(time.RFC3339)
		}
		if !s.dataCutoffAt.IsZero() {
			payload["data_cutoff_at"] = s.dataCutoffAt.UTC().Format(time.RFC3339)
		}
	}
	return json.Marshal(payload)
}

// Event is one timeline entry. Not gated: a goal, a card and a substitution are
// public facts about a football match, and the retraction flag is the integrity
// signal a client needs to cross out a withdrawn goal.
type Event struct {
	Sequence    int64  `json:"sequence"`
	FixtureID   int64  `json:"fixture_id"`
	IdentityKey string `json:"identity_key"`
	Kind        string `json:"kind"`
	Detail      string `json:"detail"`
	Minute      int    `json:"minute"`
	Extra       *int   `json:"extra,omitempty"`
	TeamID      *int64 `json:"team_id,omitempty"`
	PlayerName  string `json:"player_name,omitempty"`
	Retracted   bool   `json:"retracted"`
}

// ChangeSet is everything after a cursor, in one order.
//
// Snapshots and events share the sequence space, so a client applies this list
// in order and ends up in the same state whichever transport delivered it --
// which is the point of the stream and the polling fallback reading the same
// query.
type ChangeSet struct {
	Snapshots []Snapshot `json:"snapshots"`
	Events    []Event    `json:"events"`
	// The highest sequence in this set, or the cursor supplied when it is empty.
	// A client resumes from here; it must not compute it from the lists, which
	// may be truncated by the limit.
	Cursor int64 `json:"cursor"`
	// True when the limit truncated the set, so a client polls again immediately
	// rather than waiting for the next tick and falling further behind.
	More bool `json:"more"`
	// Carried on every locked delta, not just the first load.
	//
	// A client can legitimately start here -- reconnecting from a stored cursor
	// without re-fetching the overview -- and would otherwise never learn that
	// the blank lines it is drawing are unlockable. Repeating it costs a handful
	// of bytes on a feed that already carries a market set.
	Unlock *Unlock `json:"unlock,omitempty"`
}

// ChangeSetState is a ChangeSet pre-gate.
type ChangeSetState struct {
	Snapshots []SnapshotState
	Events    []Event
	Cursor    int64
	More      bool
}

// OverviewState is the combined first load, pre-gate.
type OverviewState struct {
	Fixtures  []FixtureState
	Snapshots []SnapshotState
	Events    []Event
	Cursor    int64
}

// Overview is the combined first load: enough for a cold client to render the
// board, the lines and the markers without fanning out, plus the cursor to
// resume from.
type Overview struct {
	Fixtures    []Fixture  `json:"fixtures"`
	Snapshots   []Snapshot `json:"snapshots"`
	Events      []Event    `json:"events"`
	Cursor      int64      `json:"cursor"`
	Entitled    bool       `json:"entitled"`
	LockedCount int        `json:"locked_count"`
	Unlock      *Unlock    `json:"unlock,omitempty"`
}

// ShapeSnapshot converts one recorded point into what this viewer may see.
func (Filter) ShapeSnapshot(viewer Viewer, state SnapshotState) Snapshot {
	shaped := Snapshot{
		FixtureID:   state.FixtureID,
		Sequence:    state.Sequence,
		Trigger:     state.Trigger,
		ModelHealth: state.ModelHealth,
		Minute:      state.Minute,
		Period:      state.Period,
		Score:       Score{Home: state.HomeGoals, Away: state.AwayGoals},
		RedCards:    Score{Home: state.HomeRedCards, Away: state.AwayRedCards},
		capturedAt:  state.CapturedAt,
		triggeredBy: state.TriggeredByKey,
	}
	if !viewer.entitled {
		// The gated branch reads nothing from state.Markets, so there is no
		// value to carry through by accident.
		shaped.Locked = true
		return shaped
	}
	shaped.markets = state.Markets
	shaped.dataCutoffAt = state.DataCutoffAt
	return shaped
}

// ShapeSnapshots preserves order: the series is a time series, and a client
// draws it in the order it arrives.
func (f Filter) ShapeSnapshots(viewer Viewer, states []SnapshotState) []Snapshot {
	shaped := make([]Snapshot, 0, len(states))
	for _, state := range states {
		shaped = append(shaped, f.ShapeSnapshot(viewer, state))
	}
	return shaped
}

// ShapeChanges gates a change set. Events pass through unchanged; snapshots do
// not.
func (f Filter) ShapeChanges(viewer Viewer, state ChangeSetState) ChangeSet {
	changes := ChangeSet{
		Snapshots: f.ShapeSnapshots(viewer, state.Snapshots),
		Events:    state.Events,
		Cursor:    state.Cursor,
		More:      state.More,
	}
	if !viewer.entitled && len(changes.Snapshots) > 0 {
		changes.Unlock = &Unlock{Reason: ReasonAuthRequired, CTA: RegisterCTA}
	}
	return changes
}

// ShapeOverview gates the combined first load.
func (f Filter) ShapeOverview(viewer Viewer, state OverviewState) Overview {
	fixtures := f.ShapeAll(viewer, state.Fixtures)
	locked := 0
	for _, fixture := range fixtures {
		locked += fixture.LockedCount
	}
	overview := Overview{
		Fixtures:    fixtures,
		Snapshots:   f.ShapeSnapshots(viewer, state.Snapshots),
		Events:      state.Events,
		Cursor:      state.Cursor,
		Entitled:    viewer.Entitled(),
		LockedCount: locked,
	}
	// One CTA for the whole payload rather than one per fixture: a board full of
	// locked cells needs to ask once.
	if !viewer.entitled && (locked > 0 || len(state.Snapshots) > 0) {
		overview.Unlock = &Unlock{Reason: ReasonAuthRequired, CTA: RegisterCTA}
	}
	return overview
}

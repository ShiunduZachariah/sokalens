package apifootball

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// EventKind is the meaning of an event, as opposed to the provider's `type`.
//
// The provider's type field does not carry the meaning: an own goal, a penalty
// and an ordinary goal all arrive as type="Goal", and only `detail`
// distinguishes them. Deriving a kind from both fields at the boundary keeps
// that reasoning in one place instead of spreading `detail` string comparisons
// through the timeline UI, where crediting an own goal to the scoring team is
// the one error a reader will definitely notice.
type EventKind string

const (
	EventKindGoal         EventKind = "goal"
	EventKindOwnGoal      EventKind = "own_goal"
	EventKindPenaltyGoal  EventKind = "penalty_goal"
	EventKindPenaltyMiss  EventKind = "penalty_missed"
	EventKindYellowCard   EventKind = "yellow_card"
	EventKindRedCard      EventKind = "red_card"
	EventKindSecondYellow EventKind = "second_yellow_card"
	EventKindSubstitution EventKind = "substitution"
	EventKindVarRetract   EventKind = "var_retraction"
	EventKindVarOther     EventKind = "var_other"
	EventKindUnknown      EventKind = "unknown"
)

// FixtureEvent is one parsed entry from the event list embedded in
// fixtures?live=all.
type FixtureEvent struct {
	FixtureID     int64
	ElapsedMinute int
	// Nil outside added time, rather than 0, so that 90' and 90+0' stay
	// distinguishable.
	ElapsedExtra *int
	// Raw provider values, preserved including inconsistent casing.
	Type   string
	Detail string
	// Derived meaning. Prefer this over Type/Detail.
	Kind         EventKind
	TeamID       *int64
	PlayerID     *int64
	PlayerName   string
	AssistID     *int64
	AssistName   string
	Comments     string
	IdentityKey  string
	IsRetraction bool
}

type liveEventEnvelope struct {
	Response []struct {
		Fixture struct {
			ID int64 `json:"id"`
		} `json:"fixture"`
		Events []rawEvent `json:"events"`
	} `json:"response"`
}

type rawEvent struct {
	Time struct {
		Elapsed *int `json:"elapsed"`
		Extra   *int `json:"extra"`
	} `json:"time"`
	Team struct {
		ID *int64 `json:"id"`
	} `json:"team"`
	Player struct {
		ID   *int64  `json:"id"`
		Name *string `json:"name"`
	} `json:"player"`
	// The assist object is present with null-valued fields on an unassisted
	// goal rather than being omitted, so presence checks must test the value,
	// not the key.
	Assist struct {
		ID   *int64  `json:"id"`
		Name *string `json:"name"`
	} `json:"assist"`
	Type     string  `json:"type"`
	Detail   string  `json:"detail"`
	Comments *string `json:"comments"`
}

// ParseLiveFixtureEvents extracts every event embedded in a fixtures?live=all
// response.
//
// The events are already in the payload we poll for live status, so this adds
// no provider request. Fixtures with no events yield nothing rather than an
// error: a match that has not kicked off is the normal case, not a fault.
func ParseLiveFixtureEvents(payload []byte) ([]FixtureEvent, error) {
	var envelope liveEventEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, fmt.Errorf("decode live fixtures payload: %w", err)
	}

	events := make([]FixtureEvent, 0, 64)
	for _, fixture := range envelope.Response {
		if fixture.Fixture.ID == 0 {
			// An event that cannot be attributed to a fixture cannot be stored
			// or displayed; dropping it is better than inventing an id.
			continue
		}
		// Occurrence counter per fixture. The provider returns the complete
		// event list on every poll, so an event's position among identical
		// ones is stable in a way its minute is not -- see deriveIdentityKey.
		occurrences := make(map[string]int, len(fixture.Events))
		for _, raw := range fixture.Events {
			event, ok := convertEvent(fixture.Fixture.ID, raw)
			if !ok {
				continue
			}
			slot := occurrenceSlot(event)
			event.IdentityKey = deriveIdentityKey(event, occurrences[slot])
			occurrences[slot]++
			events = append(events, event)
		}
	}
	return events, nil
}

func convertEvent(fixtureID int64, raw rawEvent) (FixtureEvent, bool) {
	// An event with no minute cannot be placed on a timeline. The provider has
	// never been observed sending one, which is exactly why it is worth
	// dropping loudly-but-safely rather than defaulting to 0' and putting a
	// phantom event at kickoff.
	if raw.Time.Elapsed == nil {
		return FixtureEvent{}, false
	}

	kind, retraction := classifyEvent(raw.Type, raw.Detail)

	event := FixtureEvent{
		FixtureID:     fixtureID,
		ElapsedMinute: *raw.Time.Elapsed,
		ElapsedExtra:  raw.Time.Extra,
		Type:          strings.TrimSpace(raw.Type),
		Detail:        strings.TrimSpace(raw.Detail),
		Kind:          kind,
		TeamID:        raw.Team.ID,
		PlayerID:      raw.Player.ID,
		PlayerName:    derefString(raw.Player.Name),
		AssistID:      raw.Assist.ID,
		AssistName:    derefString(raw.Assist.Name),
		Comments:      derefString(raw.Comments),
		IsRetraction:  retraction,
	}
	return event, true
}

// classifyEvent maps the provider's (type, detail) pair onto a meaning.
//
// Matching is case-insensitive because the provider's casing is not
// consistent: Goal, Card and Var are capitalised while subst is not. A
// case-sensitive switch silently drops every substitution.
func classifyEvent(rawType, rawDetail string) (EventKind, bool) {
	eventType := strings.ToLower(strings.TrimSpace(rawType))
	detail := strings.ToLower(strings.TrimSpace(rawDetail))

	switch eventType {
	case "goal":
		switch {
		case strings.Contains(detail, "own goal"):
			return EventKindOwnGoal, false
		case strings.Contains(detail, "missed penalty"):
			return EventKindPenaltyMiss, false
		case strings.Contains(detail, "penalty"):
			return EventKindPenaltyGoal, false
		default:
			return EventKindGoal, false
		}
	case "card":
		switch {
		case strings.Contains(detail, "second yellow"):
			return EventKindSecondYellow, false
		case strings.Contains(detail, "red"):
			return EventKindRedCard, false
		case strings.Contains(detail, "yellow"):
			return EventKindYellowCard, false
		default:
			return EventKindUnknown, false
		}
	case "subst", "substitution":
		return EventKindSubstitution, false
	case "var":
		// "Goal cancelled" and "Goal Disallowed - offside" both mean an event
		// already shown is no longer true. The timeline has to be able to
		// withdraw, not only append.
		if strings.Contains(detail, "cancel") || strings.Contains(detail, "disallow") {
			return EventKindVarRetract, true
		}
		return EventKindVarOther, false
	default:
		return EventKindUnknown, false
	}
}

// occurrenceSlot groups events that are identical apart from when they
// happened, so repeats of one event can be counted separately from a genuine
// second one by the same player.
func occurrenceSlot(event FixtureEvent) string {
	team := "-"
	if event.TeamID != nil {
		team = strconv.FormatInt(*event.TeamID, 10)
	}
	return string(event.Kind) + "|" + team
}

// deriveIdentityKey builds a stable identity for an event.
//
// The provider sends no event id, so re-polling a live fixture returns the same
// events again. Hashing what makes an event what it is -- fixture, kind, team
// and its index among events of that kind -- makes a re-ingest a no-op instead
// of a duplicate row.
//
// Deliberately excludes the player as well as the assist, comments and any
// observation timestamp. Every one of those is an attribute the provider
// fills in later on an event it has already reported, and anything that
// arrives late cannot be part of an identity.
//
// The player was the second version of this bug, found 2026-09-22. Bogota FC
// vs Barranquilla showed four goals in a 1-1 match: the provider reported
// each goal first with no scorer and re-sent it minutes later with the name,
// and because the player was in the hash the enriched copy inserted as a
// second goal rather than updating the first.
//
// Removing it does not merge distinct events. Two players booked for the same
// team are two entries in the provider's list and take occurrence 0 and 1; so
// do two substitutions. What collapses is only the same event seen twice at
// different levels of completeness, which is exactly what should collapse.
//
// The raw detail went the same way on 2026-09-22, and for the same reason it
// took three attempts to see: detail is *also* filled in late. Manta FC vs
// Orense SC stored one Var at 82' with an empty detail and a second, minutes
// later, reading "Penalty confirmed" -- one review, shown twice.
//
// What replaces type and detail is Kind, which is derived from both. Kind is
// the right granularity because it separates events a reader would call
// different and unites ones they would call the same: a yellow, a red and a
// second yellow are three kinds, an ordinary goal and a penalty are two, but
// a Var with no detail yet and the same Var once confirmed are one.
//
// Substitutions look like they need the same treatment and do not. The
// provider numbers them "Substitution 1", "Substitution 2" and reorders them
// between polls, and Manta does hold four rows for one pair of substitutions
// at 55' -- but those were written before the player left the hash, which is
// what made a swap look like two new events. Dropping the player already fixed
// it; the four rows are residue and want a data cleanup, not more code.
// TestReorderedSubstitutionsDoNotDouble pins that and passes either way, which
// is the point of keeping it.
//
// The general rule this converged on, after fixing the minute, the player and
// then the detail one at a time: the provider returns the complete list every
// poll, so identity belongs to an event's *position among its own kind* in
// that list, and every descriptive field is something the provider may still
// be in the middle of telling us.
//
// The original of this comment said an assist can be filled in later. It was
// right about the mechanism and wrong about the scope: the assist was not the
// only late-arriving field, and neither was the minute.
//
// Historically it also excluded: an
// assist can be filled in by a later correction to an event that is otherwise
// identical, and treating that as a new event would show the goal twice.
//
// It also excludes the minute, which is the fix. The minute was in this hash
// until 2026-09-21, and the provider revises minutes as a match settles: a
// goal recorded at 29' becomes 30', hashes differently, and inserts as a
// second goal. Manta FC vs Orense SC showed three goals in a 2-0 match --
// Angulo at 29' and again at 30', Moscarelli booked at 44' and again at 45'.
// Across production that was 13 pairs over 12 fixtures, 8 of them goals.
//
// What replaces it is `occurrence`, the event's index among identical events
// in the same poll. The provider returns the complete list every time, so
// position within it is stable where the minute is not. A revised minute
// keeps its index and updates the row; a genuine brace arrives as two entries
// and gets indices 0 and 1, so it stays two goals. Dropping the minute
// without that discriminator would have collapsed every brace into one.
func deriveIdentityKey(event FixtureEvent, occurrence int) string {
	team := "-"
	if event.TeamID != nil {
		team = strconv.FormatInt(*event.TeamID, 10)
	}

	parts := []string{
		strconv.FormatInt(event.FixtureID, 10),
		string(event.Kind),
		team,
		strconv.Itoa(occurrence),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

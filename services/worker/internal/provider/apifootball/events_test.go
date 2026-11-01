package apifootball

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// capturedPayload is the real fixtures?live=all response recorded in #159.
// The plan that specifies this parser is explicit that the six traps must be
// covered "by a real payload rather than a hand-written one", because a
// hand-written fixture only ever contains the cases its author already thought
// of -- which is the same set the parser already handles.
func capturedPayload(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "..", "..", "..",
		"packages", "contracts", "provider", "api-football", "fixtures-live-events.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read captured payload: %v", err)
	}
	return data
}

func TestParseLiveFixtureEventsAgainstCapturedPayload(t *testing.T) {
	events, err := ParseLiveFixtureEvents(capturedPayload(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("captured payload produced no events")
	}

	for i, event := range events {
		if event.FixtureID == 0 {
			t.Errorf("event %d has no fixture id", i)
		}
		if event.IdentityKey == "" {
			t.Errorf("event %d has no identity key", i)
		}
		if event.Kind == "" {
			t.Errorf("event %d has no kind", i)
		}
	}

	// Every identity must be distinct within one response. A collision here
	// would make the writer treat two different events as a re-poll of one and
	// silently drop the second.
	seen := make(map[string]int, len(events))
	for i, event := range events {
		if first, clash := seen[event.IdentityKey]; clash {
			t.Fatalf("identity collision between events %d and %d (%s %s at %d')",
				first, i, event.Type, event.Detail, event.ElapsedMinute)
		}
		seen[event.IdentityKey] = i
	}
}

// Trap 1: `subst` is lower-case while Goal/Card/Var are capitalised. A
// case-sensitive parser drops every substitution, and substitutions are the
// single most common event type in the sample.
func TestSubstitutionsSurviveLowercaseType(t *testing.T) {
	for _, raw := range []string{"subst", "Subst", "SUBST", "substitution"} {
		kind, retraction := classifyEvent(raw, "Substitution 1")
		if kind != EventKindSubstitution {
			t.Errorf("type %q classified as %q, want %q", raw, kind, EventKindSubstitution)
		}
		if retraction {
			t.Errorf("type %q marked as a retraction", raw)
		}
	}
}

// Trap 2: detail carries the meaning, not type. All three of these arrive as
// type="Goal"; crediting an own goal to the scoring team is the error a reader
// will definitely notice.
func TestGoalDetailDeterminesMeaning(t *testing.T) {
	cases := []struct {
		detail string
		want   EventKind
	}{
		{"Normal Goal", EventKindGoal},
		{"Own Goal", EventKindOwnGoal},
		{"own goal", EventKindOwnGoal},
		{"Penalty", EventKindPenaltyGoal},
		{"Missed Penalty", EventKindPenaltyMiss},
	}
	for _, tc := range cases {
		if got, _ := classifyEvent("Goal", tc.detail); got != tc.want {
			t.Errorf("Goal/%q classified as %q, want %q", tc.detail, got, tc.want)
		}
	}
}

func TestCardDetailDeterminesMeaning(t *testing.T) {
	cases := []struct {
		detail string
		want   EventKind
	}{
		{"Yellow Card", EventKindYellowCard},
		{"Red Card", EventKindRedCard},
		{"Second Yellow card", EventKindSecondYellow},
	}
	for _, tc := range cases {
		if got, _ := classifyEvent("Card", tc.detail); got != tc.want {
			t.Errorf("Card/%q classified as %q, want %q", tc.detail, got, tc.want)
		}
	}
}

// Trap 3: Var events withdraw earlier ones. Without this the timeline can only
// append, and a cancelled goal stays on screen as a goal.
func TestVarCancellationIsARetraction(t *testing.T) {
	for _, detail := range []string{"Goal cancelled", "Goal Disallowed - offside", "GOAL CANCELLED"} {
		kind, retraction := classifyEvent("Var", detail)
		if kind != EventKindVarRetract {
			t.Errorf("Var/%q classified as %q, want %q", detail, kind, EventKindVarRetract)
		}
		if !retraction {
			t.Errorf("Var/%q is not marked as a retraction", detail)
		}
	}

	// A Var review that confirms rather than cancels must NOT retract anything.
	kind, retraction := classifyEvent("Var", "Penalty confirmed")
	if retraction {
		t.Errorf("Var/Penalty confirmed wrongly marked as a retraction (kind %q)", kind)
	}
}

// Trap 4: assist is present with null-valued fields rather than absent.
func TestNullAssistBecomesEmptyNotPanic(t *testing.T) {
	payload := []byte(`{"response":[{"fixture":{"id":7},"events":[
	  {"time":{"elapsed":12,"extra":null},"team":{"id":1},"player":{"id":2,"name":"A"},
	   "assist":{"id":null,"name":null},"type":"Goal","detail":"Normal Goal","comments":null}]}]}`)

	events, err := ParseLiveFixtureEvents(payload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].AssistID != nil {
		t.Errorf("assist id = %v, want nil", *events[0].AssistID)
	}
	if events[0].AssistName != "" {
		t.Errorf("assist name = %q, want empty", events[0].AssistName)
	}
}

// Trap 5: added time is a separate field. Collapsing it loses the distinction
// between 90' and 90+3'.
func TestAddedTimeIsPreservedSeparately(t *testing.T) {
	payload := []byte(`{"response":[{"fixture":{"id":9},"events":[
	  {"time":{"elapsed":90,"extra":3},"team":{"id":1},"player":{"id":2,"name":"A"},
	   "assist":{"id":null,"name":null},"type":"Goal","detail":"Normal Goal"},
	  {"time":{"elapsed":90,"extra":null},"team":{"id":1},"player":{"id":3,"name":"B"},
	   "assist":{"id":null,"name":null},"type":"Goal","detail":"Normal Goal"}]}]}`)

	events, err := ParseLiveFixtureEvents(payload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].ElapsedExtra == nil || *events[0].ElapsedExtra != 3 {
		t.Errorf("90+3 lost its extra time: %v", events[0].ElapsedExtra)
	}
	if events[1].ElapsedExtra != nil {
		t.Errorf("90' gained extra time: %v", *events[1].ElapsedExtra)
	}
	// And the two must not collapse onto one identity.
	if events[0].IdentityKey == events[1].IdentityKey {
		t.Error("90' and 90+3' share an identity key")
	}
}

// Trap 6: there is no event id, so identity is derived -- and must be stable
// across polls, which is what makes re-ingest idempotent.
func TestIdentityIsStableAcrossRepolls(t *testing.T) {
	payload := capturedPayload(t)

	first, err := ParseLiveFixtureEvents(payload)
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	second, err := ParseLiveFixtureEvents(payload)
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("event count changed between parses: %d then %d", len(first), len(second))
	}
	for i := range first {
		if first[i].IdentityKey != second[i].IdentityKey {
			t.Fatalf("event %d identity changed between parses", i)
		}
	}
}

// A later poll that fills in a previously-missing assist is the SAME event.
// Including assist in the identity would show the goal twice.
func TestLateAssistDoesNotCreateANewIdentity(t *testing.T) {
	without := []byte(`{"response":[{"fixture":{"id":4},"events":[
	  {"time":{"elapsed":30,"extra":null},"team":{"id":1},"player":{"id":2,"name":"A"},
	   "assist":{"id":null,"name":null},"type":"Goal","detail":"Normal Goal"}]}]}`)
	with := []byte(`{"response":[{"fixture":{"id":4},"events":[
	  {"time":{"elapsed":30,"extra":null},"team":{"id":1},"player":{"id":2,"name":"A"},
	   "assist":{"id":88,"name":"B"},"type":"Goal","detail":"Normal Goal"}]}]}`)

	a, err := ParseLiveFixtureEvents(without)
	if err != nil {
		t.Fatalf("parse without assist: %v", err)
	}
	b, err := ParseLiveFixtureEvents(with)
	if err != nil {
		t.Fatalf("parse with assist: %v", err)
	}
	if a[0].IdentityKey != b[0].IdentityKey {
		t.Error("filling in an assist changed the event identity; the goal would be stored twice")
	}
}

// Two different players scoring in the same minute are different events.
func TestSameMinuteDifferentPlayersAreDistinct(t *testing.T) {
	payload := []byte(`{"response":[{"fixture":{"id":5},"events":[
	  {"time":{"elapsed":30,"extra":null},"team":{"id":1},"player":{"id":2,"name":"A"},
	   "assist":{"id":null,"name":null},"type":"Goal","detail":"Normal Goal"},
	  {"time":{"elapsed":30,"extra":null},"team":{"id":1},"player":{"id":3,"name":"B"},
	   "assist":{"id":null,"name":null},"type":"Goal","detail":"Normal Goal"}]}]}`)

	events, err := ParseLiveFixtureEvents(payload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if events[0].IdentityKey == events[1].IdentityKey {
		t.Error("two players scoring in the same minute collapsed onto one identity")
	}
}

func TestMalformedPayloadReturnsError(t *testing.T) {
	if _, err := ParseLiveFixtureEvents([]byte(`{"response":`)); err == nil {
		t.Fatal("malformed JSON parsed without error")
	}
}

func TestEventsWithoutAMinuteAreDropped(t *testing.T) {
	payload := []byte(`{"response":[{"fixture":{"id":6},"events":[
	  {"time":{"elapsed":null,"extra":null},"team":{"id":1},"player":{"id":2,"name":"A"},
	   "assist":{"id":null,"name":null},"type":"Goal","detail":"Normal Goal"}]}]}`)

	events, err := ParseLiveFixtureEvents(payload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0: an event with no minute cannot be placed on a timeline", len(events))
	}
}

func TestEventsWithoutAFixtureIDAreDropped(t *testing.T) {
	payload := []byte(`{"response":[{"fixture":{"id":0},"events":[
	  {"time":{"elapsed":10,"extra":null},"team":{"id":1},"player":{"id":2,"name":"A"},
	   "assist":{"id":null,"name":null},"type":"Goal","detail":"Normal Goal"}]}]}`)

	events, err := ParseLiveFixtureEvents(payload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0: an event with no fixture cannot be stored", len(events))
	}
}

// A fixture that has not kicked off is the normal case, not a fault.
func TestFixtureWithNoEventsIsNotAnError(t *testing.T) {
	events, err := ParseLiveFixtureEvents([]byte(`{"response":[{"fixture":{"id":8},"events":[]}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0", len(events))
	}
}

func TestEmptyResponseIsNotAnError(t *testing.T) {
	events, err := ParseLiveFixtureEvents([]byte(`{"response":[]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0", len(events))
	}
}

// An unrecognised type must not be silently discarded: a provider that adds a
// new event type should show up as unknown, not vanish.
func TestUnknownTypeIsKeptAsUnknown(t *testing.T) {
	payload := []byte(`{"response":[{"fixture":{"id":11},"events":[
	  {"time":{"elapsed":55,"extra":null},"team":{"id":1},"player":{"id":2,"name":"A"},
	   "assist":{"id":null,"name":null},"type":"Bicycle","detail":"Spectacular"}]}]}`)

	events, err := ParseLiveFixtureEvents(payload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Kind != EventKindUnknown {
		t.Errorf("kind = %q, want %q", events[0].Kind, EventKindUnknown)
	}
	if events[0].Type != "Bicycle" {
		t.Errorf("raw type = %q, want it preserved", events[0].Type)
	}
}

// The Manta FC vs Orense SC defect, 2026-09-21.
//
// The timeline showed three goals in a 2-0 match. The provider had revised one
// goal's minute from 29' to 30' and one booking's from 44' to 45'; the minute
// was part of the identity hash, so each revision hashed differently and
// inserted as a second event. Production held 13 such pairs across 12
// fixtures, 8 of them goals.
//
// The score came from the snapshot and was right. The timeline came from
// events and was wrong, so the page contradicted itself in a way any reader
// who can count would catch.

func goalPayload(minutes []int, playerID int64) []byte {
	events := make([]string, 0, len(minutes))
	for _, minute := range minutes {
		events = append(events, fmt.Sprintf(
			`{"time":{"elapsed":%d},"team":{"id":10},"player":{"id":%d,"name":"Angulo"},"type":"Goal","detail":"Normal Goal"}`,
			minute, playerID))
	}
	return []byte(`{"response":[{"fixture":{"id":4425697},"events":[` + strings.Join(events, ",") + `]}]}`)
}

func TestARevisedMinuteDoesNotBecomeASecondGoal(t *testing.T) {
	// Two polls of the same single goal, the provider having moved it by a
	// minute in between. The identity must be the same both times, or the
	// timeline grows a goal that never happened.
	first, err := ParseLiveFixtureEvents(goalPayload([]int{29}, 500))
	if err != nil {
		t.Fatalf("parse first poll: %v", err)
	}
	second, err := ParseLiveFixtureEvents(goalPayload([]int{30}, 500))
	if err != nil {
		t.Fatalf("parse second poll: %v", err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected one event per poll, got %d and %d", len(first), len(second))
	}
	if first[0].IdentityKey != second[0].IdentityKey {
		t.Fatal("a revised minute produced a new identity; this is the phantom-goal bug")
	}
}

func TestAGenuineBraceStaysTwoGoals(t *testing.T) {
	// The reason the minute could not simply be dropped. Two goals by one
	// player in the same match are two events, and collapsing them would
	// under-report a scoreline -- the opposite error, equally wrong.
	events, err := ParseLiveFixtureEvents(goalPayload([]int{29, 67}, 500))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected two events, got %d", len(events))
	}
	if events[0].IdentityKey == events[1].IdentityKey {
		t.Fatal("a brace collapsed into one goal")
	}
}

func TestABraceSurvivesAMinuteRevision(t *testing.T) {
	// Both properties at once: two real goals, one of them revised. Must stay
	// two goals with both identities unchanged.
	before, err := ParseLiveFixtureEvents(goalPayload([]int{29, 67}, 500))
	if err != nil {
		t.Fatalf("parse before: %v", err)
	}
	after, err := ParseLiveFixtureEvents(goalPayload([]int{30, 67}, 500))
	if err != nil {
		t.Fatalf("parse after: %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("expected two events, got %d", len(after))
	}
	for i := range before {
		if before[i].IdentityKey != after[i].IdentityKey {
			t.Fatalf("event %d changed identity across a minute revision", i)
		}
	}
}

func TestTheMinuteIsNotPartOfTheIdentity(t *testing.T) {
	// Stated directly, so a future change that reintroduces it fails here
	// rather than in a reader's screenshot.
	base := FixtureEvent{FixtureID: 1, Type: "Goal", Detail: "Normal Goal", ElapsedMinute: 10}
	moved := base
	moved.ElapsedMinute = 11
	if deriveIdentityKey(base, 0) != deriveIdentityKey(moved, 0) {
		t.Fatal("the elapsed minute is back in the identity hash")
	}
}

// The Bogota FC vs Barranquilla defect, 2026-09-22.
//
// Four goals in a 1-1 match. The provider reports an event first with no
// player, then re-sends it enriched with the scorer's name, and the player was
// part of the identity -- so the enriched copy inserted as a second goal.
//
// The same shape as the minute a day earlier, and the same lesson: an
// attribute the provider fills in later cannot be part of an identity.

func eventJSON(minute int, team int64, typ, detail string, player string) string {
	playerJSON := `"player":{}`
	if player != "" {
		playerJSON = fmt.Sprintf(`"player":{"id":%d,"name":%q}`, len(player)*7, player)
	}
	return fmt.Sprintf(`{"time":{"elapsed":%d},"team":{"id":%d},%s,"type":%q,"detail":%q}`,
		minute, team, playerJSON, typ, detail)
}

func eventsPayload(events ...string) []byte {
	return []byte(`{"response":[{"fixture":{"id":3957154},"events":[` + strings.Join(events, ",") + `]}]}`)
}

func TestAnEventEnrichedWithAScorerIsNotASecondGoal(t *testing.T) {
	bare, err := ParseLiveFixtureEvents(eventsPayload(eventJSON(35, 1466, "Goal", "Normal Goal", "")))
	if err != nil {
		t.Fatalf("parse bare: %v", err)
	}
	named, err := ParseLiveFixtureEvents(eventsPayload(eventJSON(35, 1466, "Goal", "Normal Goal", "L. Gomez")))
	if err != nil {
		t.Fatalf("parse named: %v", err)
	}
	if bare[0].IdentityKey != named[0].IdentityKey {
		t.Fatal("a goal gained an identity when the scorer arrived; this is the 1-1-shows-four-goals bug")
	}
}

func TestTwoPlayersBookedForOneTeamStayTwoCards(t *testing.T) {
	// The risk in removing the player from the identity. Two bookings for the
	// same team share every remaining field, so only the occurrence index
	// keeps them apart -- and merging them would under-report discipline the
	// same way the duplicate over-reported goals.
	events, err := ParseLiveFixtureEvents(eventsPayload(
		eventJSON(25, 1458, "Card", "Yellow Card", "Y. Mosquera"),
		eventJSON(70, 1458, "Card", "Yellow Card", "J. Perez"),
	))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected two cards, got %d", len(events))
	}
	if events[0].IdentityKey == events[1].IdentityKey {
		t.Fatal("two players booked for one team collapsed into one card")
	}
}

func TestSimultaneousSubstitutionsStayDistinct(t *testing.T) {
	// Both at 56', same team, differing only by detail -- a real pair from the
	// fixture that exposed the bug, and one the fix must not merge.
	events, err := ParseLiveFixtureEvents(eventsPayload(
		eventJSON(56, 1466, "subst", "Substitution 1", "S. Borja"),
		eventJSON(56, 1466, "subst", "Substitution 2", "C. Penate"),
	))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(events) != 2 || events[0].IdentityKey == events[1].IdentityKey {
		t.Fatal("two simultaneous substitutions collapsed into one")
	}
}

func TestEnrichmentAndAMinuteRevisionTogether(t *testing.T) {
	// Both defects at once, which is how they actually arrive: the provider
	// settles the clock and fills in the scorer in the same later poll.
	first, err := ParseLiveFixtureEvents(eventsPayload(eventJSON(42, 1458, "Goal", "Normal Goal", "")))
	if err != nil {
		t.Fatalf("parse first: %v", err)
	}
	second, err := ParseLiveFixtureEvents(eventsPayload(eventJSON(43, 1458, "Goal", "Normal Goal", "H. Sandoval Lasso")))
	if err != nil {
		t.Fatalf("parse second: %v", err)
	}
	if first[0].IdentityKey != second[0].IdentityKey {
		t.Fatal("a goal that gained both a minute correction and a scorer became a second goal")
	}
}

func TestTheScorerIsNotPartOfTheIdentity(t *testing.T) {
	// Stated directly, so reintroducing it fails here rather than in a 1-1
	// match showing four goals.
	id := int64(99)
	withPlayer := FixtureEvent{FixtureID: 1, Type: "Goal", Detail: "Normal Goal", PlayerID: &id}
	withoutPlayer := FixtureEvent{FixtureID: 1, Type: "Goal", Detail: "Normal Goal"}
	if deriveIdentityKey(withPlayer, 0) != deriveIdentityKey(withoutPlayer, 0) {
		t.Fatal("the player is back in the identity hash")
	}
}

// varPayload is one VAR review at 82', with the detail the provider had at the
// time. Taken from Manta FC vs Orense SC (fixture 4425697), which stored the
// review twice: once bare, once confirmed.
func varPayload(detail string) []byte {
	return []byte(fmt.Sprintf(
		`{"response":[{"fixture":{"id":4425697},"events":[`+
			`{"time":{"elapsed":82},"team":{"id":1992},"player":{"id":900,"name":"Alan Gaston Lorenzo"},`+
			`"type":"Var","detail":%q}]}]}`, detail))
}

func TestALateVarDetailDoesNotBecomeASecondReview(t *testing.T) {
	// The provider announces a review before it says what the review decided.
	// Both polls describe one stoppage; if the detail is part of the identity
	// the confirmed copy inserts beside the bare one and the timeline shows
	// two reviews of a single incident.
	bare, err := ParseLiveFixtureEvents(varPayload(""))
	if err != nil {
		t.Fatalf("parse bare poll: %v", err)
	}
	confirmed, err := ParseLiveFixtureEvents(varPayload("Penalty confirmed"))
	if err != nil {
		t.Fatalf("parse confirmed poll: %v", err)
	}
	if len(bare) != 1 || len(confirmed) != 1 {
		t.Fatalf("expected one event per poll, got %d and %d", len(bare), len(confirmed))
	}
	if bare[0].IdentityKey != confirmed[0].IdentityKey {
		t.Fatal("a late VAR detail produced a new identity; the review is now on the timeline twice")
	}
}

func TestAVarRetractionStaysDistinctFromAConfirmation(t *testing.T) {
	// The counterpart the fix must not break. Keying on Kind rather than the
	// raw detail is only safe because a cancellation and a confirmation are
	// different kinds -- collapsing them would let a disallowed goal be
	// updated into a confirmed one, which is the worst error on a timeline.
	confirmed, err := ParseLiveFixtureEvents(varPayload("Penalty confirmed"))
	if err != nil {
		t.Fatalf("parse confirmed: %v", err)
	}
	cancelled, err := ParseLiveFixtureEvents(varPayload("Goal cancelled"))
	if err != nil {
		t.Fatalf("parse cancelled: %v", err)
	}
	if confirmed[0].IdentityKey == cancelled[0].IdentityKey {
		t.Fatal("a cancellation and a confirmation share an identity")
	}
	if !cancelled[0].IsRetraction || confirmed[0].IsRetraction {
		t.Fatalf("retraction misclassified: cancelled=%v confirmed=%v",
			cancelled[0].IsRetraction, confirmed[0].IsRetraction)
	}
}

// substPayload is a double substitution at 55', in the order the provider
// listed it. Manta sent these two players as "Substitution 1" and
// "Substitution 2" and then swapped which was which on a later poll.
func substPayload(firstPlayer, secondPlayer string) []byte {
	return []byte(fmt.Sprintf(
		`{"response":[{"fixture":{"id":4425697},"events":[`+
			`{"time":{"elapsed":55},"team":{"id":1980},"player":{"id":1,"name":%q},"type":"subst","detail":"Substitution 1"},`+
			`{"time":{"elapsed":55},"team":{"id":1980},"player":{"id":2,"name":%q},"type":"subst","detail":"Substitution 2"}]}]}`,
		firstPlayer, secondPlayer))
}

func TestReorderedSubstitutionsDoNotDouble(t *testing.T) {
	// The provider's "Substitution N" is its ordering of a set, not a property
	// of any one substitution, and it is not stable between polls. Two
	// substitutions must stay two however they are ordered; production had
	// four rows for this pair.
	first, err := ParseLiveFixtureEvents(substPayload("Angulo", "Mina"))
	if err != nil {
		t.Fatalf("parse first poll: %v", err)
	}
	second, err := ParseLiveFixtureEvents(substPayload("Mina", "Angulo"))
	if err != nil {
		t.Fatalf("parse swapped poll: %v", err)
	}
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("expected two events per poll, got %d and %d", len(first), len(second))
	}
	firstKeys := map[string]bool{first[0].IdentityKey: true, first[1].IdentityKey: true}
	for _, event := range second {
		if !firstKeys[event.IdentityKey] {
			t.Fatal("a reordered substitution produced a new identity; two subs become four rows")
		}
	}
	if first[0].IdentityKey == first[1].IdentityKey {
		t.Fatal("two distinct substitutions collapsed into one")
	}
}

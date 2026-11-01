package matchstate

import (
	"testing"
	"time"
)

func TestNormalizeCoversTheStoredVocabulary(t *testing.T) {
	// Every value normalizeFixtureStatus in the ingestion package can write,
	// plus the provider spellings the football-data importer stores directly.
	for stored, want := range map[string]State{
		"upcoming": StateUpcoming, "live": StateLive, "halftime": StateHalftime,
		"second_half": StateSecondHalf, "extra_time": StateExtraTime, "break": StateBreak,
		"penalties": StatePenalties, "finished": StateFinished, "postponed": StatePostponed,
		"canceled": StateCanceled, "cancelled": StateCanceled, "abandoned": StateAbandoned,
		"suspended": StateSuspended, "interrupted": StateInterrupted, "awarded": StateAwarded,
		"walkover": StateWalkover, "delayed": StateDelayed, "scheduled": StateScheduled,
		"FT": StateFinished, "2H": StateSecondHalf, "HT": StateHalftime, "": StateUpcoming,
	} {
		if got := Normalize(stored); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", stored, got, want)
		}
	}
	// An unrecognised value is unknown rather than silently upcoming: a status
	// nobody can classify must not be presented as a fixture about to start.
	if got := Normalize("something_new"); got != StateUnknown {
		t.Errorf("Normalize of an unrecognised status = %q, want unknown", got)
	}
}

func TestTerminalStatesCannotReopen(t *testing.T) {
	// The display corruption this prevents: a settled match that starts
	// claiming to be in play again after a provider glitch.
	for _, from := range []State{StateFinished, StatePostponed, StateCanceled, StateAbandoned, StateAwarded, StateWalkover} {
		for _, to := range []State{StateLive, StateSecondHalf, StateHalftime, StateUpcoming} {
			if allowed, rejection := Allowed(from, to); allowed {
				t.Errorf("%s -> %s was allowed", from, to)
			} else if rejection.Reason == "" {
				t.Errorf("%s -> %s rejected without a reason", from, to)
			}
		}
	}
}

func TestAStartedMatchCannotReturnToPending(t *testing.T) {
	for _, from := range []State{StateLive, StateHalftime, StateSecondHalf, StateExtraTime} {
		if allowed, _ := Allowed(from, StateUpcoming); allowed {
			t.Errorf("%s -> upcoming was allowed", from)
		}
	}
	// But a pending match may of course still be re-described as pending.
	if allowed, _ := Allowed(StateScheduled, StateDelayed); !allowed {
		t.Error("scheduled -> delayed must be allowed")
	}
}

func TestNormalPlayProgressionIsAllowed(t *testing.T) {
	progression := []State{StateUpcoming, StateLive, StateHalftime, StateSecondHalf, StateExtraTime, StatePenalties, StateFinished}
	for i := 0; i < len(progression)-1; i++ {
		if allowed, rejection := Allowed(progression[i], progression[i+1]); !allowed {
			t.Errorf("%s -> %s was rejected: %s", progression[i], progression[i+1], rejection.Reason)
		}
	}
	// And a match can be abandoned or suspended from any live state.
	for _, to := range []State{StateAbandoned, StateSuspended, StateInterrupted} {
		if allowed, rejection := Allowed(StateSecondHalf, to); !allowed {
			t.Errorf("second_half -> %s was rejected: %s", to, rejection.Reason)
		}
	}
}

func TestOnlyTheReaperProducesUnknown(t *testing.T) {
	if allowed, _ := Allowed(StateSecondHalf, StateUnknown); !allowed {
		t.Error("the reaper must be able to move a stuck live match to unknown")
	}
	// Unknown must never overwrite a real result: that would discard a
	// confirmed scoreline in favour of an absence of information.
	if allowed, _ := Allowed(StateFinished, StateUnknown); allowed {
		t.Error("a finished match must not become unknown")
	}
}

func TestUnknownIsAWaitingRoomNotADeadEnd(t *testing.T) {
	// The defect this pins: unknown used to sit in the terminal set, so
	// `Allowed` refused every way out of it. The reaper could put a match
	// there and nothing could take it back out -- not even the provider
	// publishing the final score. Production reached 922 matches at unknown,
	// every one already carrying a score nobody was allowed to write.
	//
	// Unknown means "we do not have the outcome yet". The moment API-Football
	// gives us one, it must land.
	for _, to := range []State{StateFinished, StatePostponed, StateAbandoned, StateAwarded, StateWalkover, StateCanceled} {
		if allowed, rejection := Allowed(StateUnknown, to); !allowed {
			t.Errorf("unknown -> %s was rejected (%s); a reaped match could never be resolved", to, rejection.Reason)
		}
	}
	if StateUnknown.Settled() {
		t.Error("unknown is not an outcome; treating it as settled is what closed the exit")
	}
}

func TestUnknownStillMakesNoClaimAboutThePresent(t *testing.T) {
	// Leaving the settled set must not turn unknown into something the reaper
	// sweeps again. It asserts nothing about right now, so it cannot go stale,
	// and re-reaping it every pass would be pure churn.
	if !StateUnknown.Quiescent() {
		t.Fatal("unknown must stay quiescent")
	}
	kickoff := time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)
	observation := Observation{State: StateUnknown, KickoffAt: kickoff}
	if got := Assess(observation, kickoff.Add(72*time.Hour)); got != FreshnessCurrent {
		t.Fatalf("freshness = %q three days on, want current", got)
	}
}

func TestAMatchThatWasPlayedNeverReadsAsUpcomingAgain(t *testing.T) {
	// Unknown gets no exemption from the pending rule. A rescheduled fixture
	// returns as a new row; letting this one go back to `scheduled` would only
	// re-advertise a played match as something to predict.
	for _, to := range []State{StateScheduled, StateUpcoming, StateDelayed} {
		if allowed, _ := Allowed(StateUnknown, to); allowed {
			t.Errorf("unknown -> %s was allowed; a played match would read as upcoming", to)
		}
	}
}

func TestTheThreeHourSecondHalfIsCaught(t *testing.T) {
	// The actual reported defect, as it exists in the database: a match in
	// second_half whose live state was last confirmed shortly after kickoff.
	kickoff := time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)
	observation := Observation{
		State:       StateSecondHalf,
		KickoffAt:   kickoff,
		LiveStateAt: kickoff.Add(50 * time.Minute),
	}
	threeHoursIn := kickoff.Add(3 * time.Hour)

	if got := Assess(observation, threeHoursIn); got != FreshnessLost {
		t.Fatalf("freshness = %q, want lost", got)
	}
	if ClockIsCurrent(observation, threeHoursIn) {
		t.Fatal("an hours-old minute must not be presented as current")
	}
	// Three hours is inside the 210-minute hard bound, so it is the freshness
	// ladder that catches this one, not the duration bound. Both matter: the
	// ladder catches a match the provider stopped describing, the bound
	// catches a row whose freshness was never recorded at all.
	if PastHardBound(observation, threeHoursIn) {
		t.Fatal("three hours is inside the hard bound; extra time and penalties reach further")
	}
	if !ShouldReconcile(observation, threeHoursIn) {
		t.Fatal("a lost match must be queued for reconciliation")
	}
	if !PastHardBound(observation, kickoff.Add(210*time.Minute)) {
		t.Fatal("three and a half hours after kickoff is past the hard bound")
	}
}

func TestARowWithNoRecordedFreshnessIsStillBounded(t *testing.T) {
	// Every row written before live_state_at existed has a zero value. Reading
	// that as "confirmed at the epoch" would mark the entire back catalogue
	// lost; ignoring it entirely would leave the pre-existing stuck rows
	// unbounded. The duration bound covers them.
	kickoff := time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)
	observation := Observation{State: StateSecondHalf, KickoffAt: kickoff}

	if got := Assess(observation, kickoff.Add(30*time.Minute)); got != FreshnessCurrent {
		t.Errorf("a recent match with no freshness stamp = %q, want current", got)
	}
	if got := Assess(observation, kickoff.Add(4*time.Hour)); got != FreshnessLost {
		t.Errorf("a four-hour-old match with no freshness stamp = %q, want lost", got)
	}
}

func TestTheLadderFiresInOrder(t *testing.T) {
	kickoff := time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)
	now := kickoff.Add(60 * time.Minute)
	for _, testCase := range []struct {
		age  time.Duration
		want Freshness
	}{
		{30 * time.Second, FreshnessCurrent},
		{89 * time.Second, FreshnessCurrent},
		{90 * time.Second, FreshnessStale},
		{4 * time.Minute, FreshnessStale},
		{5 * time.Minute, FreshnessReconciling},
		{14 * time.Minute, FreshnessReconciling},
		{15 * time.Minute, FreshnessLost},
	} {
		observation := Observation{State: StateSecondHalf, KickoffAt: kickoff, LiveStateAt: now.Add(-testCase.age)}
		if got := Assess(observation, now); got != testCase.want {
			t.Errorf("age %s = %q, want %q", testCase.age, got, testCase.want)
		}
	}
}

func TestStatesThatMakeNoClaimAboutNowNeverGoStale(t *testing.T) {
	// A finished match is just as true an hour later. Only a claim about the
	// present moment can decay.
	//
	// Upcoming and scheduled used to be in this list and are not any more. They
	// do make a claim about the present -- "this has not happened yet" -- and
	// that claim expires the moment kickoff passes. See the test below.
	kickoff := time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)
	for _, state := range []State{StateFinished, StatePostponed, StateCanceled} {
		observation := Observation{State: state, KickoffAt: kickoff, LiveStateAt: kickoff}
		if got := Assess(observation, kickoff.Add(48*time.Hour)); got != FreshnessCurrent {
			t.Errorf("%s two days later = %q, want current", state, got)
		}
		if ShouldReconcile(observation, kickoff.Add(48*time.Hour)) {
			t.Errorf("%s must not be queued for reconciliation", state)
		}
	}
}

// A match still saying it has not kicked off, long after it did.
//
// This reverses part of the rule above, deliberately. "Has not happened yet" is
// a statement about the present, and the clock refutes it: live polling only
// reports matches in play, so one it never saw at all never leaves this state
// and nothing else revisits it. Production held 3,207 such rows on 2026-09-22,
// the oldest kicking off in December 2025, none with a score, 680 of them
// advertised in the indexable sitemap as fixtures still to come, and all of
// them reading as "starting soon" on the in-play board long after full time.
func TestPendingStatesExpireOnceKickoffHasLongPassed(t *testing.T) {
	kickoff := time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)
	for _, state := range []State{StateUpcoming, StateScheduled, StateDelayed} {
		observation := Observation{State: state, KickoffAt: kickoff}

		// Before kickoff, and shortly after it, the claim still stands: a match
		// can start late, and the sweep must never touch the forward schedule.
		if got := Assess(observation, kickoff.Add(-time.Hour)); got != FreshnessCurrent {
			t.Errorf("%s an hour before kickoff = %q, want current", state, got)
		}
		if got := Assess(observation, kickoff.Add(HardBound-time.Minute)); got != FreshnessCurrent {
			t.Errorf("%s just inside the hard bound = %q, want current", state, got)
		}

		// Past every plausible duration it is no longer a claim, it is a
		// leftover.
		if got := Assess(observation, kickoff.Add(HardBound)); got != FreshnessLost {
			t.Errorf("%s past the hard bound = %q, want lost", state, got)
		}
	}
}

func TestAHealthyLiveMatchIsLeftAlone(t *testing.T) {
	// The guard must not fire during normal play, or it will be switched off.
	kickoff := time.Date(2026, 9, 6, 14, 0, 0, 0, time.UTC)
	now := kickoff.Add(70 * time.Minute)
	observation := Observation{State: StateSecondHalf, KickoffAt: kickoff, LiveStateAt: now.Add(-20 * time.Second)}
	if !ClockIsCurrent(observation, now) {
		t.Fatal("a match polled 20 seconds ago must show its clock")
	}
	if ShouldReconcile(observation, now) {
		t.Fatal("a healthy live match must not cost a provider request")
	}
}

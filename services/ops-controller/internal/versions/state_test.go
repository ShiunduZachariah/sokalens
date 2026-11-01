package versions

import "testing"

func TestVersionLifecyclePreservesLKGUntilExplicitValidationAndRollsBack(t *testing.T) {
	v1 := &Artifact{ID: 1, SourceSHA: "1111111111111111111111111111111111111111"}
	v2 := &Artifact{ID: 2, SourceSHA: "2222222222222222222222222222222222222222"}

	state, err := nextState(State{}, SetCandidate, v1)
	if err != nil {
		t.Fatal(err)
	}
	state, err = nextState(state, PromoteCandidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state.Current.ID != 1 || state.LastKnownGood.ID != 1 || state.Previous != nil || state.Candidate != nil {
		t.Fatalf("unexpected bootstrap state: %+v", state)
	}
	state, err = nextState(state, SetCandidate, v2)
	if err != nil {
		t.Fatal(err)
	}
	state, err = nextState(state, PromoteCandidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state.Current.ID != 2 || state.Previous.ID != 1 || state.LastKnownGood.ID != 1 {
		t.Fatalf("promotion overwrote recovery evidence: %+v", state)
	}
	state, err = nextState(state, RollbackToLKG, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state.Current.ID != 1 || state.Previous.ID != 2 || state.LastKnownGood.ID != 1 || state.Candidate != nil {
		t.Fatalf("rollback did not restore exact LKG: %+v", state)
	}
}

func TestMarkLKGIsAnExplicitTransition(t *testing.T) {
	state := State{Revision: 7, Current: &Artifact{ID: 4}, LastKnownGood: &Artifact{ID: 3}}
	after, err := nextState(state, MarkLKG, nil)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != 8 || after.LastKnownGood.ID != 4 || state.LastKnownGood.ID != 3 {
		t.Fatalf("before=%+v after=%+v", state, after)
	}
}

func TestInvalidOrUnsafeTransitionsFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		state  State
		action Action
		want   error
	}{
		{"promote without candidate", State{}, PromoteCandidate, ErrMissingCandidate},
		{"rollback without LKG", State{}, RollbackToLKG, ErrMissingLKG},
		{"mark without current", State{}, MarkLKG, ErrMissingLKG},
		{"rollback already at LKG", State{Current: &Artifact{ID: 1}, LastKnownGood: &Artifact{ID: 1}}, RollbackToLKG, ErrNoStateChange},
		{"unknown action", State{}, Action("deploy_latest"), ErrInvalidCommand},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := nextState(test.state, test.action, nil)
			if err != test.want {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
		})
	}
}

func TestSettingTheSameCandidateTwiceIsRejectedAsNoOp(t *testing.T) {
	artifact := &Artifact{ID: 7}
	_, err := nextState(State{Candidate: artifact}, SetCandidate, artifact)
	if err != ErrNoStateChange {
		t.Fatalf("err=%v", err)
	}
}

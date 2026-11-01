package predictionenginepython

import (
	"context"
	"slices"
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

type recordingExecutor struct {
	commands []Command
	output   string
}

func (r *recordingExecutor) Run(_ context.Context, command Command) (Result, error) {
	r.commands = append(r.commands, command)
	out := r.output
	if out == "" {
		out = `{"scope":"in_play","fixtures_processed":0}`
	}
	return Result{Output: []byte(out)}, nil
}

func TestInPlayHandlerRunsTheInPlayScope(t *testing.T) {
	executor := &recordingExecutor{}
	handler := NewInPlayJobHandler(Config{}, executor)

	if err := handler.Run(context.Background(), jobs.Job{ID: 3, Payload: []byte(`{"scope":"in_play","batch_size":200}`)}); err != nil {
		t.Fatalf("run: %v", err)
	}

	args := executor.commands[0].Args
	if !slices.Contains(args, "run-scope") {
		t.Fatalf("args %v do not run a scope", args)
	}
	if idx := slices.Index(args, "--scope"); idx < 0 || args[idx+1] != InPlayScope {
		t.Fatalf("args %v do not name the in-play scope", args)
	}
	if idx := slices.Index(args, "--limit"); idx < 0 || args[idx+1] != "200" {
		t.Fatalf("args %v did not carry the batch size", args)
	}
}

// Architecture v2 §7.5. The in-play path writes rows in an in-play lifecycle
// stage, so a handler that a malformed payload could point at "upcoming"
// would let the two paths be confused from outside the engine. The handler
// decides, not the payload.
func TestInPlayHandlerIgnoresAScopeFromThePayload(t *testing.T) {
	executor := &recordingExecutor{}
	handler := NewInPlayJobHandler(Config{}, executor)

	if err := handler.Run(context.Background(), jobs.Job{Payload: []byte(`{"scope":"upcoming"}`)}); err != nil {
		t.Fatalf("run: %v", err)
	}

	args := executor.commands[0].Args
	idx := slices.Index(args, "--scope")
	if idx < 0 || args[idx+1] != InPlayScope {
		t.Fatalf("payload scope leaked into args %v", args)
	}
}

// Conversely: the pre-match generation handler must never reach the in-play
// scope on its own. Both directions matter -- §7.5 is about the two paths
// staying parallel, not about one of them being privileged.
func TestTheGenerationHandlerStillRunsWhateverScopeItIsGiven(t *testing.T) {
	executor := &recordingExecutor{output: `{"scope":"upcoming","fixtures_processed":1}`}
	handler := NewJobHandler(Config{}, executor)

	if err := handler.Run(context.Background(), jobs.Job{Payload: []byte(`{"scope":"upcoming"}`)}); err != nil {
		t.Fatalf("run: %v", err)
	}

	args := executor.commands[0].Args
	idx := slices.Index(args, "--scope")
	if idx < 0 || args[idx+1] != "upcoming" {
		t.Fatalf("args %v are not the pre-match scope", args)
	}
	if slices.Contains(args, "--tier") {
		t.Fatalf("the pre-match path must not carry an in-play tier: %v", args)
	}
}

func TestTheTierReachesTheCLIAndDefaultsToTheFasterCadence(t *testing.T) {
	for _, testCase := range []struct{ payload, want string }{
		{`{"tier":"B"}`, "B"},
		{`{"tier":"b"}`, "B"},
		{`{"tier":"A"}`, "A"},
		{`{}`, "A"},
		{`{"tier":"nonsense"}`, "A"},
	} {
		executor := &recordingExecutor{}
		handler := NewInPlayJobHandler(Config{}, executor)
		if err := handler.Run(context.Background(), jobs.Job{Payload: []byte(testCase.payload)}); err != nil {
			t.Fatalf("run %s: %v", testCase.payload, err)
		}
		args := executor.commands[0].Args
		idx := slices.Index(args, "--tier")
		if idx < 0 || args[idx+1] != testCase.want {
			t.Fatalf("payload %s produced args %v, want tier %s", testCase.payload, args, testCase.want)
		}
	}
}

func TestInPlayRejectsPerFixtureRuns(t *testing.T) {
	handler := NewInPlayJobHandler(Config{}, &recordingExecutor{})

	err := handler.Run(context.Background(), jobs.Job{Payload: []byte(`{"fixture_ids":[1,2]}`)})

	if err == nil {
		t.Fatal("expected per-fixture in-play runs to be refused")
	}
}

// §14: the reason a run published nothing is the operational signal, so it has
// to survive the journey from the CLI summary into the Go handler.
func TestSuppressionReasonsSurviveSummaryParsing(t *testing.T) {
	raw := `{"scope":"in_play","fixtures_processed":3,"fixtures_published":1,` +
		`"fixtures_suppressed":2,"predictions_generated":5,` +
		`"suppressed_by_reason":{"STALE_DATA":1,"CADENCE_UNCHANGED":1},"tier":"A"}`
	executor := &recordingExecutor{output: raw}
	handler := NewInPlayJobHandler(Config{}, executor)

	summaries, err := handler.RunWithSummaries(context.Background(), jobs.Job{Payload: []byte(`{}`)})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	got := summaries[0]
	if got.FixturesPublished != 1 || got.FixturesSuppressed != 2 {
		t.Fatalf("published/suppressed = %d/%d, want 1/2", got.FixturesPublished, got.FixturesSuppressed)
	}
	if got.SuppressedByReason["STALE_DATA"] != 1 {
		t.Fatalf("suppression reasons lost in parsing: %v", got.SuppressedByReason)
	}
	if got.Tier != "A" {
		t.Fatalf("tier = %q, want A", got.Tier)
	}
}

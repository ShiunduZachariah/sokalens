package predictionenginepython

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

func TestJobHandlerBuildsScopeCommandFromExistingPredictionJobPayload(t *testing.T) {
	exec := &fakeExecutor{}
	handler := NewJobHandler(Config{
		Command:    "python3",
		Module:     "prediction_engine.cli",
		WorkingDir: "services/prediction-engine",
		Env: map[string]string{
			"DATABASE_URL": "postgres://example",
			"PYTHONPATH":   ".",
		},
	}, exec)

	err := handler.Run(context.Background(), jobs.Job{
		ID:      42,
		Payload: []byte(`{"scope":"upcoming","batch_size":25}`),
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	wantArgs := []string{
		"-m", "prediction_engine.cli",
		"run-scope",
		"--scope", "upcoming",
		"--limit", "25",
	}
	champion := exec.commands[0]
	if champion.Name != "python3" {
		t.Fatalf("command name = %q, want python3", champion.Name)
	}
	if !reflect.DeepEqual(champion.Args, wantArgs) {
		t.Fatalf("command args = %#v, want %#v", champion.Args, wantArgs)
	}
	if champion.Dir != "services/prediction-engine" {
		t.Fatalf("command dir = %q, want services/prediction-engine", champion.Dir)
	}
	if !containsEnv(exec.command.Env, "DATABASE_URL=postgres://example") {
		t.Fatalf("command env missing DATABASE_URL: %#v", exec.command.Env)
	}
	if !containsEnv(exec.command.Env, "PREDICTION_ENGINE_JOB_ID=42") {
		t.Fatalf("command env missing job id: %#v", exec.command.Env)
	}
}

func TestJobHandlerBuildsFixtureCommandsForExplicitFixtureIDs(t *testing.T) {
	exec := &fakeExecutor{}
	handler := NewJobHandler(Config{Command: "python", Module: "prediction_engine.cli"}, exec)

	err := handler.Run(context.Background(), jobs.Job{Payload: []byte(`{"fixture_ids":[1001,1002]}`)})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if len(exec.commands) != 2 {
		t.Fatalf("commands = %d, want 2", len(exec.commands))
	}
	if !reflect.DeepEqual(exec.commands[0].Args, []string{"-m", "prediction_engine.cli", "run-fixture", "--fixture-id", "1001"}) {
		t.Fatalf("first command args = %#v", exec.commands[0].Args)
	}
	if !reflect.DeepEqual(exec.commands[1].Args, []string{"-m", "prediction_engine.cli", "run-fixture", "--fixture-id", "1002"}) {
		t.Fatalf("second command args = %#v", exec.commands[1].Args)
	}
}

func TestSettlementJobHandlerBuildsSettleScopeCommand(t *testing.T) {
	exec := &fakeExecutor{}
	handler := NewSettlementJobHandler(Config{Command: "python", Module: "prediction_engine.cli"}, exec)

	err := handler.Run(context.Background(), jobs.Job{Payload: []byte(`{"scope":"finished","batch_size":50}`)})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	wantArgs := []string{
		"-m", "prediction_engine.cli",
		"settle-scope",
		"--scope", "finished",
		"--limit", "50",
	}
	if !reflect.DeepEqual(exec.commands[0].Args, wantArgs) {
		t.Fatalf("command args = %#v, want %#v", exec.commands[0].Args, wantArgs)
	}
}

// A prediction whose fixture never produces a gradeable result gets no
// `prediction_results` row from `settle-scope`, and the API renders a missing
// row as `pending`. The expiry sweep is what ends that, so it must run on the
// same cycle as settlement -- not on a schedule of its own that can be
// disabled, delayed or forgotten independently.
func TestSettlementJobHandlerAlwaysRunsTheExpirySweepAfterSettling(t *testing.T) {
	exec := &fakeExecutor{}
	handler := NewSettlementJobHandler(Config{Command: "python", Module: "prediction_engine.cli"}, exec)

	err := handler.Run(context.Background(), jobs.Job{Payload: []byte(`{"scope":"finished","batch_size":50}`)})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// Settle, then follow the provider's corrections, then the expiry
	// backstop. Four commands, and the ORDER is the assertion: a fixture whose
	// result landed this cycle must be graded before anything considers it
	// expired, and a reversal must be re-settled before the recount that
	// clears its needs_recount flag. The optional shadow settlement comes last.
	wantOrder := []string{"settle-scope", "resettle-retracted", "recount-reversed", "void-expired", "settle-challengers"}
	if len(exec.commands) != len(wantOrder) {
		t.Fatalf("commands = %d, want %d (%v)", len(exec.commands), len(wantOrder), wantOrder)
	}
	for i, verb := range wantOrder {
		if exec.commands[i].Args[2] != verb {
			t.Fatalf("command %d = %q, want %q", i, exec.commands[i].Args[2], verb)
		}
	}
	wantExpiry := []string{
		"-m", "prediction_engine.cli",
		"void-expired",
		"--limit", strconv.Itoa(defaultExpiryBatchSize),
	}
	if got := commandNamed(t, exec.commands, "void-expired"); !reflect.DeepEqual(got.Args, wantExpiry) {
		t.Fatalf("expiry args = %#v, want %#v", got.Args, wantExpiry)
	}
}

func TestSettlementExpirySweepHonoursPayloadOverrides(t *testing.T) {
	exec := &fakeExecutor{}
	handler := NewSettlementJobHandler(Config{Command: "python", Module: "prediction_engine.cli"}, exec)

	payload := `{"scope":"finished","expiry_batch_size":25,"max_pending_age_hours":72}`
	if err := handler.Run(context.Background(), jobs.Job{Payload: []byte(payload)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	wantExpiry := []string{
		"-m", "prediction_engine.cli",
		"void-expired",
		"--limit", "25",
		"--max-pending-age-hours", "72",
	}
	if got := commandNamed(t, exec.commands, "void-expired"); !reflect.DeepEqual(got.Args, wantExpiry) {
		t.Fatalf("expiry args = %#v, want %#v", got.Args, wantExpiry)
	}
}

// The extra args belong to the settlement command the payload named. Handing
// them to `void-expired`, whose parser does not define them, would fail the
// whole job -- and taking settlement down with it.
func TestSettlementExpirySweepDoesNotInheritSettlementExtraArgs(t *testing.T) {
	exec := &fakeExecutor{}
	handler := NewSettlementJobHandler(Config{Command: "python", Module: "prediction_engine.cli"}, exec)

	payload := `{"scope":"historical","args":["--persist-replay"]}`
	if err := handler.Run(context.Background(), jobs.Job{Payload: []byte(payload)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// Every sweep attached to settlement, not just the expiry one: each has a
	// parser that does not define these flags, and one of them failing fails
	// the whole job and takes settlement with it.
	for _, verb := range []string{"void-expired", "resettle-retracted", "recount-reversed"} {
		sweep := commandNamed(t, exec.commands, verb)
		for _, arg := range sweep.Args {
			if arg == "--persist-replay" {
				t.Fatalf("%s inherited settlement args: %#v", verb, sweep.Args)
			}
		}
	}
}

func TestSettlementExpirySweepCanBeSkippedForOneRun(t *testing.T) {
	exec := &fakeExecutor{}
	handler := NewSettlementJobHandler(Config{Command: "python", Module: "prediction_engine.cli"}, exec)

	payload := `{"scope":"finished","skip_expiry_sweep":true,"skip_reversal_sweep":true}`
	if err := handler.Run(context.Background(), jobs.Job{Payload: []byte(payload)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// Only the optional shadow settlement rides along; the skip flags are
	// about the integrity sweeps, not the challengers.
	if len(exec.commands) != 2 || !exec.commands[1].Optional {
		t.Fatalf("commands = %#v, want settle-scope and the optional settle-challengers", exec.commands)
	}
	commandNamed(t, exec.commands[1:], "settle-challengers")
}

// The provider-correction passes run on every ordinary settlement cycle.
//
// NOTHING RAN EITHER OF THEM BEFORE THIS. The classifier, the revision
// history, resettle_fixture, recount_reversed_results, the needs_recount
// ledger column and both CLI commands were all built and tested, and no job
// invoked them -- so a goal withdrawn by VAR left its settled result standing
// and its accuracy figures counting it. There is no way for that to fail
// loudly: the sweep was not late, it did not exist. This case is the guard.
func TestSettlementRunsTheProviderCorrectionSweeps(t *testing.T) {
	exec := &fakeExecutor{}
	handler := NewSettlementJobHandler(Config{Command: "python", Module: "prediction_engine.cli"}, exec)

	if err := handler.Run(context.Background(), jobs.Job{Payload: []byte(`{"scope":"finished"}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	wantResettle := []string{
		"-m", "prediction_engine.cli",
		"resettle-retracted",
		"--limit", strconv.Itoa(defaultResettleBatchSize),
	}
	if got := commandNamed(t, exec.commands, "resettle-retracted"); !reflect.DeepEqual(got.Args, wantResettle) {
		t.Fatalf("resettle args = %#v, want %#v", got.Args, wantResettle)
	}
	wantRecount := []string{
		"-m", "prediction_engine.cli",
		"recount-reversed",
		"--limit", strconv.Itoa(defaultRecountBatchSize),
	}
	if got := commandNamed(t, exec.commands, "recount-reversed"); !reflect.DeepEqual(got.Args, wantRecount) {
		t.Fatalf("recount args = %#v, want %#v", got.Args, wantRecount)
	}
}

// The recount clears what the re-settlement marks, so it cannot run first.
// Reversed, a reversal would wait a whole cycle before reaching the accuracy
// figures -- which is the kind of lag nobody notices and nobody can explain
// later.
func TestTheRecountRunsAfterTheResettlement(t *testing.T) {
	exec := &fakeExecutor{}
	handler := NewSettlementJobHandler(Config{Command: "python", Module: "prediction_engine.cli"}, exec)

	if err := handler.Run(context.Background(), jobs.Job{Payload: []byte(`{"scope":"finished"}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	position := func(verb string) int {
		for i, command := range exec.commands {
			for _, arg := range command.Args {
				if arg == verb {
					return i
				}
			}
		}
		t.Fatalf("no command ran %q", verb)
		return -1
	}
	if position("resettle-retracted") >= position("recount-reversed") {
		t.Fatalf("recount ran at or before the resettlement: %#v", exec.commands)
	}
}

// Reversal sweeps do not ride the "settle exactly these fixtures" path, on the
// same terms as the expiry sweep: naming rows is an operator asking about those
// rows, not a request to sweep the table for corrections.
func TestTheFixtureIDsPathRunsNoSweeps(t *testing.T) {
	exec := &fakeExecutor{}
	handler := NewSettlementJobHandler(Config{Command: "python", Module: "prediction_engine.cli"}, exec)

	if err := handler.Run(context.Background(), jobs.Job{Payload: []byte(`{"fixture_ids":[101,102]}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	for _, command := range exec.commands {
		for _, arg := range command.Args {
			switch arg {
			case "resettle-retracted", "recount-reversed", "void-expired":
				t.Fatalf("the fixture-ids path ran %q: %#v", arg, exec.commands)
			}
		}
	}
}

func TestSettlementJobHandlerBuildsSettleFixtureCommandsForExplicitFixtureIDs(t *testing.T) {
	exec := &fakeExecutor{}
	handler := NewSettlementJobHandler(Config{Command: "python", Module: "prediction_engine.cli"}, exec)

	err := handler.Run(context.Background(), jobs.Job{Payload: []byte(`{"fixture_ids":[42,43]}`)})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// "Settle exactly these fixtures" is an operator asking about named rows.
	// It must not also sweep the whole table, so there are two commands here
	// and not three.
	if len(exec.commands) != 2 {
		t.Fatalf("commands = %d, want 2", len(exec.commands))
	}
	if !reflect.DeepEqual(exec.commands[0].Args, []string{"-m", "prediction_engine.cli", "settle-fixture", "--fixture-id", "42"}) {
		t.Fatalf("first command args = %#v", exec.commands[0].Args)
	}
	if !reflect.DeepEqual(exec.commands[1].Args, []string{"-m", "prediction_engine.cli", "settle-fixture", "--fixture-id", "43"}) {
		t.Fatalf("second command args = %#v", exec.commands[1].Args)
	}
}

func TestAccuracySnapshotJobHandlerBuildsSnapshotCommandWithoutScope(t *testing.T) {
	exec := &fakeExecutor{}
	handler := NewAccuracySnapshotJobHandler(Config{Command: "python", Module: "prediction_engine.cli"}, exec)

	err := handler.Run(context.Background(), jobs.Job{Payload: []byte(`{}`)})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	wantArgs := []string{"-m", "prediction_engine.cli", "snapshot-accuracy"}
	if !reflect.DeepEqual(exec.commands[0].Args, wantArgs) {
		t.Fatalf("command args = %#v, want %#v", exec.commands[0].Args, wantArgs)
	}
}

func TestAggregatePruneJobHandlerUsesSafeRetentionDefaults(t *testing.T) {
	exec := &fakeExecutor{result: Result{Output: []byte(`{"scope":"retention","public_detail_hours":24,"raw_retention_days":2,"results_examined":8,"snapshots_written":3,"results_pruned":8}`)}}
	handler := NewAggregatePruneJobHandler(Config{Command: "python", Module: "prediction_engine.cli"}, exec)

	summaries, err := handler.RunWithSummaries(context.Background(), jobs.Job{Payload: []byte(`{}`)})
	if err != nil {
		t.Fatalf("RunWithSummaries returned error: %v", err)
	}
	wantArgs := []string{"-m", "prediction_engine.cli", "aggregate-prune-results", "--public-detail-hours", "24", "--raw-retention-days", "2"}
	if !reflect.DeepEqual(exec.command.Args, wantArgs) {
		t.Fatalf("command args = %#v, want %#v", exec.command.Args, wantArgs)
	}
	if len(summaries) != 1 || summaries[0].ResultsExamined != 8 || summaries[0].ResultsPruned != 8 || summaries[0].SnapshotsWritten != 3 || summaries[0].PublicDetailHours != 24 || summaries[0].RawRetentionDays != 2 {
		t.Fatalf("summaries=%+v", summaries)
	}
}

func TestLoadConfigUsesEnvironmentWithoutHardcodedSecrets(t *testing.T) {
	cfg := LoadConfig(map[string]string{
		"DATABASE_URL":                         "postgres://from-env",
		"PREDICTION_ENGINE_CLI_COMMAND":        "python",
		"PREDICTION_ENGINE_CLI_MODULE":         "prediction_engine.worker_cli",
		"PREDICTION_ENGINE_CLI_WORKING_DIR":    "/srv/prediction-engine",
		"PREDICTION_ENGINE_CLI_TIMEOUT":        "45s",
		"PREDICTION_ENGINE_CLI_PYTHONPATH":     "/srv/prediction-engine",
		"PREDICTION_ENGINE_CLI_EXTRA_ENV_JSON": `{"MODEL_REGISTRY_PATH":"/models"}`,
	})

	if cfg.Command != "python" {
		t.Fatalf("command = %q, want python", cfg.Command)
	}
	if cfg.Module != "prediction_engine.worker_cli" {
		t.Fatalf("module = %q, want prediction_engine.worker_cli", cfg.Module)
	}
	if cfg.Script != "" {
		t.Fatalf("script = %q, want empty when module is configured", cfg.Script)
	}
	if cfg.WorkingDir != "/srv/prediction-engine" {
		t.Fatalf("working dir = %q, want /srv/prediction-engine", cfg.WorkingDir)
	}
	if got := cfg.Env["DATABASE_URL"]; got != "postgres://from-env" {
		t.Fatalf("DATABASE_URL env = %q, want postgres://from-env", got)
	}
	if got := cfg.Env["PYTHONPATH"]; got != "/srv/prediction-engine" {
		t.Fatalf("PYTHONPATH env = %q, want /srv/prediction-engine", got)
	}
	if got := cfg.Env["MODEL_REGISTRY_PATH"]; got != "/models" {
		t.Fatalf("extra env = %q, want /models", got)
	}
	if cfg.Timeout.String() != "45s" {
		t.Fatalf("timeout = %s, want 45s", cfg.Timeout)
	}
}

func TestJobHandlerReturnsExecutorOutputOnFailure(t *testing.T) {
	exec := &fakeExecutor{
		result: Result{Output: []byte("bad model input\n")},
		err:    errors.New("exit status 2"),
	}
	handler := NewJobHandler(Config{Command: "python", Module: "prediction_engine.cli"}, exec)

	err := handler.Run(context.Background(), jobs.Job{Payload: []byte(`{"scope":"upcoming"}`)})
	if err == nil {
		t.Fatal("Run returned nil, want error")
	}
	if !strings.Contains(err.Error(), "exit status 2") || !strings.Contains(err.Error(), "bad model input") {
		t.Fatalf("error = %q, want executor error and output", err.Error())
	}
}

func TestJobHandlerParsesSurfacesAndLogsSuccessfulGenerationSummary(t *testing.T) {
	var logs bytes.Buffer
	exec := &fakeExecutor{
		result: Result{Output: []byte("python diagnostic\n" + `{
  "failures": [{"fixture_id": 43, "error_type": "ValueError", "message": "invalid normalized fixture"}],
  "fixture_ids": [42, 43],
  "fixtures_failed": 1,
  "fixtures_processed": 2,
  "fixtures_skipped": 1,
  "predictions_written": 0,
  "scope": "upcoming",
  "skipped_fixture_ids": [42]
}` + "\n")},
	}
	handler := NewJobHandler(Config{
		Command:             "python",
		Module:              "prediction_engine.cli",
		Logger:              slog.New(slog.NewJSONHandler(&logs, nil)),
		ChallengersDisabled: true,
	}, exec)

	summaries, err := handler.RunWithSummaries(
		context.Background(),
		jobs.Job{ID: 42, JobKey: "predictions.generate.upcoming", Payload: []byte(`{"scope":"upcoming"}`)},
	)
	if err != nil {
		t.Fatalf("RunWithSummaries returned error: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("summaries = %d, want 1", len(summaries))
	}
	summary := summaries[0]
	if summary.Scope != "upcoming" || summary.FixturesProcessed != 2 || summary.PredictionsWritten != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.FixturesFailed != 1 || summary.FixturesSkipped != 1 {
		t.Fatalf("failure/skip counts = %d/%d, want 1/1", summary.FixturesFailed, summary.FixturesSkipped)
	}
	if len(summary.Failures) != 1 || summary.Failures[0].FixtureID != 43 {
		t.Fatalf("failures = %#v", summary.Failures)
	}

	logOutput := logs.String()
	for _, want := range []string{
		`"msg":"prediction engine python summary"`,
		`"job_id":42`,
		`"fixtures_processed":2`,
		`"predictions_written":0`,
		`"fixtures_failed":1`,
		`"fixtures_skipped":1`,
		`"fixture_id":43`,
	} {
		if !strings.Contains(logOutput, want) {
			t.Fatalf("log output %q missing %q", logOutput, want)
		}
	}
}

func TestJobHandlerRejectsSuccessfulCommandWithoutJSONSummary(t *testing.T) {
	exec := &fakeExecutor{result: Result{Output: []byte("not a JSON summary\n")}}
	handler := NewJobHandler(Config{Command: "python", Module: "prediction_engine.cli"}, exec)

	err := handler.Run(context.Background(), jobs.Job{Payload: []byte(`{"scope":"upcoming"}`)})

	if err == nil {
		t.Fatal("Run returned nil, want malformed summary error")
	}
	if !strings.Contains(err.Error(), "parse prediction engine python summary") {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestJobHandlerRejectsPayloadWithoutScopeOrFixtureIDs(t *testing.T) {
	exec := &fakeExecutor{}
	handler := NewJobHandler(Config{Command: "python", Module: "prediction_engine.cli"}, exec)

	err := handler.Run(context.Background(), jobs.Job{Payload: []byte(`{}`)})
	if err == nil {
		t.Fatal("Run returned nil, want error")
	}
	if exec.called {
		t.Fatal("executor was called for invalid payload")
	}
}

// The shadow challengers ride the job that owns their stage, run last, and
// never fail it: they publish nothing, so a fault in one must not retry or
// block the champion.
func TestChallengerCommandsRideTheirJobsAsOptionalLastSteps(t *testing.T) {
	cfg := Config{Command: "python", Module: "prediction_engine.cli"}
	cases := []struct {
		name    string
		handler func(Config, Executor) JobHandler
		payload string
		verb    string
	}{
		{"upcoming generation", NewJobHandler, `{"scope":"upcoming"}`, "run-challengers"},
		{"settlement", NewSettlementJobHandler, `{"scope":"finished"}`, "settle-challengers"},
		{"accuracy snapshot", NewAccuracySnapshotJobHandler, `{}`, "challenger-report"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &fakeExecutor{}
			if err := tc.handler(cfg, exec).Run(context.Background(), jobs.Job{Payload: []byte(tc.payload)}); err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
			last := exec.commands[len(exec.commands)-1]
			if !last.Optional || last.Args[len(last.Args)-1] != tc.verb {
				t.Fatalf("last command = %#v, want optional %s", last, tc.verb)
			}
			for _, command := range exec.commands[:len(exec.commands)-1] {
				if command.Optional {
					t.Fatalf("champion command marked optional: %#v", command)
				}
			}
		})
	}
}

func TestChallengersDoNotRideReplayOrNamedFixtureRuns(t *testing.T) {
	cfg := Config{Command: "python", Module: "prediction_engine.cli"}
	for _, payload := range []string{`{"scope":"historical"}`, `{"fixture_ids":[42]}`} {
		exec := &fakeExecutor{}
		if err := NewJobHandler(cfg, exec).Run(context.Background(), jobs.Job{Payload: []byte(payload)}); err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
		for _, command := range exec.commands {
			if command.Optional {
				t.Fatalf("payload %s ran a challenger command: %#v", payload, command)
			}
		}
	}
}

func TestChallengersCanBeSwitchedOffByEnvironment(t *testing.T) {
	cfg := LoadConfig(map[string]string{"PREDICTION_ENGINE_CHALLENGERS_ENABLED": "false"})
	exec := &fakeExecutor{}
	if err := NewJobHandler(cfg, exec).Run(context.Background(), jobs.Job{Payload: []byte(`{"scope":"upcoming"}`)}); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(exec.commands) != 1 {
		t.Fatalf("commands = %d, want only the champion run", len(exec.commands))
	}
	if LoadConfig(map[string]string{}).ChallengersDisabled {
		t.Fatal("challengers must be on by default")
	}
}

func TestOptionalChallengerFailureDoesNotFailTheJob(t *testing.T) {
	exec := &failingVerbExecutor{verb: "run-challengers"}
	handler := NewJobHandler(Config{Command: "python", Module: "prediction_engine.cli"}, exec)
	summaries, err := handler.RunWithSummaries(context.Background(), jobs.Job{Payload: []byte(`{"scope":"upcoming"}`)})
	if err != nil {
		t.Fatalf("Run returned error for a failing optional command: %v", err)
	}
	if len(summaries) != 1 || exec.calls != 2 {
		t.Fatalf("summaries = %d, calls = %d; want the champion summary from two calls", len(summaries), exec.calls)
	}
}

type failingVerbExecutor struct {
	verb  string
	calls int
}

func (f *failingVerbExecutor) Run(_ context.Context, command Command) (Result, error) {
	f.calls++
	if command.Args[len(command.Args)-1] == f.verb {
		return Result{Output: []byte("Traceback: boom")}, errors.New("exit status 1")
	}
	return Result{Output: []byte(`{"scope":"upcoming","fixtures_processed":1}`)}, nil
}

type fakeExecutor struct {
	command  Command
	commands []Command
	result   Result
	err      error
	called   bool
}

func (f *fakeExecutor) Run(_ context.Context, command Command) (Result, error) {
	f.called = true
	f.command = command
	f.commands = append(f.commands, command)
	result := f.result
	if result.Output == nil && f.err == nil {
		result.Output = []byte(`{"failures":[],"fixture_ids":[42],"fixtures_failed":0,"fixtures_processed":1,"fixtures_skipped":0,"predictions_written":1,"scope":"fixture","skipped_fixture_ids":[]}`)
	}
	return result, f.err
}

func containsEnv(env []string, want string) bool {
	for _, item := range env {
		if item == want {
			return true
		}
	}
	return false
}

// commandNamed finds a sweep by its CLI verb rather than by position.
//
// These cases used to index exec.commands[1], which made every one of them
// fail the moment another sweep was attached to settlement -- the failure was
// in the test's arithmetic, not in the behaviour. The verb is what each case
// is actually about.
func commandNamed(t *testing.T, commands []Command, verb string) Command {
	t.Helper()
	for _, command := range commands {
		for _, arg := range command.Args {
			if arg == verb {
				return command
			}
		}
	}
	t.Fatalf("no command ran %q; commands = %#v", verb, commands)
	return Command{}
}

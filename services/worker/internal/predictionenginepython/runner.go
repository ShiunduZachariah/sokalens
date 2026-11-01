package predictionenginepython

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/worker/internal/jobs"
)

const (
	JobKey       = "predictions.python.run"
	DefaultQueue = "predictions"
)

const (
	defaultCommand    = "python"
	defaultModule     = "prediction_engine.cli"
	defaultWorkingDir = "services/prediction-engine"
	defaultTimeout    = 5 * time.Minute
)

type Config struct {
	Command    string
	Module     string
	Script     string
	WorkingDir string
	Timeout    time.Duration
	Env        map[string]string
	Logger     *slog.Logger
}

type Command struct {
	Name string
	Args []string
	Dir  string
	Env  []string
}

type Result struct {
	Output []byte
}

type FixtureFailure struct {
	FixtureID int64  `json:"fixture_id"`
	ErrorType string `json:"error_type"`
	Message   string `json:"message"`
}

type Summary struct {
	Scope                string           `json:"scope"`
	FixtureID            int64            `json:"fixture_id"`
	FixtureIDs           []int64          `json:"fixture_ids"`
	FixturesProcessed    int              `json:"fixtures_processed"`
	PredictionsGenerated int              `json:"predictions_generated"`
	PredictionsInserted  int              `json:"predictions_inserted"`
	PredictionsVersioned int              `json:"predictions_versioned"`
	PredictionsUnchanged int              `json:"predictions_unchanged"`
	PredictionsStale     int              `json:"predictions_stale"`
	PredictionsWritten   int              `json:"predictions_written"`
	FixturesFailed       int              `json:"fixtures_failed"`
	FixturesSkipped      int              `json:"fixtures_skipped"`
	SkippedFixtureIDs    []int64          `json:"skipped_fixture_ids"`
	Failures             []FixtureFailure `json:"failures"`
	ResultsWritten       int              `json:"results_written"`
	ResultsExamined      int              `json:"results_examined"`
	ResultsPruned        int              `json:"results_pruned"`
	ResultsVoided        int              `json:"results_voided"`
	VoidReasons          map[string]int   `json:"void_reasons"`
	SnapshotsWritten     int              `json:"snapshots_written"`
	PublicDetailHours    int              `json:"public_detail_hours"`
	RawRetentionDays     int              `json:"raw_retention_days"`

	// In-play only (architecture v2 7.4-7.6). FixturesPublished and
	// SuppressedByReason are what make a quiet in-play run legible: a run that
	// published nothing because every snapshot was stale and a run that
	// published nothing because the job never fired produce identical
	// PredictionsGenerated, and only the reason breakdown tells them apart.
	FixturesPublished  int            `json:"fixtures_published"`
	FixturesSuppressed int            `json:"fixtures_suppressed"`
	SuppressedByReason map[string]int `json:"suppressed_by_reason"`
	Tier               string         `json:"tier"`
}

type Executor interface {
	Run(context.Context, Command) (Result, error)
}

type OSExecutor struct{}

func (OSExecutor) Run(ctx context.Context, command Command) (Result, error) {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = command.Env
	output, err := cmd.CombinedOutput()
	return Result{Output: output}, err
}

type JobHandler struct {
	cfg       Config
	exec      Executor
	operation operation
}

type operation string

// A finished fixture is frozen once and never revisited, so the batch only has
// to cover the fixtures that ended since the last sweep -- a busy Saturday peaks
// near 60 concurrent matches, so 500 is a wide margin rather than a routine load.
const defaultFinaliseSnapshotBatchSize = 500

// How many live fixtures one tracker refresh covers.
//
// 200 against a stated weekend peak of "over 200 concurrent". The cost per
// fixture is a read of its pre-match picks and an upsert each, with no provider
// call at all, so the batch is bounded to keep one pass predictable rather than
// to ration anything.
const defaultRefreshTrackerBatchSize = 200

// How many retracted-settlement fixtures one pass re-settles, and how many
// reversed rows one pass recounts.
//
// The CLI defaults are 100 and 500; these match them so the two places do not
// drift. A retraction is rare -- a handful of VAR decisions across a weekend --
// so 100 fixtures is a wide margin rather than a routine load, and the recount
// is cheap because it only visits rows a reversal actually touched.
const defaultResettleBatchSize = 100
const defaultRecountBatchSize = 500

const (
	operationGenerate       operation = "generate"
	operationSettle         operation = "settle"
	operationSnapshot       operation = "snapshot"
	operationAggregatePrune operation = "aggregate_prune"
	operationInPlay         operation = "in_play"
	operationFinaliseSnap   operation = "finalise_snapshots"
	operationRefreshTracker operation = "refresh_trackers"
)

// InPlayScope is the CLI scope name for the in-play path. It is the ONLY way
// that path is reached: architecture v2 7.5 requires the in-play branch be
// selected explicitly so a pre-match surface cannot pick up a post-kickoff
// row by accident.
const InPlayScope = "in_play"

type Payload struct {
	Scope string `json:"scope"`
	// Tier selects the 7.6 timer backstop: "A" regenerates every two minutes,
	// "B" every five. Event-driven regeneration -- goal, red card, half-time --
	// is the model's decision and is not affected by this.
	Tier              string   `json:"tier"`
	FixtureIDs        []int64  `json:"fixture_ids"`
	BatchSize         int      `json:"batch_size"`
	Args              []string `json:"args"`
	PublicDetailHours int      `json:"public_detail_hours"`
	RawRetentionDays  int      `json:"raw_retention_days"`
	// MaxPendingAgeHours bounds how long a prediction may stay pending before
	// the expiry sweep voids it. Zero leaves the engine's own default.
	MaxPendingAgeHours int `json:"max_pending_age_hours"`
	// ExpiryBatchSize caps one expiry sweep. Zero uses defaultExpiryBatchSize.
	ExpiryBatchSize int `json:"expiry_batch_size"`
	// SkipExpirySweep turns the backstop off for a single run. It exists for
	// the manual "settle exactly these fixtures" path and for an operator who
	// needs one cycle without it; it is not a configuration knob, because a
	// permanently disabled bound is the defect this job exists to prevent.
	SkipExpirySweep bool `json:"skip_expiry_sweep"`
	// ReversalBatchSize caps how many retracted-settlement fixtures one pass
	// re-settles. Zero uses defaultResettleBatchSize.
	ReversalBatchSize int `json:"reversal_batch_size"`
	// RecountBatchSize caps how many reversed rows one pass recounts into the
	// accuracy rollup. Zero uses defaultRecountBatchSize.
	RecountBatchSize int `json:"recount_batch_size"`
	// SkipReversalSweep turns the provider-correction passes off for a single
	// run, on the same terms as SkipExpirySweep: the manual fixture-ids path,
	// or an operator who needs one cycle without them. Not a configuration
	// knob -- a permanently disabled reversal sweep is the defect these passes
	// exist to prevent, and it is the integrity-critical one.
	SkipReversalSweep bool `json:"skip_reversal_sweep"`
}

// defaultExpiryBatchSize caps one expiry sweep.
//
// The sweep rides the settlement schedule (one minute by default) and does no
// provider work at all -- it is two local writes per prediction -- so the only
// thing this bounds is transaction size. At 500 a cycle the 18,197 predictions
// stranded in production on 2026-09-21 drain in well under an hour, without
// one run holding a long transaction against the settlement path.
const defaultExpiryBatchSize = 500

func LoadConfig(env map[string]string) Config {
	cfg := Config{
		Command:    value(env, "PREDICTION_ENGINE_CLI_COMMAND", defaultCommand),
		Module:     value(env, "PREDICTION_ENGINE_CLI_MODULE", defaultModule),
		Script:     env["PREDICTION_ENGINE_CLI_SCRIPT"],
		WorkingDir: value(env, "PREDICTION_ENGINE_CLI_WORKING_DIR", defaultWorkingDir),
		Timeout:    parseDuration(value(env, "PREDICTION_ENGINE_CLI_TIMEOUT", defaultTimeout.String()), defaultTimeout),
		Env:        map[string]string{},
	}
	if cfg.Script != "" {
		cfg.Module = ""
	}
	if databaseURL := env["DATABASE_URL"]; databaseURL != "" {
		cfg.Env["DATABASE_URL"] = databaseURL
	}
	if pythonPath := value(env, "PREDICTION_ENGINE_CLI_PYTHONPATH", "."); pythonPath != "" {
		cfg.Env["PYTHONPATH"] = pythonPath
	}
	for key, val := range parseExtraEnv(env["PREDICTION_ENGINE_CLI_EXTRA_ENV_JSON"]) {
		cfg.Env[key] = val
	}
	return cfg
}

func LoadConfigFromEnv() Config {
	return LoadConfig(environMap())
}

func NewJobHandler(cfg Config, executor Executor) JobHandler {
	return newJobHandler(cfg, executor, operationGenerate)
}

func NewSettlementJobHandler(cfg Config, executor Executor) JobHandler {
	return newJobHandler(cfg, executor, operationSettle)
}

func NewAccuracySnapshotJobHandler(cfg Config, executor Executor) JobHandler {
	return newJobHandler(cfg, executor, operationSnapshot)
}

func NewAggregatePruneJobHandler(cfg Config, executor Executor) JobHandler {
	return newJobHandler(cfg, executor, operationAggregatePrune)
}

// NewFinaliseSnapshotsJobHandler freezes the full-time in-play snapshot.
//
// This needs a job of its own because `fixtures?live=all` drops a match the
// moment it ends, so no live pass ever observes a fixture at the final whistle.
// Without it the permanent full-time record -- the one thing retention never
// drops -- would simply never be written.
func NewFinaliseSnapshotsJobHandler(cfg Config, executor Executor) JobHandler {
	return newJobHandler(cfg, executor, operationFinaliseSnap)
}

// NewRefreshTrackersJobHandler re-judges pre-match picks on live fixtures.
//
// Separate from the in-play handler on purpose: a fixture whose in-play
// generation is suppressed still has pre-match picks on a public surface, and
// a reader watching one expects it to keep moving. Tying the tracker to in-play
// publication would stop it exactly when the model went quiet.
func NewRefreshTrackersJobHandler(cfg Config, executor Executor) JobHandler {
	return newJobHandler(cfg, executor, operationRefreshTracker)
}

// NewInPlayJobHandler runs the in-play scope (architecture v2 7.4-7.6).
//
// It forces the scope rather than trusting the payload. The in-play path
// writes rows in an in-play lifecycle stage, and a handler that could be
// pointed at "upcoming" by a malformed payload would be a way for the two
// paths to be confused from outside the engine entirely.
func NewInPlayJobHandler(cfg Config, executor Executor) JobHandler {
	return newJobHandler(cfg, executor, operationInPlay)
}

// normalizeTier keeps an unrecognised tier on the faster cadence. Falling back
// to the slower one would silently halve the refresh rate of a live match on a
// typo, which is the more expensive direction to be wrong in.
func normalizeTier(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), "B") {
		return "B"
	}
	return "A"
}

func newJobHandler(cfg Config, executor Executor, op operation) JobHandler {
	cfg = cfg.withDefaults()
	if executor == nil {
		executor = OSExecutor{}
	}
	return JobHandler{cfg: cfg, exec: executor, operation: op}
}

func (h JobHandler) Run(ctx context.Context, job jobs.Job) error {
	_, err := h.RunWithSummaries(ctx, job)
	return err
}

func (h JobHandler) RunWithSummaries(ctx context.Context, job jobs.Job) ([]Summary, error) {
	if h.exec == nil {
		return nil, errors.New("prediction engine python executor is required")
	}
	payload, err := decodePayload(job.Payload)
	if err != nil {
		return nil, err
	}
	if err := validatePayload(payload, h.operation); err != nil {
		return nil, err
	}
	cfg := h.cfg.withDefaults()
	commands := cfg.commands(payload, job, h.operation)
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	summaries := make([]Summary, 0, len(commands))
	for _, command := range commands {
		result, err := h.exec.Run(ctx, command)
		if err != nil {
			return summaries, commandError(err, result.Output)
		}
		summary, err := parseSummary(result.Output)
		if err != nil {
			return summaries, fmt.Errorf("parse prediction engine python summary: %w", err)
		}
		summaries = append(summaries, summary)
		logSummary(ctx, cfg.Logger, job, summary)
	}
	return summaries, nil
}

func decodePayload(raw []byte) (Payload, error) {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	var payload Payload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Payload{}, err
	}
	payload.Scope = strings.TrimSpace(payload.Scope)
	return payload, nil
}

func validatePayload(payload Payload, op operation) error {
	if op == operationInPlay && len(payload.FixtureIDs) > 0 {
		return errors.New("the in-play path runs by scope; per-fixture in-play runs are not supported")
	}
	if op == operationGenerate && payload.Scope == "" && len(payload.FixtureIDs) == 0 {
		return errors.New("prediction engine python payload requires scope or fixture_ids")
	}
	for _, arg := range payload.Args {
		if strings.TrimSpace(arg) == "" {
			return errors.New("prediction engine python extra args must not be empty")
		}
	}
	return nil
}

func (cfg Config) withDefaults() Config {
	if cfg.Command == "" {
		cfg.Command = defaultCommand
	}
	if cfg.Module == "" && cfg.Script == "" {
		cfg.Module = defaultModule
	}
	if cfg.WorkingDir == "" {
		cfg.WorkingDir = defaultWorkingDir
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.Env == nil {
		cfg.Env = map[string]string{}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return cfg
}

func (cfg Config) commands(payload Payload, job jobs.Job, op operation) []Command {
	if op == operationSnapshot {
		return []Command{cfg.commandArgs([]string{"snapshot-accuracy"}, payload.Args, job)}
	}
	if op == operationFinaliseSnap {
		limit := payload.BatchSize
		if limit <= 0 {
			limit = defaultFinaliseSnapshotBatchSize
		}
		return []Command{cfg.commandArgs([]string{
			"finalise-snapshots", "--limit", strconv.Itoa(limit),
		}, payload.Args, job)}
	}
	if op == operationRefreshTracker {
		limit := payload.BatchSize
		if limit <= 0 {
			limit = defaultRefreshTrackerBatchSize
		}
		return []Command{cfg.commandArgs([]string{
			"refresh-trackers", "--limit", strconv.Itoa(limit),
		}, payload.Args, job)}
	}
	if op == operationAggregatePrune {
		publicDetailHours := payload.PublicDetailHours
		if publicDetailHours <= 0 {
			publicDetailHours = 24
		}
		rawRetentionDays := payload.RawRetentionDays
		if rawRetentionDays <= 0 {
			rawRetentionDays = 2
		}
		return []Command{cfg.commandArgs([]string{
			"aggregate-prune-results",
			"--public-detail-hours", strconv.Itoa(publicDetailHours),
			"--raw-retention-days", strconv.Itoa(rawRetentionDays),
		}, payload.Args, job)}
	}

	if len(payload.FixtureIDs) > 0 {
		commands := make([]Command, 0, len(payload.FixtureIDs))
		for _, fixtureID := range payload.FixtureIDs {
			command := "run-fixture"
			if op == operationSettle {
				command = "settle-fixture"
			}
			commands = append(commands, cfg.commandArgs(
				[]string{command, "--fixture-id", strconv.FormatInt(fixtureID, 10)},
				payload.Args,
				job,
			))
		}
		return commands
	}

	scope := payload.Scope
	if scope == "" && op == operationSettle {
		scope = "finished"
	}
	if op == operationInPlay {
		scope = InPlayScope
	}
	command := "run-scope"
	if op == operationSettle {
		command = "settle-scope"
	}
	args := []string{command, "--scope", scope}
	if payload.BatchSize > 0 {
		args = append(args, "--limit", strconv.Itoa(payload.BatchSize))
	}
	if op == operationInPlay {
		args = append(args, "--tier", normalizeTier(payload.Tier))
	}
	commands := []Command{cfg.commandArgs(args, payload.Args, job)}
	if op == operationSettle {
		// Provider corrections, in the only order that works. See
		// reversalCommands.
		commands = append(commands, cfg.reversalCommands(payload, job)...)
		if !payload.SkipExpirySweep {
			commands = append(commands, cfg.expirySweepCommand(payload, job))
		}
	}
	return commands
}

// reversalCommands re-settles fixtures whose goals the provider retracted, then
// recounts the accuracy rollup for the rows that changed.
//
// WITHOUT THIS NOTHING RAN EITHER COMMAND. The classifier, the revision
// history, `resettle_fixture`, `recount_reversed_results`, the `needs_recount`
// ledger column and both CLI commands were all built and tested, and no job
// ever invoked them. So a goal withdrawn by VAR was detected by nobody: the
// settled rows kept the result the disallowed goal produced, and the accuracy
// figures kept counting it. That is the one thing the in-play work is not
// allowed to get wrong -- "if a goal was retracted we need to show that,
// because if we don't we may have issues with integrity" -- and it was failing
// silently, which is the only way it could fail. Found by auditing which
// engine commands have a caller, after the same defect turned up in
// refresh-trackers.
//
// ATTACHED TO SETTLEMENT RATHER THAN SCHEDULED SEPARATELY, for the reason the
// expiry sweep gives and then one more:
//
//   - the three passes are one guarantee -- a prediction gets a result, that
//     result tracks the provider's corrections, and the rollup matches -- and
//     splitting them across schedules creates a state where the first runs and
//     the others silently do not;
//   - ORDER IS LOAD-BEARING HERE, which it is not for the expiry sweep.
//     `resettle-retracted` is what marks a row `needs_recount`, and
//     `recount-reversed` is what clears it. Reversed, the recount would run
//     against the previous pass's work and a reversal would wait a whole cycle
//     before reaching the accuracy figures. Two jobs cannot express that; two
//     commands in one list can.
//
// Like the expiry sweep it does not run on the fixture-ids path: "settle
// exactly these fixtures" is an operator asking about named rows, not a
// request to sweep for corrections.
func (cfg Config) reversalCommands(payload Payload, job jobs.Job) []Command {
	if payload.SkipReversalSweep {
		return nil
	}
	resettleLimit := payload.ReversalBatchSize
	if resettleLimit <= 0 {
		resettleLimit = defaultResettleBatchSize
	}
	recountLimit := payload.RecountBatchSize
	if recountLimit <= 0 {
		recountLimit = defaultRecountBatchSize
	}
	// payload.Args is deliberately not forwarded: those flags belong to
	// settle-scope, and handing them to a parser that does not define them
	// would fail the whole job.
	return []Command{
		cfg.commandArgs([]string{
			"resettle-retracted", "--limit", strconv.Itoa(resettleLimit),
		}, nil, job),
		cfg.commandArgs([]string{
			"recount-reversed", "--limit", strconv.Itoa(recountLimit),
		}, nil, job),
	}
}

// expirySweepCommand is the backstop that runs after every scoped settlement.
//
// It is attached here rather than given its own scheduled job on purpose. The
// settlement pass and the expiry pass are two halves of one guarantee -- a
// prediction gets a real result, or it gets a terminal one -- and splitting
// them across two schedules creates a state where the first runs and the
// second silently does not. Running second also means a fixture whose result
// arrived this cycle is graded properly before the sweep ever looks at it.
//
// It deliberately does not run on the fixture-ids path: "settle exactly these
// fixtures" is an operator asking about named rows, not a request to sweep the
// whole table.
func (cfg Config) expirySweepCommand(payload Payload, job jobs.Job) Command {
	limit := payload.ExpiryBatchSize
	if limit <= 0 {
		limit = defaultExpiryBatchSize
	}
	args := []string{"void-expired", "--limit", strconv.Itoa(limit)}
	if payload.MaxPendingAgeHours > 0 {
		args = append(args, "--max-pending-age-hours", strconv.Itoa(payload.MaxPendingAgeHours))
	}
	// The extra args belong to the settlement command the payload asked for;
	// forwarding them here would hand `settle-scope` flags to a parser that
	// does not define them and fail the whole job.
	return cfg.commandArgs(args, nil, job)
}

func (cfg Config) commandArgs(cliArgs []string, extraArgs []string, job jobs.Job) Command {
	args := make([]string, 0, 12+len(cliArgs)+len(extraArgs))
	switch {
	case cfg.Script != "":
		args = append(args, cfg.Script)
	case cfg.Module != "":
		args = append(args, "-m", cfg.Module)
	}
	args = append(args, cliArgs...)
	args = append(args, extraArgs...)

	env := copyEnv(cfg.Env)
	if job.ID > 0 {
		env["PREDICTION_ENGINE_JOB_ID"] = strconv.FormatInt(job.ID, 10)
	}
	if job.JobKey != "" {
		env["PREDICTION_ENGINE_JOB_KEY"] = job.JobKey
	}

	return Command{
		Name: cfg.Command,
		Args: args,
		Dir:  cfg.WorkingDir,
		Env:  mergeEnv(os.Environ(), env),
	}
}

func commandError(err error, output []byte) error {
	text := strings.TrimSpace(string(output))
	if text == "" {
		return fmt.Errorf("prediction engine python command failed: %w", err)
	}
	return fmt.Errorf("prediction engine python command failed: %w: %s", err, text)
}

func parseSummary(output []byte) (Summary, error) {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return Summary{}, errors.New("command returned an empty summary")
	}

	var lastErr error
	for offset := strings.LastIndex(trimmed, "{"); offset >= 0; offset = strings.LastIndex(trimmed[:offset], "{") {
		var summary Summary
		if err := json.Unmarshal([]byte(trimmed[offset:]), &summary); err != nil {
			lastErr = err
			continue
		}
		summary.Scope = strings.TrimSpace(summary.Scope)
		if summary.Scope == "" {
			return Summary{}, errors.New("summary scope is required")
		}
		return summary, nil
	}
	if lastErr == nil {
		return Summary{}, errors.New("command output does not end with a JSON object")
	}
	return Summary{}, fmt.Errorf("command output does not end with a JSON object: %w", lastErr)
}

func logSummary(ctx context.Context, logger *slog.Logger, job jobs.Job, summary Summary) {
	attrs := []any{
		"job_id", job.ID,
		"job_key", job.JobKey,
		"scope", summary.Scope,
		"fixture_id", summary.FixtureID,
		"fixture_ids", summary.FixtureIDs,
		"fixtures_processed", summary.FixturesProcessed,
		"predictions_generated", summary.PredictionsGenerated,
		"predictions_inserted", summary.PredictionsInserted,
		"predictions_versioned", summary.PredictionsVersioned,
		"predictions_unchanged", summary.PredictionsUnchanged,
		"predictions_stale", summary.PredictionsStale,
		"predictions_written", summary.PredictionsWritten,
		"fixtures_failed", summary.FixturesFailed,
		"fixtures_skipped", summary.FixturesSkipped,
		"skipped_fixture_ids", summary.SkippedFixtureIDs,
		"failures", summary.Failures,
		"results_written", summary.ResultsWritten,
		"results_examined", summary.ResultsExamined,
		"results_pruned", summary.ResultsPruned,
		"results_voided", summary.ResultsVoided,
		"void_reasons", summary.VoidReasons,
		"snapshots_written", summary.SnapshotsWritten,
		"public_detail_hours", summary.PublicDetailHours,
		"raw_retention_days", summary.RawRetentionDays,
		"fixtures_published", summary.FixturesPublished,
		"fixtures_suppressed", summary.FixturesSuppressed,
		"suppressed_by_reason", summary.SuppressedByReason,
		"tier", summary.Tier,
	}
	if summary.FixturesFailed > 0 {
		logger.WarnContext(ctx, "prediction engine python summary", attrs...)
		return
	}
	logger.InfoContext(ctx, "prediction engine python summary", attrs...)
}

func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	overrideKeys := map[string]struct{}{}
	for key := range overrides {
		overrideKeys[key] = struct{}{}
	}
	merged := make([]string, 0, len(base)+len(overrides))
	for _, pair := range base {
		key, _, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		if _, exists := overrideKeys[key]; exists {
			continue
		}
		merged = append(merged, pair)
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		merged = append(merged, key+"="+overrides[key])
	}
	return merged
}

func copyEnv(env map[string]string) map[string]string {
	copied := make(map[string]string, len(env))
	for key, val := range env {
		copied[key] = val
	}
	return copied
}

func environMap() map[string]string {
	env := map[string]string{}
	for _, pair := range os.Environ() {
		key, val, ok := strings.Cut(pair, "=")
		if ok {
			env[key] = val
		}
	}
	return env
}

func parseExtraEnv(raw string) map[string]string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	extra := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &extra); err != nil {
		return nil
	}
	return extra
}

func parseDuration(raw string, fallback time.Duration) time.Duration {
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func value(env map[string]string, key, fallback string) string {
	if val := strings.TrimSpace(env[key]); val != "" {
		return val
	}
	return fallback
}

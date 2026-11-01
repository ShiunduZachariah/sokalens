package commands

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/operations"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/releases"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"
)

// ---------------------------------------------------------------------------
// Runbooks (spec §14).
//
// Every mutating command is a new path by which a compromised Telegram account
// touches production, so the surface is kept as narrow as it can usefully be:
//
//   - The allowlist below is hardcoded in Go, not configuration. A runbook that
//     can be added by editing a file can be added by anything that can write
//     that file.
//   - /run never builds a command string. It writes a typed operations.Request
//     onto the durable queue and a worker executes it.
//   - An operation the deployment cannot actually perform is refused up front,
//     so no button reports success without doing anything.
//   - Destructive work — database drop or restore, credential rotation, bulk
//     deletes, rollback — is absent by construction and cannot be reached
//     regardless of confirmation (§18).
// ---------------------------------------------------------------------------

// Runbook is one allowlisted operation.
type Runbook struct {
	// Kind is the typed operation this runbook maps to.
	Kind operations.Kind
	// Arg names the single argument, empty when the runbook takes none.
	// Multi-argument runbooks are deliberately not supported: every extra
	// argument is another thing to mistype from a phone at 3am.
	Arg string
	// Confirm requires the two-step token flow before anything is dispatched.
	Confirm bool
	// Summary is shown by /run with no arguments.
	Summary string
}

// runbooks is the complete, hardcoded allowlist. Nothing outside this map is
// reachable, and no partial matching is performed.
var runbooks = map[string]Runbook{
	"retry-job": {
		Kind:    operations.RetryJob,
		Arg:     "job_id",
		Confirm: false,
		Summary: "Requeue a failed or dead-lettered job.",
	},
	"check-backup": {
		Kind:    operations.CheckBackup,
		Confirm: false,
		Summary: "Report the last verified backup.",
	},
	"restart-service": {
		Kind:    operations.RestartService,
		Arg:     "service",
		Confirm: true,
		Summary: "Restart an allowlisted container.",
	},
}

// runbookConfirmPrefix namespaces runbook tokens so one can never redeem
// /release, and vice versa.
const runbookConfirmPrefix = "runbook:"

// RunDeps are the runbook command's collaborators.
type RunDeps struct {
	// Jobs enqueues the durable job a runbook becomes.
	Jobs *jobs.Repository
	// Executor reports which operations this deployment can actually perform.
	Executor *operations.SafeExecutor
	// Confirmations is the shared token store, also used by /release.
	Confirmations *releases.Confirmations
	// Queue is the durable queue the worker drains.
	Queue string

	Now          func() time.Time
	QueryTimeout time.Duration
}

func (d RunDeps) ready() bool {
	return d.Jobs != nil && d.Executor != nil && d.Confirmations != nil
}

func (d RunDeps) queue() string {
	if d.Queue == "" {
		return "runbooks"
	}
	return d.Queue
}

func (d RunDeps) timeout() time.Duration {
	if d.QueryTimeout > 0 {
		return d.QueryTimeout
	}
	return 10 * time.Second
}

// RegisterRun adds /run and /confirm.
//
// Callers gate this on OPS_TELEGRAM_RUNBOOKS_ENABLED: read commands must be
// able to ship without runbook authority.
func RegisterRun(registry *telegram.Registry, deps RunDeps) error {
	if registry == nil {
		return errors.New("commands: registry is required")
	}
	if err := registry.Register(telegram.Registration{
		Name:     "run",
		Usage:    "<runbook> [arg]",
		Summary:  "Run an allowlisted operation. Some require confirmation.",
		Mutating: true,
		Handler:  telegram.HandlerFunc(deps.run),
	}); err != nil {
		return fmt.Errorf("register /run: %w", err)
	}
	return registry.Register(telegram.Registration{
		Name:     "confirm",
		Usage:    "<token>",
		Summary:  "Confirm a pending runbook.",
		Mutating: true,
		Handler:  telegram.HandlerFunc(deps.confirm),
	})
}

// run validates a runbook request and either dispatches it or mints a token.
func (d RunDeps) run(ctx context.Context, req telegram.Request) (telegram.Response, error) {
	if !d.ready() {
		return telegram.Response{Body: "Runbooks are " + unavailable + "."}, nil
	}
	if len(req.Args) == 0 {
		return telegram.Response{Body: d.usage()}, nil
	}

	name := strings.ToLower(strings.TrimSpace(req.Args[0]))
	book, ok := runbooks[name]
	if !ok {
		// No partial matching, no suggestions: a near-miss on a production
		// operation must fail, not guess.
		return telegram.Response{Body: "Unknown runbook. " + d.usage()}, nil
	}

	// An operation this deployment cannot perform is refused before anything
	// is written. This is what stops /run reporting success for work that
	// nothing can execute.
	if !d.Executor.Supports(book.Kind) {
		return telegram.Response{Body: fmt.Sprintf(
			"%s is not available in this deployment.\nSupported: %s",
			telegram.Code("/run "+name), telegram.Esc(d.supportedNames()))}, nil
	}

	arg, resp, ok := d.argument(book, req.Args)
	if !ok {
		return resp, nil
	}

	scoped, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()

	if !book.Confirm {
		return d.dispatch(scoped, name, book, arg, req)
	}

	confirmation, err := d.Confirmations.Issue(scoped, runbookConfirmPrefix+name,
		map[string]any{"target": arg}, req.UserID, req.ChatID)
	switch {
	case errors.Is(err, releases.ErrTooManyOutstanding):
		return telegram.Response{Body: "Too many outstanding confirmations. Let them expire before asking again."}, nil
	case err != nil:
		return telegram.Response{Body: "Could not start confirmation: " + telegram.Esc(err.Error())}, nil
	}

	var b strings.Builder
	b.WriteString("⚠️ " + telegram.Bold("Confirm "+telegram.Esc(name)) + "\n\n")
	rows := [][]string{{"operation", string(book.Kind)}}
	if arg != "" {
		rows = append(rows, []string{book.Arg, arg})
	}
	b.WriteString(telegram.PreTable(rows))
	b.WriteString("\nReply " + telegram.Code("/confirm "+confirmation.Token) + " within " +
		telegram.Esc(shortDuration(d.Confirmations.TTL())) + ".\n")
	b.WriteString("Single use, and only you can confirm it.")
	return telegram.Response{Body: b.String()}, nil
}

// confirm redeems a runbook token and dispatches the operation.
func (d RunDeps) confirm(ctx context.Context, req telegram.Request) (telegram.Response, error) {
	if !d.ready() {
		return telegram.Response{Body: "Runbooks are " + unavailable + "."}, nil
	}
	if len(req.Args) == 0 {
		return telegram.Response{Body: "Usage: " + telegram.Code("/confirm &lt;token&gt;")}, nil
	}
	token := req.Args[0]

	scoped, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()

	runbook, err := d.Confirmations.PeekRunbook(scoped, token)
	if err != nil {
		return telegram.Response{Body: tokenRejection()}, nil
	}
	// A /release token must not be redeemable here, and vice versa.
	if !strings.HasPrefix(runbook, runbookConfirmPrefix) {
		return telegram.Response{Body: tokenRejection()}, nil
	}
	name := strings.TrimPrefix(runbook, runbookConfirmPrefix)
	book, ok := runbooks[name]
	if !ok {
		return telegram.Response{Body: tokenRejection()}, nil
	}

	// Consume before dispatching. A token is spent whether or not the dispatch
	// then succeeds, which is the correct direction to fail in: a redelivered
	// Telegram update cannot run the operation twice.
	confirmation, err := d.Confirmations.Consume(scoped, token, req.UserID, runbook)
	if err != nil {
		return telegram.Response{Body: tokenRejection()}, nil
	}

	arg, _ := confirmation.Args["target"].(string)
	return d.dispatch(scoped, name, book, arg, req)
}

// dispatch writes the typed job durably. Postgres before Telegram (§7.2): the
// job exists before the reply claims it does.
func (d RunDeps) dispatch(ctx context.Context, name string, book Runbook, arg string, req telegram.Request) (telegram.Response, error) {
	payload, err := operations.JobPayload{
		Kind:        book.Kind,
		Target:      runbookTarget(book, arg),
		Scope:       "production",
		RequestedBy: req.UserID,
	}.Marshal()
	if err != nil {
		return telegram.Response{Body: "Could not build the operation payload."}, nil
	}

	job, err := d.Jobs.Enqueue(ctx, jobs.EnqueueRequest{
		Queue: d.queue(),
		Type:  "runbook:" + name,
		// The update id makes a redelivered Telegram update collide on the
		// unique idempotency key rather than enqueue a second operation
		// (§7.4, defence 3).
		IdempotencyKey: fmt.Sprintf("telegram:update:%d", req.UpdateID),
		Priority:       900,
		Payload:        payload,
		MaxAttempts:    3,
	})
	if errors.Is(err, jobs.ErrIdempotencyConflict) {
		return telegram.Response{Body: "That request was already dispatched."}, nil
	}
	if err != nil {
		return telegram.Response{Body: "Could not dispatch: " + telegram.Esc(err.Error())}, nil
	}

	var b strings.Builder
	b.WriteString("✅ " + telegram.Bold(telegram.Esc(name)+" dispatched") + "\n\n")
	rows := [][]string{{"job", shortID(job.ID)}, {"operation", string(book.Kind)}}
	if arg != "" {
		rows = append(rows, []string{book.Arg, arg})
	}
	b.WriteString(telegram.PreTable(rows))
	b.WriteString("\nWatch " + telegram.Code("/queues") + " for completion.")
	return telegram.Response{Body: b.String()}, nil
}

// argument validates the single runbook argument.
//
// Validation is strict and per-runbook. A job id must be numeric or a UUID; a
// service name is checked against the executor's allowlist when it dispatches.
func (d RunDeps) argument(book Runbook, args []string) (string, telegram.Response, bool) {
	if book.Arg == "" {
		if len(args) > 1 {
			return "", telegram.Response{Body: "That runbook takes no arguments."}, false
		}
		return "", telegram.Response{}, true
	}
	if len(args) < 2 {
		return "", telegram.Response{Body: "Usage: " + telegram.Code("/run "+argUsage(book))}, false
	}
	value := strings.TrimSpace(args[1])
	if value == "" {
		return "", telegram.Response{Body: "Usage: " + telegram.Code("/run "+argUsage(book))}, false
	}
	if len(value) > 128 {
		return "", telegram.Response{Body: "That argument is too long."}, false
	}
	if book.Arg == "job_id" && !validJobID(value) {
		return "", telegram.Response{Body: "A job id is a number or a UUID."}, false
	}
	return value, telegram.Response{}, true
}

// runbookTarget supplies a placeholder for argument-less runbooks, because
// operations.Request.Validate requires a non-empty target.
func runbookTarget(book Runbook, arg string) string {
	if book.Arg == "" {
		return "self"
	}
	return arg
}

// validJobID accepts a decimal id or a UUID and nothing else.
func validJobID(v string) bool {
	if _, err := strconv.ParseInt(v, 10, 64); err == nil {
		return true
	}
	if len(v) != 36 {
		return false
	}
	for i, r := range v {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
	}
	return true
}

func argUsage(book Runbook) string {
	for name, b := range runbooks {
		if b.Kind == book.Kind {
			if book.Arg == "" {
				return name
			}
			return name + " <" + book.Arg + ">"
		}
	}
	return "<runbook>"
}

// usage renders only the runbooks this deployment can actually perform.
func (d RunDeps) usage() string {
	names := make([]string, 0, len(runbooks))
	for name := range runbooks {
		names = append(names, name)
	}
	sort.Strings(names)

	rows := make([][]string, 0, len(names))
	for _, name := range names {
		book := runbooks[name]
		if !d.Executor.Supports(book.Kind) {
			continue
		}
		label := name
		if book.Arg != "" {
			label += " <" + book.Arg + ">"
		}
		note := book.Summary
		if book.Confirm {
			note += " (confirm)"
		}
		rows = append(rows, []string{label, note})
	}
	if len(rows) == 0 {
		return "No runbooks are available in this deployment."
	}
	return telegram.Bold("Runbooks") + "\n\n" + telegram.PreTable(rows)
}

func (d RunDeps) supportedNames() string {
	var out []string
	for name, book := range runbooks {
		if d.Executor.Supports(book.Kind) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}

// tokenRejection is deliberately uniform. Distinguishing expired from unknown
// from foreign only helps someone guessing.
func tokenRejection() string { return "Token expired or invalid." }

// shortID trims a UUID for display.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

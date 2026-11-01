package commands

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/jobs"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/operations"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/releases"
	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/telegram"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// lazyDB returns a *sql.DB that never connects. sql.Open is lazy, which is
// enough for every path that rejects a request before touching the database —
// and those are all the security-relevant ones.
func lazyDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", "postgres://unused:unused@127.0.0.1:1/unused")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// testRunDeps builds a RunDeps whose executor has no Docker client, matching
// the default deployment.
func testRunDeps(t *testing.T) RunDeps {
	t.Helper()
	db := lazyDB(t)
	executor, err := operations.NewSafeExecutor(db, nil, []string{"worker"})
	if err != nil {
		t.Fatalf("NewSafeExecutor: %v", err)
	}
	repo, err := jobs.NewRepository(jobs.SQLDB{DB: db}, nil, jobs.Config{
		MinLease: time.Second, MaxLease: time.Minute,
		InitialBackoff: time.Second, MaxBackoff: time.Minute,
		MaxPayloadBytes: 4096, Now: time.Now,
	})
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	confirmations, err := releases.NewConfirmations(releases.SQLDB{DB: db}, releases.DefaultConfirmationTTL, time.Now)
	if err != nil {
		t.Fatalf("NewConfirmations: %v", err)
	}
	return RunDeps{Jobs: repo, Executor: executor, Confirmations: confirmations, Queue: "runbooks"}
}

// TestRollbackIsNotARunbook is the load-bearing assertion of this file.
// Rollback is the operation where a wrong call from a phone is hardest to
// undo, and it runs from the host deploy path. It must not exist in the
// allowlist at all — not disabled, absent.
func TestRollbackIsNotARunbook(t *testing.T) {
	for _, name := range []string{"rollback", "roll-back", "revert", "deploy"} {
		if _, ok := runbooks[name]; ok {
			t.Fatalf("%q is present in the runbook allowlist", name)
		}
	}
	for name, book := range runbooks {
		if book.Kind == operations.Rollback {
			t.Fatalf("runbook %q maps to the rollback operation", name)
		}
	}
}

// TestUnknownRunbookIsRejectedWithoutPartialMatching: a near-miss on a
// production operation must fail, not guess.
func TestUnknownRunbookIsRejectedWithoutPartialMatching(t *testing.T) {
	deps := testRunDeps(t)
	for _, name := range []string{"retry", "retry-jo", "restart", "RESTART-SERVIC", "check", "nonsense"} {
		t.Run(name, func(t *testing.T) {
			resp, err := deps.run(context.Background(), telegram.Request{Args: []string{name}})
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if !strings.Contains(resp.Body, "Unknown runbook") {
				t.Fatalf("%q was not rejected as unknown:\n%s", name, resp.Body)
			}
		})
	}
}

// TestUnsupportedOperationIsRefusedBeforeDispatch: with Docker disabled,
// restart-service must be refused up front. Enqueuing it would report success
// for work nothing can execute — the exact failure this design avoids.
func TestUnsupportedOperationIsRefusedBeforeDispatch(t *testing.T) {
	deps := testRunDeps(t) // no Docker client
	resp, err := deps.run(context.Background(), telegram.Request{Args: []string{"restart-service", "worker"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(resp.Body, "not available in this deployment") {
		t.Fatalf("restart-service was not refused:\n%s", resp.Body)
	}
	if strings.Contains(resp.Body, "dispatched") {
		t.Fatalf("an unsupported operation reported dispatch:\n%s", resp.Body)
	}
}

// TestUsageOnlyOffersSupportedRunbooks: the menu must not advertise a button
// that cannot work.
func TestUsageOnlyOffersSupportedRunbooks(t *testing.T) {
	deps := testRunDeps(t)
	resp, err := deps.run(context.Background(), telegram.Request{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(resp.Body, "retry-job") || !strings.Contains(resp.Body, "check-backup") {
		t.Fatalf("usage omitted a supported runbook:\n%s", resp.Body)
	}
	if strings.Contains(resp.Body, "restart-service") {
		t.Fatalf("usage advertised restart-service with Docker disabled:\n%s", resp.Body)
	}
}

// TestJobIDValidation keeps a malformed argument from reaching the executor.
func TestJobIDValidation(t *testing.T) {
	valid := []string{"42", "0", "550e8400-e29b-41d4-a716-446655440000"}
	invalid := []string{"; DROP TABLE ops_jobs", "abc", "550e8400e29b41d4a716446655440000", "42; rm -rf /", ""}
	for _, v := range valid {
		if !validJobID(v) {
			t.Fatalf("validJobID(%q) = false, want true", v)
		}
	}
	for _, v := range invalid {
		if validJobID(v) {
			t.Fatalf("validJobID(%q) = true, want false", v)
		}
	}
}

func TestRetryJobRejectsBadArgument(t *testing.T) {
	deps := testRunDeps(t)
	cases := map[string]struct {
		args []string
		want string
	}{
		"missing":     {[]string{"retry-job"}, "Usage:"},
		"not numeric": {[]string{"retry-job", "abc"}, "number or a UUID"},
		"injection":   {[]string{"retry-job", "1;DROP"}, "number or a UUID"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resp, err := deps.run(context.Background(), telegram.Request{Args: tc.args})
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if !strings.Contains(resp.Body, tc.want) {
				t.Fatalf("got %q, want it to contain %q", resp.Body, tc.want)
			}
		})
	}
}

// TestArgumentlessRunbookRejectsExtraArguments stops a stray token being read
// as a target.
func TestArgumentlessRunbookRejectsExtraArguments(t *testing.T) {
	deps := testRunDeps(t)
	resp, err := deps.run(context.Background(), telegram.Request{Args: []string{"check-backup", "production"}})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(resp.Body, "takes no arguments") {
		t.Fatalf("extra argument was accepted:\n%s", resp.Body)
	}
}

// TestConfirmRequiresAToken covers the bare command.
func TestConfirmRequiresAToken(t *testing.T) {
	deps := testRunDeps(t)
	resp, err := deps.confirm(context.Background(), telegram.Request{})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !strings.Contains(resp.Body, "Usage:") {
		t.Fatalf("got %q, want usage", resp.Body)
	}
}

// TestTokenRejectionIsUniform: distinguishing expired from unknown from
// foreign only helps someone guessing.
func TestTokenRejectionIsUniform(t *testing.T) {
	msg := tokenRejection()
	for _, leak := range []string{"expired", "unknown", "another user", "consumed"} {
		if strings.Contains(strings.ToLower(msg), leak) && msg != "Token expired or invalid." {
			t.Fatalf("rejection message leaks which check failed: %q", msg)
		}
	}
	if msg != "Token expired or invalid." {
		t.Fatalf("unexpected rejection message: %q", msg)
	}
}

// TestRunbooksDegradeWhenNotWired: with no collaborators the commands must say
// so rather than panic.
func TestRunbooksDegradeWhenNotWired(t *testing.T) {
	var deps RunDeps
	for name, handler := range map[string]func(context.Context, telegram.Request) (telegram.Response, error){
		"run":     deps.run,
		"confirm": deps.confirm,
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := handler(context.Background(), telegram.Request{Args: []string{"retry-job", "1"}})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if !strings.Contains(resp.Body, unavailable) {
				t.Fatalf("expected an unavailable reply, got %q", resp.Body)
			}
		})
	}
}

// TestRunCommandsAreMarkedMutating: a mutating command must be flagged so the
// durable-write ordering in spec §7.4 applies to it.
func TestRunCommandsAreMarkedMutating(t *testing.T) {
	registry := telegram.NewRegistry()
	if err := RegisterRun(registry, testRunDeps(t)); err != nil {
		t.Fatalf("RegisterRun: %v", err)
	}
	for _, name := range []string{"run", "confirm"} {
		reg, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("/%s was not registered", name)
		}
		if !reg.Mutating {
			t.Fatalf("/%s is not marked mutating", name)
		}
	}
}

// TestEveryRunbookMapsToASupportedKindShape guards the allowlist itself: a
// runbook pointing at a kind the typed boundary does not know would fail only
// at dispatch time.
func TestEveryRunbookMapsToAKnownKind(t *testing.T) {
	known := map[operations.Kind]bool{}
	for _, k := range operations.KnownKinds() {
		known[k] = true
	}
	for name, book := range runbooks {
		if !known[book.Kind] {
			t.Fatalf("runbook %q maps to unknown kind %q", name, book.Kind)
		}
		if book.Kind == operations.RestartService && !book.Confirm {
			t.Fatalf("runbook %q restarts a container without confirmation", name)
		}
	}
}

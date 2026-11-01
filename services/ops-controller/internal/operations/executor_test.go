package operations

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// lazyDB returns a *sql.DB that has never connected. sql.Open is lazy, so this
// is enough to construct an executor and exercise every path that rejects a
// request before touching the database — which is all of the security-relevant
// ones.
func lazyDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", "postgres://unused:unused@127.0.0.1:1/unused")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func validRequest(kind Kind, target string) Request {
	return Request{Kind: kind, Target: target, Scope: "production", IdempotencyKey: "k1", Timeout: time.Minute}
}

// TestRollbackIsNotReachableFromThisSurface is the point of the whole design.
// Rollback retags the last-known-good image from the host deploy path, and it
// is the operation where a wrong call at 3am is hardest to undo. It must not
// be executable from a chat account under any configuration.
func TestRollbackIsNotReachableFromThisSurface(t *testing.T) {
	for _, docker := range []*DockerClient{nil, {}} {
		e, err := NewSafeExecutor(lazyDB(t), docker, []string{"anything"})
		if err != nil {
			t.Fatalf("NewSafeExecutor: %v", err)
		}
		if e.Supports(Rollback) {
			t.Fatal("rollback reported as supported")
		}
		_, err = e.Execute(context.Background(), validRequest(Rollback, "af72c11"))
		if !errors.Is(err, ErrNotSupported) {
			t.Fatalf("rollback error = %v, want ErrNotSupported", err)
		}
	}
}

// TestContainerControlRequiresExplicitOptIn: the Docker socket is
// root-equivalent on the host. With it disabled, restart must be neither
// offered nor attempted.
func TestContainerControlRequiresExplicitOptIn(t *testing.T) {
	e, err := NewSafeExecutor(lazyDB(t), nil, []string{"worker"})
	if err != nil {
		t.Fatalf("NewSafeExecutor: %v", err)
	}
	if e.Supports(RestartService) {
		t.Fatal("restart reported as supported with no docker client")
	}
	_, err = e.Execute(context.Background(), validRequest(RestartService, "worker"))
	if !errors.Is(err, ErrNotSupported) {
		t.Fatalf("restart error = %v, want ErrNotSupported", err)
	}
}

// TestRestartRejectsTargetsOutsideAllowlist: a free-form container name would
// let a compromised chat account reach anything the Docker daemon can see, so
// the allowlist is checked before the socket is ever touched.
func TestRestartRejectsTargetsOutsideAllowlist(t *testing.T) {
	// A non-nil client that would fail if dialled; the allowlist check must
	// short-circuit before that happens.
	e, err := NewSafeExecutor(lazyDB(t), &DockerClient{}, []string{"sokalens-worker-1"})
	if err != nil {
		t.Fatalf("NewSafeExecutor: %v", err)
	}
	result, err := e.Execute(context.Background(), validRequest(RestartService, "postgres"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Accepted {
		t.Fatal("a target outside the allowlist was accepted")
	}
}

// TestFlushCacheIsNotSupported — there is no Redis client in this service and
// adding one for a single runbook is not worth the dependency.
func TestFlushCacheIsNotSupported(t *testing.T) {
	e, _ := NewSafeExecutor(lazyDB(t), nil, nil)
	if _, err := e.Execute(context.Background(), validRequest(FlushSafeCache, "predictions")); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("flush error = %v, want ErrNotSupported", err)
	}
}

// TestSupportedKindsMatchesSupports keeps the advertised list and the gate from
// drifting apart. /run offers exactly what SupportedKinds reports, so a
// mismatch would either hide a working operation or offer a broken one.
func TestSupportedKindsMatchesSupports(t *testing.T) {
	for _, docker := range []*DockerClient{nil, {}} {
		e, _ := NewSafeExecutor(lazyDB(t), docker, []string{"worker"})
		advertised := map[Kind]bool{}
		for _, k := range e.SupportedKinds() {
			advertised[k] = true
		}
		for _, k := range KnownKinds() {
			if advertised[k] != e.Supports(k) {
				t.Fatalf("kind %q: advertised=%v Supports=%v", k, advertised[k], e.Supports(k))
			}
		}
	}
}

// TestExecuteValidatesBeforeActing: an unbounded timeout or a missing target
// must be rejected at the boundary, not inside an operation.
func TestExecuteValidatesBeforeActing(t *testing.T) {
	e, _ := NewSafeExecutor(lazyDB(t), nil, nil)
	bad := map[string]Request{
		"unknown kind":     {Kind: "shell", Target: "x", Scope: "production", IdempotencyKey: "k", Timeout: time.Minute},
		"missing target":   {Kind: RetryJob, Scope: "production", IdempotencyKey: "k", Timeout: time.Minute},
		"missing scope":    {Kind: RetryJob, Target: "x", IdempotencyKey: "k", Timeout: time.Minute},
		"no idempotency":   {Kind: RetryJob, Target: "x", Scope: "production", Timeout: time.Minute},
		"timeout too long": {Kind: RetryJob, Target: "x", Scope: "production", IdempotencyKey: "k", Timeout: time.Hour},
	}
	for name, req := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := e.Execute(context.Background(), req); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestNewSafeExecutorRequiresDatabase(t *testing.T) {
	if _, err := NewSafeExecutor(nil, nil, nil); err == nil {
		t.Fatal("expected an error for a nil database")
	}
}

// TestJobPayloadRoundTrip: the durable payload is typed end to end, so the
// worker never parses a command string.
func TestJobPayloadRoundTrip(t *testing.T) {
	in := JobPayload{Kind: RetryJob, Target: "job-1", Scope: "production", RequestedBy: 42}
	raw, err := in.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	out, err := ParseJobPayload(raw)
	if err != nil {
		t.Fatalf("ParseJobPayload: %v", err)
	}
	if out != in {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
}

// TestParseJobPayloadRejectsUnknownKind stops a hand-edited or corrupted queue
// row from reaching the executor as an unrecognised operation.
func TestParseJobPayloadRejectsUnknownKind(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"shell","target":"rm -rf /"}`,
		`{"kind":"","target":"x"}`,
		`not json`,
	} {
		if _, err := ParseJobPayload([]byte(raw)); err == nil {
			t.Fatalf("payload %q was accepted", raw)
		}
	}
}

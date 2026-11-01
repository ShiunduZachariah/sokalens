package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// ErrNotSupported is returned for an operation this deployment cannot perform.
// It is distinct from a validation failure: the request was well formed, the
// capability is simply absent. /run uses this to avoid offering a button that
// cannot do anything.
var ErrNotSupported = errors.New("operation not supported by this deployment")

// SafeExecutor performs the typed operations the controller can actually carry
// out. It never builds a shell string and never accepts one.
//
// Two capabilities, deliberately separate:
//
//   - Postgres-backed operations always work, because the controller already
//     owns that connection.
//   - Container operations need the Docker socket, which is opt-in. Mounting
//     that socket grants root-equivalent access to the host, so it is off
//     unless explicitly enabled, and the operations that need it are not
//     offered when it is off.
type SafeExecutor struct {
	db     *sql.DB
	docker *DockerClient
	// allowedServices bounds restart targets to a known list. A free-form
	// container name would let a compromised chat account reach anything on
	// the host's Docker daemon.
	allowedServices map[string]struct{}
}

// NewSafeExecutor builds an executor. docker may be nil, which disables
// container operations. allowedServices bounds restart targets.
func NewSafeExecutor(db *sql.DB, docker *DockerClient, allowedServices []string) (*SafeExecutor, error) {
	if db == nil {
		return nil, errors.New("operations: executor requires a database")
	}
	allowed := make(map[string]struct{}, len(allowedServices))
	for _, s := range allowedServices {
		if s = strings.TrimSpace(s); s != "" {
			allowed[s] = struct{}{}
		}
	}
	return &SafeExecutor{db: db, docker: docker, allowedServices: allowed}, nil
}

// Supports reports whether this deployment can perform a kind. /run consults
// it so an unavailable operation is rejected up front rather than enqueued.
func (e *SafeExecutor) Supports(kind Kind) bool {
	switch kind {
	case RetryJob, CheckBackup:
		return true
	case RestartService:
		return e.docker != nil
	default:
		// Rollback and cache flush are deliberately absent. See Execute.
		return false
	}
}

// SupportedKinds lists what this deployment can actually do.
func (e *SafeExecutor) SupportedKinds() []Kind {
	var out []Kind
	for _, k := range KnownKinds() {
		if e.Supports(k) {
			out = append(out, k)
		}
	}
	return out
}

// Execute performs one typed operation.
func (e *SafeExecutor) Execute(ctx context.Context, request Request) (Result, error) {
	if err := request.Validate(); err != nil {
		return Result{}, err
	}
	switch request.Kind {
	case RetryJob:
		return e.retryJob(ctx, request)
	case CheckBackup:
		return e.checkBackup(ctx, request)
	case RestartService:
		return e.restartService(ctx, request)
	case Rollback:
		// Rolling back means retagging the last-known-good image and
		// recreating containers, which lives in infra/deploy/ci-deploy.sh on
		// the host and is outside this container's reach. It is also the one
		// operation where a wrong call at 3am is hardest to undo, so it stays
		// a laptop-and-shell operation (spec §18).
		return Result{Reason: "rollback runs from the host deploy path, not the chat bridge"}, ErrNotSupported
	case FlushSafeCache:
		// ops-controller has no Redis client and adding one for this alone is
		// not worth the dependency.
		return Result{Reason: "cache flush requires a Redis client this service does not have"}, ErrNotSupported
	default:
		return Result{}, fmt.Errorf("unsupported operation kind %q", request.Kind)
	}
}

// retryJob returns a dead-lettered or failed job to the queue.
func (e *SafeExecutor) retryJob(ctx context.Context, request Request) (Result, error) {
	const query = `UPDATE ops_jobs
SET status = 'pending',
    available_at = now(),
    lease_owner = NULL,
    lease_token = NULL,
    lease_expires_at = NULL,
    completed_at = NULL,
    dead_lettered_at = NULL,
    attempts = 0,
    updated_at = now()
WHERE id = $1::uuid
  AND status IN ('failed', 'dead_letter', 'cancelled')`

	result, err := e.db.ExecContext(ctx, query, request.Target)
	if err != nil {
		return Result{}, fmt.Errorf("retry job: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Result{}, fmt.Errorf("retry job rows: %w", err)
	}
	if affected == 0 {
		// Either the id does not exist or the job is not in a retryable
		// state. Both mean "nothing happened", which the caller must be told.
		return Result{Accepted: false, Reason: "job not found or not in a retryable state"}, nil
	}
	return Result{Accepted: true, Reason: "job requeued"}, nil
}

// checkBackup reports the most recent verified backup. Read-only.
func (e *SafeExecutor) checkBackup(ctx context.Context, _ Request) (Result, error) {
	const query = `SELECT value::text FROM app_settings WHERE key = 'backup.last_verified' LIMIT 1`
	var raw string
	switch err := e.db.QueryRowContext(ctx, query).Scan(&raw); {
	case errors.Is(err, sql.ErrNoRows):
		return Result{Accepted: true, Reason: "no verified backup recorded"}, nil
	case err != nil:
		return Result{}, fmt.Errorf("check backup: %w", err)
	}
	return Result{Accepted: true, Reason: "last verified backup: " + raw}, nil
}

// restartService restarts one allowlisted container.
func (e *SafeExecutor) restartService(ctx context.Context, request Request) (Result, error) {
	if e.docker == nil {
		return Result{Reason: "container control is disabled"}, ErrNotSupported
	}
	if _, ok := e.allowedServices[request.Target]; !ok {
		return Result{Accepted: false, Reason: "service is not in the restart allowlist"}, nil
	}
	if err := e.docker.Restart(ctx, request.Target); err != nil {
		return Result{}, fmt.Errorf("restart %s: %w", request.Target, err)
	}
	return Result{Accepted: true, Reason: "restart requested for " + request.Target}, nil
}

// ---------------------------------------------------------------------------
// Docker
// ---------------------------------------------------------------------------

// DockerClient speaks just enough of the Docker Engine API to restart a
// container. It is a handful of lines rather than a dependency.
//
// SECURITY: the Docker socket is root-equivalent on the host. Enabling this
// widens the blast radius of a compromised bot token from "read ops data" to
// "control every container on the box". It is off by default and the socket is
// not mounted by default.
type DockerClient struct {
	http *http.Client
	// apiVersion pins the Engine API so a daemon upgrade cannot change
	// behaviour underneath us.
	apiVersion string
}

// NewDockerClient dials the Docker socket. socketPath is typically
// /var/run/docker.sock.
func NewDockerClient(socketPath string) (*DockerClient, error) {
	if strings.TrimSpace(socketPath) == "" {
		return nil, errors.New("operations: docker socket path is required")
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
	return &DockerClient{
		http:       &http.Client{Transport: transport, Timeout: 60 * time.Second},
		apiVersion: "v1.44",
	}, nil
}

// Ping verifies the socket is reachable, so a misconfiguration surfaces at
// boot rather than at 3am on the first restart attempt.
func (c *DockerClient) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/_ping", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker ping returned %d", resp.StatusCode)
	}
	return nil
}

// Restart restarts a container by name.
func (c *DockerClient) Restart(ctx context.Context, name string) error {
	url := fmt.Sprintf("http://docker/%s/containers/%s/restart", c.apiVersion, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("container %q not found", name)
	default:
		return fmt.Errorf("docker restart returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// JobPayload is the durable payload a runbook job carries. Keeping it typed
// means the worker never parses a command string.
type JobPayload struct {
	Kind   Kind   `json:"kind"`
	Target string `json:"target"`
	Scope  string `json:"scope"`
	// RequestedBy is the Telegram user id, for the audit trail.
	RequestedBy int64 `json:"requested_by"`
}

// Marshal renders the payload for the durable queue.
func (p JobPayload) Marshal() (json.RawMessage, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// ParseJobPayload reads a durable payload back.
func ParseJobPayload(raw json.RawMessage) (JobPayload, error) {
	var p JobPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return JobPayload{}, err
	}
	if !p.Kind.Valid() {
		return JobPayload{}, fmt.Errorf("unsupported operation kind %q", p.Kind)
	}
	return p, nil
}

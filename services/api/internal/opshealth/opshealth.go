// Package opshealth produces the terse status document served at /healthz.
//
// It exists to close one specific gap, named in the in-play architecture
// (§16.4): **when the VPS is down, the thing that alerts you is down too.**
// ops-controller runs on the same host it watches, so a host failure takes out
// the alerting with the service. The eventual answer is an external watchdog
// pinging a health endpoint on a cron and escalating after N consecutive
// failures — and that watchdog is only a configuration change away if a stable,
// unauthenticated endpoint already exists to ping. This is that endpoint's
// content.
//
// Three constraints shape everything here.
//
// **It is unauthenticated.** So the document carries ages and booleans, never
// counts, versions, hostnames, connection strings or error text. "The database
// is unreachable" is a fact an attacker learns from the service being down
// anyway; "connection refused to 10.0.0.4:5432" is not.
//
// **It must stay cheap.** An unauthenticated endpoint is a free lever on the
// database for anyone who finds it, so results are cached and every query is
// both time-bounded and deadline-bounded. A health check that can be turned
// into a load generator is a liability, not a safeguard.
//
// **It must answer even when its dependencies do not.** A liveness endpoint
// that hangs when Postgres hangs reports nothing at the exact moment the
// information matters most. Every probe has its own short deadline and a
// failure is recorded as a fact in the document rather than propagated as an
// error.
package opshealth

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// Age reports how long ago something last happened.
//
// Both fields are present or both absent. A missing value means "never
// observed", which is genuinely different from "observed a long time ago": on
// a fresh deployment there has been no live poll yet, and reporting that as an
// infinite age would look like an outage on the first boot.
type Age struct {
	At      *time.Time `json:"at,omitempty"`
	Seconds *int64     `json:"age_seconds,omitempty"`
}

// DependencyChecker is one reachability probe. It is satisfied by the readiness
// checkers the API already defines, so there is one definition of "can we reach
// Redis" rather than two that can disagree.
type DependencyChecker interface {
	Name() string
	Check() error
}

// Snapshot is the whole document: what is reachable, and how fresh the two
// signals that matter are.
type Snapshot struct {
	// Dependencies is reachability by name. Booleans, never error text: a
	// driver error carries hosts, ports and sometimes credentials, and this is
	// served unauthenticated.
	Dependencies map[string]bool
	// LastLivePoll is the last successful completion of the bulk live-fixtures
	// job. It is the single most useful number here: if it is minutes old
	// during a fixture window, in-play data is stale regardless of what every
	// other check says.
	LastLivePoll Age `json:"last_live_poll"`
	// LastWorkerJob is the last successful completion of any job. It stands in
	// for a worker heartbeat, because the worker's own heartbeat is a file on
	// its container's filesystem and the API cannot see it. A job completing
	// is stronger evidence anyway: it means the worker is not merely running
	// but making progress.
	LastWorkerJob Age `json:"last_worker_job"`
}

// Source produces a snapshot.
type Source interface {
	Snapshot(context.Context) Snapshot
}

// LiveFixturesJobKey is the bulk live-poll job.
//
// It is spelled out rather than imported: the worker's ingestion package is
// internal to a different module, so this module cannot reference it. Both
// sides therefore pin the same literal in a test -- the worker asserts its
// generated key equals this string, and this module asserts the constant does.
// Changing the key on either side fails the other side's test, which is the
// closest thing to a shared definition available across the module boundary.
const LiveFixturesJobKey = "provider.api-football.live"

// lookback bounds every "when did this last happen" query.
//
// Without it these are unbounded max() scans over the whole jobs table. With
// it they touch at most a day of rows, and anything older than a day is so far
// past every alerting threshold that the exact age stops carrying information.
const lookback = 24 * time.Hour

// probeTimeout is per query. Short on purpose: this endpoint's job is to
// answer, and an answer of "unreachable" three seconds late is worth less than
// one on time.
const probeTimeout = 2 * time.Second

// DatabaseDependency is the checker name that gates the freshness queries.
// It matches the name the API's readiness checker already registers.
const DatabaseDependency = "postgres"

// Repository reads the snapshot from Postgres and the dependency probes.
type Repository struct {
	db     *sql.DB
	checks []DependencyChecker
	now    func() time.Time
}

func NewRepository(db *sql.DB, checks ...DependencyChecker) *Repository {
	return &Repository{db: db, checks: checks}
}

// WithClock is for tests.
func (r *Repository) WithClock(now func() time.Time) *Repository {
	r.now = now
	return r
}

func (r *Repository) clock() time.Time {
	if r.now != nil {
		return r.now().UTC()
	}
	return time.Now().UTC()
}

// Snapshot probes the dependencies and reads the freshness ages.
//
// The probes live here rather than in the handler so that Cached covers them
// too. They are the expensive part -- a database ping and a socket dial, each
// with its own timeout -- and leaving them uncached on an unauthenticated
// endpoint would make it the free database lever the cache exists to prevent.
//
// The freshness queries are skipped when the database is already known to be
// unreachable: two more two-second timeouts would only arrive at the same
// conclusion four seconds later, on the endpoint whose whole job is to answer
// promptly.
func (r *Repository) Snapshot(ctx context.Context) Snapshot {
	snapshot := Snapshot{Dependencies: map[string]bool{}}
	databaseReachable := true
	for _, check := range r.checks {
		reachable := check.Check() == nil
		snapshot.Dependencies[check.Name()] = reachable
		if !reachable && check.Name() == DatabaseDependency {
			databaseReachable = false
		}
	}
	if r.db == nil || !databaseReachable {
		return snapshot
	}
	now := r.clock()
	since := now.Add(-lookback)
	snapshot.LastLivePoll = r.lastCompletion(ctx, now, since, LiveFixturesJobKey)
	snapshot.LastWorkerJob = r.lastCompletion(ctx, now, since, "")
	return snapshot
}

func (r *Repository) lastCompletion(ctx context.Context, now, since time.Time, jobKey string) Age {
	queryCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	// Scanned as `any` and parsed, rather than into sql.NullTime.
	//
	// Postgres hands back a real timestamp, but the same statement runs against
	// SQLite in tests, where max() over a TIMESTAMP column comes back as a
	// string that sql.NullTime refuses. Scanning tolerantly is the pattern the
	// matches package already uses for this, and the alternative -- a test
	// harness that cannot exercise the production query -- is worse than a
	// three-line parse.
	var (
		completedAt any
		err         error
	)
	if jobKey == "" {
		err = r.db.QueryRowContext(queryCtx, `
SELECT max(completed_at) FROM jobs
WHERE status = 'completed' AND completed_at IS NOT NULL AND completed_at >= $1`, since).Scan(&completedAt)
	} else {
		err = r.db.QueryRowContext(queryCtx, `
SELECT max(completed_at) FROM jobs
WHERE status = 'completed' AND completed_at IS NOT NULL AND completed_at >= $1 AND job_key = $2`, since, jobKey).Scan(&completedAt)
	}
	if err != nil || completedAt == nil {
		return Age{}
	}
	parsed, parseErr := parseTimestamp(completedAt)
	if parseErr != nil {
		return Age{}
	}
	at := parsed.UTC()
	// Clamped: a completion timestamped slightly ahead of the API's clock is
	// container clock skew, and a negative age would render as a nonsense
	// number in whatever reads this.
	seconds := max(int64(now.Sub(at).Seconds()), 0)
	return Age{At: &at, Seconds: &seconds}
}

// Cached serves a recent snapshot rather than querying on every request.
//
// /healthz is unauthenticated, so without this it is a free database lever for
// anyone who finds it. The TTL is short enough that an operator watching the
// endpoint sees a change within one poll of a cron, and long enough that a
// flood costs one query rather than one per request.
type Cached struct {
	Source Source
	TTL    time.Duration
	Now    func() time.Time

	mu       sync.Mutex
	snapshot Snapshot
	freshAt  time.Time
	hasValue bool
}

func (c *Cached) ttl() time.Duration {
	if c.TTL > 0 {
		return c.TTL
	}
	return 10 * time.Second
}

func (c *Cached) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Cached) Snapshot(ctx context.Context) Snapshot {
	if c.Source == nil {
		return Snapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.hasValue && now.Sub(c.freshAt) < c.ttl() {
		return c.snapshot
	}
	c.snapshot = c.Source.Snapshot(ctx)
	c.freshAt = now
	c.hasValue = true
	return c.snapshot
}

// parseTimestamp accepts what either driver returns.
func parseTimestamp(value any) (time.Time, error) {
	switch v := value.(type) {
	case time.Time:
		return v, nil
	case string:
		return parseTimestampString(v)
	case []byte:
		return parseTimestampString(string(v))
	default:
		return time.Time{}, fmt.Errorf("unsupported timestamp type %T", value)
	}
}

func parseTimestampString(value string) (time.Time, error) {
	// The second layout is Go's default time.Time rendering, which is what the
	// SQLite driver stores when a time.Time is bound as a parameter.
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05 -0700 MST", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp")
}

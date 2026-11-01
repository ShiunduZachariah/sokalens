package releases

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Errors.
// ---------------------------------------------------------------------------

var (
	// ErrRepositoryUnavailable mirrors the sentinel used by the other storage
	// packages in this service.
	ErrRepositoryUnavailable = errors.New("release train repository unavailable")
	// ErrNoOpenTrain: nothing has been merged into a train yet.
	ErrNoOpenTrain = errors.New("no open release train")
	// ErrEmptyTrain: the train exists but carries no PRs. /release must refuse
	// it: promoting nothing produces a deploy with no changes and a train row
	// that claims a release happened.
	ErrEmptyTrain = errors.New("release train is empty")
	// ErrTrainHeld: the train is held for its next window.
	ErrTrainHeld = errors.New("release train is held")
	// ErrAlreadyHeld: /hold on an already-held train. A hold skips the next
	// window only; letting holds stack would make an indefinite hold possible
	// one command at a time, which is exactly the state a release train is
	// supposed to make visible.
	ErrAlreadyHeld = errors.New("release train is already held")
	// ErrNotBatched: an IMMEDIATE change was offered to the train. It bypasses
	// the train by definition.
	ErrNotBatched = errors.New("immediate changes bypass the release train")
	// ErrNoWindow: every candidate window inside the search horizon is a
	// fixture-heavy blackout.
	ErrNoWindow = errors.New("no release window available")
	// ErrTrainStateConflict: a state transition raced and lost.
	ErrTrainStateConflict = errors.New("release train state conflict")
)

// ---------------------------------------------------------------------------
// State machine.
// ---------------------------------------------------------------------------

// State is ops_release_trains.status. The column's CHECK constrains it to
// exactly these five values.
//
//	                  append (BATCHED PR)
//	                        │
//	                        ▼
//	 (new) ───────────► PENDING ──────/hold──────► HELD
//	                    │    ▲                      │
//	          /release  │    └──── window passed ───┘  (hold auto-expires;
//	          or cap    │          window rolls forward)
//	                    ▼
//	                 DEPLOYING ──── CI reports success ───► DEPLOYED
//	                                                          │
//	                                                    rollback
//	                                                          ▼
//	                                                     ROLLED_BACK
//	                                                          │
//	                                        next train promotes in two halves
//
// DEPLOYING is entered by this package; DEPLOYED and ROLLED_BACK are stamped
// by the existing CI deploy path calling MarkDeployed / Rollback. Nothing here
// runs a deploy.
type State string

const (
	StatePending    State = "PENDING"
	StateDeploying  State = "DEPLOYING"
	StateDeployed   State = "DEPLOYED"
	StateRolledBack State = "ROLLED_BACK"
	StateHeld       State = "HELD"
)

// MaxTrainPRs caps a train at eight pull requests.
//
// The cap is not a storage limit, it is a debugging limit. A train is deployed
// and rolled back as a unit, so a train of N changes turns one regression into
// an N-way attribution problem. Eight is the point past which bisecting by
// reading the diff stops being realistic on a phone at 2am. When a train fills
// before its window it deploys early rather than growing: a bigger train is
// strictly worse than an earlier one.
const MaxTrainPRs = 8

// Train is one ops_release_trains row.
type Train struct {
	ID         int64
	Status     State
	PRNumbers  []int
	HeadSHA    string
	BaseSHA    string
	WindowAt   time.Time
	DeployedAt time.Time
	RolledBack time.Time
	HeldReason string
	HeldBy     int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Open reports whether the train still accumulates or awaits a window.
func (t Train) Open() bool { return t.Status == StatePending || t.Status == StateHeld }

// Full reports that the train has reached the PR cap.
func (t Train) Full() bool { return len(t.PRNumbers) >= MaxTrainPRs }

// ---------------------------------------------------------------------------
// Window policy.
// ---------------------------------------------------------------------------

var (
	eatOnce sync.Once
	eatZone *time.Location
)

// EAT returns Africa/Nairobi. When the tzdata database is unavailable (a
// scratch container with no zoneinfo), it falls back to a fixed UTC+3 zone.
// The fallback is exact, not approximate: Kenya has observed UTC+3 with no
// daylight saving since 1960, so a fixed zone and the tz database agree for
// every timestamp this service will ever schedule.
func EAT() *time.Location {
	eatOnce.Do(func() {
		if loc, err := time.LoadLocation("Africa/Nairobi"); err == nil {
			eatZone = loc
			return
		}
		eatZone = time.FixedZone("EAT", 3*60*60)
	})
	return eatZone
}

// Blackout is a period no release may be scheduled into: a fixture-heavy
// window, where live data collection and prediction refresh are under load and
// a deploy would land on the one workload the platform exists to serve.
type Blackout struct {
	Start  time.Time
	End    time.Time
	Reason string
}

// Contains reports whether t falls inside [Start, End).
func (b Blackout) Contains(t time.Time) bool {
	return !t.Before(b.Start) && t.Before(b.End)
}

// WindowPolicy is the deploy window, supplied by configuration rather than
// hardcoded. Its zero value is unusable on purpose; use DefaultWindowPolicy
// and override fields.
type WindowPolicy struct {
	// Weekday and Hour/Minute define the recurring window.
	Weekday time.Weekday
	Hour    int
	Minute  int
	// Location is the zone the window is expressed in. Wednesday 02:00 means
	// 02:00 where the operator sleeps, not 02:00 UTC.
	Location *time.Location
	// Blackouts are explicit fixture-heavy periods.
	Blackouts []Blackout
	// FixtureHeavy is an optional predicate for a schedule-derived blackout,
	// so the fixture calendar can veto a window without this package importing
	// the fixture store.
	FixtureHeavy func(time.Time) bool
	// SplitSoak is how long part one of a post-rollback split train soaks
	// before part two is due. It is the bisection interval: long enough for a
	// regression to show, short enough that part two is not a week late.
	SplitSoak time.Duration
}

// DefaultWindowPolicy is Wednesday 02:00 EAT with a one-hour split soak.
func DefaultWindowPolicy() WindowPolicy {
	return WindowPolicy{
		Weekday:   time.Wednesday,
		Hour:      2,
		Minute:    0,
		Location:  EAT(),
		SplitSoak: time.Hour,
	}
}

func (p WindowPolicy) location() *time.Location {
	if p.Location != nil {
		return p.Location
	}
	return EAT()
}

func (p WindowPolicy) splitSoak() time.Duration {
	if p.SplitSoak > 0 {
		return p.SplitSoak
	}
	return time.Hour
}

// maxWindowSearch bounds the blackout skip so a misconfigured always-true
// FixtureHeavy predicate fails loudly instead of looping.
const maxWindowSearch = 12

// Blocked reports whether a candidate window falls in a blackout.
func (p WindowPolicy) Blocked(at time.Time) (bool, string) {
	for _, b := range p.Blackouts {
		if b.Contains(at) {
			reason := b.Reason
			if reason == "" {
				reason = "blackout"
			}
			return true, reason
		}
	}
	if p.FixtureHeavy != nil && p.FixtureHeavy(at) {
		return true, "fixture-heavy window"
	}
	return false, ""
}

// NextWindow returns the first window strictly after `after` that is not a
// blackout. It is pure: no clock, no database.
func (p WindowPolicy) NextWindow(after time.Time) (time.Time, error) {
	loc := p.location()
	local := after.In(loc)
	candidate := time.Date(local.Year(), local.Month(), local.Day(), p.Hour, p.Minute, 0, 0, loc)
	delta := (int(p.Weekday) - int(candidate.Weekday()) + 7) % 7
	candidate = candidate.AddDate(0, 0, delta)
	if !candidate.After(after) {
		candidate = candidate.AddDate(0, 0, 7)
	}
	for i := 0; i < maxWindowSearch; i++ {
		if blocked, _ := p.Blocked(candidate); !blocked {
			return candidate, nil
		}
		candidate = candidate.AddDate(0, 0, 7)
	}
	return time.Time{}, ErrNoWindow
}

// ---------------------------------------------------------------------------
// Persistence seam. database/sql-shaped, exactly like internal/incidents, so
// unit tests need no driver and production wiring wraps *sql.DB.
// ---------------------------------------------------------------------------

type DB interface {
	BeginTx(context.Context, *sql.TxOptions) (Tx, error)
	QueryRowContext(context.Context, string, ...any) Row
}

type Tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) Row
	Commit() error
	Rollback() error
}

type Row interface{ Scan(...any) error }

// SQLDB adapts *sql.DB to DB.
type SQLDB struct{ DB *sql.DB }

func (d SQLDB) BeginTx(ctx context.Context, options *sql.TxOptions) (Tx, error) {
	if d.DB == nil {
		return nil, ErrRepositoryUnavailable
	}
	tx, err := d.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return sqlTx{tx: tx}, nil
}

func (d SQLDB) QueryRowContext(ctx context.Context, query string, args ...any) Row {
	if d.DB == nil {
		return errRow{err: ErrRepositoryUnavailable}
	}
	return d.DB.QueryRowContext(ctx, query, args...)
}

type sqlTx struct{ tx *sql.Tx }

func (t sqlTx) ExecContext(ctx context.Context, q string, a ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, q, a...)
}
func (t sqlTx) QueryRowContext(ctx context.Context, q string, a ...any) Row {
	return t.tx.QueryRowContext(ctx, q, a...)
}
func (t sqlTx) Commit() error   { return t.tx.Commit() }
func (t sqlTx) Rollback() error { return t.tx.Rollback() }

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// ---------------------------------------------------------------------------
// SQL. Every statement is parameterised and bounded.
//
// pr_numbers is INTEGER[]. It crosses this seam as a comma-separated text
// parameter cast in SQL rather than as a driver array type: the seam is
// database/sql-shaped so that tests need no driver, and array binding is a
// driver-specific extension. The cast is inside the statement, so the value is
// still a bound parameter and never string-concatenated into SQL.
// ---------------------------------------------------------------------------

const (
	trainColumns = `id, status,
       COALESCE(array_to_string(pr_numbers, ','), ''),
       COALESCE(head_sha, ''), COALESCE(base_sha, ''),
       window_at, deployed_at, rolled_back_at,
       COALESCE(held_reason, ''), COALESCE(held_by, 0),
       created_at, updated_at`

	lockTrainsSQL = `SELECT pg_advisory_xact_lock(hashtextextended('ops_release_trains', 0))`

	// selectOpenTrainSQL takes the oldest open train that still has room. A
	// full train stays open (it is waiting to deploy early) but must not
	// absorb more work, so a new merge opens the next train behind it.
	selectOpenTrainSQL = `SELECT ` + trainColumns + `
FROM ops_release_trains
WHERE status IN ('PENDING', 'HELD')
  AND cardinality(pr_numbers) < $1
ORDER BY id
LIMIT 1
FOR UPDATE`

	// selectHeadTrainSQL takes the oldest open train regardless of fullness.
	// This is the one /release promotes and /hold holds.
	selectHeadTrainSQL = `SELECT ` + trainColumns + `
FROM ops_release_trains
WHERE status IN ('PENDING', 'HELD')
ORDER BY id
LIMIT 1
FOR UPDATE`

	selectTrainByIDSQL = `SELECT ` + trainColumns + `
FROM ops_release_trains
WHERE id = $1
LIMIT 1`

	// lastCompletedStatusSQL is how the post-rollback split is derived without
	// a schema change: if the most recently completed train was rolled back,
	// the next promotion bisects.
	lastCompletedStatusSQL = `SELECT status
FROM ops_release_trains
WHERE status IN ('DEPLOYED', 'ROLLED_BACK')
ORDER BY COALESCE(rolled_back_at, deployed_at, updated_at) DESC, id DESC
LIMIT 1`

	insertTrainSQL = `INSERT INTO ops_release_trains
  (status, pr_numbers, head_sha, base_sha, window_at, created_at, updated_at)
VALUES ('PENDING', ` + `COALESCE(string_to_array(NULLIF($1, ''), ',')::integer[], '{}'::integer[])` + `,
        NULLIF($2, ''), NULLIF($3, ''), $4, $5, $5)
RETURNING ` + trainColumns

	updatePRsSQL = `UPDATE ops_release_trains
SET pr_numbers = COALESCE(string_to_array(NULLIF($2, ''), ',')::integer[], '{}'::integer[]),
    head_sha = COALESCE(NULLIF($3, ''), head_sha),
    base_sha = COALESCE(base_sha, NULLIF($4, '')),
    updated_at = $5
WHERE id = $1 AND status IN ('PENDING', 'HELD')
RETURNING ` + trainColumns

	holdTrainSQL = `UPDATE ops_release_trains
SET status = 'HELD', held_reason = $2, held_by = $3,
    window_at = COALESCE(window_at, $4), updated_at = $5
WHERE id = $1 AND status = 'PENDING'
RETURNING ` + trainColumns

	// expireHoldSQL is the auto-expiry: once the skipped window has passed the
	// train returns to PENDING and rolls forward onto the following window. A
	// held train is deferred, never dropped.
	expireHoldSQL = `UPDATE ops_release_trains
SET status = 'PENDING', window_at = $2, updated_at = $3
WHERE id = $1 AND status = 'HELD'
RETURNING ` + trainColumns

	promoteTrainSQL = `UPDATE ops_release_trains
SET status = 'DEPLOYING',
    pr_numbers = COALESCE(string_to_array(NULLIF($2, ''), ',')::integer[], '{}'::integer[]),
    head_sha = NULLIF($3, ''),
    updated_at = $4
WHERE id = $1 AND status = 'PENDING'
RETURNING ` + trainColumns

	markDeployedSQL = `UPDATE ops_release_trains
SET status = 'DEPLOYED', deployed_at = $2, head_sha = COALESCE(NULLIF($3, ''), head_sha),
    updated_at = $2
WHERE id = $1 AND status = 'DEPLOYING'
RETURNING ` + trainColumns

	markRolledBackSQL = `UPDATE ops_release_trains
SET status = 'ROLLED_BACK', rolled_back_at = $2, updated_at = $2
WHERE id = $1 AND status = 'DEPLOYED'
RETURNING ` + trainColumns

	// attachDeploymentSQL stamps the train identity onto the deployment row so
	// a rollback notification can say exactly what went back without joining
	// against a train row that may have moved on.
	attachDeploymentSQL = `UPDATE ops_deployments d
SET deploy_class = 'BATCHED', train_id = t.id, train_pr_numbers = t.pr_numbers
FROM ops_release_trains t
WHERE d.id = $1::uuid AND t.id = $2`

	classifyDeploymentSQL = `UPDATE ops_deployments
SET deploy_class = $2
WHERE id = $1::uuid`

	// trainDeploymentsSQL is bounded by the inner LIMIT: a train can in
	// principle be deployed per service, and this reads at most 20 rows.
	trainDeploymentsSQL = `SELECT COALESCE(string_agg(id, ','), '')
FROM (
  SELECT d.id::text AS id
  FROM ops_deployments d
  WHERE d.train_id = $1
  ORDER BY d.started_at DESC
  LIMIT 20
) s`
)

// ---------------------------------------------------------------------------
// Manager.
// ---------------------------------------------------------------------------

// Manager owns the release train state machine. It writes to Postgres before
// anything is reported to Telegram (spec §7.2), and it never deploys.
type Manager struct {
	db     DB
	policy WindowPolicy
	now    func() time.Time
}

// NewManager returns a train manager. now may be nil, meaning time.Now.
func NewManager(db DB, policy WindowPolicy, now func() time.Time) (*Manager, error) {
	if db == nil {
		return nil, ErrRepositoryUnavailable
	}
	if policy.Location == nil {
		policy.Location = EAT()
	}
	if now == nil {
		now = time.Now
	}
	return &Manager{db: db, policy: policy, now: now}, nil
}

// Policy exposes the configured window policy for rendering.
func (m *Manager) Policy() WindowPolicy { return m.policy }

// Now is the manager's clock, exported so the command layer renders the same
// instant the state machine used.
func (m *Manager) Now() time.Time { return m.now() }

// begin opens a serializable transaction and takes the train advisory lock, so
// two concurrent merges cannot both decide they are the eighth PR.
func (m *Manager) begin(ctx context.Context) (Tx, error) {
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, lockTrainsSQL); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

// AppendResult is what a merge learns about the train it joined.
type AppendResult struct {
	Train Train
	// Added is false when the PR was already on the train: appending the same
	// merge twice is a no-op, because webhook redelivery is normal.
	Added bool
	// Full reports that the train has reached MaxTrainPRs.
	Full bool
	// DeployEarly reports that the train is now due ahead of its window
	// because it filled. The caller deploys; this package does not.
	DeployEarly bool
}

// Append adds a merged BATCHED pull request to the pending train, creating the
// train if there is none. An IMMEDIATE change is rejected: it bypasses the
// train by definition, and silently batching an urgent fix is the worst
// possible outcome of a classification mistake.
func (m *Manager) Append(ctx context.Context, pr PullRequest, class Classification) (AppendResult, error) {
	if m == nil || m.db == nil {
		return AppendResult{}, ErrRepositoryUnavailable
	}
	if class.Immediate() {
		return AppendResult{}, ErrNotBatched
	}
	if pr.Number <= 0 {
		return AppendResult{}, fmt.Errorf("%w: pull request number", ErrTrainStateConflict)
	}

	tx, err := m.begin(ctx)
	if err != nil {
		return AppendResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	now := m.now().UTC()
	train, err := scanTrain(tx.QueryRowContext(ctx, selectOpenTrainSQL, MaxTrainPRs))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		window, werr := m.policy.NextWindow(now)
		if werr != nil {
			return AppendResult{}, werr
		}
		train, err = scanTrain(tx.QueryRowContext(ctx, insertTrainSQL,
			strconv.Itoa(pr.Number), pr.HeadSHA, pr.BaseSHA, window, now))
		if err != nil {
			return AppendResult{}, err
		}
		if err = tx.Commit(); err != nil {
			return AppendResult{}, err
		}
		committed = true
		return AppendResult{Train: train, Added: true, Full: train.Full(), DeployEarly: train.Full()}, nil
	case err != nil:
		return AppendResult{}, err
	}

	if containsPR(train.PRNumbers, pr.Number) {
		if err = tx.Commit(); err != nil {
			return AppendResult{}, err
		}
		committed = true
		return AppendResult{Train: train, Added: false, Full: train.Full(), DeployEarly: false}, nil
	}

	numbers := append(append([]int(nil), train.PRNumbers...), pr.Number)
	updated, err := scanTrain(tx.QueryRowContext(ctx, updatePRsSQL,
		train.ID, formatPRs(numbers), pr.HeadSHA, pr.BaseSHA, now))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AppendResult{}, ErrTrainStateConflict
		}
		return AppendResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return AppendResult{}, err
	}
	committed = true
	return AppendResult{
		Train: updated,
		Added: true,
		Full:  updated.Full(),
		// A full train deploys early rather than growing past the cap. The
		// window is a latest-by, not an earliest-at.
		DeployEarly: updated.Full(),
	}, nil
}

// Status is the /release and /hold projection of the head train.
type Status struct {
	Exists bool
	Train  Train
	// Due reports that the train should go out now, either because its window
	// has arrived or because it filled.
	Due       bool
	DueReason string
	// Held reports an active hold; SkippedWindow is the window it skips and
	// NextWindow is when the train actually goes.
	Held          bool
	SkippedWindow time.Time
	NextWindow    time.Time
	// SplitRequired reports that the previous train was rolled back, so this
	// one promotes in two halves.
	SplitRequired bool
}

// Status reads the head train and normalizes an expired hold. It is a read
// with one possible corrective write (the hold expiry), so it runs in the same
// transaction as the read.
func (m *Manager) Status(ctx context.Context) (Status, error) {
	if m == nil || m.db == nil {
		return Status{}, ErrRepositoryUnavailable
	}
	tx, err := m.begin(ctx)
	if err != nil {
		return Status{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	status, err := m.statusTx(ctx, tx)
	if err != nil && !errors.Is(err, ErrNoOpenTrain) {
		return Status{}, err
	}
	if cerr := tx.Commit(); cerr != nil {
		return Status{}, cerr
	}
	committed = true
	if errors.Is(err, ErrNoOpenTrain) {
		return Status{}, nil
	}
	return status, nil
}

// statusTx is the shared read used by Status, Hold and Promote so all three
// agree about hold expiry and split state.
func (m *Manager) statusTx(ctx context.Context, tx Tx) (Status, error) {
	now := m.now().UTC()
	train, err := scanTrain(tx.QueryRowContext(ctx, selectHeadTrainSQL))
	if errors.Is(err, sql.ErrNoRows) {
		return Status{}, ErrNoOpenTrain
	}
	if err != nil {
		return Status{}, err
	}

	status := Status{Exists: true, Train: train}

	if train.Status == StateHeld {
		// The hold covers exactly one window: the one recorded on window_at.
		// Once that instant has passed the hold has done its job and expires,
		// and the train rolls forward onto the next window rather than being
		// lost.
		if !train.WindowAt.IsZero() && !now.Before(train.WindowAt) {
			next, werr := m.policy.NextWindow(train.WindowAt)
			if werr != nil {
				return Status{}, werr
			}
			rolled, uerr := scanTrain(tx.QueryRowContext(ctx, expireHoldSQL, train.ID, next, now))
			if uerr != nil && !errors.Is(uerr, sql.ErrNoRows) {
				return Status{}, uerr
			}
			if uerr == nil {
				train = rolled
				status.Train = rolled
			}
		} else {
			next, werr := m.policy.NextWindow(train.WindowAt)
			if werr != nil {
				return Status{}, werr
			}
			status.Held = true
			status.SkippedWindow = train.WindowAt
			status.NextWindow = next
			status.SplitRequired, err = m.splitRequired(ctx, tx)
			if err != nil {
				return Status{}, err
			}
			return status, nil
		}
	}

	status.NextWindow = train.WindowAt
	switch {
	case train.Full():
		status.Due, status.DueReason = true, "train is full"
	case !train.WindowAt.IsZero() && !now.Before(train.WindowAt):
		status.Due, status.DueReason = true, "window reached"
	}
	status.SplitRequired, err = m.splitRequired(ctx, tx)
	if err != nil {
		return Status{}, err
	}
	return status, nil
}

// splitRequired derives the post-rollback bisect obligation from the trains
// themselves. There is no dedicated column and none is needed: if the most
// recently completed train ended in ROLLED_BACK, the next promotion splits.
// Deriving it means the obligation cannot be lost by a missed write, and it
// clears itself the moment a split train deploys successfully.
func (m *Manager) splitRequired(ctx context.Context, tx Tx) (bool, error) {
	var status string
	err := tx.QueryRowContext(ctx, lastCompletedStatusSQL).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return State(status) == StateRolledBack, nil
}

// HoldResult reports what a /hold actually did.
type HoldResult struct {
	Train Train
	// SkippedWindow is the window this hold skips.
	SkippedWindow time.Time
	// NextWindow is when the train goes instead. The hold expires on its own
	// once SkippedWindow passes; nobody has to remember to release it.
	NextWindow time.Time
}

// Hold skips the next window only. The train is not cancelled and its PRs are
// not dropped: after the skipped window passes the hold expires and the train
// rolls forward. Holding an already-held train is refused, because stacking
// holds one command at a time is how a train quietly never ships.
func (m *Manager) Hold(ctx context.Context, userID int64, reason string) (HoldResult, error) {
	if m == nil || m.db == nil {
		return HoldResult{}, ErrRepositoryUnavailable
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "held by operator"
	}

	tx, err := m.begin(ctx)
	if err != nil {
		return HoldResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	status, err := m.statusTx(ctx, tx)
	if err != nil {
		return HoldResult{}, err
	}
	if status.Held {
		return HoldResult{}, ErrAlreadyHeld
	}

	now := m.now().UTC()
	window := status.Train.WindowAt
	if window.IsZero() {
		if window, err = m.policy.NextWindow(now); err != nil {
			return HoldResult{}, err
		}
	}
	next, err := m.policy.NextWindow(window)
	if err != nil {
		return HoldResult{}, err
	}

	held, err := scanTrain(tx.QueryRowContext(ctx, holdTrainSQL, status.Train.ID, reason, userID, window, now))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return HoldResult{}, ErrTrainStateConflict
		}
		return HoldResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return HoldResult{}, err
	}
	committed = true
	return HoldResult{Train: held, SkippedWindow: window, NextWindow: next}, nil
}

// PromoteResult describes the promotion, including the bisect split.
type PromoteResult struct {
	// Train is the part that moved to DEPLOYING.
	Train Train
	// Split reports that the train was halved because the previous train was
	// rolled back.
	Split bool
	// FollowOn is part two, left PENDING and due after the soak. Nil when
	// there was no split.
	FollowOn *Train
	// Part and Parts are 1/1 normally and 1/2 for a split promotion.
	Part  int
	Parts int
	// FollowOnWindow is when part two becomes due.
	FollowOnWindow time.Time
}

// Promote moves the head train to DEPLOYING and returns the PR list and SHA
// range the CI deploy path will carry. It does not deploy: it records that the
// train is going out, and the existing deploy path does the rest.
//
// Post-rollback bisection: when the previous train was rolled back, the head
// train is split in half. Part one promotes now; part two becomes a new
// PENDING train due after the configured soak. A rolled-back train of N
// changes leaves N suspects, and the only cheap way to halve that number is to
// stop shipping N at a time until one train has come back clean. This is
// required behaviour, not an optimisation, and skipping it means the next
// regression is just as unattributable as the last one.
func (m *Manager) Promote(ctx context.Context) (PromoteResult, error) {
	if m == nil || m.db == nil {
		return PromoteResult{}, ErrRepositoryUnavailable
	}
	tx, err := m.begin(ctx)
	if err != nil {
		return PromoteResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	result, err := m.promoteTx(ctx, tx)
	if err != nil {
		return PromoteResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return PromoteResult{}, err
	}
	committed = true
	return result, nil
}

// PromoteConfirmed redeems a confirmation token and promotes the train inside
// one transaction, so the token is spent exactly when the train moves. A
// redelivered Telegram update, or a second operator repeating the token they
// can see in the group, finds it consumed and changes nothing.
//
// The confirmation store must be backed by the same database as this manager;
// they are wired from one *sql.DB.
func (m *Manager) PromoteConfirmed(ctx context.Context, c *Confirmations, token string, userID int64) (PromoteResult, error) {
	if m == nil || m.db == nil || c == nil {
		return PromoteResult{}, ErrRepositoryUnavailable
	}
	tx, err := m.begin(ctx)
	if err != nil {
		return PromoteResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	confirmation, err := c.consumeTx(ctx, tx, token, userID, RunbookRelease)
	if err != nil {
		return PromoteResult{}, err
	}

	result, err := m.promoteTx(ctx, tx)
	if err != nil {
		return PromoteResult{}, err
	}
	// The token names the train the operator actually looked at. If the head
	// train has changed since — another train promoted, or this one rolled
	// forward — the confirmation was for a different thing and must not be
	// silently reused for this one.
	if want, ok := confirmationTrainID(confirmation.Args); ok && want != result.Train.ID {
		return PromoteResult{}, ErrTokenMismatch
	}

	if err = tx.Commit(); err != nil {
		return PromoteResult{}, err
	}
	committed = true
	return result, nil
}

// RunbookRelease is the ops_pending_confirmations.runbook value for /release.
const RunbookRelease = "release_train_promote"

// confirmationTrainID reads the train id recorded in a confirmation's args.
// JSON numbers decode as float64.
func confirmationTrainID(args map[string]any) (int64, bool) {
	raw, ok := args["train_id"]
	if !ok {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	default:
		return 0, false
	}
}

// promoteTx is Promote's body without transaction management.
func (m *Manager) promoteTx(ctx context.Context, tx Tx) (PromoteResult, error) {
	status, err := m.statusTx(ctx, tx)
	if err != nil {
		return PromoteResult{}, err
	}
	if status.Held {
		return PromoteResult{}, ErrTrainHeld
	}
	if len(status.Train.PRNumbers) == 0 {
		return PromoteResult{}, ErrEmptyTrain
	}

	now := m.now().UTC()
	head := status.Train
	first := head.PRNumbers
	var second []int
	if status.SplitRequired && len(head.PRNumbers) > 1 {
		half := (len(head.PRNumbers) + 1) / 2
		first = head.PRNumbers[:half]
		second = head.PRNumbers[half:]
	}

	// On a split the head SHA of part one is not known here: this package does
	// not compute merge bases, and the recorded head belongs to the last PR on
	// the whole train, which is now part two's. Clearing it is honest; the CI
	// deploy path stamps the resolved SHA through MarkDeployed.
	headSHA := head.HeadSHA
	if len(second) > 0 {
		headSHA = ""
	}
	promoted, err := scanTrain(tx.QueryRowContext(ctx, promoteTrainSQL, head.ID, formatPRs(first), headSHA, now))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PromoteResult{}, ErrTrainStateConflict
		}
		return PromoteResult{}, err
	}

	result := PromoteResult{Train: promoted, Part: 1, Parts: 1}
	if len(second) > 0 {
		followWindow := now.Add(m.policy.splitSoak())
		follow, ferr := scanTrain(tx.QueryRowContext(ctx, insertTrainSQL,
			formatPRs(second), head.HeadSHA, "", followWindow, now))
		if ferr != nil {
			return PromoteResult{}, ferr
		}
		result.Split = true
		result.FollowOn = &follow
		result.Parts = 2
		result.FollowOnWindow = followWindow
	}
	return result, nil
}

// MarkDeployed records that the CI deploy path finished the train. headSHA is
// the SHA that actually went out; empty leaves the recorded value alone.
func (m *Manager) MarkDeployed(ctx context.Context, trainID int64, headSHA string) (Train, error) {
	return m.transition(ctx, markDeployedSQL, trainID, m.now().UTC(), headSHA)
}

// Rollback records that a deployed train was rolled back and returns the full
// attribution: every PR that went back, and the deployment rows that carried
// them. The next promotion will bisect (see Promote).
func (m *Manager) Rollback(ctx context.Context, trainID int64) (Attribution, error) {
	if m == nil || m.db == nil {
		return Attribution{}, ErrRepositoryUnavailable
	}
	tx, err := m.begin(ctx)
	if err != nil {
		return Attribution{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	now := m.now().UTC()
	train, err := scanTrain(tx.QueryRowContext(ctx, markRolledBackSQL, trainID, now))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Attribution{}, ErrTrainStateConflict
		}
		return Attribution{}, err
	}
	var joined string
	if err = tx.QueryRowContext(ctx, trainDeploymentsSQL, trainID).Scan(&joined); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Attribution{}, err
	}
	if err = tx.Commit(); err != nil {
		return Attribution{}, err
	}
	committed = true

	return Attribution{
		TrainID:       train.ID,
		PRNumbers:     train.PRNumbers,
		HeadSHA:       train.HeadSHA,
		BaseSHA:       train.BaseSHA,
		DeployedAt:    train.DeployedAt,
		RolledBackAt:  train.RolledBack,
		DeploymentIDs: splitNonEmpty(joined),
		// A rollback always makes the next train bisect. Stating it in the
		// attribution means the notification can say so without a second read.
		NextTrainSplits: true,
	}, nil
}

// Attribution is what a rollback notification needs in order to say exactly
// what went back. Every PR in the train is listed, not a count: "8 changes
// were reverted" is not an answer anyone can act on at 2am.
type Attribution struct {
	TrainID         int64
	PRNumbers       []int
	HeadSHA         string
	BaseSHA         string
	DeployedAt      time.Time
	RolledBackAt    time.Time
	DeploymentIDs   []string
	NextTrainSplits bool
}

// Attribution reads a train's PR list without changing its state, for a
// notification about a rollback that happened elsewhere.
func (m *Manager) Attribution(ctx context.Context, trainID int64) (Attribution, error) {
	if m == nil || m.db == nil {
		return Attribution{}, ErrRepositoryUnavailable
	}
	train, err := scanTrain(m.db.QueryRowContext(ctx, selectTrainByIDSQL, trainID))
	if err != nil {
		return Attribution{}, err
	}
	return Attribution{
		TrainID:         train.ID,
		PRNumbers:       train.PRNumbers,
		HeadSHA:         train.HeadSHA,
		BaseSHA:         train.BaseSHA,
		DeployedAt:      train.DeployedAt,
		RolledBackAt:    train.RolledBack,
		NextTrainSplits: train.Status == StateRolledBack,
	}, nil
}

// AttachDeployment stamps train identity onto an ops_deployments row: the
// train id and a snapshot of its PR list, so the deployment record stays
// readable after the train moves on.
func (m *Manager) AttachDeployment(ctx context.Context, deploymentID string, trainID int64) error {
	if m == nil || m.db == nil {
		return ErrRepositoryUnavailable
	}
	if strings.TrimSpace(deploymentID) == "" {
		return fmt.Errorf("%w: deployment id", ErrTrainStateConflict)
	}
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	res, err := tx.ExecContext(ctx, attachDeploymentSQL, deploymentID, trainID)
	if err != nil {
		return err
	}
	if affected, aerr := res.RowsAffected(); aerr == nil && affected == 0 {
		return ErrTrainStateConflict
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// ClassifyDeployment records a deploy class on a deployment row. IMMEDIATE
// deployments never join a train, so this is how they get classified.
func (m *Manager) ClassifyDeployment(ctx context.Context, deploymentID string, class Class) error {
	if m == nil || m.db == nil {
		return ErrRepositoryUnavailable
	}
	if class != ClassImmediate && class != ClassBatched {
		return fmt.Errorf("%w: deploy class %q", ErrTrainStateConflict, class)
	}
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, classifyDeploymentSQL, deploymentID, string(class)); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// transition runs a single guarded state change.
func (m *Manager) transition(ctx context.Context, query string, args ...any) (Train, error) {
	if m == nil || m.db == nil {
		return Train{}, ErrRepositoryUnavailable
	}
	tx, err := m.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return Train{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	train, err := scanTrain(tx.QueryRowContext(ctx, query, args...))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Train{}, ErrTrainStateConflict
		}
		return Train{}, err
	}
	if err = tx.Commit(); err != nil {
		return Train{}, err
	}
	committed = true
	return train, nil
}

// ---------------------------------------------------------------------------
// Row mapping and small helpers.
// ---------------------------------------------------------------------------

func scanTrain(row Row) (Train, error) {
	var (
		t          Train
		status     string
		prs        string
		window     sql.NullTime
		deployed   sql.NullTime
		rolledBack sql.NullTime
	)
	if err := row.Scan(&t.ID, &status, &prs, &t.HeadSHA, &t.BaseSHA,
		&window, &deployed, &rolledBack, &t.HeldReason, &t.HeldBy,
		&t.CreatedAt, &t.UpdatedAt); err != nil {
		return Train{}, err
	}
	t.Status = State(status)
	t.PRNumbers = parsePRs(prs)
	if window.Valid {
		t.WindowAt = window.Time.UTC()
	}
	if deployed.Valid {
		t.DeployedAt = deployed.Time.UTC()
	}
	if rolledBack.Valid {
		t.RolledBack = rolledBack.Time.UTC()
	}
	return t, nil
}

func parsePRs(joined string) []int {
	parts := splitNonEmpty(joined)
	if len(parts) == 0 {
		return nil
	}
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

func formatPRs(numbers []int) string {
	if len(numbers) == 0 {
		return ""
	}
	parts := make([]string, 0, len(numbers))
	for _, n := range numbers {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ",")
}

func splitNonEmpty(joined string) []string {
	joined = strings.TrimSpace(joined)
	if joined == "" {
		return nil
	}
	raw := strings.Split(joined, ",")
	out := make([]string, 0, len(raw))
	for _, part := range raw {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func containsPR(numbers []int, n int) bool {
	for _, existing := range numbers {
		if existing == n {
			return true
		}
	}
	return false
}

// FormatPRList renders a PR list as "#12, #14, #19" for a reply body.
func FormatPRList(numbers []int) string {
	if len(numbers) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(numbers))
	for _, n := range numbers {
		parts = append(parts, "#"+strconv.Itoa(n))
	}
	return strings.Join(parts, ", ")
}

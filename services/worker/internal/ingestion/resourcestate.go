package ingestion

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

// Adaptive TTL (architecture v2 §4.2). The largest per-request saving after
// batching: a resource whose response keeps coming back identical is fetched
// less and less often, while one that keeps changing holds its floor.
//
// The decision is a pure function of the previous state and what the response
// turned out to be. Persistence is separate, and deliberately so -- the ladder
// is the part that decides whether quota is spent, and it is worth being able
// to test every rung of it without a database.

// Backoff is the growth factor applied to the interval each time a response
// comes back unchanged. 1.5 rather than 2 so the climb from a 60s floor to a
// 4-minute ceiling takes five unchanged fetches rather than two: doubling
// overshoots a match that goes quiet for two minutes and then scores.
const Backoff = 1.5

// TTLTier is the interval range a resource is allowed to move within.
//
// Min is the floor a changed response resets to. Max is the ceiling the backoff
// climbs to and stops. Both are per-resource-kind: live statistics during a
// match want a tight band, a pre-match resource a loose one.
type TTLTier struct {
	Min time.Duration
	Max time.Duration
}

// Normalize repairs a tier rather than rejecting it. A misconfigured tier that
// returned an error would have to be handled at every call site, and the
// handling would be "fetch anyway" -- which is the expensive answer. Clamping
// to something sane and continuing spends less quota than failing open.
func (t TTLTier) Normalize() TTLTier {
	if t.Min <= 0 {
		t.Min = 30 * time.Second
	}
	if t.Max < t.Min {
		t.Max = t.Min
	}
	return t
}

// ResourceState is one row of provider_resource_state.
type ResourceState struct {
	Provider         string
	ResourceKey      string
	LastFetchedAt    time.Time
	LastResponseHash string
	UnchangedStreak  int
	CurrentInterval  time.Duration
	NextEligibleAt   time.Time
	Immutable        bool
}

// Observation is what a completed fetch learned about a resource.
type Observation struct {
	// ResponseHash of the body just fetched. Empty means "not hashable" and is
	// treated as changed: an unknown response must not extend the interval.
	ResponseHash string

	// Significant marks a change the ladder must react to immediately even if
	// the streak says otherwise -- a goal or a red card. §4.2 calls for an
	// immediate reset to the minimum regardless of streak, because the minutes
	// after a goal are exactly when the data is worth having and exactly when
	// a backed-off resource would be blind.
	Significant bool

	// Terminal marks a resource that can never change again: a finished,
	// settled fixture, or history past the retention horizon.
	Terminal bool

	// FetchedAt is when the response came back.
	FetchedAt time.Time
}

// Advance computes the next state from the previous one and what was observed.
//
// Pure: no clock, no database. Everything it needs is in its arguments, which
// is what makes the ladder testable rung by rung.
func Advance(prev ResourceState, obs Observation, tier TTLTier) ResourceState {
	tier = tier.Normalize()

	next := prev
	next.LastFetchedAt = obs.FetchedAt
	next.LastResponseHash = obs.ResponseHash

	// Terminal wins over everything. A settled fixture that also happens to
	// have an unchanged hash is not a candidate for backoff -- it is a
	// candidate for never being fetched again.
	if obs.Terminal {
		next.Immutable = true
		next.UnchangedStreak = 0
		next.CurrentInterval = tier.Max
		// Kept in the past rather than set far in the future: the due-index
		// excludes immutable rows anyway, and a sentinel date here would be a
		// second thing to keep consistent with the flag.
		next.NextEligibleAt = obs.FetchedAt
		return next
	}

	unchanged := obs.ResponseHash != "" &&
		obs.ResponseHash == prev.LastResponseHash &&
		!obs.Significant

	switch {
	case obs.Significant:
		// Something happened. Drop to the floor no matter how long the streak
		// was -- this is the case the whole mechanism exists to get right.
		next.UnchangedStreak = 0
		next.CurrentInterval = tier.Min
	case unchanged:
		next.UnchangedStreak = prev.UnchangedStreak + 1
		next.CurrentInterval = climb(prev.CurrentInterval, tier)
	default:
		// Changed, or a response we could not hash. Both reset: an unhashable
		// response is not evidence that nothing changed.
		next.UnchangedStreak = 0
		next.CurrentInterval = tier.Min
	}

	next.NextEligibleAt = obs.FetchedAt.Add(next.CurrentInterval)
	return next
}

// climb applies one step of backoff, bounded by the tier.
func climb(current time.Duration, tier TTLTier) time.Duration {
	if current < tier.Min {
		current = tier.Min
	}
	grown := time.Duration(math.Round(float64(current) * Backoff))
	if grown > tier.Max {
		return tier.Max
	}
	// Rounding a sub-second interval by 1.5 can land back on itself and stall
	// the climb. Nudge it so the streak always buys something.
	if grown <= current {
		grown = current + time.Second
		if grown > tier.Max {
			return tier.Max
		}
	}
	return grown
}

// DueAt reports whether a resource may be fetched at the given moment.
//
// An immutable resource is never due. A resource with no recorded state is
// always due -- the first fetch is what creates the state, and refusing it
// would mean nothing was ever fetched.
func (s ResourceState) DueAt(at time.Time) bool {
	if s.Immutable {
		return false
	}
	if s.NextEligibleAt.IsZero() {
		return true
	}
	return !at.Before(s.NextEligibleAt)
}

// ResourceStateRepository persists the ladder.
type ResourceStateRepository struct {
	db resourceStateDB
}

type resourceStateDB interface {
	requestLogDB
	QueryRowContext(ctx context.Context, query string, args ...any) rowScanner
}

type rowScanner interface {
	Scan(dest ...any) error
}

func NewResourceStateRepository(db resourceStateDB) ResourceStateRepository {
	return ResourceStateRepository{db: db}
}

// ResourceKey namespaces a resource id under a kind, e.g.
// ResourceKey("fixture_stats", 1234567) -> "fixture_stats:1234567".
//
// Callers should always build keys through this rather than by concatenating,
// so that a kind can never collide with another kind's ids.
func ResourceKey(kind string, id int64) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "resource"
	}
	return kind + ":" + strconv.FormatInt(id, 10)
}

const upsertResourceStateSQL = `
INSERT INTO provider_resource_state (
  provider, resource_key, last_fetched_at, last_response_hash,
  unchanged_streak, current_interval_s, next_eligible_at, immutable
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (provider, resource_key) DO UPDATE SET
  last_fetched_at    = excluded.last_fetched_at,
  last_response_hash = excluded.last_response_hash,
  unchanged_streak   = excluded.unchanged_streak,
  current_interval_s = excluded.current_interval_s,
  next_eligible_at   = excluded.next_eligible_at,
  -- Immutability is one-way. Once a fixture is settled, a later write must not
  -- quietly un-settle it and restart the spending.
  immutable          = provider_resource_state.immutable OR excluded.immutable`

// Save writes the advanced state.
func (r ResourceStateRepository) Save(ctx context.Context, state ResourceState) error {
	if r.db == nil {
		return errors.New("resource state DB is nil")
	}
	if strings.TrimSpace(state.Provider) == "" {
		return errors.New("provider is required")
	}
	if strings.TrimSpace(state.ResourceKey) == "" {
		return errors.New("resource key is required")
	}
	interval := int(state.CurrentInterval / time.Second)
	if interval < 1 {
		// The column is CHECK (> 0). Rounding a sub-second interval down to
		// zero would fail the insert on a technicality, which would turn a
		// scheduling detail into a job failure.
		interval = 1
	}
	_, err := r.db.ExecContext(ctx, upsertResourceStateSQL,
		state.Provider,
		state.ResourceKey,
		state.LastFetchedAt,
		nullableHash(state.LastResponseHash),
		state.UnchangedStreak,
		interval,
		state.NextEligibleAt,
		state.Immutable,
	)
	return err
}

const loadResourceStateSQL = `
SELECT last_fetched_at, last_response_hash, unchanged_streak,
       current_interval_s, next_eligible_at, immutable
  FROM provider_resource_state
 WHERE provider = $1 AND resource_key = $2`

// Load reads the recorded state.
//
// A resource with no row returns a zero state and no error: "never fetched" is
// a normal condition, not a failure, and a zero state is due immediately.
func (r ResourceStateRepository) Load(ctx context.Context, provider, key string) (ResourceState, error) {
	state := ResourceState{Provider: provider, ResourceKey: key}
	if r.db == nil {
		return state, errors.New("resource state DB is nil")
	}
	var (
		hash     *string
		interval int
	)
	err := r.db.QueryRowContext(ctx, loadResourceStateSQL, provider, key).Scan(
		&state.LastFetchedAt,
		&hash,
		&state.UnchangedStreak,
		&interval,
		&state.NextEligibleAt,
		&state.Immutable,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ResourceState{Provider: provider, ResourceKey: key}, nil
		}
		return state, err
	}
	if hash != nil {
		state.LastResponseHash = *hash
	}
	state.CurrentInterval = time.Duration(interval) * time.Second
	return state, nil
}

func nullableHash(hash string) any {
	if hash == "" {
		return nil
	}
	return hash
}

// sqlResourceStateDB adapts *sql.DB to resourceStateDB.
//
// The interface returns rowScanner rather than *sql.Row so the repository can
// be tested without a database. *sql.Row already satisfies rowScanner, so the
// only thing missing was something to widen the return type -- and because
// nothing did, the only implementations were test fakes and the adaptive TTL
// ladder was never connected to anything in production. The statistics job
// therefore refetched every fixture it was handed, 94,220 times in a day
// across 8,757 fixtures, which exhausted the provider's daily quota and froze
// live ingestion for fifteen hours on 2026-09-22.
type sqlResourceStateDB struct{ db *sql.DB }

func (s sqlResourceStateDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, query, args...)
}

func (s sqlResourceStateDB) QueryRowContext(ctx context.Context, query string, args ...any) rowScanner {
	return s.db.QueryRowContext(ctx, query, args...)
}

// NewSQLResourceStateRepository builds the repository over a real database.
func NewSQLResourceStateRepository(db *sql.DB) *ResourceStateRepository {
	repo := NewResourceStateRepository(sqlResourceStateDB{db: db})
	return &repo
}

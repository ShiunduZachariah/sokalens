package notifications

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Store is the outbox persistence boundary.
//
// Every state change a notification can undergo is a method here, and none of
// them deletes a row. That is not tidiness: it is the mechanism behind "a
// Telegram send failure must never lose a notification". A notification is
// only ever moved out of 'pending' by MarkDelivered (it reached someone) or by
// MarkTerminal (it is durably recorded as failed or suppressed, and still
// readable). A crash between Claim and either call leaves the row leased, and
// the lease expiring returns it to the claim query.
type Store interface {
	// Enqueue inserts a notification, or collapses it onto the open row with
	// the same fingerprint. It returns the row's identity and whether a new
	// row was created; created=false means a correlated observation was
	// absorbed and no second message will be sent.
	Enqueue(ctx context.Context, n Notification) (EnqueueResult, error)

	// EnqueueOnce inserts a notification keyed by DedupKey, which is unique
	// for all time. A second call with the same key is a no-op returning
	// created=false. Used by the digest and the suppression notice, which must
	// not repeat across a restart.
	EnqueueOnce(ctx context.Context, n Notification) (EnqueueResult, error)

	// Touch records a correlated observation against the open row for a
	// fingerprint, if one exists. It never creates a row: a repeat observation
	// of an incident whose notification was already delivered must not produce
	// a second message.
	Touch(ctx context.Context, fingerprint string, at time.Time) error

	// Claim leases up to limit due notifications, most urgent first,
	// incrementing attempts and pushing next_attempt_at out by lease. The
	// lease is what makes a crash mid-delivery recoverable rather than lossy.
	Claim(ctx context.Context, limit int, lease time.Duration, now time.Time) ([]Notification, error)

	// MarkDelivered records a successful delivery. deliveryErr is the error
	// from an *earlier* transport in the same attempt and is retained: a row
	// delivered by email after Telegram failed must still show why.
	MarkDelivered(ctx context.Context, id, transport, deliveryErr string, at time.Time) error

	// MarkRetry returns a notification to the queue at nextAttempt with the
	// failure recorded.
	MarkRetry(ctx context.Context, id, deliveryErr string, nextAttempt, at time.Time) error

	// MarkTerminal moves a notification to 'failed' or 'suppressed'. Both are
	// durable, queryable states; neither deletes anything.
	MarkTerminal(ctx context.Context, id, status, deliveryErr string, at time.Time) error

	// DigestBacklog reads the undelivered INFO notifications raised in
	// [since, until).
	DigestBacklog(ctx context.Context, since, until time.Time, limit int) ([]Notification, error)

	// MarkDigested closes INFO rows once the digest carrying them was sent.
	MarkDigested(ctx context.Context, ids []string, at time.Time) error
}

// EnqueueResult reports what Enqueue did.
type EnqueueResult struct {
	ID string
	// Created is false when the notification collapsed onto an existing open
	// row, or when a DedupKey already existed.
	Created bool
	// Tier is the row's tier after the merge, which may have been raised.
	Tier Tier
	// OccurrenceCount is the row's count after the merge.
	OccurrenceCount int64
}

// ---------------------------------------------------------------------------
// DB seam. Intentionally database/sql-compatible and identical in shape to the
// one in internal/incidents, so tests need no driver and production wiring
// wraps the same *sql.DB main.go already opens.
// ---------------------------------------------------------------------------

// DB is the query surface the store needs.
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) Row
}

// Rows matches database/sql's *Rows for the subset used here.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

// Row matches database/sql's *Row.
type Row interface{ Scan(dest ...any) error }

// SQLDB adapts *sql.DB to DB.
type SQLDB struct{ DB *sql.DB }

func (d SQLDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if d.DB == nil {
		return nil, ErrStoreUnavailable
	}
	return d.DB.ExecContext(ctx, query, args...)
}

func (d SQLDB) QueryContext(ctx context.Context, query string, args ...any) (Rows, error) {
	if d.DB == nil {
		return nil, ErrStoreUnavailable
	}
	rows, err := d.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (d SQLDB) QueryRowContext(ctx context.Context, query string, args ...any) Row {
	if d.DB == nil {
		return errRow{err: ErrStoreUnavailable}
	}
	return d.DB.QueryRowContext(ctx, query, args...)
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// ---------------------------------------------------------------------------
// SQL.
// ---------------------------------------------------------------------------

const (
	// enqueueSQL inserts, or merges onto the open row for the fingerprint.
	//
	// The conflict target is the partial unique index from migration 000059;
	// the WHERE clause repeats the index predicate so Postgres can infer it.
	//
	// Two properties of the merge are load-bearing:
	//
	//  1. tier is raised, never lowered. An INFO observation landing on an
	//     open IMPORTANT row must not demote it out of the send path.
	//  2. status returns to 'pending' whenever the merged tier is sendable.
	//     Without this, an IMPORTANT alert correlating onto an open INFO row
	//     (status 'digest') would be raised in tier and then never sent — the
	//     tier CHECK would permit it and the claim query would not see it.
	//
	// next_attempt_at is deliberately NOT reset. A row a worker has already
	// leased stays leased; a correlated observation bumps the count, it does
	// not schedule a second send.
	enqueueSQL = `INSERT INTO ops_notification_outbox (
  tier, subject, body, fingerprint, incident_id, incident_reference,
  occurrence_count, status, created_at, updated_at, next_attempt_at
) VALUES ($1, $2, $3, $4, $5::uuid, $6, $7, $8, $9, $9, $9)
ON CONFLICT (fingerprint) WHERE status IN ('pending', 'digest')
DO UPDATE SET
  occurrence_count = ops_notification_outbox.occurrence_count + 1,
  tier = CASE
    WHEN ops_notification_outbox.tier = 'action_required' OR EXCLUDED.tier = 'action_required' THEN 'action_required'
    WHEN ops_notification_outbox.tier = 'important' OR EXCLUDED.tier = 'important' THEN 'important'
    ELSE 'info'
  END,
  status = CASE
    WHEN ops_notification_outbox.tier IN ('important', 'action_required')
      OR EXCLUDED.tier IN ('important', 'action_required') THEN 'pending'
    ELSE ops_notification_outbox.status
  END,
  subject = EXCLUDED.subject,
  body = EXCLUDED.body,
  incident_id = COALESCE(EXCLUDED.incident_id, ops_notification_outbox.incident_id),
  incident_reference = COALESCE(EXCLUDED.incident_reference, ops_notification_outbox.incident_reference),
  updated_at = GREATEST(ops_notification_outbox.updated_at, EXCLUDED.updated_at)
RETURNING id, tier, occurrence_count`

	// enqueueOnceSQL is exactly-once by dedup_key, for all time.
	enqueueOnceSQL = `INSERT INTO ops_notification_outbox (
  tier, subject, body, fingerprint, dedup_key, incident_id, incident_reference,
  occurrence_count, status, created_at, updated_at, next_attempt_at
) VALUES ($1, $2, $3, $4, $5, $6::uuid, $7, $8, $9, $10, $10, $10)
ON CONFLICT (dedup_key) DO NOTHING
RETURNING id, tier, occurrence_count`

	// existingDedupSQL resolves the row a DO NOTHING conflict landed on.
	existingDedupSQL = `SELECT id, tier, occurrence_count
FROM ops_notification_outbox WHERE dedup_key = $1`

	touchSQL = `UPDATE ops_notification_outbox
SET occurrence_count = occurrence_count + 1,
    updated_at = GREATEST(updated_at, $2)
WHERE fingerprint = $1 AND status IN ('pending', 'digest')`

	// claimSQL leases due work. FOR UPDATE SKIP LOCKED keeps two claimers from
	// racing; the ORDER BY puts ACTION REQUIRED ahead of IMPORTANT so a storm
	// of the latter cannot starve the former.
	//
	// tier = 'info' cannot appear here: an INFO row is never in status
	// 'pending' (CHECK, migration 000059). The predicate is stated anyway so
	// this query reads correctly on its own.
	claimSQL = `UPDATE ops_notification_outbox
SET attempts = attempts + 1,
    next_attempt_at = $2,
    updated_at = $1
WHERE id IN (
  SELECT id FROM ops_notification_outbox
  WHERE status = 'pending' AND tier <> 'info' AND next_attempt_at <= $1
  ORDER BY CASE tier WHEN 'action_required' THEN 0 WHEN 'important' THEN 1 ELSE 2 END,
           created_at
  LIMIT $3
  FOR UPDATE SKIP LOCKED
)
RETURNING id, tier, subject, body, fingerprint, COALESCE(incident_id::text, ''),
          COALESCE(incident_reference, 0), occurrence_count, attempts, created_at`

	markDeliveredSQL = `UPDATE ops_notification_outbox
SET status = 'delivered',
    transport = $2,
    delivered_at = $4,
    delivery_error = NULLIF(btrim($3), ''),
    updated_at = $4
WHERE id = $1::uuid AND status <> 'delivered'`

	markRetrySQL = `UPDATE ops_notification_outbox
SET status = 'pending',
    delivery_error = NULLIF(btrim($2), ''),
    next_attempt_at = $3,
    updated_at = $4
WHERE id = $1::uuid AND status = 'pending'`

	markTerminalSQL = `UPDATE ops_notification_outbox
SET status = $2,
    delivery_error = NULLIF(btrim($3), ''),
    updated_at = $4
WHERE id = $1::uuid AND status = 'pending'`

	digestBacklogSQL = `SELECT id, tier, subject, body, fingerprint,
       COALESCE(incident_id::text, ''), COALESCE(incident_reference, 0),
       occurrence_count, attempts, created_at
FROM ops_notification_outbox
WHERE status = 'digest' AND created_at >= $1 AND created_at < $2
ORDER BY created_at
LIMIT $3`

	markDigestedSQL = `UPDATE ops_notification_outbox
SET status = 'delivered',
    transport = 'digest',
    delivered_at = $2,
    updated_at = $2
WHERE id = ANY($1::uuid[]) AND status = 'digest'`
)

// PostgresStore is the durable Store.
type PostgresStore struct {
	db DB
	// maxClaim bounds a single claim regardless of what the caller asks for.
	// The worker's queue is bounded; the claim must be too, or a storm turns
	// into one enormous result set.
	maxClaim int
	// maxDigest bounds the digest backlog read for the same reason.
	maxDigest int
}

var _ Store = (*PostgresStore)(nil)

const (
	defaultMaxClaim  = 64
	defaultMaxDigest = 200
)

// NewPostgresStore builds the durable store.
func NewPostgresStore(db DB) (*PostgresStore, error) {
	if db == nil {
		return nil, ErrStoreUnavailable
	}
	return &PostgresStore{db: db, maxClaim: defaultMaxClaim, maxDigest: defaultMaxDigest}, nil
}

func (s *PostgresStore) Enqueue(ctx context.Context, n Notification) (EnqueueResult, error) {
	if s == nil || s.db == nil {
		return EnqueueResult{}, ErrStoreUnavailable
	}
	n, err := n.Normalize(time.Now())
	if err != nil {
		return EnqueueResult{}, err
	}
	// An insert always writes exactly one occurrence, which is what makes the
	// "count moved past what we asked for" test below unambiguous. A caller
	// wanting to record several observations at once calls Touch.
	n.OccurrenceCount = 1
	row := s.db.QueryRowContext(ctx, enqueueSQL,
		string(n.Tier), n.Subject, n.Body, n.Fingerprint,
		nullString(n.IncidentID), nullInt64(n.IncidentReference),
		n.OccurrenceCount, n.Tier.initialStatus(), n.CreatedAt)

	var (
		id    string
		tier  string
		count int64
	)
	if err := row.Scan(&id, &tier, &count); err != nil {
		return EnqueueResult{}, err
	}
	// A merge is identified by the count having moved past the one the insert
	// would have written. This avoids the xmax=0 trick, which reads as folklore
	// and breaks the day the query is rewritten.
	return EnqueueResult{
		ID:              id,
		Created:         count == n.OccurrenceCount,
		Tier:            Tier(tier),
		OccurrenceCount: count,
	}, nil
}

func (s *PostgresStore) EnqueueOnce(ctx context.Context, n Notification) (EnqueueResult, error) {
	if s == nil || s.db == nil {
		return EnqueueResult{}, ErrStoreUnavailable
	}
	n, err := n.Normalize(time.Now())
	if err != nil {
		return EnqueueResult{}, err
	}
	if n.DedupKey == "" {
		return EnqueueResult{}, errors.New("notifications: EnqueueOnce requires a dedup key")
	}
	row := s.db.QueryRowContext(ctx, enqueueOnceSQL,
		string(n.Tier), n.Subject, n.Body, n.Fingerprint, n.DedupKey,
		nullString(n.IncidentID), nullInt64(n.IncidentReference),
		n.OccurrenceCount, n.Tier.initialStatus(), n.CreatedAt)

	var (
		id    string
		tier  string
		count int64
	)
	switch err := row.Scan(&id, &tier, &count); {
	case err == nil:
		return EnqueueResult{ID: id, Created: true, Tier: Tier(tier), OccurrenceCount: count}, nil
	case errors.Is(err, sql.ErrNoRows):
		// DO NOTHING fired: the key already exists. Resolve it so the caller
		// still gets an identity rather than an ambiguous empty result.
		if err := s.db.QueryRowContext(ctx, existingDedupSQL, n.DedupKey).Scan(&id, &tier, &count); err != nil {
			return EnqueueResult{}, err
		}
		return EnqueueResult{ID: id, Created: false, Tier: Tier(tier), OccurrenceCount: count}, nil
	default:
		return EnqueueResult{}, err
	}
}

func (s *PostgresStore) Touch(ctx context.Context, fingerprint string, at time.Time) error {
	if s == nil || s.db == nil {
		return ErrStoreUnavailable
	}
	if fingerprint == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, touchSQL, fingerprint, at)
	return err
}

func (s *PostgresStore) Claim(ctx context.Context, limit int, lease time.Duration, now time.Time) ([]Notification, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreUnavailable
	}
	if limit < 1 {
		return nil, nil
	}
	if limit > s.maxClaim {
		limit = s.maxClaim
	}
	if lease <= 0 {
		lease = defaultClaimLease
	}
	rows, err := s.db.QueryContext(ctx, claimSQL, now, now.Add(lease), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNotifications(rows, limit)
}

func (s *PostgresStore) MarkDelivered(ctx context.Context, id, transport, deliveryErr string, at time.Time) error {
	if s == nil || s.db == nil {
		return ErrStoreUnavailable
	}
	_, err := s.db.ExecContext(ctx, markDeliveredSQL, id, transport, truncateForColumn(deliveryErr), at)
	return err
}

func (s *PostgresStore) MarkRetry(ctx context.Context, id, deliveryErr string, nextAttempt, at time.Time) error {
	if s == nil || s.db == nil {
		return ErrStoreUnavailable
	}
	_, err := s.db.ExecContext(ctx, markRetrySQL, id, truncateForColumn(deliveryErr), nextAttempt, at)
	return err
}

func (s *PostgresStore) MarkTerminal(ctx context.Context, id, status, deliveryErr string, at time.Time) error {
	if s == nil || s.db == nil {
		return ErrStoreUnavailable
	}
	if status != StatusFailed && status != StatusSuppressed {
		return errors.New("notifications: terminal status must be failed or suppressed")
	}
	_, err := s.db.ExecContext(ctx, markTerminalSQL, id, status, truncateForColumn(deliveryErr), at)
	return err
}

func (s *PostgresStore) DigestBacklog(ctx context.Context, since, until time.Time, limit int) ([]Notification, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreUnavailable
	}
	if limit < 1 || limit > s.maxDigest {
		limit = s.maxDigest
	}
	rows, err := s.db.QueryContext(ctx, digestBacklogSQL, since, until, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNotifications(rows, limit)
}

func (s *PostgresStore) MarkDigested(ctx context.Context, ids []string, at time.Time) error {
	if s == nil || s.db == nil {
		return ErrStoreUnavailable
	}
	if len(ids) == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, markDigestedSQL, uuidArray(ids), at)
	return err
}

func scanNotifications(rows Rows, limit int) ([]Notification, error) {
	out := make([]Notification, 0, limit)
	for rows.Next() {
		if len(out) >= limit {
			// Defence in depth against a LIMIT that did not bind. The caller's
			// queue is bounded; this slice must be too.
			break
		}
		var (
			n    Notification
			tier string
		)
		if err := rows.Scan(&n.ID, &tier, &n.Subject, &n.Body, &n.Fingerprint,
			&n.IncidentID, &n.IncidentReference, &n.OccurrenceCount, &n.Attempts, &n.CreatedAt); err != nil {
			return nil, err
		}
		n.Tier = Tier(tier)
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// uuidArray renders a Postgres uuid[] literal. The ids come from this table's
// own id column, but they are still validated: anything that is not a bare
// UUID is dropped rather than interpolated.
func uuidArray(ids []string) string {
	out := make([]byte, 0, len(ids)*38+2)
	out = append(out, '{')
	first := true
	for _, id := range ids {
		if !looksLikeUUID(id) {
			continue
		}
		if !first {
			out = append(out, ',')
		}
		first = false
		out = append(out, id...)
	}
	return string(append(out, '}'))
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
			if !isHex {
				return false
			}
		}
	}
	return true
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt64(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

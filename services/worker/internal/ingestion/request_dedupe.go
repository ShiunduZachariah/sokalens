package ingestion

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"time"
)

// ErrDeduplicated is returned by a provider job when another worker holds an
// active lease for the identical provider request. The processor treats it as
// a retryable error, so the job is deferred (with backoff) instead of dropped:
// once the competing worker's lease expires or is released, a retry performs
// the fetch itself. This prevents two workers from ever sending the same
// provider request concurrently without ever losing work.
var ErrDeduplicated = errors.New("provider request deduplicated by another worker")

type dedupeRowScanner interface {
	Scan(...any) error
}

type dedupeRowQueryer interface {
	QueryRowContext(context.Context, string, ...any) dedupeRowScanner
}

type dedupeSQLRowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type dedupeExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// RequestDedupe is a cross-worker, database-backed lease for provider requests.
// Two workers that would issue the identical request (same provider, endpoint,
// and canonicalized params) cannot both hold the lease: the second is told to
// defer. The lease is released as soon as the provider call completes (success
// or failure), so sequential retries of the same job are never blocked; the
// TTL is only a crash-safety net.
type RequestDedupe struct {
	db  any
	ttl time.Duration
}

func NewRequestDedupe(db any, ttl time.Duration) *RequestDedupe {
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	return &RequestDedupe{db: db, ttl: ttl}
}

// Claim attempts to claim the lease for fingerprint. It returns (true, nil)
// when this worker should proceed, (false, nil) when another worker holds an
// active lease (caller should defer with ErrDeduplicated), and an error when
// the lease store is unavailable.
func (d *RequestDedupe) Claim(ctx context.Context, fingerprint, workerID string) (bool, error) {
	if d == nil || d.db == nil {
		return true, nil
	}
	var row dedupeRowScanner
	switch db := d.db.(type) {
	case dedupeRowQueryer:
		row = db.QueryRowContext(ctx, claimLeaseSQL, fingerprint, int(d.ttl.Seconds()), workerID)
	case dedupeSQLRowQueryer:
		row = db.QueryRowContext(ctx, claimLeaseSQL, fingerprint, int(d.ttl.Seconds()), workerID)
	default:
		return false, errors.New("request dedupe DB does not support QueryRowContext")
	}
	var owner string
	err := row.Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		// Conflict row exists and its lease has not expired: another worker is
		// fetching this exact request right now.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Release clears the lease owned by workerID so a deferred job can retry.
// Best-effort: a failed release only expires via the TTL.
func (d *RequestDedupe) Release(ctx context.Context, fingerprint, workerID string) error {
	if d == nil || d.db == nil {
		return nil
	}
	db, ok := d.db.(dedupeExecer)
	if !ok {
		return errors.New("request dedupe DB does not support ExecContext")
	}
	_, err := db.ExecContext(ctx, `
DELETE FROM provider_request_dedupe
WHERE fingerprint = $1 AND worker_id = $2`, fingerprint, workerID)
	return err
}

const claimLeaseSQL = `
INSERT INTO provider_request_dedupe (fingerprint, claimed_at, expires_at, worker_id)
VALUES ($1, now(), now() + ($2 * interval '1 second'), $3)
ON CONFLICT (fingerprint) DO UPDATE
SET claimed_at = now(),
    expires_at = now() + ($2 * interval '1 second'),
    worker_id = EXCLUDED.worker_id
WHERE provider_request_dedupe.expires_at < now()
RETURNING worker_id`

// RequestFingerprint derives a canonical, collision-safe identity for a
// provider request: provider + endpoint + canonicalized params.
func RequestFingerprint(provider, endpoint string, params url.Values) string {
	digest := sha256.Sum256([]byte(hashParams(params)))
	return provider + ":" + endpoint + ":" + hex.EncodeToString(digest[:])
}

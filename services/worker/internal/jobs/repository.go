package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const LeaseDuration = 15 * time.Minute

var ErrLeaseLost = errors.New("job lease lost")

type scanner interface {
	Scan(...any) error
}

type repositoryDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type scannerQueryer interface {
	QueryRowContext(context.Context, string, ...any) scanner
}

type sqlRowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Repository struct {
	db           any
	maxAttempts  int
	leaseSeconds int
}

type NewJob struct {
	JobKey               string
	Queue                string
	Priority             int
	Payload              []byte
	IdempotencyKey       string
	DeduplicateCompleted bool
	RunAfter             time.Time
}

type Job struct {
	ID             int64
	JobKey         string
	Queue          string
	Payload        []byte
	Attempts       int
	MaxAttempts    int
	IdempotencyKey string
}

func NewRepository(db any) Repository {
	return NewRepositoryWithDefaults(db, 0, 0)
}

// NewRepositoryWithDefaults allows operators to override the per-job retry
// budget and lease duration. Zero values keep the legacy defaults.
func NewRepositoryWithDefaults(db any, maxAttempts int, leaseDuration time.Duration) Repository {
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	if leaseDuration <= 0 {
		leaseDuration = LeaseDuration
	}
	return Repository{db: db, maxAttempts: maxAttempts, leaseSeconds: int(leaseDuration.Seconds())}
}

func (r Repository) Enqueue(ctx context.Context, job NewJob) (int64, error) {
	if r.db == nil {
		return 0, errors.New("jobs DB is nil")
	}
	if job.JobKey == "" {
		return 0, errors.New("job key is required")
	}
	if job.Queue == "" {
		job.Queue = "default"
	}
	if job.Priority == 0 {
		job.Priority = 100
	}
	if len(job.Payload) == 0 || !json.Valid(job.Payload) {
		job.Payload = []byte(`{}`)
	}
	if job.RunAfter.IsZero() {
		job.RunAfter = time.Now().UTC()
	}

	var id int64
	row, err := r.queryRow(ctx, `
WITH existing AS (
  SELECT id
  FROM jobs
  WHERE $7::boolean
    AND $6::text IS NOT NULL
    AND idempotency_key = $6
  ORDER BY id DESC
  LIMIT 1
), inserted AS (
INSERT INTO jobs (
  job_key,
  status,
  payload,
  queue,
  priority,
  run_after,
  idempotency_key,
  max_attempts
) SELECT $1, 'queued', $2::jsonb, $3, $4, $5, $6, $8
WHERE NOT $7::boolean
   OR $6::text IS NULL
   OR NOT EXISTS (SELECT 1 FROM existing)
ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL AND status IN ('queued', 'running')
DO UPDATE SET updated_at = jobs.updated_at
RETURNING id
)
SELECT id FROM inserted
UNION ALL
SELECT id FROM existing
LIMIT 1`,
		job.JobKey,
		string(job.Payload),
		job.Queue,
		job.Priority,
		job.RunAfter,
		nullableString(job.IdempotencyKey),
		job.DeduplicateCompleted,
		r.maxAttempts,
	)
	if err != nil {
		return 0, err
	}
	err = row.Scan(&id)
	return id, err
}

func (r Repository) ClaimNext(ctx context.Context, queue, workerID string, now time.Time) (Job, bool, error) {
	if r.db == nil {
		return Job{}, false, errors.New("jobs DB is nil")
	}
	if queue == "" {
		queue = "default"
	}
	if workerID == "" {
		return Job{}, false, errors.New("worker ID is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	db, err := r.execDB()
	if err != nil {
		return Job{}, false, err
	}
	if _, err := db.ExecContext(ctx, `
UPDATE jobs
SET
  status = 'failed',
  locked_by = NULL,
  locked_at = NULL,
  last_error = COALESCE(last_error, 'job lease expired after final attempt'),
  updated_at = $1
WHERE status = 'running'
  AND locked_at <= $1::timestamptz - ($2 * interval '1 second')
  AND attempts >= max_attempts`, now, r.leaseSeconds); err != nil {
		return Job{}, false, err
	}

	var job Job
	row, err := r.queryRow(ctx, `
UPDATE jobs
SET
  status = 'running',
  locked_by = $2,
  locked_at = $3::timestamptz,
  attempts = attempts + 1,
  updated_at = $3
WHERE id = (
  SELECT id
  FROM jobs
  WHERE queue = $1
    AND attempts < max_attempts
    AND (
      (status = 'queued' AND run_after <= $3::timestamptz AND locked_at IS NULL)
      OR (status = 'running' AND locked_at <= $3::timestamptz - ($4 * interval '1 second'))
    )
  ORDER BY priority ASC, run_after ASC, id ASC
  FOR UPDATE SKIP LOCKED
  LIMIT 1
)
RETURNING id, job_key, queue, payload, attempts, max_attempts, COALESCE(idempotency_key, '')`,
		queue,
		workerID,
		now,
		r.leaseSeconds,
	)
	if err != nil {
		return Job{}, false, err
	}
	err = row.Scan(&job.ID, &job.JobKey, &job.Queue, &job.Payload, &job.Attempts, &job.MaxAttempts, &job.IdempotencyKey)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	return job, true, nil
}

func (r Repository) RenewLease(ctx context.Context, id int64, workerID string, now time.Time) error {
	if workerID == "" {
		return errors.New("worker ID is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	db, err := r.execDB()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `
UPDATE jobs
SET locked_at = $3,
    updated_at = $3
WHERE id = $1
  AND status = 'running'
  AND locked_by = $2`, id, workerID, now)
	if err != nil {
		return err
	}
	return requireOwnedUpdate(result)
}

func (r Repository) Complete(ctx context.Context, id int64, workerID string, now time.Time) error {
	if workerID == "" {
		return errors.New("worker ID is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	db, err := r.execDB()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `
UPDATE jobs
SET
  status = 'completed',
  completed_at = $3,
  locked_by = NULL,
  locked_at = NULL,
  last_error = NULL,
  updated_at = $3
WHERE id = $1
  AND status = 'running'
  AND locked_by = $2`, id, workerID, now)
	if err != nil {
		return err
	}
	return requireOwnedUpdate(result)
}

func (r Repository) Retry(ctx context.Context, id int64, workerID, reason string, runAfter time.Time) error {
	if workerID == "" {
		return errors.New("worker ID is required")
	}
	if runAfter.IsZero() {
		runAfter = time.Now().UTC()
	}
	db, err := r.execDB()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `
UPDATE jobs
SET
  status = 'queued',
  locked_by = NULL,
  locked_at = NULL,
  run_after = $4,
  last_error = $3,
  updated_at = $4
WHERE id = $1
  AND status = 'running'
  AND locked_by = $2`, id, workerID, reason, runAfter)
	if err != nil {
		return err
	}
	return requireOwnedUpdate(result)
}

// Defer releases a job until provider capacity is available without spending
// one of its retry attempts. ClaimNext incremented attempts when it acquired
// the lease, so this transition restores that increment while retaining the
// reason and exact capacity deadline for operators.
func (r Repository) Defer(ctx context.Context, id int64, workerID, reason string, runAfter time.Time) error {
	if workerID == "" {
		return errors.New("worker ID is required")
	}
	if runAfter.IsZero() {
		runAfter = time.Now().UTC()
	}
	db, err := r.execDB()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `
UPDATE jobs
SET
  status = 'queued',
  attempts = GREATEST(attempts - 1, 0),
  locked_by = NULL,
  locked_at = NULL,
  run_after = $4,
  last_error = $3,
  updated_at = $4
WHERE id = $1
  AND status = 'running'
  AND locked_by = $2`, id, workerID, reason, runAfter)
	if err != nil {
		return err
	}
	return requireOwnedUpdate(result)
}

func (r Repository) Fail(ctx context.Context, id int64, workerID, reason string, now time.Time) error {
	if workerID == "" {
		return errors.New("worker ID is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	db, err := r.execDB()
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `
UPDATE jobs
SET
  status = 'failed',
  locked_by = NULL,
  locked_at = NULL,
  last_error = $3,
  updated_at = $4
WHERE id = $1
  AND status = 'running'
  AND locked_by = $2`, id, workerID, reason, now)
	if err != nil {
		return err
	}
	return requireOwnedUpdate(result)
}

func ShouldRetry(job Job) bool {
	return job.Attempts < job.MaxAttempts
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (r Repository) queryRow(ctx context.Context, query string, args ...any) (scanner, error) {
	switch db := r.db.(type) {
	case scannerQueryer:
		return db.QueryRowContext(ctx, query, args...), nil
	case sqlRowQueryer:
		return db.QueryRowContext(ctx, query, args...), nil
	default:
		return nil, errors.New("jobs DB does not support QueryRowContext")
	}
}

func (r Repository) execDB() (repositoryDB, error) {
	db, ok := r.db.(repositoryDB)
	if !ok {
		return nil, errors.New("jobs DB does not support ExecContext")
	}
	return db, nil
}

func requireOwnedUpdate(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrLeaseLost
	}
	return nil
}

package jobs

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"time"
)

type DB interface {
	BeginTx(context.Context, *sql.TxOptions) (Tx, error)
}

type Tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) Row
	Commit() error
	Rollback() error
}

type Row interface{ Scan(...any) error }

type SQLDB struct{ DB *sql.DB }

func (d SQLDB) BeginTx(ctx context.Context, options *sql.TxOptions) (Tx, error) {
	if d.DB == nil {
		return nil, ErrUnavailable
	}
	tx, err := d.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return sqlTx{Tx: tx}, nil
}

type sqlTx struct{ Tx *sql.Tx }

func (t sqlTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, query, args...)
}
func (t sqlTx) QueryRowContext(ctx context.Context, query string, args ...any) Row {
	return t.Tx.QueryRowContext(ctx, query, args...)
}
func (t sqlTx) Commit() error   { return t.Tx.Commit() }
func (t sqlTx) Rollback() error { return t.Tx.Rollback() }

type Repository struct {
	db          DB
	accelerator Accelerator
	config      Config
}

func NewRepository(db DB, accelerator Accelerator, config Config) (*Repository, error) {
	if db == nil || config.Now == nil || config.MinLease <= 0 || config.MaxLease < config.MinLease ||
		config.InitialBackoff <= 0 || config.MaxBackoff < config.InitialBackoff || config.MaxPayloadBytes <= 0 {
		return nil, ErrUnavailable
	}
	return &Repository{db: db, accelerator: accelerator, config: config}, nil
}

func (r *Repository) Enqueue(ctx context.Context, request EnqueueRequest) (Job, error) {
	specifiedAvailability := !request.AvailableAt.IsZero()
	request.Queue = strings.ToLower(strings.TrimSpace(request.Queue))
	request.Type = strings.ToLower(strings.TrimSpace(request.Type))
	request.IncidentID = strings.ToLower(strings.TrimSpace(request.IncidentID))
	request.IdempotencyKey = strings.ToLower(strings.TrimSpace(request.IdempotencyKey))
	if !specifiedAvailability {
		request.AvailableAt = r.now()
	} else {
		request.AvailableAt = request.AvailableAt.UTC()
	}
	payload, err := normalizePayload(request.Payload, r.config.MaxPayloadBytes)
	if err != nil {
		return Job{}, err
	}
	request.Payload = payload
	if err = validateEnqueue(request); err != nil {
		return Job{}, err
	}

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Job{}, unavailable(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, lockIdempotencySQL, request.IdempotencyKey); err != nil {
		return Job{}, unavailable(err)
	}
	stored, err := scanJob(tx.QueryRowContext(ctx, findByKeySQL, request.IdempotencyKey))
	if err == nil {
		if !sameEnqueue(stored, request, specifiedAvailability) {
			return Job{}, ErrIdempotencyConflict
		}
		if err = tx.Commit(); err != nil {
			return Job{}, unavailable(err)
		}
		committed = true
		return stored, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Job{}, unavailable(err)
	}
	createdAt := r.now()
	created, err := scanJob(tx.QueryRowContext(ctx, insertJobSQL,
		request.IncidentID, request.Queue, request.Type, request.IdempotencyKey,
		request.Priority, []byte(request.Payload), request.AvailableAt, request.MaxAttempts, createdAt))
	if err != nil {
		return Job{}, unavailable(err)
	}
	if err = tx.Commit(); err != nil {
		return Job{}, unavailable(err)
	}
	committed = true
	r.notify(ctx, created.Queue, created.AvailableAt)
	return created, nil
}

func (r *Repository) Claim(ctx context.Context, request ClaimRequest) (Job, error) {
	request.Queue = strings.ToLower(strings.TrimSpace(request.Queue))
	request.Worker = strings.TrimSpace(request.Worker)
	if !validName(request.Queue) || !validWorker(request.Worker) || !r.validLease(request.Lease) {
		return Job{}, ErrInvalidRequest
	}
	token, err := randomUUID()
	if err != nil {
		return Job{}, unavailable(err)
	}
	now := r.now()
	return r.queryMutation(ctx, claimJobSQL, request.Queue, now, request.Worker, token, now.Add(request.Lease))
}

func (r *Repository) Renew(ctx context.Context, request RenewRequest) (Job, error) {
	request.LeaseRequest = normalizeLeaseRequest(request.LeaseRequest)
	if !validLeaseRequest(request.LeaseRequest) || !r.validLease(request.Lease) {
		return Job{}, ErrInvalidRequest
	}
	now := r.now()
	return r.leaseMutation(ctx, renewJobSQL, request.JobID, request.Worker, request.LeaseToken, now, now.Add(request.Lease))
}

func (r *Repository) Complete(ctx context.Context, request LeaseRequest) (Job, error) {
	request = normalizeLeaseRequest(request)
	if !validLeaseRequest(request) {
		return Job{}, ErrInvalidRequest
	}
	return r.leaseMutation(ctx, completeJobSQL, request.JobID, request.Worker, request.LeaseToken, r.now())
}

func (r *Repository) Fail(ctx context.Context, failure Failure) (Job, error) {
	failure.LeaseRequest = normalizeLeaseRequest(failure.LeaseRequest)
	failure.Code = strings.ToLower(strings.TrimSpace(failure.Code))
	failure.Message = strings.TrimSpace(failure.Message)
	if !validLeaseRequest(failure.LeaseRequest) || !validFailure(failure) {
		return Job{}, ErrInvalidRequest
	}
	now := r.now()
	return r.leaseMutation(ctx, failJobSQL, failure.JobID, failure.Worker, failure.LeaseToken,
		now, failure.Class, failure.Code, failure.Message,
		r.config.InitialBackoff.Seconds(), r.config.MaxBackoff.Seconds())
}

// ReclaimExpired makes abandoned leases visible again in bounded batches. A
// lease that consumed its final attempt is dead-lettered instead of retried.
func (r *Repository) ReclaimExpired(ctx context.Context, limit int) (int64, error) {
	if limit < 1 || limit > 1000 {
		return 0, ErrInvalidRequest
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, unavailable(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	result, err := tx.ExecContext(ctx, reclaimExpiredSQL, r.now(), limit)
	if err != nil {
		return 0, unavailable(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, unavailable(err)
	}
	if err = tx.Commit(); err != nil {
		return 0, unavailable(err)
	}
	committed = true
	return count, nil
}

func (r *Repository) queryMutation(ctx context.Context, query string, args ...any) (Job, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Job{}, unavailable(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	job, err := scanJob(tx.QueryRowContext(ctx, query, args...))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, unavailable(err)
	}
	if err = tx.Commit(); err != nil {
		return Job{}, unavailable(err)
	}
	committed = true
	return job, nil
}

func (r *Repository) leaseMutation(ctx context.Context, query string, args ...any) (Job, error) {
	job, err := r.queryMutation(ctx, query, args...)
	if errors.Is(err, ErrNotFound) {
		return Job{}, ErrLeaseLost
	}
	return job, err
}

func (r *Repository) notify(ctx context.Context, queue string, availableAt time.Time) {
	if r.accelerator != nil {
		// A failed hint cannot roll back an already committed authoritative row.
		_ = r.accelerator.NotifyAvailable(ctx, AvailabilityHint{Queue: queue, AvailableAt: availableAt})
	}
}

func (r *Repository) now() time.Time { return r.config.Now().UTC() }
func (r *Repository) validLease(value time.Duration) bool {
	return value >= r.config.MinLease && value <= r.config.MaxLease
}

func unavailable(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func validName(value string) bool { return namePattern.MatchString(value) }
func validWorker(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 128
}
func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}
func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func validateEnqueue(request EnqueueRequest) error {
	if !validName(request.Queue) || !validName(request.Type) || !validDigest(request.IdempotencyKey) ||
		(request.IncidentID != "" && !validUUID(request.IncidentID)) || request.Priority < 0 || request.Priority > 1000 ||
		request.MaxAttempts < 1 || request.MaxAttempts > 100 || request.AvailableAt.IsZero() {
		return ErrInvalidRequest
	}
	return nil
}

func validLeaseRequest(request LeaseRequest) bool {
	return validUUID(request.JobID) && validUUID(request.LeaseToken) && validWorker(request.Worker)
}

func normalizeLeaseRequest(request LeaseRequest) LeaseRequest {
	request.JobID = strings.ToLower(strings.TrimSpace(request.JobID))
	request.LeaseToken = strings.ToLower(strings.TrimSpace(request.LeaseToken))
	request.Worker = strings.TrimSpace(request.Worker)
	return request
}

func validFailure(failure Failure) bool {
	if failure.Class != FailureRetryable && failure.Class != FailurePermanent {
		return false
	}
	return validName(failure.Code) && failure.Message != "" && len(failure.Message) <= 2048
}

func normalizePayload(payload json.RawMessage, maximum int) (json.RawMessage, error) {
	if len(payload) == 0 || len(payload) > maximum {
		return nil, ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, ErrInvalidRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidRequest
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, ErrInvalidRequest
	}
	canonical, err := json.Marshal(value)
	if err != nil || len(canonical) > maximum {
		return nil, ErrInvalidRequest
	}
	return canonical, nil
}

func sameEnqueue(job Job, request EnqueueRequest, compareAvailability bool) bool {
	if job.IncidentID != request.IncidentID || job.Queue != request.Queue || job.Type != request.Type ||
		job.Priority != request.Priority || job.MaxAttempts != request.MaxAttempts || !jsonEqual(job.Payload, request.Payload) {
		return false
	}
	return !compareAvailability || job.AvailableAt.Equal(request.AvailableAt)
}

func jsonEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	rightDecoder.UseNumber()
	return leftDecoder.Decode(&leftValue) == nil && rightDecoder.Decode(&rightValue) == nil &&
		reflect.DeepEqual(leftValue, rightValue)
}

func randomUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func scanJob(row Row) (Job, error) {
	var job Job
	var payload []byte
	var leaseExpires, started, completed, deadLettered sql.NullTime
	err := row.Scan(
		&job.ID, &job.IncidentID, &job.Queue, &job.Type, &job.IdempotencyKey,
		&job.Priority, &payload, &job.Status, &job.AvailableAt, &job.Attempts, &job.MaxAttempts,
		&job.LeaseOwner, &job.LeaseToken, &leaseExpires,
		&job.LastErrorClass, &job.LastErrorCode, &job.LastErrorMessage,
		&job.CreatedAt, &started, &completed, &deadLettered, &job.UpdatedAt,
	)
	if err != nil {
		return Job{}, err
	}
	job.Payload = append(json.RawMessage(nil), payload...)
	job.LeaseExpiresAt = leaseExpires.Time
	job.StartedAt = started.Time
	job.CompletedAt = completed.Time
	job.DeadLetteredAt = deadLettered.Time
	return job, nil
}

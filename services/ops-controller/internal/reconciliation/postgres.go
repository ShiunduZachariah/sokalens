package reconciliation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

type DB interface {
	BeginTx(context.Context, *sql.TxOptions) (Tx, error)
}
type Tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (Rows, error)
	QueryRowContext(context.Context, string, ...any) Row
	Commit() error
	Rollback() error
}
type Row interface{ Scan(...any) error }
type Rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

type SQLDB struct{ DB *sql.DB }

func (database SQLDB) BeginTx(ctx context.Context, options *sql.TxOptions) (Tx, error) {
	if database.DB == nil {
		return nil, ErrRepositoryUnavailable
	}
	tx, err := database.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return sqlTx{Tx: tx}, nil
}

type sqlTx struct{ Tx *sql.Tx }

func (transaction sqlTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return transaction.Tx.ExecContext(ctx, query, args...)
}
func (transaction sqlTx) QueryRowContext(ctx context.Context, query string, args ...any) Row {
	return transaction.Tx.QueryRowContext(ctx, query, args...)
}
func (transaction sqlTx) QueryContext(ctx context.Context, query string, args ...any) (Rows, error) {
	return transaction.Tx.QueryContext(ctx, query, args...)
}
func (transaction sqlTx) Commit() error   { return transaction.Tx.Commit() }
func (transaction sqlTx) Rollback() error { return transaction.Tx.Rollback() }

type PostgresRepository struct{ db DB }

func NewPostgresRepository(db DB) (*PostgresRepository, error) {
	if db == nil {
		return nil, ErrRepositoryUnavailable
	}
	return &PostgresRepository{db: db}, nil
}

var ErrIdempotencyConflict = errors.New("reconciliation: idempotency conflict")

func (repository *PostgresRepository) Apply(ctx context.Context, run Run) (Run, error) {
	inputHash, err := hashRun(run)
	if err != nil {
		return Run{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	tx, err := repository.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Run{}, fmt.Errorf("%w: %v", ErrRepositoryUnavailable, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('entitlement-reconciliation:' || $1, 0))`, run.IdempotencyKey); err != nil {
		return Run{}, repositoryError(err)
	}
	var existingID int64
	var existingHash string
	err = tx.QueryRowContext(ctx, `SELECT id, input_hash FROM entitlement_reconciliation_runs WHERE idempotency_key = $1`, run.IdempotencyKey).Scan(&existingID, &existingHash)
	if err == nil {
		if existingHash != inputHash {
			return Run{}, ErrIdempotencyConflict
		}
		stored, loadErr := loadRun(ctx, tx, existingID)
		if loadErr != nil {
			return Run{}, repositoryError(loadErr)
		}
		if err = tx.Commit(); err != nil {
			return Run{}, repositoryError(err)
		}
		committed = true
		stored.Applied = false
		return stored, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Run{}, repositoryError(err)
	}
	var runID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO entitlement_reconciliation_runs
  (idempotency_key,input_hash,provider,environment,started_at,completed_at,result,item_count,matched_count,mismatch_count,failed_count,correction_count)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
		run.IdempotencyKey, inputHash, run.Provider, run.Environment, run.StartedAt, run.CompletedAt, run.Result, len(run.Items), run.MatchedCount, run.MismatchCount, run.FailedCount, run.CorrectionCount).Scan(&runID)
	if err != nil {
		return Run{}, repositoryError(err)
	}
	for _, item := range run.Items {
		if err = repository.applyItem(ctx, tx, runID, run, item); err != nil {
			return Run{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Run{}, repositoryError(err)
	}
	committed = true
	run.Applied = true
	return run, nil
}

func (repository *PostgresRepository) applyItem(ctx context.Context, tx Tx, runID int64, run Run, item Item) error {
	details, _ := json.Marshal(map[string]string{"failure_code": item.FailureCode})
	var subscriptionID any
	if item.SubscriptionID > 0 {
		subscriptionID = item.SubscriptionID
	}
	var observed any
	if item.ProviderObservedAt != nil {
		observed = *item.ProviderObservedAt
	}
	var itemID int64
	err := tx.QueryRowContext(ctx, `INSERT INTO entitlement_reconciliation_items
  (run_id,subscription_id,provider_subscription_id,classification,result,internal_revision,provider_revision,provider_observed_at,details)
VALUES ($1,$2,NULLIF($3,''),$4,$5,NULLIF($6,''),NULLIF($7,''),$8,$9) RETURNING id`,
		runID, subscriptionID, item.ProviderSubscriptionID, item.Classification, item.Result, item.InternalRevision, item.ProviderRevision, observed, details).Scan(&itemID)
	if err != nil {
		return repositoryError(err)
	}
	for _, correction := range item.Corrections {
		if err = repository.applyCorrection(ctx, tx, runID, itemID, run, item, correction); err != nil {
			return err
		}
	}
	return nil
}

func (repository *PostgresRepository) applyCorrection(ctx context.Context, tx Tx, runID, itemID int64, run Run, item Item, correction Correction) error {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('entitlement-correction:' || $1, 0))`, correction.IdempotencyKey); err != nil {
		return repositoryError(err)
	}
	var ignored string
	err := tx.QueryRowContext(ctx, `SELECT idempotency_key FROM entitlement_reconciliation_effects WHERE idempotency_key=$1`, correction.IdempotencyKey).Scan(&ignored)
	applied := errors.Is(err, sql.ErrNoRows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return repositoryError(err)
	}
	payload, _ := json.Marshal(correction)
	var subscriptionID, grantID any
	if correction.SubscriptionID > 0 {
		subscriptionID = correction.SubscriptionID
	}
	if correction.GrantID > 0 {
		grantID = correction.GrantID
	}
	var correctionID int64
	err = tx.QueryRowContext(ctx, `INSERT INTO entitlement_reconciliation_corrections
  (run_id,item_id,idempotency_key,subscription_id,grant_id,action,target_status,effective_at,provider_revision,applied,details)
VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,NULLIF($9,''),$10,$11) RETURNING id`, runID, itemID, correction.IdempotencyKey, subscriptionID, grantID, correction.Action, correction.TargetStatus, correction.EffectiveAt, correction.ProviderRevision, applied, payload).Scan(&correctionID)
	if err != nil {
		return repositoryError(err)
	}
	if !applied {
		return nil
	}
	if err = applyEffect(ctx, tx, run, item, correction, correctionID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO entitlement_reconciliation_effects (idempotency_key,first_correction_id,applied_at) VALUES ($1,$2,$3)`, correction.IdempotencyKey, correctionID, correction.EffectiveAt)
	return repositoryError(err)
}

func applyEffect(ctx context.Context, tx Tx, run Run, item Item, correction Correction, correctionID int64) error {
	var result sql.Result
	var err error
	switch correction.Action {
	case FreezeNewGrants:
		var subscriptionID any
		if correction.SubscriptionID > 0 {
			subscriptionID = correction.SubscriptionID
		}
		result, err = tx.ExecContext(ctx, `INSERT INTO entitlement_grant_freezes
  (provider,environment,subscription_id,active,reason,last_correction_id,frozen_at)
VALUES ($1,$2,$3,true,'provider_reconciliation_failure',$4,$5)
ON CONFLICT (provider,environment,subscription_id) DO UPDATE
SET active=true,reason=EXCLUDED.reason,last_correction_id=EXCLUDED.last_correction_id,frozen_at=EXCLUDED.frozen_at,released_at=NULL`, run.Provider, run.Environment, subscriptionID, correctionID, correction.EffectiveAt)
	case UnfreezeNewGrants:
		result, err = tx.ExecContext(ctx, `UPDATE entitlement_grant_freezes SET active=false,released_at=$4,last_correction_id=$5
WHERE provider=$1 AND environment=$2 AND subscription_id=$3 AND active=true`, run.Provider, run.Environment, correction.SubscriptionID, correction.EffectiveAt, correctionID)
	case CorrectSubscription:
		result, err = tx.ExecContext(ctx, `UPDATE subscriptions SET status=$4,current_period_start=$5,current_period_end=$6,grace_ends_at=$7,cancel_at_period_end=$8,provider_revision=$9,provider_event_time=$10,updated_at=$10
WHERE id=$1 AND provider=$2 AND environment=$3 AND provider_subscription_id=$11`, correction.SubscriptionID, run.Provider, run.Environment, correction.TargetStatus, correction.PeriodStart, correction.PeriodEnd, correction.GraceEndsAt, correction.CancelAtPeriodEnd, correction.ProviderRevision, correction.EffectiveAt, item.ProviderSubscriptionID)
	case VerifyGrant:
		result, err = tx.ExecContext(ctx, `UPDATE user_entitlement_grants SET reconciliation_verified_at=$3,reconciliation_revision=$4
WHERE id=$1 AND source_type='subscription' AND source_id=$2::text AND revoked_at IS NULL`, correction.GrantID, correction.SubscriptionID, correction.EffectiveAt, correction.ProviderRevision)
	case RevokeGrant:
		result, err = tx.ExecContext(ctx, `UPDATE user_entitlement_grants SET revoked_at=$3,reconciliation_verified_at=$3,reconciliation_revision=$4
WHERE id=$1 AND source_type='subscription' AND source_id=$2::text AND revoked_at IS NULL`, correction.GrantID, correction.SubscriptionID, correction.EffectiveAt, correction.ProviderRevision)
	default:
		return ErrInvalidRequest
	}
	if err != nil {
		return repositoryError(err)
	}
	if correction.Action == CorrectSubscription || correction.Action == VerifyGrant || correction.Action == RevokeGrant {
		affected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return repositoryError(rowsErr)
		}
		if affected != 1 {
			return ErrIdempotencyConflict
		}
	}
	return nil
}

func hashRun(run Run) (string, error) {
	copy := run
	copy.Applied = false
	encoded, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}
func repositoryError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrRepositoryUnavailable, err)
}

func loadRun(ctx context.Context, tx Tx, id int64) (Run, error) {
	var run Run
	var itemCount int
	err := tx.QueryRowContext(ctx, `SELECT idempotency_key,provider,environment,started_at,completed_at,result,item_count,matched_count,mismatch_count,failed_count,correction_count FROM entitlement_reconciliation_runs WHERE id=$1`, id).Scan(&run.IdempotencyKey, &run.Provider, &run.Environment, &run.StartedAt, &run.CompletedAt, &run.Result, &itemCount, &run.MatchedCount, &run.MismatchCount, &run.FailedCount, &run.CorrectionCount)
	if err != nil {
		return Run{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,COALESCE(subscription_id,0),COALESCE(provider_subscription_id,''),classification,result,COALESCE(internal_revision,''),COALESCE(provider_revision,''),provider_observed_at,details FROM entitlement_reconciliation_items WHERE run_id=$1 ORDER BY id`, id)
	if err != nil {
		return Run{}, err
	}
	defer rows.Close()
	itemIDs := make([]int64, 0, itemCount)
	for rows.Next() {
		var itemID int64
		var observed sql.NullTime
		var details []byte
		var item Item
		if err = rows.Scan(&itemID, &item.SubscriptionID, &item.ProviderSubscriptionID, &item.Classification, &item.Result, &item.InternalRevision, &item.ProviderRevision, &observed, &details); err != nil {
			return Run{}, err
		}
		if observed.Valid {
			item.ProviderObservedAt = &observed.Time
		}
		var metadata map[string]string
		_ = json.Unmarshal(details, &metadata)
		item.FailureCode = metadata["failure_code"]
		run.Items = append(run.Items, item)
		itemIDs = append(itemIDs, itemID)
	}
	if err = rows.Err(); err != nil {
		return Run{}, err
	}
	for index, itemID := range itemIDs {
		correctionRows, queryErr := tx.QueryContext(ctx, `SELECT details FROM entitlement_reconciliation_corrections WHERE item_id=$1 ORDER BY id`, itemID)
		if queryErr != nil {
			return Run{}, queryErr
		}
		for correctionRows.Next() {
			var payload []byte
			var correction Correction
			if err = correctionRows.Scan(&payload); err != nil {
				correctionRows.Close()
				return Run{}, err
			}
			if err = json.Unmarshal(payload, &correction); err != nil {
				correctionRows.Close()
				return Run{}, err
			}
			run.Items[index].Corrections = append(run.Items[index].Corrections, correction)
		}
		if err = correctionRows.Err(); err != nil {
			correctionRows.Close()
			return Run{}, err
		}
		correctionRows.Close()
	}
	return run, nil
}

// Package durablebudget provides a restart-durable, controller-safe budget
// boundary backed by PostgreSQL. It satisfies packages/ai-contract's Budget and
// ReservationHandle interfaces so the AI workflow can keep conservative
// worst-case reservations, settle actual usage, and retain the worst-case
// charge on failure, all without double-spend across concurrent controllers.
package durablebudget

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	aicontract "github.com/ShiunduZachariah/sports-prediction-platform/packages/ai-contract"
)

var (
	ErrRepositoryUnavailable = errors.New("AI budget repository unavailable")
	ErrReservationNotFound   = errors.New("AI budget reservation not found")
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

// SQLDB adapts *sql.DB to the narrow DB interface used by tests.
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
func (transaction sqlTx) Commit() error   { return transaction.Tx.Commit() }
func (transaction sqlTx) Rollback() error { return transaction.Tx.Rollback() }

type Repository struct {
	db    DB
	now   func() time.Time
	newID func() string
}

func NewRepository(db DB) (*Repository, error) {
	if db == nil {
		return nil, ErrRepositoryUnavailable
	}
	return &Repository{
		db:    db,
		now:   time.Now,
		newID: newReservationID,
	}, nil
}

func newReservationID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("reservation-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

const (
	lockGlobalSQL     = `SELECT global_active, normal_active FROM ops_ai_budget_global WHERE id = TRUE FOR UPDATE`
	upsertUsageSQL    = `INSERT INTO ops_ai_budget_usage (incident_id) VALUES ($1) ON CONFLICT (incident_id) DO NOTHING`
	lockUsageSQL      = `SELECT attempts, tokens, cost_micros, active, normal_active FROM ops_ai_budget_usage WHERE incident_id = $1 FOR UPDATE`
	updateUsageSQL    = `UPDATE ops_ai_budget_usage SET attempts = $2, tokens = $3, cost_micros = $4, active = $5, normal_active = $6, updated_at = now() WHERE incident_id = $1`
	updateGlobalSQL   = `UPDATE ops_ai_budget_global SET global_active = $1, normal_active = $2, updated_at = now() WHERE id = TRUE`
	insertReserveSQL  = `INSERT INTO ops_ai_budget_reservations (reservation_id, incident_id, priority, reserved_attempts, reserved_tokens, reserved_cost_micros, status, lease_until) VALUES ($1, $2, $3, $4, $5, $6, 'active', $7)`
	lockReserveSQL    = `SELECT incident_id, priority, reserved_attempts, reserved_tokens, reserved_cost_micros, status FROM ops_ai_budget_reservations WHERE reservation_id = $1 FOR UPDATE`
	markReserveSQL    = `UPDATE ops_ai_budget_reservations SET status = $2, updated_at = now() WHERE reservation_id = $1`
	selectUsageSQL    = `SELECT attempts, tokens, cost_micros, active FROM ops_ai_budget_usage WHERE incident_id = $1`
	selectExpiredSQL  = `SELECT reservation_id, incident_id, priority FROM ops_ai_budget_reservations WHERE status = 'active' AND lease_until < $1 ORDER BY lease_until LIMIT 1 FOR UPDATE SKIP LOCKED`
)

// Reserve atomically charges the conservative worst-case envelope and returns a
// reservation id. It fails closed with the ai-contract sentinel errors when the
// policy is exhausted or concurrency is full.
func (r *Repository) Reserve(ctx context.Context, policy aicontract.BudgetPolicy, incidentID string, priority aicontract.Priority, attempts int, tokens, costMicros int64, lease time.Duration) (string, error) {
	if r == nil {
		return "", ErrRepositoryUnavailable
	}
	if incidentID == "" || attempts <= 0 || tokens <= 0 || costMicros < 0 {
		return "", errors.New("reserved attempts and tokens must be positive and cost non-negative")
	}
	normal := priority == aicontract.PriorityNormal
	if !normal && priority != aicontract.PriorityCritical {
		return "", fmt.Errorf("invalid priority %q", priority)
	}

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var globalActive, normalActive int
	if err := tx.QueryRowContext(ctx, lockGlobalSQL).Scan(&globalActive, &normalActive); err != nil {
		return "", err
	}
	if globalActive+1 > policy.GlobalParallelLimit ||
		(normal && normalActive+1 > policy.GlobalParallelLimit-policy.ReservedCriticalSlots) {
		return "", aicontract.ErrConcurrencyFull
	}

	if _, err := tx.ExecContext(ctx, upsertUsageSQL, incidentID); err != nil {
		return "", err
	}
	var usageAttempts, usageActive, usageNormalActive int
	var usageTokens, usageCost int64
	if err := tx.QueryRowContext(ctx, lockUsageSQL, incidentID).Scan(&usageAttempts, &usageTokens, &usageCost, &usageActive, &usageNormalActive); err != nil {
		return "", err
	}
	if usageAttempts+attempts > policy.MaxAttemptsPerIncident ||
		usageTokens+tokens > policy.MaxTokensPerIncident ||
		usageCost+costMicros > policy.MaxCostMicrosPerIncident {
		return "", aicontract.ErrBudgetExhausted
	}
	if usageActive+1 > policy.MaxParallelPerIncident {
		return "", aicontract.ErrConcurrencyFull
	}

	newUsageNormal := usageNormalActive
	if normal {
		newUsageNormal++
	}
	if _, err := tx.ExecContext(ctx, updateUsageSQL, incidentID, usageAttempts+attempts, usageTokens+tokens, usageCost+costMicros, usageActive+1, newUsageNormal); err != nil {
		return "", err
	}
	newGlobalNormal := normalActive
	if normal {
		newGlobalNormal++
	}
	if _, err := tx.ExecContext(ctx, updateGlobalSQL, globalActive+1, newGlobalNormal); err != nil {
		return "", err
	}

	reservationID := r.newID()
	if _, err := tx.ExecContext(ctx, insertReserveSQL, reservationID, incidentID, string(priority), attempts, tokens, costMicros, r.now().Add(lease)); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return reservationID, nil
}

// Settle replaces a reservation's conservative charge with actual usage. It is
// idempotent and fails closed: invalid or out-of-reservation usage retains the
// worst-case charge while releasing the concurrency slot.
func (r *Repository) Settle(ctx context.Context, reservationID string, actual aicontract.ProviderUsage) error {
	return r.finish(ctx, reservationID, actual, false)
}

// Release retains the worst-case charge and releases the concurrency slot. It
// is idempotent and is the required fallback when actual usage is unavailable.
func (r *Repository) Release(ctx context.Context, reservationID string) error {
	return r.finish(ctx, reservationID, aicontract.ProviderUsage{}, true)
}

func (r *Repository) finish(ctx context.Context, reservationID string, actual aicontract.ProviderUsage, forceRelease bool) error {
	if r == nil {
		return ErrRepositoryUnavailable
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var incidentID, priority, status string
	var reservedAttempts int
	var reservedTokens, reservedCost int64
	if err := tx.QueryRowContext(ctx, lockReserveSQL, reservationID).Scan(&incidentID, &priority, &reservedAttempts, &reservedTokens, &reservedCost, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrReservationNotFound
		}
		return err
	}
	if status != "active" {
		return nil
	}

	var usageAttempts, usageActive, usageNormalActive int
	var usageTokens, usageCost int64
	if err := tx.QueryRowContext(ctx, lockUsageSQL, incidentID).Scan(&usageAttempts, &usageTokens, &usageCost, &usageActive, &usageNormalActive); err != nil {
		return err
	}
	var globalActive, normalActive int
	if err := tx.QueryRowContext(ctx, lockGlobalSQL).Scan(&globalActive, &normalActive); err != nil {
		return err
	}

	resultErr := error(nil)
	settled := false
	if forceRelease {
		resultErr = aicontract.ErrUsageAccounting
	} else {
		wellFormed := actual.Complete && actual.Attempts >= 0 &&
			actual.InputTokens >= 0 && actual.OutputTokens >= 0 && actual.TotalTokens >= 0 &&
			actual.InputTokens <= actual.TotalTokens && actual.OutputTokens == actual.TotalTokens-actual.InputTokens &&
			actual.CostMicros >= 0 &&
			(actual.Attempts > 0 || (actual.TotalTokens == 0 && actual.CostMicros == 0))
		withinReservation := wellFormed && actual.Attempts <= reservedAttempts &&
			actual.TotalTokens <= reservedTokens && actual.CostMicros <= reservedCost
		if withinReservation {
			usageAttempts -= reservedAttempts - actual.Attempts
			usageTokens -= reservedTokens - actual.TotalTokens
			usageCost -= reservedCost - actual.CostMicros
			settled = true
		} else {
			resultErr = aicontract.ErrUsageAccounting
			if wellFormed {
				if actual.Attempts > reservedAttempts {
					usageAttempts += actual.Attempts - reservedAttempts
				}
				if actual.TotalTokens > reservedTokens {
					usageTokens += actual.TotalTokens - reservedTokens
				}
				if actual.CostMicros > reservedCost {
					usageCost += actual.CostMicros - reservedCost
				}
			}
		}
	}

	newUsageActive := saturatingDec(usageActive)
	newUsageNormal := usageNormalActive
	if priority == string(aicontract.PriorityNormal) {
		newUsageNormal = saturatingDec(usageNormalActive)
	}
	if _, err := tx.ExecContext(ctx, updateUsageSQL, incidentID, usageAttempts, usageTokens, usageCost, newUsageActive, newUsageNormal); err != nil {
		return err
	}
	newGlobalActive := saturatingDec(globalActive)
	newNormalActive := normalActive
	if priority == string(aicontract.PriorityNormal) {
		newNormalActive = saturatingDec(normalActive)
	}
	if _, err := tx.ExecContext(ctx, updateGlobalSQL, newGlobalActive, newNormalActive); err != nil {
		return err
	}

	newStatus := "released"
	if settled {
		newStatus = "settled"
	}
	if _, err := tx.ExecContext(ctx, markReserveSQL, reservationID, newStatus); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return resultErr
}

// ReclaimExpired releases one expired active reservation, if any, retaining its
// worst-case charge and freeing its concurrency slot. It returns true when a
// reservation was reclaimed so callers can loop until the backlog is drained.
func (r *Repository) ReclaimExpired(ctx context.Context, lease time.Duration) (bool, error) {
	if r == nil {
		return false, ErrRepositoryUnavailable
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var reservationID, incidentID, priority string
	if err := tx.QueryRowContext(ctx, selectExpiredSQL, r.now().Add(-lease)).Scan(&reservationID, &incidentID, &priority); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}

	var usageAttempts, usageActive, usageNormalActive int
	var usageTokens, usageCost int64
	if err := tx.QueryRowContext(ctx, lockUsageSQL, incidentID).Scan(&usageAttempts, &usageTokens, &usageCost, &usageActive, &usageNormalActive); err != nil {
		return false, err
	}
	var globalActive, normalActive int
	if err := tx.QueryRowContext(ctx, lockGlobalSQL).Scan(&globalActive, &normalActive); err != nil {
		return false, err
	}

	newUsageActive := saturatingDec(usageActive)
	newUsageNormal := usageNormalActive
	if priority == string(aicontract.PriorityNormal) {
		newUsageNormal = saturatingDec(usageNormalActive)
	}
	if _, err := tx.ExecContext(ctx, updateUsageSQL, incidentID, usageAttempts, usageTokens, usageCost, newUsageActive, newUsageNormal); err != nil {
		return false, err
	}
	newGlobalActive := saturatingDec(globalActive)
	newNormalActive := normalActive
	if priority == string(aicontract.PriorityNormal) {
		newNormalActive = saturatingDec(normalActive)
	}
	if _, err := tx.ExecContext(ctx, updateGlobalSQL, newGlobalActive, newNormalActive); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, markReserveSQL, reservationID, "released"); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// Usage returns the persisted committed usage for an incident, or zero when the
// incident has no ledger row.
func (r *Repository) Usage(ctx context.Context, incidentID string) (aicontract.Usage, error) {
	if r == nil {
		return aicontract.Usage{}, ErrRepositoryUnavailable
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return aicontract.Usage{}, err
	}
	defer tx.Rollback()
	var attempts, active int
	var tokens, cost int64
	if err := tx.QueryRowContext(ctx, selectUsageSQL, incidentID).Scan(&attempts, &tokens, &cost, &active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return aicontract.Usage{}, nil
		}
		return aicontract.Usage{}, err
	}
	if err := tx.Commit(); err != nil {
		return aicontract.Usage{}, err
	}
	return aicontract.Usage{Attempts: attempts, Tokens: tokens, CostMicros: cost, Active: active}, nil
}

func saturatingDec(v int) int {
	if v <= 0 {
		return 0
	}
	return v - 1
}

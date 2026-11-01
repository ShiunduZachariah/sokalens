package durablebudget

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	aicontract "github.com/ShiunduZachariah/sports-prediction-platform/packages/ai-contract"
)

type fakeDB struct {
	tx      *fakeTx
	options *sql.TxOptions
}

func (d *fakeDB) BeginTx(_ context.Context, options *sql.TxOptions) (Tx, error) {
	d.options = options
	return d.tx, nil
}

type fakeTx struct {
	rows      []Row
	rowIndex  int
	execs     []string
	execErrAt int
	committed bool
	rolledBack bool
}

type fakeRow struct{ vals []any }

func (r fakeRow) Scan(dest ...any) error {
	for i, v := range r.vals {
		switch d := dest[i].(type) {
		case *int:
			*d = v.(int)
		case *int64:
			*d = v.(int64)
		case *string:
			*d = v.(string)
		case *time.Time:
			*d = v.(time.Time)
		case *bool:
			*d = v.(bool)
		default:
			return fmt.Errorf("unsupported scan dest %T", dest[i])
		}
	}
	return nil
}

type fakeResult struct{ affected int64 }

func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeResult) RowsAffected() (int64, error) { return r.affected, nil }

func (tx *fakeTx) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	tx.execs = append(tx.execs, query)
	if tx.execErrAt > 0 && len(tx.execs) == tx.execErrAt {
		return nil, errors.New("forced execution failure")
	}
	return fakeResult{affected: 1}, nil
}
func (tx *fakeTx) QueryRowContext(_ context.Context, _ string, _ ...any) Row {
	row := tx.rows[tx.rowIndex]
	tx.rowIndex++
	return row
}
func (tx *fakeTx) Commit() error   { tx.committed = true; return nil }
func (tx *fakeTx) Rollback() error { tx.rolledBack = true; return nil }

func testPolicy() aicontract.BudgetPolicy {
	return aicontract.BudgetPolicy{
		MaxTokensPerIncident:     10_000,
		MaxCostMicrosPerIncident: 10_000,
		MaxAttemptsPerIncident:   10,
		MaxParallelPerIncident:   2,
		GlobalParallelLimit:      4,
		ReservedCriticalSlots:    1,
	}
}

func newTestRepo(t *testing.T) (*Repository, *fakeDB, *fakeTx) {
	t.Helper()
	tx := &fakeTx{}
	db := &fakeDB{tx: tx}
	repo, err := NewRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	repo.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	repo.newID = func() string { return "reservation-1" }
	return repo, db, tx
}

func TestReserveChargesWorstCaseAndReturnsReservation(t *testing.T) {
	repo, _, tx := newTestRepo(t)
	tx.rows = []Row{
		fakeRow{vals: []any{0, 0}},                        // global_active, normal_active
		fakeRow{vals: []any{0, int64(0), int64(0), 0, 0}}, // attempts, tokens, cost, active, normal_active
	}
	id, err := repo.Reserve(context.Background(), testPolicy(), "inc-1", aicontract.PriorityCritical, 2, 100, 50, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if id != "reservation-1" {
		t.Fatalf("reservation id = %q", id)
	}
	if !tx.committed {
		t.Fatal("reservation transaction was not committed")
	}
	if len(tx.execs) != 4 {
		t.Fatalf("execs = %d, want 4: %v", len(tx.execs), tx.execs)
	}
}

func TestReserveFailsClosedWhenBudgetExhausted(t *testing.T) {
	repo, _, tx := newTestRepo(t)
	tx.rows = []Row{
		fakeRow{vals: []any{0, 0}},                              // global
		fakeRow{vals: []any{10, int64(100), int64(100), 0, 0}}, // attempts already at max
	}
	_, err := repo.Reserve(context.Background(), testPolicy(), "inc-1", aicontract.PriorityCritical, 1, 10, 1, time.Minute)
	if !errors.Is(err, aicontract.ErrBudgetExhausted) {
		t.Fatalf("error = %v, want ErrBudgetExhausted", err)
	}
	if len(tx.execs) != 1 { // only the upsert; no reservation insert
		t.Fatalf("execs = %d, want 1: %v", len(tx.execs), tx.execs)
	}
}

func TestSettleReplacesWithActualUsage(t *testing.T) {
	repo, _, tx := newTestRepo(t)
	tx.rows = []Row{
		fakeRow{vals: []any{"inc-1", "critical", 3, int64(300), int64(30), "active"}}, // reservation
		fakeRow{vals: []any{3, int64(300), int64(30), 1, 0}},                         // usage
		fakeRow{vals: []any{1, 0}},                                                   // global
	}
	err := repo.Settle(context.Background(), "reservation-1", aicontract.ProviderUsage{
		Attempts: 2, InputTokens: 20, OutputTokens: 10, TotalTokens: 30, CostMicros: 6, Complete: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !tx.committed {
		t.Fatal("settle transaction not committed")
	}
	// updateUsage should write attempts=3-(3-2)=2, tokens=300-(300-30)=30, cost=30-(30-6)=6, active=0
	if len(tx.execs) != 3 {
		t.Fatalf("execs = %d, want 3: %v", len(tx.execs), tx.execs)
	}
}

func TestReleaseRetainsWorstCaseCharge(t *testing.T) {
	repo, _, tx := newTestRepo(t)
	tx.rows = []Row{
		fakeRow{vals: []any{"inc-1", "normal", 3, int64(300), int64(30), "active"}},
		fakeRow{vals: []any{3, int64(300), int64(30), 1, 1}},
		fakeRow{vals: []any{1, 1}},
	}
	err := repo.Release(context.Background(), "reservation-1")
	if !errors.Is(err, aicontract.ErrUsageAccounting) {
		t.Fatalf("error = %v, want ErrUsageAccounting", err)
	}
	if !tx.committed {
		t.Fatal("release transaction not committed")
	}
}

func TestUsageReturnsPersistedState(t *testing.T) {
	repo, _, tx := newTestRepo(t)
	tx.rows = []Row{fakeRow{vals: []any{2, int64(30), int64(6), 1}}}
	usage, err := repo.Usage(context.Background(), "inc-1")
	if err != nil {
		t.Fatal(err)
	}
	if usage != (aicontract.Usage{Attempts: 2, Tokens: 30, CostMicros: 6, Active: 1}) {
		t.Fatalf("usage = %+v", usage)
	}
}

package replaystore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/aiworkflow"
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
	rows          []Row
	rowIndex      int
	execs         []string
	affected      []int64
	affectedIndex int
	committed     bool
}

type fakeRow struct{ vals []any }

func (r fakeRow) Scan(dest ...any) error {
	for i, v := range r.vals {
		switch d := dest[i].(type) {
		case *string:
			*d = v.(string)
		case *[]byte:
			*d = v.([]byte)
		case *time.Time:
			*d = v.(time.Time)
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
	affected := int64(1)
	if tx.affectedIndex < len(tx.affected) {
		affected = tx.affected[tx.affectedIndex]
	}
	tx.affectedIndex++
	return fakeResult{affected: affected}, nil
}
func (tx *fakeTx) QueryRowContext(_ context.Context, _ string, _ ...any) Row {
	row := tx.rows[tx.rowIndex]
	tx.rowIndex++
	return row
}
func (tx *fakeTx) Commit() error   { tx.committed = true; return nil }
func (tx *fakeTx) Rollback() error { return nil }

func newTestStore(t *testing.T) (*Store, *fakeTx) {
	t.Helper()
	tx := &fakeTx{}
	store, err := New(&fakeDB{tx: tx}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	return store, tx
}

func TestClaimInsertsAndOwnsWorkItem(t *testing.T) {
	store, tx := newTestStore(t)
	tx.affected = []int64{1}
	claim, err := store.Claim(context.Background(), "inc-1", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if !claim.Owned || claim.Replayed {
		t.Fatalf("claim = %+v, want owned", claim)
	}
	if !tx.committed {
		t.Fatal("claim transaction not committed")
	}
}

func TestClaimReturnsCompletedResult(t *testing.T) {
	store, tx := newTestStore(t)
	payload, err := json.Marshal(aiworkflow.Result{IncidentID: "inc-1", IdempotencyKey: "key-1"})
	if err != nil {
		t.Fatal(err)
	}
	tx.affected = []int64{0}
	tx.rows = []Row{fakeRow{vals: []any{"done", payload, time.Time{}}}}
	claim, err := store.Claim(context.Background(), "inc-1", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if claim.Owned || !claim.Replayed {
		t.Fatalf("claim = %+v, want replayed", claim)
	}
	if claim.Result.IncidentID != "inc-1" || claim.Result.IdempotencyKey != "key-1" {
		t.Fatalf("replayed result = %+v", claim.Result)
	}
}

func TestClaimFailsClosedWhenRunningUnexpired(t *testing.T) {
	store, tx := newTestStore(t)
	tx.affected = []int64{0}
	tx.rows = []Row{fakeRow{vals: []any{"running", []byte(nil), time.Unix(1_700_000_100, 0).UTC()}}}
	_, err := store.Claim(context.Background(), "inc-1", "key-1")
	if !errors.Is(err, aiworkflow.ErrReplayClaimed) {
		t.Fatalf("error = %v, want ErrReplayClaimed", err)
	}
}

func TestClaimTakesOverExpiredRunningClaim(t *testing.T) {
	store, tx := newTestStore(t)
	tx.affected = []int64{0, 1}
	tx.rows = []Row{fakeRow{vals: []any{"running", []byte(nil), time.Unix(1_699_999_000, 0).UTC()}}}
	claim, err := store.Claim(context.Background(), "inc-1", "key-1")
	if err != nil {
		t.Fatal(err)
	}
	if !claim.Owned {
		t.Fatalf("claim = %+v, want takeover ownership", claim)
	}
}

func TestCompleteStoresResult(t *testing.T) {
	store, tx := newTestStore(t)
	tx.affected = []int64{1}
	err := store.Complete(context.Background(), aiworkflow.Result{IncidentID: "inc-1", IdempotencyKey: "key-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !tx.committed {
		t.Fatal("complete transaction not committed")
	}
}

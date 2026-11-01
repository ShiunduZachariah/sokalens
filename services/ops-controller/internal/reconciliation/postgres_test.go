package reconciliation

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeDB struct {
	tx  *fakeTx
	err error
}

func (database fakeDB) BeginTx(context.Context, *sql.TxOptions) (Tx, error) {
	return database.tx, database.err
}

type fakeRow struct{ scan func(...any) error }

func (row fakeRow) Scan(dest ...any) error { return row.scan(dest...) }

type fakeRows struct{}

func (fakeRows) Next() bool        { return false }
func (fakeRows) Scan(...any) error { return nil }
func (fakeRows) Err() error        { return nil }
func (fakeRows) Close() error      { return nil }

type fakeResult int64

func (result fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (result fakeResult) RowsAffected() (int64, error) { return int64(result), nil }

type fakeTx struct {
	rows                  []Row
	rowIndex              int
	execQueries           []string
	execErr               error
	committed, rolledBack bool
}

func (transaction *fakeTx) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	transaction.execQueries = append(transaction.execQueries, query)
	if transaction.execErr != nil {
		return nil, transaction.execErr
	}
	return fakeResult(1), nil
}
func (transaction *fakeTx) QueryRowContext(_ context.Context, _ string, _ ...any) Row {
	row := transaction.rows[transaction.rowIndex]
	transaction.rowIndex++
	return row
}
func (*fakeTx) QueryContext(context.Context, string, ...any) (Rows, error) { return fakeRows{}, nil }
func (transaction *fakeTx) Commit() error                                  { transaction.committed = true; return nil }
func (transaction *fakeTx) Rollback() error                                { transaction.rolledBack = true; return nil }

func TestPostgresRepositoryRollsBackAtomicRunOnInsertFailure(t *testing.T) {
	insertFailure := errors.New("insert failed")
	tx := &fakeTx{rows: []Row{
		fakeRow{scan: func(...any) error { return sql.ErrNoRows }},
		fakeRow{scan: func(...any) error { return insertFailure }},
	}}
	repository, err := NewPostgresRepository(fakeDB{tx: tx})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	_, err = repository.Apply(context.Background(), Run{IdempotencyKey: "run", Provider: "billing", Environment: "test", StartedAt: now, CompletedAt: now, Result: "completed"})
	if !errors.Is(err, ErrRepositoryUnavailable) || !tx.rolledBack || tx.committed {
		t.Fatalf("err=%v rollback=%t commit=%t", err, tx.rolledBack, tx.committed)
	}
}

func TestPostgresRepositoryRejectsRunKeyWithDifferentInput(t *testing.T) {
	tx := &fakeTx{rows: []Row{fakeRow{scan: func(dest ...any) error {
		*(dest[0].(*int64)) = 7
		*(dest[1].(*string)) = strings.Repeat("0", 64)
		return nil
	}}}}
	repository, err := NewPostgresRepository(fakeDB{tx: tx})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	_, err = repository.Apply(context.Background(), Run{IdempotencyKey: "conflict", Provider: "billing", Environment: "test", StartedAt: now, CompletedAt: now, Result: "completed"})
	if !errors.Is(err, ErrIdempotencyConflict) || !tx.rolledBack || tx.committed {
		t.Fatalf("err=%v rollback=%t commit=%t", err, tx.rolledBack, tx.committed)
	}
}

func TestScopedMutationSQLCannotTouchUnrelatedSubscriptionsOrGrants(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	run := Run{Provider: "billing", Environment: "test"}
	item := Item{ProviderSubscriptionID: "provider-sub"}
	tests := []struct {
		action Action
		want   []string
	}{
		{CorrectSubscription, []string{"where id=$1 and provider=$2 and environment=$3 and provider_subscription_id=$11"}},
		{VerifyGrant, []string{"where id=$1", "source_type='subscription'", "source_id=$2::text"}},
		{RevokeGrant, []string{"where id=$1", "source_type='subscription'", "source_id=$2::text", "revoked_at is null"}},
		{FreezeNewGrants, []string{"provider,environment,subscription_id"}},
	}
	for _, test := range tests {
		t.Run(string(test.action), func(t *testing.T) {
			tx := &fakeTx{}
			correction := Correction{Action: test.action, SubscriptionID: 42, GrantID: 99, TargetStatus: "active", EffectiveAt: now, ProviderRevision: "r1"}
			if err := applyEffect(context.Background(), tx, run, item, correction, 1); err != nil {
				t.Fatal(err)
			}
			if len(tx.execQueries) != 1 {
				t.Fatalf("queries=%d", len(tx.execQueries))
			}
			query := strings.ToLower(strings.Join(strings.Fields(tx.execQueries[0]), " "))
			for _, fragment := range test.want {
				if !strings.Contains(query, fragment) {
					t.Fatalf("query %q missing %q", query, fragment)
				}
			}
		})
	}
}

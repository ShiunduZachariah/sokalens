package incidents

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/alerts"
)

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i, value := range r.values {
		switch target := dest[i].(type) {
		case *string:
			*target = value.(string)
		case *int64:
			*target = value.(int64)
		}
	}
	return nil
}

type fakeTx struct {
	rows      []Row
	execErr   error
	commitErr error
	queries   []string
	args      [][]any
	commits   int
	rollbacks int
}

func (t *fakeTx) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	t.queries = append(t.queries, query)
	t.args = append(t.args, args)
	if t.execErr != nil {
		err := t.execErr
		t.execErr = nil
		return nil, err
	}
	return fakeResult{}, nil
}
func (t *fakeTx) QueryRowContext(_ context.Context, query string, args ...any) Row {
	t.queries = append(t.queries, query)
	t.args = append(t.args, args)
	row := t.rows[0]
	t.rows = t.rows[1:]
	return row
}
func (t *fakeTx) Commit() error   { t.commits++; return t.commitErr }
func (t *fakeTx) Rollback() error { t.rollbacks++; return nil }

type fakeResult struct{}

func (fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (fakeResult) RowsAffected() (int64, error) { return 1, nil }

type fakeDB struct {
	txs     []Tx
	errs    []error
	options []*sql.TxOptions
}

func (d *fakeDB) BeginTx(_ context.Context, options *sql.TxOptions) (Tx, error) {
	d.options = append(d.options, options)
	if len(d.errs) > 0 {
		err := d.errs[0]
		d.errs = d.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	tx := d.txs[0]
	d.txs = d.txs[1:]
	return tx, nil
}

type stateError struct{ state string }

func (e stateError) Error() string    { return e.state }
func (e stateError) SQLState() string { return e.state }

func observation() Observation {
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	o, _ := Classify(accepted("event-1", "api", "prod", "latency", alerts.SeverityCritical, now))
	return o
}

func TestPostgresInsertUsesSerializableLockAndBoundedCorrelation(t *testing.T) {
	tx := &fakeTx{rows: []Row{
		fakeRow{err: sql.ErrNoRows},
		fakeRow{err: sql.ErrNoRows},
		fakeRow{values: []any{"incident-1"}},
	}}
	db := &fakeDB{txs: []Tx{tx}}
	repository, err := NewPostgresRepository(db, 45*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	result, err := repository.Observe(context.Background(), observation())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.OccurrenceCount != 1 || result.IncidentID != "incident-1" || tx.commits != 1 || tx.rollbacks != 0 {
		t.Fatalf("unexpected result/transaction: %+v commits=%d rollbacks=%d", result, tx.commits, tx.rollbacks)
	}
	if db.options[0].Isolation != sql.LevelSerializable || !strings.Contains(tx.queries[0], "pg_advisory_xact_lock") {
		t.Fatalf("missing serialization contract: options=%+v query=%s", db.options[0], tx.queries[0])
	}
	threshold := tx.args[2][1].(time.Time)
	if want := observation().ObservedAt.Add(-45 * time.Minute); !threshold.Equal(want) {
		t.Fatalf("correlation threshold=%s want=%s", threshold, want)
	}
	upper := tx.args[2][2].(time.Time)
	if want := observation().ObservedAt.Add(45 * time.Minute); !upper.Equal(want) {
		t.Fatalf("correlation upper bound=%s want=%s", upper, want)
	}
}

func TestPostgresExactDuplicateReturnsWithoutIncrement(t *testing.T) {
	tx := &fakeTx{rows: []Row{fakeRow{values: []any{"incident-1", int64(7)}}}}
	repository, _ := NewPostgresRepository(&fakeDB{txs: []Tx{tx}}, time.Hour)
	result, err := repository.Observe(context.Background(), observation())
	if err != nil {
		t.Fatal(err)
	}
	if result.Created || result.OccurrenceCount != 7 || tx.commits != 1 {
		t.Fatalf("unexpected duplicate result: %+v", result)
	}
	if len(tx.queries) != 2 || !strings.Contains(tx.queries[1], "ops_incident_observations") {
		t.Fatalf("duplicate performed unexpected writes: %v", tx.queries)
	}
}

func TestPostgresNewCorrelatedObservationUpdatesCountAndEscalates(t *testing.T) {
	tx := &fakeTx{rows: []Row{
		fakeRow{err: sql.ErrNoRows},
		fakeRow{values: []any{"incident-1", int64(7)}},
	}}
	repository, _ := NewPostgresRepository(&fakeDB{txs: []Tx{tx}}, time.Hour)
	result, err := repository.Observe(context.Background(), observation())
	if err != nil {
		t.Fatal(err)
	}
	if result.Created || result.OccurrenceCount != 8 {
		t.Fatalf("unexpected correlated result: %+v", result)
	}
	update := tx.queries[len(tx.queries)-1]
	for _, required := range []string{"severity = CASE", "GREATEST(last_observed_at", "occurrence_count", "WHERE id = $1"} {
		if !strings.Contains(update, required) {
			t.Fatalf("update lacks %q:\n%s", required, update)
		}
	}
}

func TestPostgresRetriesTransactionalConflictsAndRollsBack(t *testing.T) {
	first := &fakeTx{execErr: stateError{state: "40001"}}
	second := &fakeTx{rows: []Row{
		fakeRow{err: sql.ErrNoRows}, fakeRow{err: sql.ErrNoRows}, fakeRow{values: []any{"incident-2"}},
	}}
	repository, _ := NewPostgresRepository(&fakeDB{txs: []Tx{first, second}}, time.Hour)
	result, err := repository.Observe(context.Background(), observation())
	if err != nil || result.IncidentID != "incident-2" || first.rollbacks != 1 || second.commits != 1 {
		t.Fatalf("retry result=%+v err=%v first rollback=%d second commits=%d", result, err, first.rollbacks, second.commits)
	}
}

func TestPostgresFailsClosedOnNonRetryableAndCommitErrors(t *testing.T) {
	lockFailure := &fakeTx{execErr: errors.New("database unavailable")}
	repository, _ := NewPostgresRepository(&fakeDB{txs: []Tx{lockFailure}}, time.Hour)
	if _, err := repository.Observe(context.Background(), observation()); !errors.Is(err, ErrRepositoryUnavailable) || lockFailure.rollbacks != 1 {
		t.Fatalf("lock failure err=%v rollbacks=%d", err, lockFailure.rollbacks)
	}

	commitFailure := &fakeTx{rows: []Row{
		fakeRow{err: sql.ErrNoRows}, fakeRow{err: sql.ErrNoRows}, fakeRow{values: []any{"incident-3"}},
	}, commitErr: errors.New("commit failed")}
	repository, _ = NewPostgresRepository(&fakeDB{txs: []Tx{commitFailure}}, time.Hour)
	if _, err := repository.Observe(context.Background(), observation()); !errors.Is(err, ErrRepositoryUnavailable) || commitFailure.rollbacks != 1 {
		t.Fatalf("commit failure err=%v rollbacks=%d", err, commitFailure.rollbacks)
	}
}

func TestPostgresRejectsInvalidObservationBeforeTransaction(t *testing.T) {
	db := &fakeDB{}
	repository, _ := NewPostgresRepository(db, time.Hour)
	if _, err := repository.Observe(context.Background(), Observation{}); !errors.Is(err, ErrInvalidObservation) {
		t.Fatalf("invalid observation error=%v", err)
	}
	if len(db.options) != 0 {
		t.Fatal("invalid observation opened a transaction")
	}
}

func TestPostgresSQLIsSchemaBoundedAndObserveOnly(t *testing.T) {
	all := strings.Join([]string{lockFingerprintSQL, findObservationSQL, findCorrelatedSQL, updateIncidentSQL, insertIncidentSQL, insertObservationSQL}, "\n")
	for _, required := range []string{"ops_incidents", "ops_incident_observations", "source_event_id", "idempotency_key", "fingerprint", "status IN ('open', 'investigating', 'mitigated')", "RETURNING id"} {
		if !strings.Contains(all, required) {
			t.Fatalf("SQL contract lacks %q", required)
		}
	}
	for _, forbidden := range []string{"ops_actions", "ops_jobs", "DELETE ", "TRUNCATE ", "ops_deployments"} {
		if strings.Contains(all, forbidden) {
			t.Fatalf("observe-only SQL contains forbidden %q", forbidden)
		}
	}
}

package versions

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeDB struct {
	tx      *fakeTx
	options *sql.TxOptions
}

func (database *fakeDB) BeginTx(_ context.Context, options *sql.TxOptions) (Tx, error) {
	database.options = options
	return database.tx, nil
}

type fakeTx struct {
	rows       []Row
	rowIndex   int
	execs      []string
	execErrAt  int
	committed  bool
	rolledBack bool
}

type fakeRow struct{ scan func(...any) error }

func (row fakeRow) Scan(dest ...any) error { return row.scan(dest...) }

type fakeResult int64

func (result fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (result fakeResult) RowsAffected() (int64, error) { return int64(result), nil }

func (transaction *fakeTx) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	transaction.execs = append(transaction.execs, query)
	if transaction.execErrAt > 0 && len(transaction.execs) == transaction.execErrAt {
		return nil, errors.New("forced execution failure")
	}
	return fakeResult(1), nil
}
func (transaction *fakeTx) QueryRowContext(_ context.Context, _ string, _ ...any) Row {
	row := transaction.rows[transaction.rowIndex]
	transaction.rowIndex++
	return row
}
func (transaction *fakeTx) Commit() error   { transaction.committed = true; return nil }
func (transaction *fakeTx) Rollback() error { transaction.rolledBack = true; return nil }

func validCandidateCommand() Command {
	now := time.Unix(1_700_000_000, 0).UTC()
	return Command{
		Component: "ops-controller", Environment: "staging", IdempotencyKey: "release-42",
		Action: SetCandidate, ExpectedRevision: 0, Actor: "release-automation", Reason: "validated build",
		OccurredAt: now,
		Artifact: &Artifact{
			SourceSHA: strings.Repeat("a", 40), ImageDigest: "sha256:" + strings.Repeat("b", 64),
			ManifestDigest: "sha256:" + strings.Repeat("c", 64), RegisteredAt: now,
			RetainUntil: now.Add(365 * 24 * time.Hour), RetentionReason: "rollback and audit evidence",
		},
	}
}

func TestPostgresApplyUsesSerializableScopeLockAndAtomicAudit(t *testing.T) {
	tx := &fakeTx{rows: []Row{
		fakeRow{scan: func(...any) error { return sql.ErrNoRows }}, // no replay
		fakeRow{scan: func(...any) error { return sql.ErrNoRows }}, // no head
		fakeRow{scan: func(dest ...any) error { *(dest[0].(*int64)) = 9; return nil }},
		fakeRow{scan: func(dest ...any) error { *(dest[0].(*int64)) = 1; return nil }},
	}}
	database := &fakeDB{tx: tx}
	repository, err := NewPostgresRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	result, err := repository.Apply(context.Background(), validCandidateCommand())
	if err != nil {
		t.Fatal(err)
	}
	if database.options == nil || database.options.Isolation != sql.LevelSerializable {
		t.Fatalf("isolation=%v", database.options)
	}
	if !result.Applied || result.State.Revision != 1 || result.State.Candidate == nil || result.State.Candidate.ID != 9 {
		t.Fatalf("result=%+v", result)
	}
	if !tx.committed || tx.rolledBack || len(tx.execs) != 2 {
		t.Fatalf("commit=%t rollback=%t execs=%d", tx.committed, tx.rolledBack, len(tx.execs))
	}
	if !strings.Contains(tx.execs[0], "pg_advisory_xact_lock") || !strings.Contains(tx.execs[1], "ops_component_version_transitions") {
		t.Fatalf("missing lock/audit SQL: %#v", tx.execs)
	}
}

func TestPostgresApplyRejectsStaleExpectedRevisionBeforeMutation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	tx := &fakeTx{rows: []Row{
		fakeRow{scan: func(...any) error { return sql.ErrNoRows }},
		fakeRow{scan: func(dest ...any) error {
			*(dest[0].(*int64)) = 4
			*(dest[1].(*sql.NullInt64)) = sql.NullInt64{}
			*(dest[2].(*sql.NullInt64)) = sql.NullInt64{}
			*(dest[3].(*sql.NullInt64)) = sql.NullInt64{}
			*(dest[4].(*sql.NullInt64)) = sql.NullInt64{}
			*(dest[5].(*time.Time)) = now
			return nil
		}},
	}}
	database := &fakeDB{tx: tx}
	repository, _ := NewPostgresRepository(database)
	command := validCandidateCommand()
	command.ExpectedRevision = 3
	_, err := repository.Apply(context.Background(), command)
	if !errors.Is(err, ErrStaleRevision) || !tx.rolledBack || tx.committed || len(tx.execs) != 1 {
		t.Fatalf("err=%v commit=%t rollback=%t execs=%d", err, tx.committed, tx.rolledBack, len(tx.execs))
	}
}

func TestPostgresApplyRejectsNonMonotonicTransitionTimeBeforeMutation(t *testing.T) {
	command := validCandidateCommand()
	command.ExpectedRevision = 1
	tx := &fakeTx{rows: []Row{
		fakeRow{scan: func(...any) error { return sql.ErrNoRows }},
		fakeRow{scan: func(dest ...any) error {
			*(dest[0].(*int64)) = 1
			*(dest[1].(*sql.NullInt64)) = sql.NullInt64{}
			*(dest[2].(*sql.NullInt64)) = sql.NullInt64{}
			*(dest[3].(*sql.NullInt64)) = sql.NullInt64{}
			*(dest[4].(*sql.NullInt64)) = sql.NullInt64{}
			*(dest[5].(*time.Time)) = command.OccurredAt
			return nil
		}},
	}}
	repository, _ := NewPostgresRepository(&fakeDB{tx: tx})
	_, err := repository.Apply(context.Background(), command)
	if !errors.Is(err, ErrStaleRevision) || !tx.rolledBack || tx.committed || len(tx.execs) != 1 {
		t.Fatalf("err=%v commit=%t rollback=%t execs=%d", err, tx.committed, tx.rolledBack, len(tx.execs))
	}
}

func TestPostgresApplyRollsBackHeadWhenAuditWriteFails(t *testing.T) {
	tx := &fakeTx{execErrAt: 2, rows: []Row{
		fakeRow{scan: func(...any) error { return sql.ErrNoRows }},
		fakeRow{scan: func(...any) error { return sql.ErrNoRows }},
		fakeRow{scan: func(dest ...any) error { *(dest[0].(*int64)) = 9; return nil }},
		fakeRow{scan: func(dest ...any) error { *(dest[0].(*int64)) = 1; return nil }},
	}}
	repository, _ := NewPostgresRepository(&fakeDB{tx: tx})
	_, err := repository.Apply(context.Background(), validCandidateCommand())
	if !errors.Is(err, ErrRepositoryUnavailable) || !tx.rolledBack || tx.committed {
		t.Fatalf("err=%v commit=%t rollback=%t", err, tx.committed, tx.rolledBack)
	}
}

func TestPostgresApplyReturnsHistoricalResultForExactRetry(t *testing.T) {
	command := validCandidateCommand()
	inputHash, err := hashCommand(command)
	if err != nil {
		t.Fatal(err)
	}
	tx := &fakeTx{rows: []Row{fakeRow{scan: func(dest ...any) error {
		*(dest[0].(*string)) = inputHash
		*(dest[1].(*int64)) = 1
		*(dest[2].(*sql.NullInt64)) = sql.NullInt64{}
		*(dest[3].(*sql.NullInt64)) = sql.NullInt64{}
		*(dest[4].(*sql.NullInt64)) = sql.NullInt64{}
		*(dest[5].(*sql.NullInt64)) = sql.NullInt64{}
		*(dest[6].(*time.Time)) = command.OccurredAt
		return nil
	}}}}
	repository, _ := NewPostgresRepository(&fakeDB{tx: tx})
	result, err := repository.Apply(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.State.Revision != 1 || !tx.committed || tx.rolledBack || len(tx.execs) != 1 {
		t.Fatalf("result=%+v commit=%t rollback=%t execs=%d", result, tx.committed, tx.rolledBack, len(tx.execs))
	}
}

func TestPostgresApplyRejectsIdempotencyKeyReuseWithDifferentInput(t *testing.T) {
	tx := &fakeTx{rows: []Row{fakeRow{scan: func(dest ...any) error {
		*(dest[0].(*string)) = strings.Repeat("0", 64)
		*(dest[1].(*int64)) = 1
		*(dest[2].(*sql.NullInt64)) = sql.NullInt64{}
		*(dest[3].(*sql.NullInt64)) = sql.NullInt64{}
		*(dest[4].(*sql.NullInt64)) = sql.NullInt64{}
		*(dest[5].(*sql.NullInt64)) = sql.NullInt64{}
		*(dest[6].(*time.Time)) = time.Unix(1_700_000_000, 0).UTC()
		return nil
	}}}}
	repository, _ := NewPostgresRepository(&fakeDB{tx: tx})
	_, err := repository.Apply(context.Background(), validCandidateCommand())
	if !errors.Is(err, ErrIdempotencyConflict) || !tx.rolledBack || tx.committed || len(tx.execs) != 1 {
		t.Fatalf("err=%v commit=%t rollback=%t execs=%d", err, tx.committed, tx.rolledBack, len(tx.execs))
	}
}

func TestSQLConcurrencySeamsRemainScopedAndCompareAndSwap(t *testing.T) {
	lock := strings.ToLower(strings.Join(strings.Fields(advisoryLockSQL), " "))
	head := strings.ToLower(strings.Join(strings.Fields(loadHeadSQL), " "))
	upsert := strings.ToLower(strings.Join(strings.Fields(upsertHeadSQL), " "))
	for name, value := range map[string]struct {
		value     string
		fragments []string
	}{
		"lock": {lock, []string{"component-version:", "$1", "$2", "pg_advisory_xact_lock"}},
		"head": {head, []string{"component=$1", "environment=$2", "for update"}},
		"cas":  {upsert, []string{"on conflict (component,environment)", "where ops_component_version_heads.revision=$9", "returning revision"}},
	} {
		t.Run(name, func(t *testing.T) {
			for _, fragment := range value.fragments {
				if !strings.Contains(value.value, fragment) {
					t.Fatalf("SQL %q missing %q", value.value, fragment)
				}
			}
		})
	}
}

func TestCommandValidationRejectsFloatingOrIncompleteIdentity(t *testing.T) {
	command := validCandidateCommand()
	command.Artifact.ImageDigest = "latest"
	if !errors.Is(validateCommand(command), ErrInvalidCommand) {
		t.Fatal("floating image tag accepted")
	}
	command = validCandidateCommand()
	command.Artifact.SourceSHA = "main"
	if !errors.Is(validateCommand(command), ErrInvalidCommand) {
		t.Fatal("floating source identity accepted")
	}
	command = validCandidateCommand()
	command.Environment = ""
	if !errors.Is(validateCommand(command), ErrInvalidCommand) {
		t.Fatal("implicit environment accepted")
	}
	command = validCandidateCommand()
	command.Artifact.RegisteredAt = command.OccurredAt.Add(time.Minute)
	if !errors.Is(validateCommand(command), ErrInvalidCommand) {
		t.Fatal("future artifact registration accepted")
	}
	command = validCandidateCommand()
	command.Artifact.RetainUntil = command.OccurredAt.Add(-time.Second)
	if !errors.Is(validateCommand(command), ErrInvalidCommand) {
		t.Fatal("already-expired retention evidence accepted")
	}
}

func TestCommandHashIsDeterministicAcrossMetadataInsertionOrder(t *testing.T) {
	first := validCandidateCommand()
	first.Metadata = map[string]string{"z": "last", "a": "first"}
	second := validCandidateCommand()
	second.Metadata = map[string]string{"a": "first", "z": "last"}
	left, err := hashCommand(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := hashCommand(second)
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("left=%s right=%s", left, right)
	}
}

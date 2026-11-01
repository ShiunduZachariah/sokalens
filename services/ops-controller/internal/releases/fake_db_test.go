package releases

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"
)

// memoryDB is an in-memory stand-in for Postgres, following the fake-DB
// approach in internal/incidents/postgres_test.go: unit tests need no driver
// and no live database.
//
// It dispatches on the package's own SQL constants by equality rather than by
// pattern matching, so renaming or rewriting a statement breaks the fake
// loudly instead of silently falling through to a default branch.
type memoryDB struct {
	trains []*Train
	nextID int64
	confs  map[string]*confRecord
	// deployTrain maps an ops_deployments id to the train stamped on it.
	deployTrain map[string]int64
	// failBegin, when set, makes BeginTx fail.
	failBegin error
}

type confRecord struct {
	token       string
	runbook     string
	args        string
	requestedBy int64
	chatID      int64
	createdAt   time.Time
	expiresAt   time.Time
	consumedAt  sql.NullTime
}

func newMemoryDB() *memoryDB {
	return &memoryDB{nextID: 1, confs: map[string]*confRecord{}, deployTrain: map[string]int64{}}
}

func (db *memoryDB) clone() *memoryDB {
	out := &memoryDB{
		nextID:      db.nextID,
		confs:       make(map[string]*confRecord, len(db.confs)),
		deployTrain: make(map[string]int64, len(db.deployTrain)),
		failBegin:   db.failBegin,
	}
	for _, t := range db.trains {
		copied := *t
		copied.PRNumbers = append([]int(nil), t.PRNumbers...)
		out.trains = append(out.trains, &copied)
	}
	for k, v := range db.confs {
		copied := *v
		out.confs[k] = &copied
	}
	for k, v := range db.deployTrain {
		out.deployTrain[k] = v
	}
	return out
}

func (db *memoryDB) restore(snapshot *memoryDB) {
	db.trains = snapshot.trains
	db.nextID = snapshot.nextID
	db.confs = snapshot.confs
	db.deployTrain = snapshot.deployTrain
}

func (db *memoryDB) BeginTx(context.Context, *sql.TxOptions) (Tx, error) {
	if db.failBegin != nil {
		return nil, db.failBegin
	}
	return &memTx{db: db, snapshot: db.clone()}, nil
}

func (db *memoryDB) QueryRowContext(_ context.Context, query string, args ...any) Row {
	return db.query(query, args)
}

type memTx struct {
	db        *memoryDB
	snapshot  *memoryDB
	done      bool
	commits   int
	rollbacks int
}

func (t *memTx) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	return t.db.exec(query, args)
}
func (t *memTx) QueryRowContext(_ context.Context, query string, args ...any) Row {
	return t.db.query(query, args)
}
func (t *memTx) Commit() error {
	t.commits++
	t.done = true
	return nil
}
func (t *memTx) Rollback() error {
	t.rollbacks++
	if !t.done {
		t.db.restore(t.snapshot)
		t.done = true
	}
	return nil
}

type memResult struct{ affected int64 }

func (r memResult) LastInsertId() (int64, error) { return 0, nil }
func (r memResult) RowsAffected() (int64, error) { return r.affected, nil }

// ---------------------------------------------------------------------------
// Statement dispatch.
// ---------------------------------------------------------------------------

func (db *memoryDB) exec(query string, args []any) (sql.Result, error) {
	switch query {
	case lockTrainsSQL:
		return memResult{affected: 1}, nil
	case consumeConfirmationSQL:
		record, ok := db.confs[argString(args, 0)]
		if !ok || record.consumedAt.Valid {
			return memResult{affected: 0}, nil
		}
		record.consumedAt = sql.NullTime{Time: argTime(args, 1), Valid: true}
		return memResult{affected: 1}, nil
	case insertConfirmationSQL:
		token := argString(args, 0)
		if _, exists := db.confs[token]; exists {
			return nil, fmt.Errorf("duplicate token %q", token)
		}
		db.confs[token] = &confRecord{
			token:       token,
			runbook:     argString(args, 1),
			args:        argString(args, 2),
			requestedBy: argInt64(args, 3),
			chatID:      argInt64(args, 4),
			createdAt:   argTime(args, 5),
			expiresAt:   argTime(args, 6),
		}
		return memResult{affected: 1}, nil
	case attachDeploymentSQL:
		id, trainID := argString(args, 0), argInt64(args, 1)
		if db.findTrain(trainID) == nil {
			return memResult{affected: 0}, nil
		}
		db.deployTrain[id] = trainID
		return memResult{affected: 1}, nil
	case classifyDeploymentSQL:
		return memResult{affected: 1}, nil
	default:
		return nil, fmt.Errorf("memoryDB: unexpected exec %q", firstLine(query))
	}
}

func (db *memoryDB) query(query string, args []any) Row {
	switch query {
	case selectOpenTrainSQL:
		capacity := argInt(args, 0)
		for _, t := range db.trains {
			if t.Open() && len(t.PRNumbers) < capacity {
				return db.trainRow(t)
			}
		}
		return memRow{err: sql.ErrNoRows}

	case selectHeadTrainSQL:
		for _, t := range db.trains {
			if t.Open() {
				return db.trainRow(t)
			}
		}
		return memRow{err: sql.ErrNoRows}

	case selectTrainByIDSQL:
		if t := db.findTrain(argInt64(args, 0)); t != nil {
			return db.trainRow(t)
		}
		return memRow{err: sql.ErrNoRows}

	case lastCompletedStatusSQL:
		completed := make([]*Train, 0, len(db.trains))
		for _, t := range db.trains {
			if t.Status == StateDeployed || t.Status == StateRolledBack {
				completed = append(completed, t)
			}
		}
		if len(completed) == 0 {
			return memRow{err: sql.ErrNoRows}
		}
		sort.SliceStable(completed, func(i, j int) bool {
			return completionOrder(completed[i]).After(completionOrder(completed[j]))
		})
		return memRow{values: []any{string(completed[0].Status)}}

	case insertTrainSQL:
		train := &Train{
			ID:        db.nextID,
			Status:    StatePending,
			PRNumbers: parsePRs(argString(args, 0)),
			HeadSHA:   argString(args, 1),
			BaseSHA:   argString(args, 2),
			WindowAt:  argTime(args, 3),
			CreatedAt: argTime(args, 4),
			UpdatedAt: argTime(args, 4),
		}
		db.nextID++
		db.trains = append(db.trains, train)
		return db.trainRow(train)

	case updatePRsSQL:
		train := db.findTrain(argInt64(args, 0))
		if train == nil || !train.Open() {
			return memRow{err: sql.ErrNoRows}
		}
		train.PRNumbers = parsePRs(argString(args, 1))
		if head := argString(args, 2); head != "" {
			train.HeadSHA = head
		}
		if train.BaseSHA == "" {
			train.BaseSHA = argString(args, 3)
		}
		train.UpdatedAt = argTime(args, 4)
		return db.trainRow(train)

	case holdTrainSQL:
		train := db.findTrain(argInt64(args, 0))
		if train == nil || train.Status != StatePending {
			return memRow{err: sql.ErrNoRows}
		}
		train.Status = StateHeld
		train.HeldReason = argString(args, 1)
		train.HeldBy = argInt64(args, 2)
		if train.WindowAt.IsZero() {
			train.WindowAt = argTime(args, 3)
		}
		train.UpdatedAt = argTime(args, 4)
		return db.trainRow(train)

	case expireHoldSQL:
		train := db.findTrain(argInt64(args, 0))
		if train == nil || train.Status != StateHeld {
			return memRow{err: sql.ErrNoRows}
		}
		train.Status = StatePending
		train.WindowAt = argTime(args, 1)
		train.UpdatedAt = argTime(args, 2)
		return db.trainRow(train)

	case promoteTrainSQL:
		train := db.findTrain(argInt64(args, 0))
		if train == nil || train.Status != StatePending {
			return memRow{err: sql.ErrNoRows}
		}
		train.Status = StateDeploying
		train.PRNumbers = parsePRs(argString(args, 1))
		train.HeadSHA = argString(args, 2)
		train.UpdatedAt = argTime(args, 3)
		return db.trainRow(train)

	case markDeployedSQL:
		train := db.findTrain(argInt64(args, 0))
		if train == nil || train.Status != StateDeploying {
			return memRow{err: sql.ErrNoRows}
		}
		train.Status = StateDeployed
		train.DeployedAt = argTime(args, 1)
		if head := argString(args, 2); head != "" {
			train.HeadSHA = head
		}
		train.UpdatedAt = argTime(args, 1)
		return db.trainRow(train)

	case markRolledBackSQL:
		train := db.findTrain(argInt64(args, 0))
		if train == nil || train.Status != StateDeployed {
			return memRow{err: sql.ErrNoRows}
		}
		train.Status = StateRolledBack
		train.RolledBack = argTime(args, 1)
		train.UpdatedAt = argTime(args, 1)
		return db.trainRow(train)

	case trainDeploymentsSQL:
		trainID := argInt64(args, 0)
		ids := make([]string, 0, len(db.deployTrain))
		for id, owner := range db.deployTrain {
			if owner == trainID {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		return memRow{values: []any{strings.Join(ids, ",")}}

	case countOutstandingSQL:
		user, now, limit := argInt64(args, 0), argTime(args, 1), argInt(args, 2)
		count := 0
		for _, record := range db.confs {
			if record.requestedBy == user && !record.consumedAt.Valid && record.expiresAt.After(now) {
				count++
			}
			if count >= limit {
				break
			}
		}
		return memRow{values: []any{count}}

	case selectConfirmationSQL:
		record, ok := db.confs[argString(args, 0)]
		if !ok {
			return memRow{err: sql.ErrNoRows}
		}
		return memRow{values: []any{
			record.runbook, record.args, record.requestedBy, record.chatID,
			record.createdAt, record.expiresAt, record.consumedAt,
		}}

	default:
		return memRow{err: fmt.Errorf("memoryDB: unexpected query %q", firstLine(query))}
	}
}

func completionOrder(t *Train) time.Time {
	switch {
	case !t.RolledBack.IsZero():
		return t.RolledBack
	case !t.DeployedAt.IsZero():
		return t.DeployedAt
	default:
		return t.UpdatedAt
	}
}

func (db *memoryDB) findTrain(id int64) *Train {
	for _, t := range db.trains {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (db *memoryDB) trainRow(t *Train) memRow {
	return memRow{values: []any{
		t.ID, string(t.Status), formatPRs(t.PRNumbers), t.HeadSHA, t.BaseSHA,
		nullable(t.WindowAt), nullable(t.DeployedAt), nullable(t.RolledBack),
		t.HeldReason, t.HeldBy, t.CreatedAt, t.UpdatedAt,
	}}
}

func nullable(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// ---------------------------------------------------------------------------
// Row scanning.
// ---------------------------------------------------------------------------

type memRow struct {
	values []any
	err    error
}

func (r memRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return fmt.Errorf("memoryDB: scan wants %d columns, row has %d", len(dest), len(r.values))
	}
	for i, value := range r.values {
		switch target := dest[i].(type) {
		case *int64:
			*target = value.(int64)
		case *int:
			*target = value.(int)
		case *string:
			*target = value.(string)
		case *time.Time:
			*target = value.(time.Time)
		case *sql.NullTime:
			switch v := value.(type) {
			case nil:
				*target = sql.NullTime{}
			case sql.NullTime:
				*target = v
			case time.Time:
				*target = sql.NullTime{Time: v, Valid: true}
			default:
				return fmt.Errorf("memoryDB: column %d is %T, not a timestamp", i, value)
			}
		default:
			return fmt.Errorf("memoryDB: unsupported scan target %T at column %d", dest[i], i)
		}
	}
	return nil
}

func argString(args []any, i int) string {
	if i >= len(args) {
		return ""
	}
	s, _ := args[i].(string)
	return s
}

func argInt64(args []any, i int) int64 {
	if i >= len(args) {
		return 0
	}
	switch v := args[i].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	default:
		return 0
	}
}

func argInt(args []any, i int) int { return int(argInt64(args, i)) }

func argTime(args []any, i int) time.Time {
	if i >= len(args) {
		return time.Time{}
	}
	t, _ := args[i].(time.Time)
	return t
}

func firstLine(query string) string {
	if idx := strings.IndexByte(query, '\n'); idx >= 0 {
		return query[:idx]
	}
	return query
}

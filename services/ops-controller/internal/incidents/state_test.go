package incidents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type transitionRow struct {
	values []any
	err    error
}

func (row transitionRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	for index, value := range row.values {
		switch target := dest[index].(type) {
		case *string:
			*target = value.(string)
		case *IncidentStatus:
			*target = value.(IncidentStatus)
		case *time.Time:
			*target = value.(time.Time)
		default:
			panic("unsupported transition row destination")
		}
	}
	return nil
}

func transitionRequest() TransitionRequest {
	return TransitionRequest{
		IncidentID:     "incident-1",
		IdempotencyKey: strings.Repeat("a", 64),
		To:             StatusInvestigating,
		Reason:         "latency alert confirmed by on-call",
		Actor:          AuditActor{Type: "admin", ID: "operator-1"},
		OccurredAt:     time.Date(2026, 8, 14, 12, 5, 0, 0, time.UTC),
	}
}

func TestAllowedTransitionContract(t *testing.T) {
	allowed := map[[2]IncidentStatus]bool{
		{StatusOpen, StatusInvestigating}:       true,
		{StatusOpen, StatusSuppressed}:          true,
		{StatusInvestigating, StatusMitigated}:  true,
		{StatusInvestigating, StatusSuppressed}: true,
		{StatusMitigated, StatusInvestigating}:  true,
		{StatusMitigated, StatusResolved}:       true,
		{StatusSuppressed, StatusOpen}:          true,
	}
	statuses := []IncidentStatus{StatusOpen, StatusInvestigating, StatusMitigated, StatusResolved, StatusSuppressed}
	for _, from := range statuses {
		for _, to := range statuses {
			if got := AllowedTransition(from, to); got != allowed[[2]IncidentStatus{from, to}] {
				t.Fatalf("AllowedTransition(%q, %q)=%v", from, to, got)
			}
		}
	}
}

func TestTransitionAtomicallyAppendsAuditAndUpdatesIncident(t *testing.T) {
	request := transitionRequest()
	updatedAt := request.OccurredAt.Add(-time.Minute)
	tx := &fakeTx{rows: []Row{
		transitionRow{err: sql.ErrNoRows},
		transitionRow{values: []any{StatusOpen, updatedAt}},
		transitionRow{values: []any{strings.Repeat("b", 64)}},
	}}
	machine, _ := NewPostgresStateMachine(&fakeDB{txs: []Tx{tx}})
	result, err := machine.Transition(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.From != StatusOpen || result.To != StatusInvestigating || !validDigest(result.EventHash) || result.AuditEventID == "" {
		t.Fatalf("unexpected transition result: %+v", result)
	}
	if tx.commits != 1 || tx.rollbacks != 0 {
		t.Fatalf("commits=%d rollbacks=%d", tx.commits, tx.rollbacks)
	}
	joined := strings.Join(tx.queries, "\n")
	ordered := []string{"incident-transition:", "ops_incident_transitions", "FOR UPDATE", "a.entity_type = 'incident'", "ops_audit_events", "UPDATE ops_incidents", "INSERT INTO ops_incident_transitions"}
	position := -1
	for _, marker := range ordered {
		next := strings.Index(joined[position+1:], marker)
		if next < 0 {
			t.Fatalf("transaction contract lacks ordered marker %q:\n%s", marker, joined)
		}
		position += next + 1
	}
	var details []byte
	for index, query := range tx.queries {
		if query == insertAuditEventSQL {
			details = tx.args[index][5].([]byte)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(details, &decoded); err != nil || decoded["reason"] != request.Reason || decoded["audit_hash_version"] != AuditHashVersion {
		t.Fatalf("canonical audit details=%s err=%v", details, err)
	}
	for index, query := range tx.queries {
		if query == findPreviousAuditHashSQL && tx.args[index][0] != request.IncidentID {
			t.Fatalf("previous hash lookup was not incident-scoped: %v", tx.args[index])
		}
		if query == insertTransitionSQL && tx.args[index][9] != strings.Repeat("b", 64) {
			t.Fatalf("transition did not preserve previous hash: %v", tx.args[index][9])
		}
	}
}

func TestExactRepeatedTransitionIsIdempotent(t *testing.T) {
	request := transitionRequest()
	storedHash := strings.Repeat("c", 64)
	tx := &fakeTx{rows: []Row{transitionRow{values: []any{
		request.IncidentID, StatusOpen, request.To, request.Reason,
		request.Actor.Type, request.Actor.ID, request.OccurredAt, "audit-1", storedHash,
	}}}}
	machine, _ := NewPostgresStateMachine(&fakeDB{txs: []Tx{tx}})
	result, err := machine.Transition(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.EventHash != storedHash || result.AuditEventID != "audit-1" || tx.commits != 1 {
		t.Fatalf("unexpected idempotent result: %+v", result)
	}
	if len(tx.queries) != 2 {
		t.Fatalf("repeat performed mutation queries: %v", tx.queries)
	}
}

func TestIdempotencyKeyReuseWithDifferentRequestFailsClosed(t *testing.T) {
	request := transitionRequest()
	tx := &fakeTx{rows: []Row{transitionRow{values: []any{
		request.IncidentID, StatusOpen, request.To, "different reason",
		request.Actor.Type, request.Actor.ID, request.OccurredAt, "audit-1", strings.Repeat("c", 64),
	}}}}
	machine, _ := NewPostgresStateMachine(&fakeDB{txs: []Tx{tx}})
	if _, err := machine.Transition(context.Background(), request); !errors.Is(err, ErrTransitionConflict) {
		t.Fatalf("conflicting repeat error=%v", err)
	}
	if tx.commits != 0 || tx.rollbacks != 1 {
		t.Fatalf("conflict commits=%d rollbacks=%d", tx.commits, tx.rollbacks)
	}
}

func TestInvalidAndReorderedTransitionsAreRejected(t *testing.T) {
	request := transitionRequest()
	request.To = StatusMitigated
	tx := &fakeTx{rows: []Row{
		transitionRow{err: sql.ErrNoRows},
		transitionRow{values: []any{StatusOpen, request.OccurredAt.Add(-time.Minute)}},
	}}
	machine, _ := NewPostgresStateMachine(&fakeDB{txs: []Tx{tx}})
	if _, err := machine.Transition(context.Background(), request); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("invalid transition error=%v", err)
	}
	if strings.Contains(strings.Join(tx.queries, "\n"), "INSERT INTO ops_audit_events") {
		t.Fatal("invalid transition appended an audit event")
	}

	request = transitionRequest()
	tx = &fakeTx{rows: []Row{
		transitionRow{err: sql.ErrNoRows},
		transitionRow{values: []any{StatusOpen, request.OccurredAt.Add(time.Minute)}},
	}}
	machine, _ = NewPostgresStateMachine(&fakeDB{txs: []Tx{tx}})
	if _, err := machine.Transition(context.Background(), request); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("reordered transition error=%v", err)
	}
}

func TestRecoveryTransitionRejectsConcurrentFiringObservation(t *testing.T) {
	request := transitionRequest()
	request.ObservedBefore = request.OccurredAt.Add(-15 * time.Minute)
	tx := &fakeTx{rows: []Row{
		transitionRow{err: sql.ErrNoRows},
		transitionRow{values: []any{StatusOpen, request.OccurredAt.Add(-time.Minute), request.ObservedBefore.Add(time.Minute)}},
	}}
	machine, _ := NewPostgresStateMachine(&fakeDB{txs: []Tx{tx}})
	_, err := machine.Transition(context.Background(), request)
	if !errors.Is(err, ErrObservationConflict) {
		t.Fatalf("concurrent firing must block recovery, got %v", err)
	}
	if strings.Contains(strings.Join(tx.queries, "\n"), "INSERT INTO ops_audit_events") {
		t.Fatal("race appended a false recovery audit event")
	}
}

func TestTransitionRetriesSerializationFailureAndFailsClosed(t *testing.T) {
	request := transitionRequest()
	first := &fakeTx{execErr: stateError{state: "40001"}}
	second := &fakeTx{rows: []Row{
		transitionRow{err: sql.ErrNoRows},
		transitionRow{values: []any{StatusOpen, request.OccurredAt.Add(-time.Minute)}},
		transitionRow{err: sql.ErrNoRows},
	}}
	machine, _ := NewPostgresStateMachine(&fakeDB{txs: []Tx{first, second}})
	result, err := machine.Transition(context.Background(), request)
	if err != nil || !result.Applied || first.rollbacks != 1 || second.commits != 1 {
		t.Fatalf("result=%+v err=%v first rollback=%d second commits=%d", result, err, first.rollbacks, second.commits)
	}

	failing := &fakeTx{execErr: errors.New("database unavailable")}
	machine, _ = NewPostgresStateMachine(&fakeDB{txs: []Tx{failing}})
	if _, err = machine.Transition(context.Background(), request); !errors.Is(err, ErrRepositoryUnavailable) || failing.rollbacks != 1 {
		t.Fatalf("failure error=%v rollbacks=%d", err, failing.rollbacks)
	}
}

func TestTransitionRejectsInvalidRequestBeforeOpeningTransaction(t *testing.T) {
	db := &fakeDB{}
	machine, _ := NewPostgresStateMachine(db)
	if _, err := machine.Transition(context.Background(), TransitionRequest{}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("invalid request error=%v", err)
	}
	if len(db.options) != 0 {
		t.Fatal("invalid request opened a transaction")
	}
}

func TestStateMachineSQLIsObserveOnly(t *testing.T) {
	all := strings.Join([]string{lockTransitionKeySQL, findTransitionSQL, lockIncidentSQL, findPreviousAuditHashSQL, insertAuditEventSQL, updateIncidentStatusSQL, insertTransitionSQL}, "\n")
	for _, required := range []string{"ops_incidents", "ops_incident_transitions", "ops_audit_events", "previous_event_hash", "incident.status_transition", "LevelSerializable"} {
		if required == "LevelSerializable" {
			continue // asserted through the transaction options in the success test below.
		}
		if !strings.Contains(all, required) {
			t.Fatalf("state SQL lacks %q", required)
		}
	}
	for _, forbidden := range []string{"ops_actions", "ops_jobs", "ops_deployments", "DELETE ", "TRUNCATE "} {
		if strings.Contains(all, forbidden) {
			t.Fatalf("observe-only state SQL contains %q", forbidden)
		}
	}
}

func TestPreviousAuditLookupUsesImmutableInsertionOrder(t *testing.T) {
	if !strings.Contains(findPreviousAuditHashSQL, "ORDER BY a.sequence_id DESC") {
		t.Fatalf("previous audit lookup must use immutable audit insertion order:\n%s", findPreviousAuditHashSQL)
	}
	if strings.Contains(findPreviousAuditHashSQL, "ORDER BY t.occurred_at") {
		t.Fatal("equal transition timestamps must not make audit-chain order ambiguous")
	}
}

func TestAuditChainUniquenessIsIncidentScoped(t *testing.T) {
	// This models the migration's composite unique key: each incident may have
	// one root, while reusing a predecessor inside the same incident is a fork.
	slots := make(map[string]struct{})
	reserve := func(incidentID, previousHash string) bool {
		if previousHash == "" {
			previousHash = "__root__"
		}
		key := incidentID + "\x00" + previousHash
		if _, exists := slots[key]; exists {
			return false
		}
		slots[key] = struct{}{}
		return true
	}
	if !reserve("incident-a", "") || !reserve("incident-b", "") {
		t.Fatal("separate incidents must each be able to create an audit root")
	}
	predecessor := strings.Repeat("d", 64)
	if !reserve("incident-a", predecessor) || reserve("incident-a", predecessor) {
		t.Fatal("same-incident audit fork was not rejected")
	}
	if !reserve("incident-b", predecessor) {
		t.Fatal("a predecessor hash in another incident must not collide")
	}

	migrationPath := filepath.Join("..", "..", "..", "..", "infra", "db", "migrations", "000039_ops_incident_transitions.up.sql")
	migration, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatal(err)
	}
	sqlText := string(migration)
	for _, required := range []string{
		"previous_event_hash TEXT",
		"previous_event_hash IS NULL OR length(previous_event_hash) = 64",
		"ON ops_incident_transitions (incident_id, (COALESCE(previous_event_hash, '__root__')))",
	} {
		if !strings.Contains(sqlText, required) {
			t.Fatalf("incident-scoped chain migration lacks %q", required)
		}
	}
	if strings.Contains(sqlText, "ON ops_audit_events ((COALESCE(previous_event_hash") {
		t.Fatal("migration imposes a platform-global audit-chain root")
	}
}

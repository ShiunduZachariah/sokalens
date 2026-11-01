package versions

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
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

type PostgresRepository struct{ db DB }

func NewPostgresRepository(db DB) (*PostgresRepository, error) {
	if db == nil {
		return nil, ErrRepositoryUnavailable
	}
	return &PostgresRepository{db: db}, nil
}

const (
	advisoryLockSQL = `SELECT pg_advisory_xact_lock(hashtextextended('component-version:' || $1 || ':' || $2, 0))`
	loadHeadSQL     = `SELECT revision,current_version_id,previous_version_id,last_known_good_version_id,candidate_version_id,updated_at
FROM ops_component_version_heads WHERE component=$1 AND environment=$2 FOR UPDATE`
	upsertHeadSQL = `INSERT INTO ops_component_version_heads
  (component,environment,revision,current_version_id,previous_version_id,last_known_good_version_id,candidate_version_id,updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (component,environment) DO UPDATE SET
  revision=EXCLUDED.revision,current_version_id=EXCLUDED.current_version_id,
  previous_version_id=EXCLUDED.previous_version_id,last_known_good_version_id=EXCLUDED.last_known_good_version_id,
  candidate_version_id=EXCLUDED.candidate_version_id,updated_at=EXCLUDED.updated_at
WHERE ops_component_version_heads.revision=$9
RETURNING revision`
)

// Apply transactionally changes registry pointers and writes append-only audit
// evidence. It does not deploy, shift traffic, execute a rollback, or call a
// provider. Exact idempotent retries return their historical resulting state.
func (repository *PostgresRepository) Apply(ctx context.Context, command Command) (Result, error) {
	if err := validateCommand(command); err != nil {
		return Result{}, err
	}
	inputHash, err := hashCommand(command)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidCommand, err)
	}
	tx, err := repository.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Result{}, repositoryError(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, advisoryLockSQL, command.Component, command.Environment); err != nil {
		return Result{}, repositoryError(err)
	}

	if replay, found, replayErr := repository.loadReplay(ctx, tx, command, inputHash); replayErr != nil {
		return Result{}, replayErr
	} else if found {
		if err = tx.Commit(); err != nil {
			return Result{}, repositoryError(err)
		}
		committed = true
		return Result{State: replay, Applied: false}, nil
	}

	before, err := repository.loadHead(ctx, tx, command.Component, command.Environment)
	if err != nil {
		return Result{}, err
	}
	if before.Revision != command.ExpectedRevision {
		return Result{}, ErrStaleRevision
	}
	if !before.UpdatedAt.IsZero() && !command.OccurredAt.After(before.UpdatedAt) {
		return Result{}, ErrStaleRevision
	}

	var candidate *Artifact
	if command.Action == SetCandidate {
		candidate, err = repository.registerArtifact(ctx, tx, command)
		if err != nil {
			return Result{}, err
		}
	}
	after, err := nextState(before, command.Action, candidate)
	if err != nil {
		return Result{}, err
	}
	after.Component = command.Component
	after.Environment = command.Environment
	after.UpdatedAt = command.OccurredAt.UTC()

	var writtenRevision int64
	err = tx.QueryRowContext(ctx, upsertHeadSQL,
		command.Component, command.Environment, after.Revision,
		artifactID(after.Current), artifactID(after.Previous), artifactID(after.LastKnownGood), artifactID(after.Candidate), after.UpdatedAt,
		command.ExpectedRevision,
	).Scan(&writtenRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, ErrStaleRevision
	}
	if err != nil {
		return Result{}, repositoryError(err)
	}
	if writtenRevision != after.Revision {
		return Result{}, ErrStaleRevision
	}
	if err = repository.appendTransition(ctx, tx, command, inputHash, before, after); err != nil {
		return Result{}, err
	}
	if err = tx.Commit(); err != nil {
		return Result{}, repositoryError(err)
	}
	committed = true
	return Result{State: after, Applied: true}, nil
}

func (repository *PostgresRepository) loadHead(ctx context.Context, tx Tx, component, environment string) (State, error) {
	state := State{Component: component, Environment: environment}
	var currentID, previousID, lkgID, candidateID sql.NullInt64
	err := tx.QueryRowContext(ctx, loadHeadSQL, component, environment).Scan(
		&state.Revision, &currentID, &previousID, &lkgID, &candidateID, &state.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return State{}, repositoryError(err)
	}
	if state.Current, err = repository.loadArtifact(ctx, tx, currentID); err != nil {
		return State{}, err
	}
	if state.Previous, err = repository.loadArtifact(ctx, tx, previousID); err != nil {
		return State{}, err
	}
	if state.LastKnownGood, err = repository.loadArtifact(ctx, tx, lkgID); err != nil {
		return State{}, err
	}
	if state.Candidate, err = repository.loadArtifact(ctx, tx, candidateID); err != nil {
		return State{}, err
	}
	return state, nil
}

func (repository *PostgresRepository) registerArtifact(ctx context.Context, tx Tx, command Command) (*Artifact, error) {
	artifact := cloneArtifact(command.Artifact)
	metadata, err := json.Marshal(artifact.Metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid artifact metadata", ErrInvalidCommand)
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO ops_component_versions
  (component,environment,source_sha,image_digest,manifest_digest,registered_at,retain_until,retention_reason,metadata)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (component,environment,source_sha,image_digest,manifest_digest) DO NOTHING RETURNING id`,
		command.Component, command.Environment, artifact.SourceSHA, artifact.ImageDigest, artifact.ManifestDigest,
		artifact.RegisteredAt.UTC(), artifact.RetainUntil.UTC(), artifact.RetentionReason, metadata,
	).Scan(&artifact.ID)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT id,registered_at,retain_until,retention_reason,metadata
FROM ops_component_versions
WHERE component=$1 AND environment=$2 AND source_sha=$3 AND image_digest=$4 AND manifest_digest=$5`,
			command.Component, command.Environment, artifact.SourceSHA, artifact.ImageDigest, artifact.ManifestDigest,
		).Scan(&artifact.ID, &artifact.RegisteredAt, &artifact.RetainUntil, &artifact.RetentionReason, &metadata)
		if err == nil {
			_ = json.Unmarshal(metadata, &artifact.Metadata)
		}
	}
	if err != nil {
		return nil, repositoryError(err)
	}
	return artifact, nil
}

func (repository *PostgresRepository) appendTransition(ctx context.Context, tx Tx, command Command, inputHash string, before, after State) error {
	metadata, err := json.Marshal(command.Metadata)
	if err != nil {
		return fmt.Errorf("%w: invalid transition metadata", ErrInvalidCommand)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ops_component_version_transitions
  (component,environment,idempotency_key,input_hash,action,expected_revision,resulting_revision,
   before_current_id,before_previous_id,before_lkg_id,before_candidate_id,
   after_current_id,after_previous_id,after_lkg_id,after_candidate_id,actor,reason,occurred_at,metadata)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
		command.Component, command.Environment, command.IdempotencyKey, inputHash, command.Action,
		command.ExpectedRevision, after.Revision,
		artifactID(before.Current), artifactID(before.Previous), artifactID(before.LastKnownGood), artifactID(before.Candidate),
		artifactID(after.Current), artifactID(after.Previous), artifactID(after.LastKnownGood), artifactID(after.Candidate),
		command.Actor, command.Reason, command.OccurredAt.UTC(), metadata,
	)
	return repositoryError(err)
}

func (repository *PostgresRepository) loadReplay(ctx context.Context, tx Tx, command Command, inputHash string) (State, bool, error) {
	var storedHash string
	var currentID, previousID, lkgID, candidateID sql.NullInt64
	state := State{Component: command.Component, Environment: command.Environment}
	err := tx.QueryRowContext(ctx, `SELECT input_hash,resulting_revision,after_current_id,after_previous_id,after_lkg_id,after_candidate_id,occurred_at
FROM ops_component_version_transitions
WHERE component=$1 AND environment=$2 AND idempotency_key=$3`, command.Component, command.Environment, command.IdempotencyKey).Scan(
		&storedHash, &state.Revision, &currentID, &previousID, &lkgID, &candidateID, &state.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, repositoryError(err)
	}
	if storedHash != inputHash {
		return State{}, false, ErrIdempotencyConflict
	}
	if state.Current, err = repository.loadArtifact(ctx, tx, currentID); err != nil {
		return State{}, false, err
	}
	if state.Previous, err = repository.loadArtifact(ctx, tx, previousID); err != nil {
		return State{}, false, err
	}
	if state.LastKnownGood, err = repository.loadArtifact(ctx, tx, lkgID); err != nil {
		return State{}, false, err
	}
	if state.Candidate, err = repository.loadArtifact(ctx, tx, candidateID); err != nil {
		return State{}, false, err
	}
	return state, true, nil
}

func (repository *PostgresRepository) loadArtifact(ctx context.Context, tx Tx, id sql.NullInt64) (*Artifact, error) {
	if !id.Valid {
		return nil, nil
	}
	artifact := &Artifact{ID: id.Int64}
	var metadata []byte
	err := tx.QueryRowContext(ctx, `SELECT source_sha,image_digest,manifest_digest,registered_at,retain_until,retention_reason,metadata
FROM ops_component_versions WHERE id=$1`, id.Int64).Scan(
		&artifact.SourceSHA, &artifact.ImageDigest, &artifact.ManifestDigest, &artifact.RegisteredAt,
		&artifact.RetainUntil, &artifact.RetentionReason, &metadata,
	)
	if err != nil {
		return nil, repositoryError(err)
	}
	if err = json.Unmarshal(metadata, &artifact.Metadata); err != nil {
		return nil, repositoryError(err)
	}
	return artifact, nil
}

var scopePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
var sourcePattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validateCommand(command Command) error {
	if len(command.Component) > 128 || len(command.Environment) > 64 ||
		!scopePattern.MatchString(command.Component) || !scopePattern.MatchString(command.Environment) ||
		command.IdempotencyKey == "" || len(command.IdempotencyKey) > 256 || command.ExpectedRevision < 0 ||
		strings.TrimSpace(command.Actor) == "" || len(command.Actor) > 256 ||
		strings.TrimSpace(command.Reason) == "" || len(command.Reason) > 2048 || command.OccurredAt.IsZero() {
		return ErrInvalidCommand
	}
	switch command.Action {
	case SetCandidate:
		if command.Artifact == nil || command.Artifact.ID != 0 ||
			!sourcePattern.MatchString(command.Artifact.SourceSHA) ||
			!digestPattern.MatchString(command.Artifact.ImageDigest) ||
			!digestPattern.MatchString(command.Artifact.ManifestDigest) ||
			command.Artifact.RegisteredAt.IsZero() || command.Artifact.RetainUntil.Before(command.Artifact.RegisteredAt) ||
			command.Artifact.RegisteredAt.After(command.OccurredAt) || command.Artifact.RetainUntil.Before(command.OccurredAt) ||
			strings.TrimSpace(command.Artifact.RetentionReason) == "" {
			return ErrInvalidCommand
		}
	case PromoteCandidate, MarkLKG, RollbackToLKG:
		if command.Artifact != nil {
			return ErrInvalidCommand
		}
	default:
		return ErrInvalidCommand
	}
	return nil
}

func hashCommand(command Command) (string, error) {
	encoded, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func artifactID(artifact *Artifact) any {
	if artifact == nil {
		return nil
	}
	return artifact.ID
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

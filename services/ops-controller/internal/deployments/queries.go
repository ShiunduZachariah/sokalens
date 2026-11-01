// Package deployments provides read-only projections over ops_deployments for
// the operator read commands.
//
// It is a new package rather than an addition to internal/versions: that
// package owns the ops_component_versions* registry (digest-pinned deployable
// artifacts and their candidate/LKG transitions), a different table family and
// a different concern. ops_deployments is deployment lifecycle state and had
// no Go owner at all. internal/incidents keeps its own narrow deployment read
// for CorrelateDeployment because that is an incident-centric question; the
// /deploys listing is a deployment-centric one and belongs here.
//
// Everything in this file is a bounded SELECT. There are no writes and no
// schema changes.
package deployments

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrRepositoryUnavailable mirrors the sentinel used by the other storage
// packages in this service.
var ErrRepositoryUnavailable = errors.New("deployment repository unavailable")

// Queryer is a read-only, non-transactional seam over *sql.DB.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (Rows, error)
}

// Rows is the bounded multi-row cursor contract; *sql.Rows satisfies it.
type Rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

// SQLQueryer adapts *sql.DB to Queryer.
type SQLQueryer struct{ DB *sql.DB }

func (q SQLQueryer) QueryContext(ctx context.Context, query string, args ...any) (Rows, error) {
	if q.DB == nil {
		return nil, ErrRepositoryUnavailable
	}
	return q.DB.QueryContext(ctx, query, args...)
}

const (
	// DefaultListLimit and MaxListLimit bound the listing. The caller renders
	// a handful and reports a remainder.
	DefaultListLimit = 20
	MaxListLimit     = 50
)

// RollbackState describes where a deployment sits in its service's chain. It
// is derived only from columns that exist: the row's own status, and whether a
// later deployment names it as its predecessor.
type RollbackState string

const (
	// RollbackNone: the deployment is the current head of its chain and was
	// not itself rolled back.
	RollbackNone RollbackState = "none"
	// RollbackRolledBack: ops_deployments.status is 'rolled_back'.
	RollbackRolledBack RollbackState = "rolled_back"
	// RollbackSuperseded: a later deployment points at this one via
	// previous_deployment_id, so it is no longer the head.
	RollbackSuperseded RollbackState = "superseded"
)

// Deployment is one row of the /deploys listing.
type Deployment struct {
	ID          string
	Key         string
	Type        string
	Environment string
	Service     string
	// SourceCommit is the deployed SHA; PreviousCommit is the SHA of the
	// deployment this one replaced, empty when previous_deployment_id is null.
	SourceCommit   string
	PreviousCommit string
	// DeployClass comes from the additive ops_deployments.deploy_class column
	// added in migration 000059. It is empty when unset.
	DeployClass   string
	Status        string
	InitiatedBy   string
	StartedAt     time.Time
	Completed     bool
	CompletedAt   time.Time
	Age           time.Duration
	RollbackState RollbackState
}

// Listing is the /deploys projection.
type Listing struct {
	AsOf        time.Time
	Environment string
	Deployments []Deployment
	// Truncated reports that the limit was reached and older deployments exist.
	Truncated bool
}

// recentDeploymentsSQL bounds the scan with LIMIT inside the CTE, then decorates
// only those rows. An empty $2 means "all environments".
const recentDeploymentsSQL = `WITH recent AS (
  SELECT d.id, d.deployment_key, d.deployment_type, d.environment, d.service,
         d.source_commit, d.deploy_class, d.status, d.initiated_by,
         d.started_at, d.completed_at, d.previous_deployment_id
  FROM ops_deployments d
  WHERE ($2 = '' OR d.environment = $2)
  ORDER BY d.started_at DESC, d.id DESC
  LIMIT $1
)
SELECT r.id::text, r.deployment_key, r.deployment_type, r.environment, r.service,
       r.source_commit, COALESCE(p.source_commit, ''),
       COALESCE(r.deploy_class, ''), r.status, r.initiated_by,
       r.started_at,
       r.completed_at IS NOT NULL,
       COALESCE(r.completed_at, r.started_at),
       EXISTS (SELECT 1 FROM ops_deployments n WHERE n.previous_deployment_id = r.id)
FROM recent r
LEFT JOIN ops_deployments p ON p.id = r.previous_deployment_id
ORDER BY r.started_at DESC, r.id DESC`

// Queries is the read-only deployment projection API. It never writes.
type Queries struct {
	db  Queryer
	now func() time.Time
}

// NewQueries returns the read-only deployment query API. now may be nil,
// meaning time.Now; tests inject a fixed clock.
func NewQueries(db Queryer, now func() time.Time) (*Queries, error) {
	if db == nil {
		return nil, ErrRepositoryUnavailable
	}
	if now == nil {
		now = time.Now
	}
	return &Queries{db: db, now: now}, nil
}

func boundLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultListLimit
	case limit > MaxListLimit:
		return MaxListLimit
	default:
		return limit
	}
}

// Recent returns the most recent deployments, newest first, with SHA, deploy
// class, age, status, and rollback state. An empty environment means all
// environments. No deployments yields an empty listing, not an error.
func (q *Queries) Recent(ctx context.Context, limit int, environment string) (Listing, error) {
	if q == nil || q.db == nil {
		return Listing{}, ErrRepositoryUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Listing{}, err
	}
	bounded := boundLimit(limit)
	asOf := q.now().UTC()
	listing := Listing{
		AsOf:        asOf,
		Environment: environment,
		Deployments: make([]Deployment, 0, bounded),
	}

	rows, err := q.db.QueryContext(ctx, recentDeploymentsSQL, bounded, environment)
	if err != nil {
		return Listing{}, err
	}
	defer rows.Close()

	for rows.Next() {
		var d Deployment
		var superseded bool
		if err := rows.Scan(&d.ID, &d.Key, &d.Type, &d.Environment, &d.Service,
			&d.SourceCommit, &d.PreviousCommit, &d.DeployClass, &d.Status,
			&d.InitiatedBy, &d.StartedAt, &d.Completed, &d.CompletedAt,
			&superseded); err != nil {
			return Listing{}, err
		}
		if !d.Completed {
			d.CompletedAt = time.Time{}
		}
		if age := asOf.Sub(d.StartedAt); age > 0 {
			d.Age = age
		}
		d.RollbackState = rollbackState(d.Status, superseded)
		listing.Deployments = append(listing.Deployments, d)
	}
	if err := rows.Err(); err != nil {
		return Listing{}, err
	}
	listing.Truncated = len(listing.Deployments) == bounded
	return listing, nil
}

// rollbackState prefers the row's own terminal status over chain position: a
// deployment that was rolled back stays reported as rolled back even once a
// later deployment supersedes it.
func rollbackState(status string, superseded bool) RollbackState {
	switch {
	case status == "rolled_back":
		return RollbackRolledBack
	case superseded:
		return RollbackSuperseded
	default:
		return RollbackNone
	}
}

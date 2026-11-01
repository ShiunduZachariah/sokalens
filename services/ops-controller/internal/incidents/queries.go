package incidents

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/ops-controller/internal/alerts"
)

// Read-only incident projections backing the operator read commands.
//
// Everything in this file is a bounded SELECT. There are no writes, no schema
// changes, and no rendering: callers receive typed structs and decide how to
// display them. The queries assume migration 000059 has added the additive
// ops_incidents.reference identity column; the UUID id remains the join key.

// Queryer is a read-only, non-transactional seam over *sql.DB. It mirrors the
// database/sql-compatible DB/Tx seam used elsewhere in this package so tests
// need no driver, while keeping reads out of the write path's transactions.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (Rows, error)
	QueryRowContext(context.Context, string, ...any) Row
}

// Rows is the bounded multi-row cursor contract. It matches database/sql's
// *sql.Rows so SQLQueryer can adapt without translation.
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

func (q SQLQueryer) QueryRowContext(ctx context.Context, query string, args ...any) Row {
	if q.DB == nil {
		return errRow{err: ErrRepositoryUnavailable}
	}
	return q.DB.QueryRowContext(ctx, query, args...)
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

// ActiveStatuses is what these read paths mean by "open": an incident that has
// neither been resolved nor suppressed. It matches idx_ops_incidents_active,
// so the list and count queries stay index-backed.
var ActiveStatuses = []IncidentStatus{StatusOpen, StatusInvestigating, StatusMitigated}

const (
	// DefaultListLimit and MaxListLimit bound every list read. The Telegram
	// caller renders at most 10 and appends "+N more", so the default returns
	// enough rows to compute that remainder without an unbounded scan.
	DefaultListLimit = 25
	MaxListLimit     = 50

	// DeploymentLookback bounds how far back CorrelateDeployment will look for
	// a deployment preceding an incident's onset.
	DeploymentLookback = 24 * time.Hour

	strongCorrelationWindow   = 30 * time.Minute
	moderateCorrelationWindow = 2 * time.Hour
)

// Summary is one row of the open-incident list.
type Summary struct {
	ID              string
	Reference       int64
	Title           string
	Summary         string
	Service         string
	Environment     string
	Severity        alerts.Severity
	Status          IncidentStatus
	FirstObservedAt time.Time
	LastObservedAt  time.Time
	OccurrenceCount int64
}

// Detail is the full single-incident projection behind /incident. It repeats
// Summary's fields rather than embedding it: the struct has both a Summary
// type and a Summary column, and embedding would make detail.Summary resolve
// to the struct instead of the text.
type Detail struct {
	ID              string
	Reference       int64
	Title           string
	Summary         string
	Service         string
	Environment     string
	Classification  string
	Severity        alerts.Severity
	Status          IncidentStatus
	Source          string
	SourceEventID   string
	Fingerprint     string
	FirstObservedAt time.Time
	LastObservedAt  time.Time
	Acknowledged    bool
	AcknowledgedAt  time.Time
	Resolved        bool
	ResolvedAt      time.Time
	UpdatedAt       time.Time
	Evidence        json.RawMessage
	OccurrenceCount int64
}

// SeverityCounts is the /status severity breakdown over active incidents.
type SeverityCounts struct {
	Critical int64
	Warning  int64
	Info     int64
	Total    int64
}

// CorrelationStrength is how confidently a deployment is implicated in an
// incident's onset. See CorrelateDeployment for the rule.
type CorrelationStrength string

const (
	CorrelationNone     CorrelationStrength = "none"
	CorrelationWeak     CorrelationStrength = "weak"
	CorrelationModerate CorrelationStrength = "moderate"
	CorrelationStrong   CorrelationStrength = "strong"
)

// DeploymentRef is the minimum deployment identity an incident read needs.
// The richer /deploys listing lives in internal/deployments.
type DeploymentRef struct {
	ID           string
	Key          string
	Service      string
	Environment  string
	SourceCommit string
	Status       string
	StartedAt    time.Time
}

// DeploymentCorrelation reports the deployment in effect when an incident
// started, the deployment it replaced, and how strongly the two are linked.
type DeploymentCorrelation struct {
	IncidentID     string
	OnsetAt        time.Time
	Service        string
	Environment    string
	Current        DeploymentRef
	Previous       DeploymentRef
	HasCurrent     bool
	HasPrevious    bool
	ServiceMatched bool
	// SinceDeploy is OnsetAt minus Current.StartedAt. It is zero when
	// HasCurrent is false.
	SinceDeploy time.Duration
	Strength    CorrelationStrength
	Lookback    time.Duration
}

const (
	listOpenIncidentsSQL = `WITH active AS (
  SELECT i.id, i.reference, i.title, i.summary, i.severity, i.status,
         i.first_observed_at, i.last_observed_at, i.evidence
  FROM ops_incidents i
  WHERE i.status IN ('open', 'investigating', 'mitigated')
  ORDER BY CASE i.severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END,
           i.last_observed_at DESC,
           i.id
  LIMIT $1
)
SELECT a.id,
       COALESCE(a.reference, 0),
       a.title,
       a.summary,
       COALESCE(a.evidence->>'service', ''),
       COALESCE(a.evidence->>'environment', ''),
       a.severity,
       a.status,
       a.first_observed_at,
       a.last_observed_at,
       (SELECT COUNT(*) FROM ops_incident_observations o WHERE o.incident_id = a.id)
FROM active a
ORDER BY CASE a.severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END,
         a.last_observed_at DESC,
         a.id`

	getIncidentByReferenceSQL = `SELECT i.id,
       COALESCE(i.reference, 0),
       i.title,
       i.summary,
       COALESCE(i.evidence->>'service', ''),
       COALESCE(i.evidence->>'environment', ''),
       COALESCE(i.evidence->>'classification', ''),
       i.severity,
       i.status,
       i.source,
       i.source_event_id,
       i.fingerprint,
       i.first_observed_at,
       i.last_observed_at,
       i.acknowledged_at IS NOT NULL,
       COALESCE(i.acknowledged_at, i.first_observed_at),
       i.resolved_at IS NOT NULL,
       COALESCE(i.resolved_at, i.first_observed_at),
       i.updated_at,
       i.evidence::text,
       (SELECT COUNT(*) FROM ops_incident_observations o WHERE o.incident_id = i.id)
FROM ops_incidents i
WHERE i.reference = $1
LIMIT 1`

	countOpenBySeveritySQL = `SELECT
  COUNT(*) FILTER (WHERE severity = 'critical'),
  COUNT(*) FILTER (WHERE severity = 'warning'),
  COUNT(*) FILTER (WHERE severity = 'info'),
  COUNT(*)
FROM ops_incidents
WHERE status IN ('open', 'investigating', 'mitigated')`

	incidentOnsetSQL = `SELECT i.first_observed_at,
       COALESCE(i.evidence->>'service', ''),
       COALESCE(i.evidence->>'environment', '')
FROM ops_incidents i
WHERE i.id = $1
LIMIT 1`

	correlateDeploymentSQL = `WITH current_deploy AS (
  SELECT d.id, d.deployment_key, d.service, d.environment, d.source_commit,
         d.status, d.started_at, d.previous_deployment_id
  FROM ops_deployments d
  WHERE d.started_at <= $1
    AND d.started_at >= $2
    AND ($3 = '' OR d.service = $3)
    AND ($4 = '' OR d.environment = $4)
  ORDER BY d.started_at DESC, d.id
  LIMIT 1
)
SELECT c.id::text, c.deployment_key, c.service, c.environment, c.source_commit,
       c.status, c.started_at,
       p.id IS NOT NULL,
       COALESCE(p.id::text, ''), COALESCE(p.deployment_key, ''),
       COALESCE(p.service, ''), COALESCE(p.environment, ''),
       COALESCE(p.source_commit, ''), COALESCE(p.status, ''),
       COALESCE(p.started_at, c.started_at)
FROM current_deploy c
LEFT JOIN ops_deployments p ON p.id = c.previous_deployment_id`
)

// Queries is the read-only incident projection API. It never writes.
type Queries struct{ db Queryer }

// NewQueries returns the read-only incident query API.
func NewQueries(db Queryer) (*Queries, error) {
	if db == nil {
		return nil, ErrRepositoryUnavailable
	}
	return &Queries{db: db}, nil
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

// ListOpen returns active incidents ordered most severe first, then most
// recently observed, with an occurrence count taken from
// ops_incident_observations. The limit is clamped to MaxListLimit so a caller
// can never ask for an unbounded scan.
func (q *Queries) ListOpen(ctx context.Context, limit int) ([]Summary, error) {
	if q == nil || q.db == nil {
		return nil, ErrRepositoryUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := q.db.QueryContext(ctx, listOpenIncidentsSQL, boundLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	summaries := make([]Summary, 0, boundLimit(limit))
	for rows.Next() {
		var s Summary
		var severity, status string
		if err := rows.Scan(&s.ID, &s.Reference, &s.Title, &s.Summary, &s.Service,
			&s.Environment, &severity, &status, &s.FirstObservedAt,
			&s.LastObservedAt, &s.OccurrenceCount); err != nil {
			return nil, err
		}
		s.Severity = alerts.Severity(severity)
		s.Status = IncidentStatus(status)
		summaries = append(summaries, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return summaries, nil
}

// GetByReference resolves one incident by its short operator reference (the
// additive ops_incidents.reference identity column, rendered as "#143"). A
// reference that matches no row returns ErrIncidentNotFound, which the caller
// can render without distinguishing "never existed" from "purged".
func (q *Queries) GetByReference(ctx context.Context, reference int64) (Detail, error) {
	if q == nil || q.db == nil {
		return Detail{}, ErrRepositoryUnavailable
	}
	if err := ctx.Err(); err != nil {
		return Detail{}, err
	}
	if reference <= 0 {
		return Detail{}, ErrIncidentNotFound
	}

	var detail Detail
	var severity, status, evidence string
	err := q.db.QueryRowContext(ctx, getIncidentByReferenceSQL, reference).Scan(
		&detail.ID, &detail.Reference, &detail.Title, &detail.Summary,
		&detail.Service, &detail.Environment, &detail.Classification,
		&severity, &status, &detail.Source, &detail.SourceEventID,
		&detail.Fingerprint, &detail.FirstObservedAt, &detail.LastObservedAt,
		&detail.Acknowledged, &detail.AcknowledgedAt,
		&detail.Resolved, &detail.ResolvedAt, &detail.UpdatedAt,
		&evidence, &detail.OccurrenceCount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Detail{}, ErrIncidentNotFound
		}
		return Detail{}, err
	}
	detail.Severity = alerts.Severity(severity)
	detail.Status = IncidentStatus(status)
	if evidence != "" {
		detail.Evidence = json.RawMessage(evidence)
	}
	if !detail.Acknowledged {
		detail.AcknowledgedAt = time.Time{}
	}
	if !detail.Resolved {
		detail.ResolvedAt = time.Time{}
	}
	return detail, nil
}

// CountOpenBySeverity returns the active-incident breakdown for the /status
// line. It is a single aggregate over the partial active index.
func (q *Queries) CountOpenBySeverity(ctx context.Context) (SeverityCounts, error) {
	if q == nil || q.db == nil {
		return SeverityCounts{}, ErrRepositoryUnavailable
	}
	if err := ctx.Err(); err != nil {
		return SeverityCounts{}, err
	}
	var counts SeverityCounts
	if err := q.db.QueryRowContext(ctx, countOpenBySeveritySQL).Scan(
		&counts.Critical, &counts.Warning, &counts.Info, &counts.Total); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SeverityCounts{}, nil
		}
		return SeverityCounts{}, err
	}
	return counts, nil
}

// CorrelateDeployment reports the deployment that was in effect when an
// incident started, plus the deployment it replaced.
//
// The rule, deliberately simple so an operator can check it by hand:
//
//  1. Take the incident's first_observed_at as the onset. There is no
//     deployment SHA on the incident, so correlation is by time only.
//  2. Candidates are ops_deployments rows started at or before the onset and
//     no earlier than DeploymentLookback before it. When the incident's
//     evidence carries a service (and environment), candidates are restricted
//     to it.
//  3. The current deployment is the latest candidate; the previous one is
//     whatever its previous_deployment_id points at.
//  4. Strength is the gap between the deploy and the onset: strong within 30
//     minutes, moderate within 2 hours, weak within the lookback, none if
//     there is no candidate.
//  5. If the incident carries no service in its evidence, the match is not
//     service-scoped and the strength is capped at weak, however close the
//     deploy was. Coincidence in time across unrelated services is not
//     evidence.
//
// A missing incident returns ErrIncidentNotFound. An incident with no
// deployment in the window returns a correlation with HasCurrent false and
// CorrelationNone, not an error: "nothing deployed" is a real answer.
func (q *Queries) CorrelateDeployment(ctx context.Context, incidentID string) (DeploymentCorrelation, error) {
	if q == nil || q.db == nil {
		return DeploymentCorrelation{}, ErrRepositoryUnavailable
	}
	if err := ctx.Err(); err != nil {
		return DeploymentCorrelation{}, err
	}
	if incidentID == "" {
		return DeploymentCorrelation{}, ErrIncidentNotFound
	}

	correlation := DeploymentCorrelation{
		IncidentID: incidentID,
		Strength:   CorrelationNone,
		Lookback:   DeploymentLookback,
	}
	err := q.db.QueryRowContext(ctx, incidentOnsetSQL, incidentID).Scan(
		&correlation.OnsetAt, &correlation.Service, &correlation.Environment)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DeploymentCorrelation{}, ErrIncidentNotFound
		}
		return DeploymentCorrelation{}, err
	}

	var current, previous DeploymentRef
	var hasPrevious bool
	err = q.db.QueryRowContext(ctx, correlateDeploymentSQL,
		correlation.OnsetAt,
		correlation.OnsetAt.Add(-DeploymentLookback),
		correlation.Service,
		correlation.Environment,
	).Scan(
		&current.ID, &current.Key, &current.Service, &current.Environment,
		&current.SourceCommit, &current.Status, &current.StartedAt,
		&hasPrevious, &previous.ID, &previous.Key, &previous.Service,
		&previous.Environment, &previous.SourceCommit, &previous.Status,
		&previous.StartedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return correlation, nil
		}
		return DeploymentCorrelation{}, err
	}

	correlation.Current = current
	correlation.HasCurrent = true
	correlation.ServiceMatched = correlation.Service != "" && current.Service == correlation.Service
	if hasPrevious {
		correlation.Previous = previous
		correlation.HasPrevious = true
	}
	correlation.SinceDeploy = correlation.OnsetAt.Sub(current.StartedAt)
	correlation.Strength = correlationStrength(correlation.SinceDeploy, correlation.ServiceMatched)
	return correlation, nil
}

func correlationStrength(since time.Duration, serviceMatched bool) CorrelationStrength {
	switch {
	case since < 0 || since > DeploymentLookback:
		return CorrelationNone
	case !serviceMatched:
		return CorrelationWeak
	case since <= strongCorrelationWindow:
		return CorrelationStrong
	case since <= moderateCorrelationWindow:
		return CorrelationModerate
	default:
		return CorrelationWeak
	}
}

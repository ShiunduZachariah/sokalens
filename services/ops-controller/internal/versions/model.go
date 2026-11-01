// Package versions provides the PostgreSQL-authoritative registry for exact,
// digest-pinned deployable component versions. It selects versions but has no
// deployment, traffic-shifting, or rollback execution authority.
package versions

import (
	"errors"
	"time"
)

type Action string

const (
	SetCandidate     Action = "set_candidate"
	PromoteCandidate Action = "promote_candidate"
	MarkLKG          Action = "mark_lkg"
	RollbackToLKG    Action = "rollback_to_lkg"
)

// Artifact is immutable release identity. ImageDigest and ManifestDigest are
// OCI-style sha256 digests; SourceSHA is an exact 40- or 64-character hex SHA.
type Artifact struct {
	ID              int64
	SourceSHA       string
	ImageDigest     string
	ManifestDigest  string
	RegisteredAt    time.Time
	RetainUntil     time.Time
	RetentionReason string
	Metadata        map[string]string
}

type State struct {
	Component     string
	Environment   string
	Revision      int64
	Current       *Artifact
	Previous      *Artifact
	LastKnownGood *Artifact
	Candidate     *Artifact
	UpdatedAt     time.Time
}

// Command is a compare-and-swap transition. ExpectedRevision protects callers
// from acting on stale version evidence; IdempotencyKey makes exact retries safe.
type Command struct {
	Component        string
	Environment      string
	IdempotencyKey   string
	Action           Action
	ExpectedRevision int64
	Artifact         *Artifact
	Actor            string
	Reason           string
	OccurredAt       time.Time
	Metadata         map[string]string
}

type Result struct {
	State   State
	Applied bool
}

var (
	ErrInvalidCommand        = errors.New("versions: invalid command")
	ErrRepositoryUnavailable = errors.New("versions: repository unavailable")
	ErrIdempotencyConflict   = errors.New("versions: idempotency conflict")
	ErrStaleRevision         = errors.New("versions: stale expected revision")
	ErrMissingCandidate      = errors.New("versions: candidate is required")
	ErrMissingLKG            = errors.New("versions: last-known-good version is required")
	ErrNoStateChange         = errors.New("versions: transition would not change state")
)

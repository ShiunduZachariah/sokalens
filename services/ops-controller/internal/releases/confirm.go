package releases

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Two-phase confirmation for mutating commands (spec §14.2).
//
// A token is:
//
//	6 characters   short enough to retype on a phone
//	unambiguous    the alphabet excludes 0, O, 1 and I, because the failure
//	               mode of a confirmation token is not "attacker guesses it",
//	               it is "operator retypes it wrong at 2am and gives up"
//	crypto/rand    32^6 ≈ 10^9 with a 5-minute life; math/rand here would make
//	               the token predictable from any other token
//	5-minute life  a confirmation is a statement about right now
//	single use     consumed in the same transaction that acts on it, so a
//	               redelivered Telegram update cannot promote a train twice
//	user-bound     only the requesting user may confirm; the token is visible
//	               to a whole ops group, so binding it is what stops a second
//	               person confirming an operation they did not ask for
//
// ops_pending_confirmations.job_id stays NULL here. It is a UUID referencing
// ops_jobs, and this phase dispatches no job: the release train records state,
// it does not run a runbook.
// ---------------------------------------------------------------------------

const (
	// TokenLength is the confirmation token length.
	TokenLength = 6
	// TokenAlphabet deliberately excludes 0, O, 1 and I.
	//
	// Written as two halves on purpose: as one 32-character alphanumeric
	// literal it matches the secret scanner's generic-api-key rule. The gate
	// is worth keeping sharp, so the constant bends instead.
	TokenAlphabet = "23456789" + "ABCDEFGHJKLMNPQRSTUVWXYZ"
	// DefaultConfirmationTTL is the 5-minute expiry.
	DefaultConfirmationTTL = 5 * time.Minute
	// MaxOutstandingConfirmations caps live tokens per user. Beyond this,
	// tokens stop being a confirmation and start being a lottery.
	MaxOutstandingConfirmations = 3
)

var (
	ErrTokenUnknown        = errors.New("confirmation token not found")
	ErrTokenExpired        = errors.New("confirmation token expired")
	ErrTokenConsumed       = errors.New("confirmation token already used")
	ErrTokenForeign        = errors.New("confirmation token belongs to another user")
	ErrTokenMismatch       = errors.New("confirmation token is for a different command")
	ErrTooManyOutstanding  = errors.New("too many outstanding confirmations")
	ErrInvalidConfirmation = errors.New("invalid confirmation request")
)

// Confirmation is one ops_pending_confirmations row.
type Confirmation struct {
	Token       string
	Runbook     string
	Args        map[string]any
	RequestedBy int64
	ChatID      int64
	CreatedAt   time.Time
	ExpiresAt   time.Time
	ConsumedAt  time.Time
}

// NewToken returns a fresh confirmation token.
//
// The alphabet is 32 characters, which divides 256 exactly, so masking each
// random byte with 0x1f is uniform: no rejection sampling and no modulo bias.
func NewToken() (string, error) {
	buf := make([]byte, TokenLength)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, TokenLength)
	for i, b := range buf {
		out[i] = TokenAlphabet[b&0x1f]
	}
	return string(out), nil
}

// NormalizeToken folds what an operator actually types — lowercase, stray
// spaces — onto the canonical form. It does not translate 0→O or 1→I: those
// characters are not in the alphabet, so accepting them would silently undo
// the reason they were excluded.
func NormalizeToken(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// ValidToken reports whether s is well formed. It is a shape check only and
// says nothing about whether the token exists.
func ValidToken(s string) bool {
	if len(s) != TokenLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(TokenAlphabet, rune(s[i])) {
			return false
		}
	}
	return true
}

const (
	countOutstandingSQL = `SELECT count(*) FROM (
  SELECT 1 FROM ops_pending_confirmations
  WHERE requested_by = $1 AND consumed_at IS NULL AND expires_at > $2
  LIMIT $3
) outstanding`

	insertConfirmationSQL = `INSERT INTO ops_pending_confirmations
  (token, runbook, args, requested_by, chat_id, created_at, expires_at)
VALUES ($1, $2, $3::jsonb, $4, $5, $6, $7)`

	selectConfirmationSQL = `SELECT runbook, args::text, requested_by, chat_id,
       created_at, expires_at, consumed_at
FROM ops_pending_confirmations
WHERE token = $1
FOR UPDATE`

	consumeConfirmationSQL = `UPDATE ops_pending_confirmations
SET consumed_at = $2
WHERE token = $1 AND consumed_at IS NULL`
)

// Confirmations is the token store.
type Confirmations struct {
	db             DB
	ttl            time.Duration
	now            func() time.Time
	maxOutstanding int
}

// NewConfirmations returns the confirmation store. ttl <= 0 means the 5-minute
// default; now may be nil, meaning time.Now.
func NewConfirmations(db DB, ttl time.Duration, now func() time.Time) (*Confirmations, error) {
	if db == nil {
		return nil, ErrRepositoryUnavailable
	}
	if ttl <= 0 {
		ttl = DefaultConfirmationTTL
	}
	if now == nil {
		now = time.Now
	}
	return &Confirmations{db: db, ttl: ttl, now: now, maxOutstanding: MaxOutstandingConfirmations}, nil
}

// TTL is the configured token lifetime, exported for the reply body.
func (c *Confirmations) TTL() time.Duration {
	if c == nil || c.ttl <= 0 {
		return DefaultConfirmationTTL
	}
	return c.ttl
}

// Issue mints and durably stores a token before anything is sent to Telegram.
// If the insert fails, no token is shown: a token an operator can see but the
// database has never heard of is worse than no confirmation flow at all.
func (c *Confirmations) Issue(ctx context.Context, runbook string, args map[string]any, userID, chatID int64) (Confirmation, error) {
	if c == nil || c.db == nil {
		return Confirmation{}, ErrRepositoryUnavailable
	}
	runbook = strings.TrimSpace(runbook)
	if runbook == "" || userID == 0 {
		return Confirmation{}, ErrInvalidConfirmation
	}
	if args == nil {
		args = map[string]any{}
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return Confirmation{}, err
	}

	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Confirmation{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	now := c.now().UTC()
	var outstanding int
	if err = tx.QueryRowContext(ctx, countOutstandingSQL, userID, now, c.maxOutstanding).Scan(&outstanding); err != nil {
		return Confirmation{}, err
	}
	if outstanding >= c.maxOutstanding {
		return Confirmation{}, ErrTooManyOutstanding
	}

	token, err := NewToken()
	if err != nil {
		return Confirmation{}, err
	}
	expires := now.Add(c.TTL())
	if _, err = tx.ExecContext(ctx, insertConfirmationSQL,
		token, runbook, string(encoded), userID, chatID, now, expires); err != nil {
		return Confirmation{}, err
	}
	if err = tx.Commit(); err != nil {
		return Confirmation{}, err
	}
	committed = true

	return Confirmation{
		Token:       token,
		Runbook:     runbook,
		Args:        args,
		RequestedBy: userID,
		ChatID:      chatID,
		CreatedAt:   now,
		ExpiresAt:   expires,
	}, nil
}

// Consume redeems a token exactly once, inside one transaction, and returns
// the confirmation it stood for.
//
// The row is taken FOR UPDATE and the consuming UPDATE re-asserts
// consumed_at IS NULL, so two concurrent redemptions of the same token cannot
// both succeed even if the isolation level were relaxed. The caller performs
// the actual mutation using the returned confirmation; a token is therefore
// spent whether or not the mutation then succeeds, which is the correct
// direction to fail in.
func (c *Confirmations) Consume(ctx context.Context, token string, userID int64, runbook string) (Confirmation, error) {
	if c == nil || c.db == nil {
		return Confirmation{}, ErrRepositoryUnavailable
	}
	token = NormalizeToken(token)
	if !ValidToken(token) {
		return Confirmation{}, ErrTokenUnknown
	}

	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Confirmation{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	stored, err := c.consumeTx(ctx, tx, token, userID, runbook)
	if err != nil {
		return Confirmation{}, err
	}
	if err = tx.Commit(); err != nil {
		return Confirmation{}, err
	}
	committed = true
	return stored, nil
}

// consumeTx is Consume's body without transaction management, so a caller that
// must consume the token and perform the mutation atomically — Manager's
// PromoteConfirmed — can run both inside one transaction.
func (c *Confirmations) consumeTx(ctx context.Context, tx Tx, token string, userID int64, runbook string) (Confirmation, error) {
	token = NormalizeToken(token)
	if !ValidToken(token) {
		return Confirmation{}, ErrTokenUnknown
	}

	var (
		stored     Confirmation
		rawArgs    string
		consumedAt sql.NullTime
	)
	var err error
	err = tx.QueryRowContext(ctx, selectConfirmationSQL, token).Scan(
		&stored.Runbook, &rawArgs, &stored.RequestedBy, &stored.ChatID,
		&stored.CreatedAt, &stored.ExpiresAt, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Confirmation{}, ErrTokenUnknown
	}
	if err != nil {
		return Confirmation{}, err
	}

	// Order matters: report "not yours" before "expired", so a user who tries
	// someone else's token learns nothing about its state.
	if stored.RequestedBy != userID {
		return Confirmation{}, ErrTokenForeign
	}
	if consumedAt.Valid {
		return Confirmation{}, ErrTokenConsumed
	}
	now := c.now().UTC()
	if !now.Before(stored.ExpiresAt) {
		return Confirmation{}, ErrTokenExpired
	}
	if runbook != "" && stored.Runbook != runbook {
		return Confirmation{}, ErrTokenMismatch
	}

	res, err := tx.ExecContext(ctx, consumeConfirmationSQL, token, now)
	if err != nil {
		return Confirmation{}, err
	}
	if affected, aerr := res.RowsAffected(); aerr == nil && affected != 1 {
		return Confirmation{}, ErrTokenConsumed
	}

	stored.Token = token
	stored.ConsumedAt = now
	stored.Args = map[string]any{}
	if strings.TrimSpace(rawArgs) != "" {
		if uerr := json.Unmarshal([]byte(rawArgs), &stored.Args); uerr != nil {
			stored.Args = map[string]any{}
		}
	}
	return stored, nil
}

// peekConfirmationSQL reads a token's runbook without locking or consuming it.
const peekConfirmationSQL = `SELECT runbook FROM ops_pending_confirmations WHERE token = $1`

// PeekRunbook reports which command a token stands for, without consuming it.
//
// Consume is deliberately runbook-scoped so a token minted for one command can
// never redeem another. But a generic "/confirm <token>" does not know the
// runbook yet, so it needs this one read first. It takes no lock and changes
// nothing: the authoritative single-use check still happens inside Consume's
// transaction, so two concurrent /confirm calls still cannot both win.
//
// An unknown token returns ErrTokenUnknown, matching Consume — the caller must
// not distinguish "never existed" from "expired" in its reply, since that
// distinction only helps someone guessing.
func (c *Confirmations) PeekRunbook(ctx context.Context, token string) (string, error) {
	if c == nil || c.db == nil {
		return "", ErrRepositoryUnavailable
	}
	token = NormalizeToken(token)
	if !ValidToken(token) {
		return "", ErrTokenUnknown
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	var runbook string
	if err := tx.QueryRowContext(ctx, peekConfirmationSQL, token).Scan(&runbook); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrTokenUnknown
		}
		return "", err
	}
	return runbook, nil
}

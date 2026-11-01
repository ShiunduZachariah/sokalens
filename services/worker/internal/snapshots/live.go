package snapshots

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// LiveSnapshot is one observation of a match in play.
//
// Pointers where the provider may genuinely not know. A zero would be a
// different claim: "nil elapsed" means the feed did not say, while "0" would
// mean kickoff. The in-play model eventually trains on these rows, so a column
// that is confidently wrong is worse than one that is honestly empty.
type LiveSnapshot struct {
	FixtureID         int64
	Provider          string
	CapturedAt        time.Time
	ProviderTimestamp *time.Time

	Status        string
	Period        string
	ElapsedMinute *int
	ElapsedExtra  *int

	HomeGoals *int
	AwayGoals *int

	// Red cards are not carried by the bulk live endpoint. They stay nil until
	// something that observes cards writes them.
	HomeRedCards *int
	AwayRedCards *int
}

// Hash is the change-detection key (§4.3).
//
// It covers exactly the observed fields and deliberately excludes CapturedAt:
// hashing the capture time would make every snapshot unique and defeat the
// whole mechanism. Nil is encoded distinctly from zero, so a feed that stops
// reporting a value registers as a change rather than as "still zero".
func (s LiveSnapshot) Hash() string {
	digest := sha256.New()
	write := func(parts ...string) {
		for _, part := range parts {
			digest.Write([]byte(part))
			digest.Write([]byte{0})
		}
	}
	write(strconv.FormatInt(s.FixtureID, 10), s.Provider, strings.ToLower(s.Status), strings.ToLower(s.Period))
	for _, value := range []*int{s.ElapsedMinute, s.ElapsedExtra, s.HomeGoals, s.AwayGoals, s.HomeRedCards, s.AwayRedCards} {
		if value == nil {
			write("~")
			continue
		}
		write(strconv.Itoa(*value))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// previousSnapshotWindow bounds the change-detection lookup to the partitions
// that can plausibly hold the previous observation of a match now in play.
const previousSnapshotWindow = 24 * time.Hour

// Repository appends live snapshots.
type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) Repository { return Repository{db: db} }

// Append writes a snapshot unless it is byte-identical to the previous one for
// that fixture.
//
// The skip is a WRITE-side decision only. The fetch that produced this
// observation still happened and still counted, because a poll returning
// unchanged data is exactly how the system learns the match is still being
// watched -- suppressing the request would recreate the staleness the freshness
// work exists to detect. What it saves is rows and index depth on quiet
// matches, which §4.3 puts at roughly a third of write volume.
//
// Returns whether a row was written.
func (r Repository) Append(ctx context.Context, snapshot LiveSnapshot) (bool, error) {
	if r.db == nil {
		return false, errors.New("snapshot DB is nil")
	}
	if snapshot.FixtureID <= 0 {
		return false, errors.New("snapshot requires a positive fixture id")
	}
	if strings.TrimSpace(snapshot.Status) == "" {
		return false, errors.New("snapshot requires a status")
	}
	if snapshot.CapturedAt.IsZero() {
		return false, errors.New("snapshot requires a capture time")
	}

	hash := snapshot.Hash()
	var previous sql.NullString
	// The captured_at bound is not a filter on correctness -- it is partition
	// pruning, and it earns its place on one specific case.
	//
	// Measured against fourteen partitions and 40k rows: for a fixture that
	// already has a recent snapshot, the bound barely matters, because the
	// ordered Append plus LIMIT 1 finds the row in the newest partition and
	// stops. For a fixture with NO recent snapshot it matters a lot -- there is
	// nothing to stop on, so every partition is scanned. 31 shared buffers
	// without the bound against 9 with it, and the gap widens with every
	// partition retained.
	//
	// That case is not rare: it is the first observation of every match, and
	// any fixture whose previous snapshot has aged into a dropped partition.
	//
	// A day is comfortably longer than any match plus its stoppages, so the
	// previous snapshot of a fixture that is in play right now is always inside
	// it. If none is found the row is written, which is the correct outcome for
	// a fixture nobody has observed in a day.
	err := r.db.QueryRowContext(ctx, `
SELECT response_hash FROM fixture_live_snapshots
WHERE fixture_id = $1 AND captured_at >= $2
ORDER BY captured_at DESC
LIMIT 1`, snapshot.FixtureID, snapshot.CapturedAt.UTC().Add(-previousSnapshotWindow)).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read previous snapshot hash: %w", err)
	}
	if previous.Valid && previous.String == hash {
		return false, nil
	}

	_, err = r.db.ExecContext(ctx, `
INSERT INTO fixture_live_snapshots (
  fixture_id, provider, captured_at, provider_timestamp,
  status, period, elapsed_minute, elapsed_extra,
  home_goals, away_goals, home_red_cards, away_red_cards, response_hash
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		snapshot.FixtureID,
		snapshot.Provider,
		snapshot.CapturedAt.UTC(),
		nullableTime(snapshot.ProviderTimestamp),
		snapshot.Status,
		nullableText(snapshot.Period),
		nullableInt(snapshot.ElapsedMinute),
		nullableInt(snapshot.ElapsedExtra),
		nullableInt(snapshot.HomeGoals),
		nullableInt(snapshot.AwayGoals),
		nullableInt(snapshot.HomeRedCards),
		nullableInt(snapshot.AwayRedCards),
		hash,
	)
	if err != nil {
		return false, fmt.Errorf("append live snapshot: %w", err)
	}
	return true, nil
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullableTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC()
}

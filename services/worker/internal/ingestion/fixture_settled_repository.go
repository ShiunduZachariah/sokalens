package ingestion

import (
	"context"
	"database/sql"
)

// FixtureSettledRepository answers whether a fixture has finished.
//
// Deliberately generous about what counts as finished. A fixture that was
// abandoned, awarded or cancelled is as final as one that played out, and its
// statistics -- however sparse -- will not change either. Treating only 'FT'
// as terminal would leave those rows being re-read forever.
type FixtureSettledRepository struct {
	db *sql.DB
}

func NewFixtureSettledRepository(db *sql.DB) FixtureSettledRepository {
	return FixtureSettledRepository{db: db}
}

// settledFixtureSQL reads the projection the rest of ingestion writes to.
//
// finished_at is the authority rather than the status string: statuses arrive
// from the provider in several spellings and casings, and a fixture with a
// finish time is finished whatever it is currently labelled.
const settledFixtureSQL = `
SELECT COALESCE(
         finished_at IS NOT NULL
         OR lower(status) IN ('ft','aet','pen','finished','awarded','walkover','abandoned','cancelled','canceled'),
         false)
FROM fixtures
WHERE id = $1`

func (r FixtureSettledRepository) Settled(ctx context.Context, fixtureID int64) (bool, error) {
	if r.db == nil {
		return false, nil
	}
	var settled bool
	err := r.db.QueryRowContext(ctx, settledFixtureSQL, fixtureID).Scan(&settled)
	if err != nil {
		if err == sql.ErrNoRows {
			// A fixture we have statistics for but no row for is not something
			// to declare immutable on a guess.
			return false, nil
		}
		return false, err
	}
	return settled, nil
}

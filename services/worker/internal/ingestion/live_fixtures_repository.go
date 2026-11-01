package ingestion

import (
	"context"
	"database/sql"
	"errors"
)

// LiveFixturesRepository answers "which matches are in play right now", in the
// provider's ids, for the batched in-play statistics poll.
type LiveFixturesRepository struct {
	db *sql.DB
}

func NewLiveFixturesRepository(db *sql.DB) LiveFixturesRepository {
	return LiveFixturesRepository{db: db}
}

// DefaultLiveFixtureLimit caps one poll's coverage.
//
// 300 against a stated weekend peak of "over 200 concurrent" -- headroom for a
// busy Saturday without an unbounded query. At twenty per request that is at
// most 15 requests a minute; the cap exists so a database in a strange state
// (every fixture stuck 'live', which the match-state reaper has had to clean up
// before) cannot turn one tick into hundreds of provider calls.
const DefaultLiveFixtureLimit = 300

// LiveProviderFixtureIDs returns the API-Football fixture ids of matches
// currently in play, most recently active first.
//
// The provider id comes from provider_entity_maps and nowhere else. A live
// match with no active mapping is skipped rather than having an id derived
// from the canonical one -- they are different numbers, and treating one as
// the other is the identity mistake that split a team's history in two.
//
// Ordered by live_state_at descending so that if the limit does bite, the
// matches dropped are the least recently updated, not an arbitrary slice.
func (r LiveFixturesRepository) LiveProviderFixtureIDs(ctx context.Context, limit int) ([]int64, error) {
	if r.db == nil {
		return nil, errors.New("live fixtures DB is nil")
	}
	if limit <= 0 {
		limit = DefaultLiveFixtureLimit
	}

	rows, err := r.db.QueryContext(ctx, `
SELECT map.provider_entity_id::BIGINT
FROM matches m
JOIN provider_entity_maps map
  ON map.provider = 'api-football'
 AND map.entity_type = 'match'
 AND map.retired_at IS NULL
 AND map.canonical_entity_type = 'match'
 AND map.canonical_entity_id = m.id::TEXT
WHERE m.status IN ('live', 'halftime', 'second_half', 'extra_time', 'penalties', 'break')
  -- 'interrupted' and 'suspended' are deliberately excluded. Their statistics
  -- are not changing, so polling them spends quota to re-read the same numbers,
  -- and a match that resumes returns to a live status and is picked up again.
  AND map.provider_entity_id ~ '^[0-9]+$'
ORDER BY m.live_state_at DESC NULLS LAST, m.id DESC
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

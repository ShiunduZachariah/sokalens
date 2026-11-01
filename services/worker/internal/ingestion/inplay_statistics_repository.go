package ingestion

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// InPlayStatisticsRepository appends per-team match statistics to the in-play
// time series, fixture_statistics_snapshots.
//
// SEPARATE FROM StatisticsRepository ON PURPOSE, and it deliberately does NOT
// touch fixture_statistics.
//
// It would be one line to also upsert the current-value table on every live
// poll, and it would be a bad line. fixture_statistics is what settlement
// grades corner and card markets against at full time. A live poll writing
// there puts minute-70 counts in the settlement source; that is harmless only
// for as long as the post-match statistics pass runs afterwards and overwrites
// them. The first time that pass fails or is skipped -- a provider hiccup, a
// fixture the post-match sweep missed -- every corner and card market on the
// match settles against a partial count, and "over 8.5 corners" grades as
// lost on a match that finished with eleven.
//
// So the live series is written here and read as a series; the current-value
// table stays owned by the post-match pass, and a market with no final
// statistics closes as unsettled_data_unavailable rather than being graded
// against a mid-match number. Losing a market to missing data is a cost the
// contract already accounts for. Settling it wrongly is not.
type InPlayStatisticsRepository struct {
	db statisticsDB
}

func NewInPlayStatisticsRepository(db statisticsDB) InPlayStatisticsRepository {
	return InPlayStatisticsRepository{db: db}
}

// AppendSnapshot records one team's statistics as they stood at capturedAt.
//
// matchMinute is the minute of play the counts describe and may be nil: a
// payload that arrives without a clock is still a usable series point, and a
// guessed minute is worse than none, because the ladder conditions on it.
//
// Returns false when a row for this (fixture, team, captured_at) already
// exists. Re-observing one cutoff is normal -- the poll batches twenty
// fixtures and the provider's statistics refresh about once a minute, so two
// passes routinely see the same numbers -- and the second observation is not a
// new point in the series. Nothing is overwritten: a snapshot is what we knew
// then, and rewriting it would destroy the only record of what a market
// published at that minute was conditioned on.
func (r InPlayStatisticsRepository) AppendSnapshot(
	ctx context.Context,
	fixtureID int64,
	teamID int64,
	capturedAt time.Time,
	matchMinute *int,
	stats StatRecord,
) (bool, error) {
	if r.db == nil {
		return false, errors.New("in-play statistics repository DB is nil")
	}
	if fixtureID <= 0 {
		return false, errors.New("fixture id is required")
	}
	if teamID <= 0 {
		return false, errors.New("team id is required")
	}
	if capturedAt.IsZero() {
		// A zero capture time would land every observation of every match in
		// the same primary key, so the series would hold exactly one row per
		// team for all time and silently discard the rest.
		return false, errors.New("captured_at is required")
	}

	result, err := r.db.ExecContext(ctx, `
INSERT INTO fixture_statistics_snapshots (
  fixture_id,
  team_id,
  captured_at,
  match_minute,
  shots_on_target,
  shots_total,
  shots_blocked,
  shots_inside_box,
  shots_outside_box,
  possession_pct,
  corners,
  fouls,
  offsides,
  yellow_cards,
  red_cards,
  goalkeeper_saves,
  passes_total,
  passes_accurate,
  pass_accuracy,
  xg,
  card_points,
  source
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, 'api-football-live'
)
ON CONFLICT (fixture_id, team_id, captured_at) DO NOTHING`,
		fixtureID,
		teamID,
		capturedAt.UTC(),
		matchMinute,
		stats.ShotsOnTarget,
		stats.ShotsTotal,
		stats.ShotsBlocked,
		stats.ShotsInsideBox,
		stats.ShotsOutsideBox,
		stats.PossessionPct,
		stats.Corners,
		stats.Fouls,
		stats.Offsides,
		stats.YellowCards,
		stats.RedCards,
		stats.GoalkeeperSaves,
		stats.PassesTotal,
		stats.PassesAccurate,
		stats.PassAccuracy,
		stats.XG,
		stats.CardPoints,
	)
	if err != nil {
		return false, err
	}
	return rowsAffectedOrTrue(result), nil
}

// rowsAffectedOrTrue reports whether the insert stored a row.
//
// A driver that does not implement RowsAffected is reported as stored. The
// caller uses this only for the "captures stored" counter in its log line, so
// an optimistic count on an exotic driver misreports a number; treating an
// unknown as "not stored" would instead make a healthy pass look like it was
// writing nothing at all.
func rowsAffectedOrTrue(result sql.Result) bool {
	if result == nil {
		return true
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return true
	}
	return affected > 0
}

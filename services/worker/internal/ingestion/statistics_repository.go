package ingestion

import (
	"context"
	"database/sql"
	"errors"
)

type statisticsDB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// StatisticsRepository persists normalized per-team fixture statistics.
type StatisticsRepository struct {
	db statisticsDB
}

// StatRecord holds the nullable columns of fixture_statistics. Pointer fields
// map to NULL when the provider did not report that metric.
type StatRecord struct {
	ShotsOnTarget   *int
	ShotsTotal      *int
	ShotsBlocked    *int
	ShotsInsideBox  *int
	ShotsOutsideBox *int
	PossessionPct   *float64
	Corners         *int
	Fouls           *int
	Offsides        *int
	YellowCards     *int
	RedCards        *int
	GoalkeeperSaves *int
	PassesTotal     *int
	PassesAccurate  *int
	PassAccuracy    *float64
	XG              *float64
	CardPoints      *int
}

func NewStatisticsRepository(db statisticsDB) StatisticsRepository {
	return StatisticsRepository{db: db}
}

// Upsert inserts or updates one team's statistics for a fixture. The source is
// always 'api-football' because the free providers do not expose these metrics.
func (r StatisticsRepository) Upsert(ctx context.Context, fixtureID int64, teamID int64, stats StatRecord) error {
	if r.db == nil {
		return errors.New("statistics repository DB is nil")
	}
	if fixtureID <= 0 {
		return errors.New("fixture id is required")
	}
	if teamID <= 0 {
		return errors.New("team id is required")
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO fixture_statistics (
  fixture_id,
  team_id,
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
  source,
  updated_at
) VALUES (
  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, 'api-football', CURRENT_TIMESTAMP
)
ON CONFLICT (fixture_id, team_id) DO UPDATE SET
  shots_on_target = EXCLUDED.shots_on_target,
  shots_total = EXCLUDED.shots_total,
  shots_blocked = EXCLUDED.shots_blocked,
  shots_inside_box = EXCLUDED.shots_inside_box,
  shots_outside_box = EXCLUDED.shots_outside_box,
  possession_pct = EXCLUDED.possession_pct,
  corners = EXCLUDED.corners,
  fouls = EXCLUDED.fouls,
  offsides = EXCLUDED.offsides,
  yellow_cards = EXCLUDED.yellow_cards,
  red_cards = EXCLUDED.red_cards,
  goalkeeper_saves = EXCLUDED.goalkeeper_saves,
  passes_total = EXCLUDED.passes_total,
  passes_accurate = EXCLUDED.passes_accurate,
  pass_accuracy = EXCLUDED.pass_accuracy,
  xg = EXCLUDED.xg,
  card_points = EXCLUDED.card_points,
  source = EXCLUDED.source,
  updated_at = CURRENT_TIMESTAMP`,
		fixtureID,
		teamID,
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
	return err
}

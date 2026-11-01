package seourl

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Postgres-backed Store and Source for the crawlable URL set.

// unresolvedGrace must equal fixturepolicy.UnresolvedGrace in services/api.
//
// Copied rather than imported because the two live in different Go modules and
// the API's copy is under internal/, so there is no way to share it. The
// duplication is load-bearing: this is the window in which an unresolved
// earlier fixture withholds a prediction, and if the sitemap prices it
// differently from the API it will advertise pages that render noindex --
// which is the bug this whole block exists to stop.
//
// TestUnresolvedGraceMatchesTheAPI reads the API's source and fails if they
// drift, because nothing else would notice until a deploy failed.
const unresolvedGrace = 6 * time.Hour

// Repository reads and writes seo_urls.
type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

// Upsert records decisions and stamps last_seen_at.
//
// One statement for the batch rather than one per row. At 12,903 fixtures a
// row-at-a-time loop is 12,903 round trips, and a sweep that slow is a sweep
// that either holds a connection for minutes or gets abandoned half-finished.
//
// last_seen_at is always advanced, including for rows whose decision did not
// change. That is the whole basis of retirement: a row still being confirmed
// is a row whose entity still exists, and "unchanged" is confirmation too.
func (r *Repository) Upsert(ctx context.Context, rows []Row) (int, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("seo url repository has no database")
	}
	if len(rows) == 0 {
		return 0, nil
	}

	var (
		values []string
		args   []any
	)
	for i, row := range rows {
		base := i * 8
		values = append(values, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8))

		var entityID any
		if row.ID != 0 {
			entityID = row.ID
		}
		var reason any
		if row.Reason != ReasonNone {
			reason = string(row.Reason)
		}
		var lastModified any
		if !row.LastModified.IsZero() {
			lastModified = row.LastModified.UTC()
		}
		args = append(args,
			row.Path, string(row.Type), entityID, row.Volatile,
			row.Indexable, reason, lastModified, time.Now().UTC(),
		)
	}

	query := `
INSERT INTO seo_urls (path, entity_type, entity_id, volatile, indexable, excluded_reason, last_modified, last_seen_at)
VALUES ` + strings.Join(values, ",") + `
ON CONFLICT (path) DO UPDATE SET
  entity_type     = EXCLUDED.entity_type,
  entity_id       = EXCLUDED.entity_id,
  volatile        = EXCLUDED.volatile,
  indexable       = EXCLUDED.indexable,
  excluded_reason = EXCLUDED.excluded_reason,
  -- Never overwrite a real modification time with nothing. A lastmod that
  -- appears and disappears between crawls teaches Google to stop trusting the
  -- field across the whole property.
  last_modified   = COALESCE(EXCLUDED.last_modified, seo_urls.last_modified),
  last_seen_at    = EXCLUDED.last_seen_at,
  updated_at      = now()`

	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		// The write landed; only the count is unavailable. Reporting zero
		// would make a healthy sweep look like it did nothing.
		return len(rows), nil
	}
	return int(affected), nil
}

// RetireUnseen withdraws rows that have gone unconfirmed.
//
// It marks rather than deletes. A retired row still answers "why is this URL
// not in Google?", and deleting it would mean the next sweep rediscovers the
// path, re-advertises it, and then retires it again -- a flap that is worse
// for crawl trust than either steady state.
func (r *Repository) RetireUnseen(ctx context.Context, volatile bool, before time.Time) (int, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("seo url repository has no database")
	}
	result, err := r.db.ExecContext(ctx, `
UPDATE seo_urls
SET indexable = false, excluded_reason = 'retired', updated_at = now()
WHERE volatile = $1
  AND last_seen_at < $2
  AND (indexable OR excluded_reason IS DISTINCT FROM 'retired')`,
		volatile, before.UTC())
	if err != nil {
		return 0, err
	}
	retired, _ := result.RowsAffected()
	// A renamed entity is different from a vanished one. Once a sweep confirms
	// its new canonical path, the previous path redirects immediately and must
	// leave the sitemap immediately. The 72-hour grace above still protects
	// entities that have no replacement path, such as a temporarily missed row.
	result, err = r.db.ExecContext(ctx, `
UPDATE seo_urls AS old
SET indexable = false, excluded_reason = 'retired', updated_at = now()
WHERE old.volatile = $1
  AND old.entity_id IS NOT NULL
  AND (old.indexable OR old.excluded_reason IS DISTINCT FROM 'retired')
  AND EXISTS (
    SELECT 1 FROM seo_urls AS newer
    WHERE newer.entity_type = old.entity_type
      AND newer.entity_id = old.entity_id
      AND newer.path <> old.path
      AND newer.last_seen_at > old.last_seen_at
  )`, volatile)
	if err != nil {
		return int(retired), err
	}
	replaced, _ := result.RowsAffected()
	return int(retired + replaced), nil
}

// CatalogSource reads the entities that should have URLs.
type CatalogSource struct {
	db *sql.DB
	// FixtureWindow bounds how far back fixtures are considered. Beyond the
	// archive threshold a fixture is not indexable anyway, so scanning further
	// only costs rows -- but a little past it is deliberate, because that is
	// how a freshly archived fixture gets its row updated rather than simply
	// going unconfirmed and waiting three days for retirement.
	FixtureWindow time.Duration
}

func NewCatalogSource(db *sql.DB) *CatalogSource {
	return &CatalogSource{db: db, FixtureWindow: 21 * 24 * time.Hour}
}

// Entities streams one volatility band.
func (c *CatalogSource) Entities(ctx context.Context, volatile bool, offset, limit int) ([]Entity, error) {
	if c == nil || c.db == nil {
		return nil, errors.New("seo catalog source has no database")
	}
	if volatile {
		return c.fixtures(ctx, offset, limit)
	}
	return c.stable(ctx, offset, limit)
}

// fixtures loads the volatile band.
//
// Ordered by id, not kickoff, because the sweep pages through this and an
// ordering that shifts between pages would skip rows. Kickoff moves when a
// match is rescheduled; an id does not.
func (c *CatalogSource) fixtures(ctx context.Context, offset, limit int) ([]Entity, error) {
	window := c.FixtureWindow
	if window <= 0 {
		window = 21 * 24 * time.Hour
	}
	rows, err := c.db.QueryContext(ctx, `
SELECT
  m.id,
  m.status,
  m.kickoff_at,
  l.name,
  m.home_team_id, m.away_team_id,
  ht.name, at.name,
  -- has_prediction must mean "the page will show one", not "a row exists".
  --
  -- The fixture page is noindex unless the API serves it a prediction, and the
  -- API applies gates this query did not: a row can exist, be current, and
  -- still never reach a reader. Counting rows therefore advertised pages that
  -- noindex themselves -- the same mismatch already fixed for teams and
  -- leagues, and on 2026-09-22 it failed the deploy's sitemap gate on
  -- rostov-ii-vs-sevastopol-4427643, which had ten current rows and showed
  -- "No published prediction".
  --
  -- These four conditions mirror services/api/internal/predictions/source.go.
  -- If that gate changes, this has to change with it; the deploy's
  -- indexability check is what catches the two drifting apart.
  EXISTS (
    SELECT 1 FROM engine_predictions p
    WHERE p.fixture_id = m.id
      AND p.superseded_at IS NULL
      AND p.gate_tier IN ('BASIC', 'FULL')
      AND p.horizon_eligible = true
      AND p.embargo_active = false
      AND p.generated_at <= m.kickoff_at
  ) AS has_prediction,
  (SELECT max(p2.generated_at) FROM engine_predictions p2
     WHERE p2.fixture_id = m.id AND p2.superseded_at IS NULL) AS last_published,
  -- The fifth gate, and the only one that is temporary.
  --
  -- The API withholds a prediction while either team has a match that kicked
  -- off and has no result yet, because the model's inputs are stale until it
  -- lands. That suppression is bounded at six hours -- past it the row is
  -- treated as a data defect rather than a result still arriving -- so a
  -- fixture caught by it comes back on its own.
  --
  -- Included anyway. A sitemap that lists a page which is noindex right now is
  -- wrong right now, and the alternative is a deploy gate that fails whenever
  -- a result runs late. It costs some churn: a URL can leave the sitemap and
  -- return within the same six hours. That is the honest answer, and it is
  -- bounded -- on 2026-09-22 it was one URL out of ~21,000.
  NOT EXISTS (
    SELECT 1 FROM matches earlier
    WHERE earlier.id <> m.id
      AND earlier.kickoff_at < m.kickoff_at
      AND earlier.kickoff_at < now()
      AND earlier.kickoff_at >= now() - $4::interval
      AND (earlier.home_team_id IN (m.home_team_id, m.away_team_id)
           OR earlier.away_team_id IN (m.home_team_id, m.away_team_id))
      AND (earlier.home_score IS NULL OR earlier.away_score IS NULL)
  ) AS teams_are_settled,
  -- Settled match history per side, before this kickoff.
  --
  -- The predicate is copied from services/api/internal/matches/source.go, which
  -- serves the same two figures to the fixture page as home_history_count and
  -- away_history_count. The page gates its own robots tag on them, so any
  -- difference here would put the sitemap and the page back out of step -- the
  -- defect this package exists to prevent. Kicked off earlier, concluded, and
  -- both scores present: an abandoned match with no score is not history the
  -- preview can say anything about.
  --
  -- Counted across EVERY competition, deliberately: there is no league_id
  -- predicate here, and adding one back is a bug.
  --
  -- It was one, briefly. Scoping these counts to m.league_id made a promoted
  -- club read as zero in its new division despite forty matches one tier down,
  -- and -- far worse -- made BOTH sides of every cup and continental tie read
  -- zero, because a club's league record is not in the FA Cup or the Champions
  -- League. The gate would have withheld precisely the highest-demand fixtures
  -- on the property. Caught on 2026-09-28 while it was still shipping disabled.
  --
  -- A team's record is a fact about the team, not about the competition it
  -- happens to be playing this week, and not about the country either: a side
  -- that plays across two federations has played either way.
  (
    SELECT COUNT(*) FROM matches hh
    WHERE hh.kickoff_at < m.kickoff_at
      AND lower(hh.status) IN ('ft', 'aet', 'pen', 'finished')
      AND hh.home_score IS NOT NULL
      AND hh.away_score IS NOT NULL
      AND (hh.home_team_id = m.home_team_id OR hh.away_team_id = m.home_team_id)
  ) AS home_history_count,
  (
    SELECT COUNT(*) FROM matches ah
    WHERE ah.kickoff_at < m.kickoff_at
      AND lower(ah.status) IN ('ft', 'aet', 'pen', 'finished')
      AND ah.home_score IS NOT NULL
      AND ah.away_score IS NOT NULL
      AND (ah.home_team_id = m.away_team_id OR ah.away_team_id = m.away_team_id)
  ) AS away_history_count
FROM matches m
JOIN leagues l ON l.id = m.league_id
JOIN teams ht ON ht.id = m.home_team_id
JOIN teams at ON at.id = m.away_team_id
WHERE m.kickoff_at > now() - $1::interval
ORDER BY m.id
OFFSET $2 LIMIT $3`,
		fmt.Sprintf("%d hours", int(window.Hours())), offset, limit,
		// The same window the API prices this suppression at; see the note on
		// unresolvedGrace for why it is a copy rather than an import.
		fmt.Sprintf("%d hours", int(unresolvedGrace.Hours())))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Entity, 0, limit)
	for rows.Next() {
		var (
			id                 int64
			status             sql.NullString
			kickoff            sql.NullTime
			leagueName         sql.NullString
			homeID, awayID     int64
			homeName, awayName sql.NullString
			hasPrediction      bool
			lastPublished      sql.NullTime
			teamsSettled       bool
			homeHistory        int
			awayHistory        int
		)
		if err := rows.Scan(&id, &status, &kickoff, &leagueName, &homeID, &awayID,
			&homeName, &awayName, &hasPrediction, &lastPublished, &teamsSettled,
			&homeHistory, &awayHistory); err != nil {
			return nil, err
		}
		entity := Entity{
			Path: FixturePath(leagueName.String, homeName.String, awayName.String, id),
			Type: TypeFixture,
			ID:   id,
			// Both halves of "the page will show a prediction": one that
			// cleared the publication gates, and no earlier fixture still
			// withholding it. Either alone advertises a noindex page.
			Status:        status.String,
			HasPrediction: hasPrediction && teamsSettled,
			HomeHistory:   homeHistory,
			AwayHistory:   awayHistory,
			// Set here, and only here, because this is the query that actually
			// selected the two columns above. See Entity.HistoryKnown.
			HistoryKnown: true,
		}
		if kickoff.Valid {
			entity.KickoffAt = kickoff.Time.UTC()
		}
		if lastPublished.Valid {
			entity.LastModified = lastPublished.Time.UTC()
		}
		out = append(out, entity)
	}
	return out, rows.Err()
}

// stable loads teams and leagues.
//
// HasContent must be the same question the team page asks, because the page
// decides its own robots tag and a sitemap that disagrees advertises a
// noindexed URL.
//
// It did disagree. This asked for a kickoff in the past; the page asks
// isIndexableTeam(teamForm(team)), which counts only *settled* results -- ones
// with scores. A fixture that kicked off and never finished satisfied the
// first and not the second, so the club was advertised and then noindexed
// itself. AFC Leopards was the example: one match, status 'scheduled', kickoff
// long past, page reading "0 recent matches". 80 of 12,734 team URLs were in
// that state on 2026-09-21.
//
// So this now requires a finished match with a score, which is what "settled"
// means on the page side.
//
// Leagues had the same fault in a milder form: this asked for any match ever,
// while the league page indexes on upcoming_matches or recent_matches being
// non-empty. A competition holding nothing but an unplayed past fixture was
// advertised and noindexed -- Kenyan Premier League, 1 match, 0 upcoming,
// 0 finished. Only 4 URLs, fixed here because it is the same question and
// leaving one side wrong invites the next person to copy it.
func (c *CatalogSource) stable(ctx context.Context, offset, limit int) ([]Entity, error) {
	rows, err := c.db.QueryContext(ctx, `
SELECT 'team' AS kind, t.id, t.slug,
       EXISTS (SELECT 1 FROM matches m
               WHERE (m.home_team_id = t.id OR m.away_team_id = t.id)
                 AND m.status IN ('finished', 'awarded', 'walkover')
                 AND m.home_score IS NOT NULL
                 AND m.away_score IS NOT NULL) AS has_content
FROM teams t
UNION ALL
SELECT 'league', l.id, l.slug,
       EXISTS (SELECT 1 FROM matches m WHERE m.league_id = l.id
                 AND (m.kickoff_at > now()
                      OR (m.status IN ('finished', 'awarded', 'walkover')
                          AND m.home_score IS NOT NULL
                          AND m.away_score IS NOT NULL))) AS has_content
FROM leagues l
ORDER BY 1, 2
OFFSET $1 LIMIT $2`, offset, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Entity, 0, limit)
	for rows.Next() {
		var (
			kind       string
			id         int64
			slug       sql.NullString
			hasContent bool
		)
		if err := rows.Scan(&kind, &id, &slug, &hasContent); err != nil {
			return nil, err
		}
		entity := Entity{ID: id, HasContent: hasContent}
		if kind == "team" {
			entity.Type = TypeTeam
			entity.Path = TeamPath(slug.String)
		} else {
			entity.Type = TypeLeague
			entity.Path = LeaguePath(slug.String)
		}
		out = append(out, entity)
	}
	return out, rows.Err()
}

package acca

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var schemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Repository persists ephemeral inventory. Schema is explicit so QA can use
// acca_qa without granting the worker write access to application tables.
type Repository struct {
	db     *sql.DB
	schema string
}

func NewRepository(db *sql.DB, schema string) (*Repository, error) {
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}
	if schema == "" {
		schema = "public"
	}
	if !schemaPattern.MatchString(schema) {
		return nil, fmt.Errorf("invalid ACCA schema")
	}
	return &Repository{db: db, schema: schema}, nil
}

func (r *Repository) UpsertBatch(ctx context.Context, period, intent string, slips []Slip) error {
	if len(slips) == 0 {
		return nil
	}
	if len(slips) > MaxActiveInventory {
		return fmt.Errorf("inventory exceeds ceiling")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	slipsTable, legsTable := r.schema+".acca_slips", r.schema+".acca_slip_legs"
	for _, s := range slips {
		var id string
		err = tx.QueryRowContext(ctx, fmt.Sprintf(`INSERT INTO %s (composition_hash,content_hash,market_tab,period,leg_count,combined_fair_odds,ranking_score,earliest_kickoff,latest_kickoff,odds_band,generation_intent) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (composition_hash) DO UPDATE SET content_hash=EXCLUDED.content_hash,combined_fair_odds=EXCLUDED.combined_fair_odds,ranking_score=EXCLUDED.ranking_score,earliest_kickoff=EXCLUDED.earliest_kickoff,latest_kickoff=EXCLUDED.latest_kickoff,odds_band=EXCLUDED.odds_band,updated_at=now(),status='active' WHERE %s.content_hash IS DISTINCT FROM EXCLUDED.content_hash OR %s.status <> 'active' RETURNING id`, slipsTable, slipsTable, slipsTable), s.CompositionHash, s.ContentHash, s.Tab, period, len(s.Legs), s.CombinedFairOdds, s.RankingScore, s.EarliestKickoff, s.LatestKickoff, s.OddsBand, intent).Scan(&id)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		for i, l := range s.Legs {
			_, err = tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s (slip_id,position,prediction_source,prediction_id,prediction_version,match_id,market_key,selection,fair_odds_at_generation) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (slip_id,position) DO UPDATE SET fair_odds_at_generation=EXCLUDED.fair_odds_at_generation,prediction_version=EXCLUDED.prediction_version`, legsTable), id, i, l.Source, l.PredictionID, l.PredictionVersion, l.MatchID, l.Market, l.Selection, l.FairOdds)
			if err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, retireOverflowQuery(slipsTable), MaxActiveInventory); err != nil {
		return err
	}
	return tx.Commit()
}

// retireOverflowQuery trims the active pool to its ceiling.
//
// It ranks within each (market_tab, leg_count, odds_band) cell and takes the
// best of each before taking a second from any, rather than ranking globally.
//
// Globally by ranking_score was the whole reason the inventory hugged the
// standards floor. ranking_score is confidence, and a long-odds slip is built
// from low-confidence legs, so every high-odds slip sorted to the bottom and
// was retired first. Composing across odds windows could not survive it: a
// slip composed at 70.03 for a reader who asked for 5 legs between 70 and 100
// was invalidated in the same run that created it, and the pool converged on
// the cheapest slip clearing the floor at every leg count -- 201 cells of a
// 1,596-cell grid, every 5-leg slip between 2.76 and 3.55.
//
// The round-robin keeps the ceiling exactly and still prefers confidence, but
// within a cell rather than across the whole pool. A cell with one slip keeps
// it; a cell with eighty loses its weakest first.
func retireOverflowQuery(slipsTable string) string {
	return fmt.Sprintf(`WITH ranked AS (
  SELECT id,
    ROW_NUMBER() OVER (
      PARTITION BY market_tab, leg_count, odds_band
      ORDER BY ranking_score DESC, earliest_kickoff ASC, updated_at DESC, id
    ) AS depth,
    ranking_score, earliest_kickoff, updated_at
  FROM %s WHERE status='active'
), round_robin AS (
  SELECT id, ROW_NUMBER() OVER (
    ORDER BY depth ASC, ranking_score DESC, earliest_kickoff ASC, updated_at DESC, id
  ) AS position
  FROM ranked
)
UPDATE %s s SET status='invalid', updated_at=now()
FROM round_robin r WHERE s.id=r.id AND r.position > $1`, slipsTable, slipsTable)
}

func (r *Repository) Expire(ctx context.Context) (sql.Result, error) {
	return r.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET status='expired',updated_at=now() WHERE status='active' AND earliest_kickoff <= now()`, r.schema+".acca_slips"))
}

// Prune deletes slips that left the active pool more than retainFor ago.
//
// Nothing removed these before, so the table accumulated every slip ever
// generated: 658k rows against a bounded active pool of 3,000, 92% of it
// 'invalid'. That is not merely wasted storage. The deploy readiness gate
// counts DISTINCT composition_hash across the whole table, so every dead row
// makes the gate slower, and it had reached ~43s against a 15s budget --
// failing every deploy until the budget was raised.
//
// Keyed on updated_at rather than earliest_kickoff because it asks the question
// that matters here: how long ago did this slip stop being active. A slip
// invalidated today for a superseded prediction may still have a kickoff a
// fortnight out, and deleting it on kickoff date would keep it for that
// fortnight despite it being dead on arrival.
//
// Legs are removed by ON DELETE CASCADE on acca_slip_legs.slip_id, and nothing
// else references acca_slips, so this cannot orphan or break a foreign key.
//
// Deletes in bounded batches: an unbounded DELETE of ~600k rows takes a long
// transaction and a lot of WAL on a table the worker writes to every fifteen
// minutes. Each batch commits on its own, so a slow run costs a partial prune
// rather than a lock held across the whole sweep -- the next run continues.
func (r *Repository) Prune(ctx context.Context, retainFor time.Duration, batch int) (int64, error) {
	if retainFor <= 0 || batch <= 0 {
		return 0, nil
	}
	slipsTable := r.schema + ".acca_slips"
	statement := fmt.Sprintf(`DELETE FROM %s WHERE id IN (
  SELECT id FROM %s
  WHERE status <> 'active' AND updated_at < now() - $1::interval
  LIMIT $2
)`, slipsTable, slipsTable)
	interval := fmt.Sprintf("%d seconds", int64(retainFor.Seconds()))
	var total int64
	for {
		result, err := r.db.ExecContext(ctx, statement, interval, batch)
		if err != nil {
			return total, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += affected
		if affected < int64(batch) {
			return total, nil
		}
		// A cancelled context must stop the sweep rather than start another
		// batch; the remainder is picked up by the next scheduled run.
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

// InvalidateStale checks only the bounded active inventory, not historical slips.
// The full eligibility projection is authoritative; the sampled composition pool
// must not invalidate otherwise-qualified predictions which were not sampled.
func (r *Repository) InvalidateStale(ctx context.Context) (sql.Result, error) {
	return r.db.ExecContext(ctx, staleInventoryQuery(r.schema))
}

func staleInventoryQuery(schema string) string {
	return fmt.Sprintf(`UPDATE %s.acca_slips s SET status='invalid',updated_at=now()
WHERE s.status='active' AND (
 s.combined_fair_odds < round(power(1.5, s.leg_count::double precision / 2)::numeric, 4)
 OR EXISTS (SELECT 1 FROM %s.acca_slip_legs floor_leg WHERE floor_leg.slip_id=s.id AND floor_leg.fair_odds_at_generation < 1.10)
 OR EXISTS (
  SELECT 1 FROM %s.acca_slip_legs l WHERE l.slip_id=s.id AND NOT EXISTS (
    SELECT 1 FROM %s.eligible_acca_predictions_v e
    WHERE e.prediction_source=l.prediction_source AND e.prediction_id=l.prediction_id
      AND e.prediction_version IS NOT DISTINCT FROM l.prediction_version
      AND e.match_id=l.match_id AND e.market_key=l.market_key AND e.selection=l.selection
      AND e.market_key <> 'correct_score' AND e.probability > 0 AND e.probability < 1
      AND e.fair_odds > 1
  )
 ))`, schema, schema, schema, schema)
}

func (r *Repository) InvalidateByPrediction(ctx context.Context, source, predictionID string) (sql.Result, error) {
	return r.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s s SET status='invalid',updated_at=now() WHERE status='active' AND EXISTS (SELECT 1 FROM %s l WHERE l.slip_id=s.id AND l.prediction_source=$1 AND l.prediction_id=$2)`, r.schema+".acca_slips", r.schema+".acca_slip_legs"), source, predictionID)
}

func QualifiedName(schema, table string) string {
	return strings.TrimSpace(schema) + "." + strings.TrimSpace(table)
}

// OutstandingDemand reads filter corners readers asked for and did not get.
//
// Ordered by how often each was asked and how recently, so a corner wanted
// fifty times today outranks one wanted once last week. Bounded by the caller
// because chasing demand must not starve the grid that serves everyone else.
func (r *Repository) OutstandingDemand(ctx context.Context, limit int) ([]DemandRow, error) {
	if r == nil || r.db == nil || limit <= 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT market_tab, leg_count, min_fair_odds, max_fair_odds, period
FROM %s.acca_demand
WHERE satisfied_at IS NULL
ORDER BY request_count DESC, last_seen_at DESC
LIMIT $1`, r.schema), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]DemandRow, 0, limit)
	for rows.Next() {
		var row DemandRow
		if err := rows.Scan(&row.Tab, &row.Legs, &row.MinFairOdds, &row.MaxFairOdds, &row.Period); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// MarkDemandSatisfied records that a corner is now stocked.
//
// Errors are swallowed: the slip is already composed and published by the time
// this runs, and failing the run over a bookkeeping write would throw away
// inventory that was correctly built. The cost of a lost write is that the
// corner is composed again next run, which is harmless.
func (r *Repository) MarkDemandSatisfied(ctx context.Context, intent Intent) {
	if r == nil || r.db == nil {
		return
	}
	_, _ = r.db.ExecContext(ctx, fmt.Sprintf(`
UPDATE %s.acca_demand
SET satisfied_at = now()
WHERE market_tab = $1 AND leg_count = $2
  AND min_fair_odds = $3 AND max_fair_odds = $4 AND period = $5`, r.schema),
		intent.Tab, intent.Legs, intent.MinFairOdds, intent.MaxFairOdds, intent.Period)
}

package acca

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"time"
)

var sourceSchemaPattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

type SQLCandidateSource struct {
	db     *sql.DB
	schema string
}

func NewSQLCandidateSource(db *sql.DB, schema string) (*SQLCandidateSource, error) {
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}
	if schema == "" {
		schema = "public"
	}
	if !sourceSchemaPattern.MatchString(schema) {
		return nil, fmt.Errorf("invalid ACCA schema")
	}
	return &SQLCandidateSource{db: db, schema: schema}, nil
}
func (s *SQLCandidateSource) EligibleCandidates(ctx context.Context, now time.Time) ([]Candidate, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`WITH ranked_per_fixture AS (
		SELECT prediction_source,prediction_id,prediction_version,match_id,market_key,selection,probability,kickoff_at,ranking_score,
			ROW_NUMBER() OVER (PARTITION BY market_key,match_id ORDER BY ranking_score DESC,kickoff_at,prediction_source,prediction_id) AS fixture_rank
		FROM %s.eligible_acca_predictions_v
		WHERE kickoff_at>$1 AND market_key <> 'correct_score'
	), ranked_candidates AS (
		SELECT prediction_source,prediction_id,prediction_version,match_id,market_key,selection,probability,kickoff_at,ranking_score,
			ROW_NUMBER() OVER (PARTITION BY market_key ORDER BY ranking_score DESC,kickoff_at,prediction_source,prediction_id) AS market_rank
		FROM ranked_per_fixture
		WHERE fixture_rank=1
	)
	SELECT prediction_source,prediction_id,prediction_version,match_id,market_key,selection,probability,kickoff_at,ranking_score
	FROM ranked_candidates
	WHERE market_rank <= 100
	ORDER BY market_rank,ranking_score DESC,kickoff_at,market_key,prediction_source,prediction_id
	LIMIT 3000`, s.schema), now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Candidate, 0, 3000)
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.Source, &c.PredictionID, &c.PredictionVersion, &c.MatchID, &c.Market, &c.Selection, &c.Probability, &c.Kickoff, &c.Rank); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

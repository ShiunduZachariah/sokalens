package analysis

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

var ErrNotFound = errors.New("prepared analysis not found")

type Result struct {
	MatchID        int64    `json:"match_id"`
	Market         string   `json:"market"`
	Pick           string   `json:"pick"`
	Confidence     float64  `json:"confidence"`
	Summary        string   `json:"summary"`
	Reasoning      []string `json:"reasoning"`
	Advanced       bool     `json:"advanced"`
	ConfidenceTier string   `json:"confidence_tier"`
}

type Service struct{ db *sql.DB }

func NewService(db *sql.DB) Service { return Service{db: db} }

// Get reads only the current prepared model output. It never invokes a model or
// an external football provider from a public page request.
func (s Service) Get(ctx context.Context, matchID int64, advanced bool) (Result, error) {
	// `generated_at <= kickoff_at` is the pre-match guard the rest of the
	// public read paths already carry, and it was the one missing here.
	//
	// It matters more than it looks. This endpoint is reachable by guests --
	// three free views, no account -- and it takes the newest current
	// prediction for the fixture. The moment the in-play engine starts
	// writing post-kickoff rows with `superseded_at IS NULL`, "newest" is an
	// in-play prediction, and this query would hand the paid asset to an
	// anonymous visitor without a single line of this file changing. The
	// gated in-play surface lives at /v1/inplay/ behind the entitlement
	// filter; this path stays pre-match only.
	query := `SELECT ep.fixture_id, ep.market, ep.prediction, ep.confidence*100, ep.confidence_tier, ep.explanation
FROM engine_predictions AS ep
JOIN fixtures AS f ON f.id = ep.fixture_id
WHERE ep.fixture_id=$1 AND ep.superseded_at IS NULL AND ep.gate_tier <> 'SKIP' AND ep.generated_at <= f.kickoff_at`
	if !advanced {
		query += ` AND ep.market IN ('1x2','over_under_2_5','btts')`
	}
	query += ` ORDER BY ep.is_best_bet DESC, ep.confidence DESC, ep.generated_at DESC LIMIT 1`
	var result Result
	var explanation string
	if err := s.db.QueryRowContext(ctx, query, matchID).Scan(&result.MatchID, &result.Market, &result.Pick, &result.Confidence, &result.ConfidenceTier, &explanation); errors.Is(err, sql.ErrNoRows) {
		return Result{}, ErrNotFound
	} else if err != nil {
		return Result{}, err
	}
	result.Advanced = advanced
	result.Summary = strings.TrimSpace(explanation)
	for _, value := range strings.FieldsFunc(explanation, func(r rune) bool { return r == '\n' || r == ';' }) {
		if reason := strings.TrimSpace(value); reason != "" {
			result.Reasoning = append(result.Reasoning, reason)
		}
	}
	if len(result.Reasoning) == 0 && result.Summary != "" {
		result.Reasoning = []string{result.Summary}
	}
	return result, nil
}

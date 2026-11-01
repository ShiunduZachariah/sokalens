package acca

import (
	"fmt"
	"math"
)

const (
	MinimumSystemLegFairOdds      = 1.10
	minimumTwoLegCombinedFairOdds = 1.50
)

// MinimumSystemCombinedFairOdds is the publication floor for SokaLens-created
// inventory. On-demand/user-controlled combinations are intentionally exempt.
func MinimumSystemCombinedFairOdds(legs int) float64 {
	if legs < 2 {
		return minimumTwoLegCombinedFairOdds
	}
	return math.Pow(minimumTwoLegCombinedFairOdds, float64(legs)/2)
}

// SystemStandardSQLPredicate keeps invalid legacy rows out of every public
// inventory read immediately, before the worker's next reconciliation run.
func SystemStandardSQLPredicate(schema, slipAlias string) string {
	// The floor is rounded to the scale combined_fair_odds is stored at.
	//
	// That column is numeric with 4 decimal places. A slip composed exactly at
	// the floor stores as 1.8371 while power(1.5, 3/2.0) evaluates to
	// 1.8371173070873836, so an unrounded comparison finds it short by 1.7e-05
	// and rejects a slip that satisfies the standard as written -- the rule is
	// "at least the floor", and this one is at it. Rounding the threshold to the
	// same scale compares like with like; it does not lower the bar.
	//
	// Observed in production: one slip in 3,000 sat on the boundary, which was
	// enough to hold invalid at 1 forever, and acca-readiness.sh requires zero.
	// Every frontend deploy failed its 40-minute gate until this was fixed.
	return fmt.Sprintf("%s.combined_fair_odds >= round(power(1.5, %s.leg_count::double precision / 2)::numeric, 4) AND NOT EXISTS (SELECT 1 FROM %s.acca_slip_legs standard_leg WHERE standard_leg.slip_id=%s.id AND standard_leg.fair_odds_at_generation < 1.10)", slipAlias, slipAlias, schema, slipAlias)
}

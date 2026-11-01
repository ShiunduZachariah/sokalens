package acca

import "math"

const (
	MinimumSystemLegFairOdds      = 1.10
	minimumTwoLegCombinedFairOdds = 1.50
)

// MinimumSystemCombinedFairOdds keeps the same minimum geometric-average
// price as a two-leg 1.50 ACCA. Added legs therefore have to add meaningful
// return instead of merely multiplying operational and selection risk.
func MinimumSystemCombinedFairOdds(legs int) float64 {
	if legs < 2 {
		return minimumTwoLegCombinedFairOdds
	}
	return math.Pow(minimumTwoLegCombinedFairOdds, float64(legs)/2)
}

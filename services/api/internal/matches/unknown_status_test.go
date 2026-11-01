package matches

import "testing"

// The worker's reaper writes 'unknown' to a match that passed every plausible
// duration and that no provider observation could resolve. It is neither a
// result nor a match in play, and it must not be quietly filed as either.
//
// Filing it as live would leave the stuck match sitting in the in-play surface
// forever, which is the defect the reaper exists to end. Filing it as finished
// would present an unconfirmed match as a settled one -- inventing a result is
// worse than admitting there isn't one.
func TestUnknownIsNeitherLiveNorUpcomingNorFinished(t *testing.T) {
	if isLiveLikeStatus("unknown") {
		t.Error("an unresolved match must drop out of the live surfaces")
	}
	if isUpcomingLikeStatus("unknown") {
		t.Error("an unresolved match has already kicked off")
	}
	if normalizeLegacyStatus("unknown") != "unknown" {
		t.Error("unknown must not be rewritten into another status")
	}
	for _, filter := range []string{"live", "upcoming", "finished"} {
		if matchesStatusFilter("unknown", []string{filter}) {
			t.Errorf("unknown matched the %q filter", filter)
		}
	}
	// It is still addressable explicitly, so an operator can find these rows.
	if !matchesStatusFilter("unknown", []string{"unknown"}) {
		t.Error("unknown must remain queryable by name")
	}
}

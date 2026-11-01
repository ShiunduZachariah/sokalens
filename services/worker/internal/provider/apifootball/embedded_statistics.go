package apifootball

import (
	"fmt"
	"strings"
)

// Embedded statistics on `fixtures?ids=`.
//
// WHY THIS MATTERS: `fixtures?ids=` takes up to 20 fixture ids and, per the
// provider's own "how to save calls to the API" guidance, returns events,
// lineups, statistics and player statistics for each one. That turns live
// statistics for 200 concurrent matches from ~72,000 requests a day
// (`fixtures/statistics` once per fixture per minute) into ~3,000. It is the
// difference between corners, shots, fouls and possession being affordable
// in-play and not.
//
// THE SHAPE HERE IS DOCUMENTED, NOT CAPTURED. `FixtureItem` has never modelled
// these objects, and no response carrying them has been held in this repository.
// `testdata/fixtures-ids-embedded.synthetic.json` is written from the
// documentation and says so in its own first field; it is an ASSUMPTION the
// tests pin, not evidence.
//
// So the assumption is isolated to `fixtureStatisticsBlock` below and to nothing
// else. One captured response either confirms it or shows exactly which struct
// tag to change, and `cmd/capturefixtureids` exists to take that capture. Until
// then `ParseFixturesWithStatistics` returns no statistics rather than guessing
// when the field is absent, so a wrong assumption reads as "the provider did not
// send statistics" and never as fabricated numbers.

// MaxCaptureIDs is the provider cap on `ids=`, mirroring
// ingestion.MaxFixtureIDsPerRequest. Duplicated rather than imported because
// this package sits below ingestion and must not depend upward.
const MaxCaptureIDs = 20

// fixtureStatisticsBlock is one team's statistics as embedded in a fixture.
//
// Deliberately identical in shape to the standalone `/fixtures/statistics`
// response element, because the provider documents the embedded object as the
// same `/statistics` payload. If the capture shows otherwise, this is the one
// type that changes.
type fixtureStatisticsBlock struct {
	Team struct {
		ID int `json:"id"`
	} `json:"team"`
	Statistics []struct {
		Type  string `json:"type"`
		Value any    `json:"value"`
	} `json:"statistics"`
}

// fixtureItemWithStatistics is FixtureItem plus the embedded objects.
//
// A separate type rather than new fields on FixtureItem: every existing caller
// parses the bulk `live=all` and date responses through FixtureItem, and those
// do not carry statistics. Widening the shared type would have every one of them
// silently accept a field the endpoint never sends, and would make a future
// reader think the bulk poll supplies statistics when it does not.
type fixtureItemWithStatistics struct {
	FixtureItem
	Statistics []fixtureStatisticsBlock `json:"statistics"`
}

// FixtureWithStatistics is one fixture and whatever statistics came with it.
type FixtureWithStatistics struct {
	Fixture FixtureItem
	// Empty when the response carried no statistics for this fixture, which is
	// the honest reading of an absent field and of a league the provider has no
	// statistics for. Callers must treat empty as "not observed", never as zero:
	// `fixture_live_snapshots` records nullable counts for exactly this reason.
	Statistics []TeamStatistics
}

// ParseFixturesWithStatistics parses a `fixtures?ids=` response.
//
// Returns the fixtures in the order the provider sent them, each with its
// embedded statistics normalised through the same rules as
// `ParseFixtureStatistics`: types and values trimmed, nulls collapsed to the
// empty string, a team with no statistics retained so the caller decides.
//
// A fixture whose statistics block carries no usable team id is returned with no
// statistics rather than rejected. The fixture itself -- score, clock, status,
// events -- is still worth having, and losing a whole poll because one league
// reports statistics oddly would take the live board down for every match in the
// batch.
func ParseFixturesWithStatistics(body []byte) ([]FixtureWithStatistics, error) {
	var envelope listEnvelope[fixtureItemWithStatistics]
	if err := parseProviderEnvelope(body, &envelope); err != nil {
		return nil, err
	}
	out := make([]FixtureWithStatistics, 0, len(envelope.Response))
	for index, item := range envelope.Response {
		if item.Fixture.ID <= 0 {
			return nil, fmt.Errorf("fixture id is required at index %d", index)
		}
		out = append(out, FixtureWithStatistics{
			Fixture:    item.FixtureItem,
			Statistics: normalizeEmbeddedStatistics(item.Statistics),
		})
	}
	return out, nil
}

// normalizeEmbeddedStatistics applies ParseFixtureStatistics' rules to the
// embedded blocks.
//
// Shared rules rather than a second implementation: a corner count that reads
// differently depending on which endpoint delivered it is a defect that would
// only show up as an inexplicable market disagreement.
func normalizeEmbeddedStatistics(blocks []fixtureStatisticsBlock) []TeamStatistics {
	if len(blocks) == 0 {
		return nil
	}
	teams := make([]TeamStatistics, 0, len(blocks))
	for _, block := range blocks {
		if block.Team.ID <= 0 {
			// Unattributable statistics. Skipped rather than guessed at, the same
			// posture the ingestion jobs take on an unmapped provider team --
			// assigning a corner count to the wrong side is worse than having
			// none.
			continue
		}
		team := TeamStatistics{
			TeamID:     block.Team.ID,
			Statistics: make([]StatisticItem, 0, len(block.Statistics)),
		}
		for _, stat := range block.Statistics {
			value, _ := stringifyProviderField(stat.Value)
			team.Statistics = append(team.Statistics, StatisticItem{
				Type:  strings.TrimSpace(stat.Type),
				Value: strings.TrimSpace(value),
			})
		}
		teams = append(teams, team)
	}
	if len(teams) == 0 {
		return nil
	}
	return teams
}

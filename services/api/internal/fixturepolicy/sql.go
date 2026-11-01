// Package fixturepolicy centralizes public fixture eligibility SQL.
package fixturepolicy

import (
	"fmt"
	"time"
)

// ResolvedStatusSQLList is the canonical set of statuses that mean "this match
// will not change again through normal play", rendered as a SQL IN-list.
//
// It mirrors the `terminal` set in
// services/worker/internal/matchstate/state.go, including the three values the
// earlier version of this clause left out:
//
//   - `awarded` and `walkover` are decided results, just not played ones.
//   - `unknown` is where the reaper parks a match that is past every plausible
//     duration and that reconciliation could not resolve. matchstate declares
//     it terminal on purpose: nothing further is coming for that row. Treating
//     it as "still pending" is what made a single un-reconciled match suppress
//     both its teams indefinitely.
//
// The provider spellings (`ft`, `aet`, `pen`) stay in the list because
// historical rows and the football-data importer store them directly, exactly
// as matchstate.Normalize tolerates them.
const ResolvedStatusSQLList = `('ft', 'aet', 'pen', 'finished', 'awarded', 'walkover', 'unknown', ` +
	`'cancelled', 'canceled', 'postponed', 'abandoned')`

// UnresolvedGraceHours bounds how long an already-started match may suppress
// its teams' later fixtures while its result is awaited.
//
// matchstate.HardBound says 210 minutes is the longest a match can plausibly
// still be in play; six hours is that bound plus room for a results feed that
// reports late. Past it, a non-terminal status is a data defect in that row,
// not a result still on its way -- and a data defect in one row must not keep
// deleting predictions for every later fixture those two teams play.
const UnresolvedGrace = 6 * time.Hour

// UnresolvedEarlierFixtureClause hides a later fixture while either team has an
// earlier match that has already kicked off, has no result yet, and is still
// inside the grace window in which a result can plausibly arrive.
//
// Each of those three conditions is load-bearing, and the version this replaced
// had none of them:
//
//   - **Already kicked off.** The old clause matched any earlier fixture,
//     including ones still in the future. A fixture that has not been played
//     cannot be "awaiting its result", so a team playing Wednesday and Sunday
//     had its Sunday fixture suppressed by its own Wednesday fixture -- for the
//     whole horizon, because Wednesday stays `upcoming` until Wednesday. In
//     production on 2026-09-20 this alone hid 522 fixtures.
//   - **No result yet.** A row carrying a full-time score is resolved whatever
//     its status text says.
//   - **Inside the grace window.** The old clause looked back 14 days with no
//     upper bound on the wait, so any row whose status never advanced past
//     `upcoming` or `unknown` suppressed both its teams for a fortnight. That
//     was the dominant cause: 1,931 of the 2,098 suppressed fixtures in the
//     seven-day horizon were blocked by a match that had already been played.
//
// The two moments are rendered as RFC 3339 literals from the caller's clock
// rather than written as `now() - interval '6 hours'`. Two reasons, and both
// were learned the hard way:
//
//   - The same statements run against PostgreSQL in production and SQLite in
//     the source tests, and `now()` and `interval` parse in only one of them.
//     The clause this replaced sidestepped that with `CURRENT_DATE - 14`,
//     which SQLite happily evaluates to nonsense -- so the tests exercised a
//     window no production query ever used, and the defect was invisible to
//     them.
//   - Every caller already holds an injectable clock. Reading the wall clock
//     inside the database instead made the window untestable at a chosen
//     moment, which is exactly what a fixture-eligibility rule needs.
//
// The aliases are deliberately allow-listed because they are interpolated into
// SQL text. The timestamps are formatted from a time.Time, so they cannot
// carry anything else.
func UnresolvedEarlierFixtureClause(alias string, now time.Time) string {
	if alias != "f" && alias != "m" {
		panic("unsupported fixture SQL alias")
	}
	table, homeGoals, awayGoals := "fixtures", "home_goals_ft", "away_goals_ft"
	if alias == "m" {
		table, homeGoals, awayGoals = "matches", "home_score", "away_score"
	}
	moment := now.UTC()
	startedBefore := moment.Format(time.RFC3339)
	graceFrom := moment.Add(-UnresolvedGrace).Format(time.RFC3339)
	return fmt.Sprintf(`
  AND NOT EXISTS (
    SELECT 1
    FROM %[1]s AS earlier_unresolved
    WHERE earlier_unresolved.id <> %[2]s.id
      AND earlier_unresolved.kickoff_at < %[2]s.kickoff_at
      AND earlier_unresolved.kickoff_at < '%[5]s'
      AND earlier_unresolved.kickoff_at >= '%[6]s'
      AND (
        earlier_unresolved.home_team_id IN (%[2]s.home_team_id, %[2]s.away_team_id)
        OR earlier_unresolved.away_team_id IN (%[2]s.home_team_id, %[2]s.away_team_id)
      )
      AND earlier_unresolved.%[3]s IS NULL
      AND earlier_unresolved.%[4]s IS NULL
      AND lower(COALESCE(earlier_unresolved.status, 'scheduled')) NOT IN
        %[7]s
  )`, table, alias, homeGoals, awayGoals, startedBefore, graceFrom, ResolvedStatusSQLList)
}

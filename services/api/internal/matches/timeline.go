package matches

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/httpserver"
)

// The live timeline read path.
//
// Events are written by the worker from the event list embedded in
// fixtures?live=all (in-play plan §4) and land in fixture_events, which is
// append-only and partitioned by observed_at.
//
// Three properties of that table shape this query:
//
//  1. There is no unique constraint on identity. PostgreSQL requires the
//     partition key in every unique constraint, and the partition key is
//     observed_at -- which differs between polls of the same event, so the
//     constraint would permit exactly the duplicate it exists to prevent.
//     Idempotency is enforced by the writer. The read path does not assume the
//     writer is perfect: DISTINCT ON (identity_key) means a duplicate that does
//     slip in renders once rather than showing a goal twice.
//  2. Retraction is a flag, not a delete. A cancelled goal stays on the
//     timeline marked retracted, because a timeline that silently drops it
//     cannot explain why the score does not match what the reader saw.
//  3. team_id is the provider's id, not ours. It is resolved to a side through
//     provider_entity_maps; an unresolved team yields an empty side rather than
//     a guess, and the UI renders such an event without taking a side.
const timelineLimit = 500

// matchTimeline reads one fixture's events in match-clock order.
//
// fixture_events.fixture_id is the CANONICAL match id, which is what the writer
// resolves before storing and what this query joins on. It is not the provider's
// fixture id: in QA the same fixture is provider 9900001 and match 3932546, so
// treating the two as interchangeable silently returns nothing. fixture_events
// .team_id is the opposite -- the provider's team id, resolved to a side through
// provider_entity_maps below. A match with no events -- not yet kicked off, or
// the writer disabled -- is the normal case and yields an empty timeline, never
// an error.
func (s Source) matchTimeline(ctx context.Context, matchID int64) ([]httpserver.MatchEvent, error) {
	if s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
WITH fixture AS (
  SELECT id, home_team_id, away_team_id
  FROM matches
  WHERE id = $1
)
SELECT DISTINCT ON (fe.identity_key)
  fe.elapsed_minute,
  fe.elapsed_extra,
  fe.event_type,
  fe.event_detail,
  COALESCE(fe.player_name, ''),
  COALESCE(fe.assist_name, ''),
  COALESCE(fe.comments, ''),
  fe.is_retracted,
  CASE
    WHEN fe.team_id IS NULL THEN ''
    WHEN pem.canonical_entity_id = f.home_team_id::text THEN 'home'
    WHEN pem.canonical_entity_id = f.away_team_id::text THEN 'away'
    ELSE ''
  END AS side
FROM fixture_events fe
JOIN fixture f ON f.id = fe.fixture_id
LEFT JOIN provider_entity_maps pem
  ON pem.entity_type = 'team'
  AND pem.provider = fe.provider
  AND pem.retired_at IS NULL
  AND pem.canonical_entity_id IS NOT NULL
  AND pem.provider_entity_id = fe.team_id::text
WHERE fe.fixture_id = $1
-- Latest observation of each event wins: is_retracted is set by a later poll
-- than the one that first recorded the goal.
ORDER BY fe.identity_key, fe.observed_at DESC
LIMIT $2
`, matchID, timelineLimit)
	if err != nil {
		return nil, fmt.Errorf("read fixture timeline: %w", err)
	}
	defer rows.Close()

	events := make([]httpserver.MatchEvent, 0, 32)
	for rows.Next() {
		var (
			event     httpserver.MatchEvent
			extra     sql.NullInt64
			rawType   string
			rawDetail string
		)
		if err := rows.Scan(
			&event.Minute,
			&extra,
			&rawType,
			&rawDetail,
			&event.PlayerName,
			&event.AssistName,
			&event.Comments,
			&event.Retracted,
			&event.Side,
		); err != nil {
			return nil, fmt.Errorf("scan fixture timeline: %w", err)
		}
		if extra.Valid {
			value := int(extra.Int64)
			event.Extra = &value
		}
		event.Type = rawType
		event.Detail = rawDetail
		event.Kind = string(EventKind(rawType, rawDetail))
		event.Clock = clockLabel(event.Minute, event.Extra)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fixture timeline: %w", err)
	}

	sortTimeline(events)
	return events, nil
}

// sortTimeline puts events in the order they happened.
//
// Added time sorts after the minute it extends: 90' precedes 90+1'. Sorting
// here rather than in SQL is forced by DISTINCT ON, which requires the
// distinct expression to lead the ORDER BY, and is cheap on a single
// fixture's events.
func sortTimeline(events []httpserver.MatchEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Minute != events[j].Minute {
			return events[i].Minute < events[j].Minute
		}
		return extraValue(events[i].Extra) < extraValue(events[j].Extra)
	})
}

func extraValue(extra *int) int {
	if extra == nil {
		return 0
	}
	return *extra
}

// clockLabel renders the match clock the way a scoreboard does.
//
// Served rather than left to each client because added time is the part every
// client gets wrong: 90+3 arrives as two numbers and collapsing them to "93'"
// moves an injury-time winner to a minute that was never played.
func clockLabel(minute int, extra *int) string {
	if extra != nil && *extra > 0 {
		return fmt.Sprintf("%d+%d'", minute, *extra)
	}
	return fmt.Sprintf("%d'", minute)
}

// EventKind maps the provider's (type, detail) pair onto a meaning.
//
// This mirrors classifyEvent in the worker's apifootball package, which cannot
// be imported across the module boundary (it lives under services/worker's
// internal/ tree). The duplication is deliberate and bounded: the strings below
// are the API's published contract, asserted by TestEventKindMatchesTheWriter's
// table. If the writer ever persists its derived kind as a column, delete this
// and read that column instead.
//
// Matching is case-insensitive because the provider's casing is not consistent:
// Goal, Card and Var are capitalised while subst is not. A case-sensitive
// switch silently drops every substitution, the most common event there is.
func EventKind(rawType, rawDetail string) string {
	eventType := strings.ToLower(strings.TrimSpace(rawType))
	detail := strings.ToLower(strings.TrimSpace(rawDetail))

	switch eventType {
	case "goal":
		switch {
		// An own goal and a penalty are both type="Goal". Only detail
		// separates them, and crediting an own goal to the scoring team is the
		// one error a reader will certainly notice.
		case strings.Contains(detail, "own goal"):
			return "own_goal"
		case strings.Contains(detail, "missed penalty"):
			return "penalty_missed"
		case strings.Contains(detail, "penalty"):
			return "penalty_goal"
		default:
			return "goal"
		}
	case "card":
		switch {
		case strings.Contains(detail, "second yellow"):
			return "second_yellow_card"
		case strings.Contains(detail, "red"):
			return "red_card"
		case strings.Contains(detail, "yellow"):
			return "yellow_card"
		default:
			return "unknown"
		}
	case "subst", "substitution":
		return "substitution"
	case "var":
		if strings.Contains(detail, "cancel") || strings.Contains(detail, "disallow") {
			return "var_retraction"
		}
		return "var_other"
	default:
		return "unknown"
	}
}

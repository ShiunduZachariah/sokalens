package matches

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ShiunduZachariah/sports-prediction-platform/services/api/internal/httpserver"
)

// disciplineWindow is how many of a team's recent matches the card figures
// cover. Ten is the same window the team page's form block reads, so the two
// surfaces describe the same run of games.
const disciplineWindow = 10

// matchDiscipline reads both teams' recent card records from before a
// fixture's kickoff.
//
// It counts yellow_cards and red_cards, never card_points. card_points is
// written in two scales -- the football-data importer stores yellow + 2*red,
// the live statistics job stores yellow*10 + red*25 -- so an average over it
// mixes units. Raw counts mean the same thing whichever writer stored them.
//
// A null card count is read as zero only when the team's statistics row was
// actually captured (fouls, shots or corners present). API-Football reports a
// zero as null, so ignoring nulls would drop every clean match and inflate the
// averages; a row with nothing captured at all is a missing record, not a
// clean one, and the whole fixture is skipped.
//
// Both teams' rows must be usable for a fixture to count, because the match
// total is part of the answer and half a total is a wrong one.
func (s Source) matchDiscipline(ctx context.Context, matchID int64) (*httpserver.MatchDiscipline, error) {
	var homeID, awayID sql.NullInt64
	var kickoffAt any
	err := s.db.QueryRowContext(ctx, `SELECT home_team_id, away_team_id, kickoff_at FROM matches WHERE id = $1`, matchID).
		Scan(&homeID, &awayID, &kickoffAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read fixture teams: %w", err)
	}
	if !homeID.Valid || !awayID.Valid {
		return nil, nil
	}
	kickoff, err := parseTimestamp(kickoffAt)
	if err != nil {
		return nil, err
	}
	home, err := s.teamDiscipline(ctx, homeID.Int64, kickoff)
	if err != nil {
		return nil, err
	}
	away, err := s.teamDiscipline(ctx, awayID.Int64, kickoff)
	if err != nil {
		return nil, err
	}
	if home.Matches == 0 && away.Matches == 0 {
		return nil, nil
	}
	return &httpserver.MatchDiscipline{Home: home, Away: away}, nil
}

func (s Source) teamDiscipline(ctx context.Context, teamID int64, before time.Time) (httpserver.TeamDiscipline, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
  COALESCE(own.yellow_cards, 0), COALESCE(own.red_cards, 0),
  COALESCE(opp.yellow_cards, 0), COALESCE(opp.red_cards, 0)
FROM matches m
JOIN fixture_statistics own ON own.fixture_id = m.id AND own.team_id = $1
JOIN fixture_statistics opp ON opp.fixture_id = m.id AND opp.team_id <> $1
WHERE (m.home_team_id = $1 OR m.away_team_id = $1)
  AND m.kickoff_at < $2
  AND lower(m.status) IN ('ft', 'aet', 'pen', 'finished')
  AND (own.yellow_cards IS NOT NULL OR own.red_cards IS NOT NULL
       OR own.fouls IS NOT NULL OR own.shots_total IS NOT NULL OR own.corners IS NOT NULL)
  AND (opp.yellow_cards IS NOT NULL OR opp.red_cards IS NOT NULL
       OR opp.fouls IS NOT NULL OR opp.shots_total IS NOT NULL OR opp.corners IS NOT NULL)
ORDER BY m.kickoff_at DESC
LIMIT $3`, teamID, before.UTC().Format(time.RFC3339), disciplineWindow)
	if err != nil {
		return httpserver.TeamDiscipline{}, fmt.Errorf("read team discipline: %w", err)
	}
	defer rows.Close()

	var out httpserver.TeamDiscipline
	for rows.Next() {
		var ownYellow, ownRed, oppYellow, oppRed int
		if err := rows.Scan(&ownYellow, &ownRed, &oppYellow, &oppRed); err != nil {
			return httpserver.TeamDiscipline{}, fmt.Errorf("scan team discipline: %w", err)
		}
		out.Matches++
		out.YellowCards += ownYellow
		out.RedCards += ownRed
		out.MatchCards += ownYellow + ownRed + oppYellow + oppRed
	}
	return out, rows.Err()
}

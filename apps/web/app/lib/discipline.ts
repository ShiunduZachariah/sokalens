/**
 * Card records as the match detail API serves them: totals over each team's
 * last ten finished matches before kickoff, counted from yellow and red cards
 * (never the card_points column, which is stored in two different scales).
 */
export type TeamDiscipline = {
  matches: number;
  yellowCards: number;
  redCards: number;
  /** Both sides' cards across those matches. */
  matchCards: number;
};

export type MatchDiscipline = { home: TeamDiscipline | null; away: TeamDiscipline | null };

function count(value: unknown) {
  const number = typeof value === "number" ? value : Number.NaN;
  return Number.isInteger(number) && number >= 0 ? number : null;
}

/**
 * One team's record, or null when it is missing or does not add up.
 *
 * A record whose own cards exceed the match total, or that claims cards from
 * zero matches, is inconsistent; quoting it would print an impossible figure,
 * so it is dropped rather than repaired.
 */
export function parseTeamDiscipline(raw: unknown): TeamDiscipline | null {
  if (!raw || typeof raw !== "object") return null;
  const record = raw as Record<string, unknown>;
  const matches = count(record.matches);
  const yellowCards = count(record.yellow_cards);
  const redCards = count(record.red_cards);
  const matchCards = count(record.match_cards);
  if (matches === null || yellowCards === null || redCards === null || matchCards === null) return null;
  if (matches === 0) return null;
  if (yellowCards + redCards > matchCards) return null;
  return { matches, yellowCards, redCards, matchCards };
}

export function parseMatchDiscipline(raw: unknown): MatchDiscipline | null {
  if (!raw || typeof raw !== "object") return null;
  const record = raw as Record<string, unknown>;
  const home = parseTeamDiscipline(record.home);
  const away = parseTeamDiscipline(record.away);
  return home || away ? { home, away } : null;
}

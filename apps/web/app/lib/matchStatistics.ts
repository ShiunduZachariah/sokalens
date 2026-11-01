/**
 * In-match statistics, as the provider reported them.
 *
 * Every value is nullable because absent and zero are different answers, and
 * the API keeps them apart deliberately: of 11,076 rows in fixture_statistics,
 * red_cards is explicitly 0 in 4,371 and missing in 5,710. A match where nobody
 * was sent off and a match where nobody counted are not the same fixture, and
 * rendering both as "0" invents a measurement for the second.
 *
 * So the parser preserves null rather than defaulting, and the table prints an
 * em dash for it. `Number(value ?? 0)` here would quietly undo the whole point
 * of the contract.
 */

export type TeamMatchStats = {
  possessionPct: number | null;
  shotsTotal: number | null;
  shotsOnTarget: number | null;
  corners: number | null;
  fouls: number | null;
  offsides: number | null;
  yellowCards: number | null;
  redCards: number | null;
};

export type MatchStatistics = { home: TeamMatchStats; away: TeamMatchStats };

/** The eight rows the design draws, in its order. */
export const MATCH_STAT_ROWS: Array<{ key: keyof TeamMatchStats; label: string; unit?: string }> = [
  { key: "possessionPct", label: "Possession", unit: "%" },
  { key: "shotsTotal", label: "Total shots" },
  { key: "shotsOnTarget", label: "Shots on target" },
  { key: "corners", label: "Corners" },
  { key: "fouls", label: "Fouls" },
  { key: "offsides", label: "Offsides" },
  { key: "yellowCards", label: "Yellow cards" },
  { key: "redCards", label: "Red cards" },
];

function numberOrNull(value: unknown): number | null {
  if (value === null || value === undefined) return null;
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : null;
}

function parseSide(raw: unknown): TeamMatchStats {
  const side = (raw ?? {}) as Record<string, unknown>;
  return {
    possessionPct: numberOrNull(side.possession_pct),
    shotsTotal: numberOrNull(side.shots_total),
    shotsOnTarget: numberOrNull(side.shots_on_target),
    corners: numberOrNull(side.corners),
    fouls: numberOrNull(side.fouls),
    offsides: numberOrNull(side.offsides),
    yellowCards: numberOrNull(side.yellow_cards),
    redCards: numberOrNull(side.red_cards),
  };
}

export function parseMatchStatistics(raw: unknown): MatchStatistics | null {
  if (!raw || typeof raw !== "object") return null;
  const block = raw as Record<string, unknown>;
  const stats = { home: parseSide(block.home), away: parseSide(block.away) };
  // A block where neither side reported anything is not worth a tab.
  const reported = MATCH_STAT_ROWS.some(
    (row) => stats.home[row.key] !== null || stats.away[row.key] !== null,
  );
  return reported ? stats : null;
}

/** Whether there is anything to draw. */
export function hasMatchStatistics(stats: MatchStatistics | null): stats is MatchStatistics {
  return stats !== null;
}

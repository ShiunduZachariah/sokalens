/**
 * The 1 · X · 2 marker the refreshed home and today pages show beside a pick.
 *
 * The design draws three cells with the picked outcome filled. Its mock data is
 * entirely match-result picks, so it never has to say what the marker does for
 * a Both Teams to Score or a Total Goals prediction -- and the real feed ranks
 * every market, so most rows are not match-result picks at all.
 *
 * Returning null for those is deliberate. Three empty cells would read as
 * "none of these outcomes", which is a claim about the match; the honest render
 * for a BTTS pick is no marker, because 1/X/2 is not the question it answers.
 */
export type ResultSide = "home" | "draw" | "away";

/** Market keys whose selections name a match-result side. */
const RESULT_MARKETS = new Set(["1x2", "match_result", "full_time_result", "result"]);

/**
 * Selection spellings seen across providers and catalogues for each side.
 *
 * Intentionally strict: an unrecognised selection returns null rather than
 * guessing a side. Highlighting the wrong cell states the wrong prediction,
 * which is worse than showing no marker.
 */
const SIDES: Record<string, ResultSide> = {
  "1": "home",
  home: "home",
  home_win: "home",
  "home win": "home",
  x: "draw",
  draw: "draw",
  tie: "draw",
  "2": "away",
  away: "away",
  away_win: "away",
  "away win": "away",
};

export function resultSide(marketKey: string, selection: string): ResultSide | null {
  const market = String(marketKey ?? "").trim().toLowerCase();
  if (!RESULT_MARKETS.has(market)) return null;
  const key = String(selection ?? "").trim().toLowerCase().replace(/\s+/g, " ");
  return SIDES[key] ?? null;
}

export type ResultCell = { key: "1" | "X" | "2"; label: string; picked: boolean };

/**
 * The three cells, or null when this prediction is not a match-result pick.
 * `label` is the accessible name; the single character is what is drawn.
 */
export function resultCells(marketKey: string, selection: string): ResultCell[] | null {
  const side = resultSide(marketKey, selection);
  if (!side) return null;
  return [
    { key: "1", label: "Home win", picked: side === "home" },
    { key: "X", label: "Draw", picked: side === "draw" },
    { key: "2", label: "Away win", picked: side === "away" },
  ];
}

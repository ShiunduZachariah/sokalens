/**
 * Merge an engine feed with its provider fallback into one row per fixture.
 *
 * Lives here, apart from `data/livePredictions.ts`, because that module reaches
 * for the API client and cannot be imported by the test runner. The merge rule
 * is worth testing directly, so it is kept free of imports.
 */

export type MergeableRow = {
  matchId: number;
  kickoffAt: string;
};

/**
 * Engine rows win: a fixture the engine has published is never replaced by its
 * provider fallback. Provider rows fill the gaps and are converted on the way
 * in. Sorted by kickoff ascending, so any downstream ranking that ties on
 * everything else still shows a sensible order.
 */
export function mergeByMatchId<Engine extends MergeableRow, Provider extends { matchId: number }>(
  engine: readonly Engine[],
  provider: readonly Provider[],
  toEngineRow: (row: Provider) => Engine,
): Engine[] {
  const seen = new Set<number>();
  const merged: Engine[] = [];

  for (const row of engine) {
    if (seen.has(row.matchId)) continue;
    seen.add(row.matchId);
    merged.push(row);
  }
  for (const row of provider) {
    if (seen.has(row.matchId)) continue;
    seen.add(row.matchId);
    merged.push(toEngineRow(row));
  }

  return merged.sort((left, right) => Date.parse(left.kickoffAt) - Date.parse(right.kickoffAt));
}

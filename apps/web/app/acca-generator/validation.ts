export type AccaGeneratorInput = { legs: number; minOdds: number; maxOdds: number };

export function validateAccaGeneratorInput(legsValue: string, minValue: string, maxValue: string): AccaGeneratorInput {
  const legs = Number(legsValue);
  if (!Number.isInteger(legs) || legs < 2 || legs > 20) throw new Error("Legs must be a whole number between 2 and 20.");
  const minOdds = Number(minValue);
  if (!Number.isFinite(minOdds) || minOdds < 1) throw new Error("Minimum fair odds must be at least 1.");
  const maxOdds = Number(maxValue);
  if (!Number.isFinite(maxOdds) || maxOdds < minOdds) throw new Error("Maximum fair odds must be greater than or equal to minimum fair odds.");
  if (maxOdds > 1000) throw new Error("Maximum fair odds cannot exceed 1000.");
  return { legs, minOdds, maxOdds };
}

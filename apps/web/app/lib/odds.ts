export type OddsFormat = "decimal" | "fractional" | "american";

export const ODDS_FORMAT_STORAGE_KEY = "sokalens.odds-format.v1";
export const ODDS_FORMAT_EVENT = "sokalens:odds-format-change";

export const oddsFormatLabels: Record<OddsFormat, string> = {
  decimal: "Decimal",
  fractional: "Fractional",
  american: "American",
};

export function isOddsFormat(value: string): value is OddsFormat {
  return value === "decimal" || value === "fractional" || value === "american";
}

function greatestCommonDivisor(left: number, right: number): number {
  let a = Math.abs(left);
  let b = Math.abs(right);
  while (b > 0) [a, b] = [b, a % b];
  return a || 1;
}

function fractionalOdds(decimal: number): string {
  const net = decimal - 1;
  let bestNumerator = 1;
  let bestDenominator = 1;
  let bestDifference = Number.POSITIVE_INFINITY;
  for (let denominator = 1; denominator <= 20; denominator += 1) {
    const numerator = Math.max(1, Math.round(net * denominator));
    const difference = Math.abs((numerator / denominator) - net);
    if (difference < bestDifference) {
      bestNumerator = numerator;
      bestDenominator = denominator;
      bestDifference = difference;
    }
  }
  const divisor = greatestCommonDivisor(bestNumerator, bestDenominator);
  return `${bestNumerator / divisor}/${bestDenominator / divisor}`;
}

export function formatOdds(value: number | null, format: OddsFormat = "decimal"): string {
  if (value == null) return "—";
  if (!Number.isFinite(value)) return "∞";
  if (format === "fractional") return value <= 1 ? "—" : fractionalOdds(value);
  if (format === "american") {
    if (value <= 1) return "—";
    const american = value >= 2 ? Math.round((value - 1) * 100) : Math.round(-100 / (value - 1));
    return american > 0 ? `+${american}` : String(american);
  }
  return value >= 1_000_000_000 ? value.toExponential(2) : value.toFixed(2);
}

export function readOddsFormat(): OddsFormat {
  if (typeof window === "undefined") return "decimal";
  try {
    const stored = window.localStorage.getItem(ODDS_FORMAT_STORAGE_KEY);
    return stored === "fractional" || stored === "american" ? stored : "decimal";
  } catch {
    return "decimal";
  }
}

export function writeOddsFormat(format: OddsFormat): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(ODDS_FORMAT_STORAGE_KEY, format);
  } catch {
    // The preference remains active for this render when storage is unavailable.
  }
  window.dispatchEvent(new CustomEvent(ODDS_FORMAT_EVENT, { detail: { format } }));
}

/**
 * Shared presentation helpers for confidence, risk tiers, plurals, and
 * kickoff times. Every public surface should render confidence and kickoffs
 * through these helpers so the wording and timezone stay consistent.
 *
 * Kickoffs arrive as RFC3339 UTC strings and are always displayed in
 * Africa/Nairobi (EAT, UTC+3) via Intl.DateTimeFormat — never the server's
 * local zone and never a hardcoded +3 offset.
 */

/**
 * Server-rendered times are UTC, labelled as such.
 *
 * Every time on the site used to be formatted in Africa/Nairobi with a
 * "Timezone: EAT" badge, which is the wrong time for most readers and tells
 * anyone looking where the operator sits. Per-request formatting is not an
 * option either: the HTML is cached at the edge, so one visitor's zone would
 * be served to everyone behind the same cache entry.
 *
 * UTC is accurate and regionally neutral. Surfaces that a reader actually
 * looks at use <LocalTime>, which corrects to their own zone after hydration.
 */
const SERVER_TIME_ZONE = "UTC";

/** Round to a whole number and clamp to the 0-100 confidence range. */
export function clampPercent(value: number): number {
  const rounded = Math.round(Number(value));
  if (!Number.isFinite(rounded)) {
    return 0;
  }
  return Math.max(0, Math.min(100, rounded));
}

/** Render a confidence percentage identically everywhere, e.g. "72%". */
export function formatConfidence(value: number): string {
  return `${clampPercent(value)}%`;
}

/**
 * ONE risk tiering scheme for the whole app: High >= 70, Medium 50-69,
 * Low < 50. Vocabulary matches the risk badge labels ("Higher"/"Balanced"/
 * "Lower") so any surface that tiers risk by confidence shares the scheme.
 */
export type RiskTier = "higher" | "balanced" | "lower";

export function riskTier(confidence: number): RiskTier {
  const value = clampPercent(confidence);
  if (value >= 70) {
    return "higher";
  }
  if (value >= 50) {
    return "balanced";
  }
  return "lower";
}

export function riskTierLabel(tier: RiskTier): string {
  switch (tier) {
    case "higher":
      return "Higher risk";
    case "balanced":
      return "Balanced risk";
    default:
      return "Lower risk";
  }
}

/** "1 match", "2 matches", "0 predictions". */
export function pluralize(count: number, singular: string, plural?: string): string {
  const value = Math.abs(Math.floor(Number(count)));
  if (!Number.isFinite(value)) {
    return `0 ${plural ?? `${singular}s`}`;
  }
  if (value === 1) {
    return `1 ${singular}`;
  }
  return `${value} ${plural ?? `${singular}s`}`;
}

function parseDate(value: string | Date): Date | null {
  const date = typeof value === "string" ? new Date(value) : value;
  return Number.isNaN(date.getTime()) ? null : date;
}

/**
 * The one kickoff formatter on the site.
 *
 * There used to be two of these plus a third inside `LocalTime`, and sixteen
 * call sites rendered their output straight into the markup as plain text. A
 * string formatted on the server cannot be corrected in the browser, so those
 * sites showed UTC to every reader while the `LocalTime` ones showed the
 * reader's own zone -- a fixture page printed 3:00 PM in its header and 12:00
 * on its scoreboard for the same kickoff.
 *
 * Nothing should call this directly to render a time. Render `<LocalTime />`,
 * which calls it with the reader's zone after hydration; the server pass and
 * the pre-paint script both go through here too, so there is exactly one place
 * where kickoff formatting can be wrong.
 *
 * `timeZoneName` must never be added to these options: Intl throws
 * "TypeError: Invalid option : option" when it meets dateStyle or timeStyle,
 * which took production down. `intlOptions.test.mjs` enforces that.
 */
/**
 * Ask for the reader's own timezone rather than a named one.
 *
 * `timeZone: undefined` cannot mean this, because an absent key falls back to
 * the parameter default -- which is UTC, and that is exactly the bug it caused:
 * LocalTime's effect passed `undefined` meaning "the viewer's zone" and got UTC
 * back, so every time that the pre-paint pass did not reach stayed in UTC on a
 * reader's screen. A sentinel makes the two cases impossible to confuse.
 */
export const VIEWER_TIME_ZONE = Symbol.for("sokalens.viewerTimeZone");

export function formatInstant(
  value: string | Date,
  { timeOnly = false, timeZone = SERVER_TIME_ZONE }: { timeOnly?: boolean; timeZone?: string | typeof VIEWER_TIME_ZONE } = {},
): string | null {
  const date = parseDate(value);
  if (!date) {
    return null;
  }
  // Omitting `timeZone` entirely is what makes Intl resolve the reader's own
  // zone; there is no value that means it.
  return new Intl.DateTimeFormat("en", {
    ...(timeOnly ? {} : { dateStyle: "medium" as const }),
    timeStyle: "short",
    ...(timeZone === VIEWER_TIME_ZONE ? {} : { timeZone }),
  }).format(date);
}

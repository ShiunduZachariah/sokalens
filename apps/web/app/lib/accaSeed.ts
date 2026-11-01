/**
 * Per-visitor ordering seed for the ACCA catalogue.
 *
 * The API already shuffles by seed -- acca/source.go orders by
 * `md5(composition_hash || $1)` -- and the web route already forwards a `seed`
 * parameter. Nothing was generating one, so every visitor received the same
 * order and the same handful of slips got all the attention.
 *
 * The seed must be STABLE for a visitor. A fresh seed between pages would
 * reorder the catalogue mid-scroll and break cursor pagination, which is a
 * shipped and tested guarantee.
 */

export const ACCA_SEED_COOKIE = "sokalens.acca.seed";

/** 48 bits: plenty of distinct orderings, short enough to need no encoding. */
const SEED_PATTERN = /^[0-9a-f]{12}$/;

export function isValidSeed(value: string | null | undefined): boolean {
  return SEED_PATTERN.test(String(value ?? ""));
}

export function mintSeed(): string {
  const bytes = new Uint8Array(6);
  // crypto is available in both the Next server runtime and the browser.
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

/**
 * Reuse the visitor's seed when it is well formed, otherwise mint a fresh one.
 * Anything unexpected is replaced rather than trusted: the value reaches a SQL
 * ORDER BY as a bound parameter, and a validated shape keeps it boring.
 */
export function resolveAccaSeed(cookieValue: string | null | undefined): string {
  return isValidSeed(cookieValue) ? String(cookieValue) : mintSeed();
}

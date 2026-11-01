/**
 * The one safe way to put a provider-supplied value into an `<img src>`.
 *
 * Team and league crests arrive as free-form strings, and a missing one does
 * not always arrive as null: it can arrive as the four characters `null`.
 * `?? ""` does not catch that, and neither does a truthiness guard, so
 * `src="null"` reached the browser. A relative src resolves against the
 * current directory, so every fixture page requested
 * `/predictions/{league}/null` and every team page `/teams/null` -- 404s at a
 * rate of roughly 14,000 a day, almost all of them Meta's crawler fetching
 * link previews.
 *
 * Returning `undefined` rather than an empty string matters: React omits the
 * attribute entirely, so the browser issues no request at all, and every call
 * site's existing fallback (initials, a placeholder crest) renders because the
 * value is falsy.
 *
 * Guarding here rather than in each data mapper is deliberate. There are four
 * mappers and eleven render sites, and the next data source added will not
 * know about this. The render layer is the last point where it can be made
 * impossible.
 */
const SENTINELS = new Set(["null", "undefined", "nan", "none", "false", "0"]);

export function imageSrc(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  const trimmed = value.trim();
  if (!trimmed || SENTINELS.has(trimmed.toLowerCase())) return undefined;
  // Anything that is not an absolute URL or a root-relative path would resolve
  // against the current directory, which is exactly the failure being fixed.
  if (!/^https?:\/\//i.test(trimmed) && !trimmed.startsWith("/")) return undefined;
  return trimmed;
}

/** Whether a provider value is usable as an image source. */
export function hasImage(value: unknown): boolean {
  return imageSrc(value) !== undefined;
}

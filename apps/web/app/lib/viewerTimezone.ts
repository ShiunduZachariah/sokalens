/**
 * The one timezone every other time on the site is derived from.
 *
 * It is UTC, and it is defined once here because it used to be the string
 * "Africa/Nairobi" repeated in nine places. That is what let the two halves of
 * the site disagree: displayed kickoff times were being localised to the
 * reader while "today" was still being decided on East Africa's clock, so a
 * match could appear under today and render with yesterday's date.
 *
 * UTC is the neutral choice rather than a regional one: no reader is
 * privileged, no crawler can infer where the site is operated from, and the
 * day boundary is the same for the cache, the sitemap and every visitor. A
 * viewer who has set a timezone still gets their own day window -- this is
 * only the answer when nobody has said otherwise, which includes every bot.
 */
export const DEFAULT_VIEWER_TIMEZONE = "UTC";

export function validViewerTimezone(value: string | undefined | null) {
  const candidate = value?.trim();
  if (!candidate || candidate.length > 64) return DEFAULT_VIEWER_TIMEZONE;
  try {
    new Intl.DateTimeFormat("en", { timeZone: candidate }).format(new Date());
    return candidate;
  } catch {
    return DEFAULT_VIEWER_TIMEZONE;
  }
}

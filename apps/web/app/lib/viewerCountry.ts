/**
 * Which country is the reader in?
 *
 * Used to rank the home / top predictions surface so a viewer in São Paulo does
 * not lead with a Kenyan Premier League fixture. Cloudflare fronts production
 * (confirmed by CF-RAY on sokalens.com) and already supplies `cf-ipcountry`,
 * which apps/web/app/api/account/proxyHeaders.ts also reads.
 *
 * Returns "" for unknown rather than guessing. An empty result is meaningful:
 * the ranking falls back to global top leagues, which is never worse than the
 * kickoff-only ordering it replaces.
 */

/** Cloudflare sends these when it cannot resolve a real country. */
const NON_COUNTRIES = new Set(["XX", "T1", "AP", "EU"]);

function normalize(value: string | null | undefined): string {
  const code = String(value ?? "").trim().toUpperCase();
  if (!/^[A-Z]{2}$/.test(code)) return "";
  return NON_COUNTRIES.has(code) ? "" : code;
}

/**
 * A region subtag, if the locale carries one. "pt-BR" -> "BR". A bare language
 * such as "en" carries no region and must not be turned into a country.
 */
export function countryFromLocale(locale: string | null | undefined): string {
  const parts = String(locale ?? "").split(/[-_]/);
  return parts.length < 2 ? "" : normalize(parts[1]);
}

type HeaderLike = { get(name: string): string | null | undefined } | Map<string, string> | null;

function readHeader(headers: HeaderLike, name: string): string {
  if (!headers) return "";
  const value = typeof (headers as Map<string, string>).get === "function"
    ? (headers as Map<string, string>).get(name)
    : undefined;
  return String(value ?? "");
}

export function resolveViewerCountry(headers: HeaderLike, locale: string | null | undefined): string {
  return normalize(readHeader(headers, "cf-ipcountry")) || countryFromLocale(locale);
}

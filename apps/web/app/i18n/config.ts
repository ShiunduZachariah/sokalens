export const supportedLocales = ["en", "es", "fr", "pt", "ar", "de", "it"] as const;
export type Locale = (typeof supportedLocales)[number];

export const defaultLocale: Locale = "en";
export const localeCookieName = "sokalens_locale";

export const localeDetails: Record<Locale, { direction: "ltr" | "rtl"; label: string; ogLocale: string }> = {
  en: { direction: "ltr", label: "English", ogLocale: "en_GB" },
  es: { direction: "ltr", label: "Español", ogLocale: "es_ES" },
  fr: { direction: "ltr", label: "Français", ogLocale: "fr_FR" },
  pt: { direction: "ltr", label: "Português", ogLocale: "pt_PT" },
  ar: { direction: "rtl", label: "العربية", ogLocale: "ar_AR" },
  de: { direction: "ltr", label: "Deutsch", ogLocale: "de_DE" },
  it: { direction: "ltr", label: "Italiano", ogLocale: "it_IT" },
};

const countryHints: Partial<Record<string, Locale>> = {
  AE: "ar", AR: "es", AT: "de", BR: "pt", CH: "de", CL: "es", CO: "es",
  DE: "de", DZ: "ar", EC: "es", EG: "ar", ES: "es", FR: "fr", IT: "it",
  MA: "ar", MX: "es", PT: "pt", SA: "ar", TN: "ar", UY: "es",
};

export function isLocale(value: string | null | undefined): value is Locale {
  return Boolean(value && supportedLocales.includes(value.toLowerCase() as Locale));
}

export function normalizeLocale(value: string | null | undefined): Locale | undefined {
  const language = value?.trim().toLowerCase().split(/[-_]/)[0];
  return isLocale(language) ? language : undefined;
}

export function localeFromAcceptLanguage(value: string | null | undefined): Locale | undefined {
  return value?.split(",").map((part) => part.trim().split(";")[0]).map(normalizeLocale).find(Boolean) as Locale | undefined;
}

export function localeFromCountry(value: string | null | undefined): Locale | undefined {
  return value ? countryHints[value.toUpperCase()] : undefined;
}

export function resolveLocale(input: {
  account?: string | null;
  browser?: string | null;
  cookie?: string | null;
  country?: string | null;
  selected?: string | null;
}) {
  const candidates = [
    ["selected", normalizeLocale(input.selected)],
    ["account", normalizeLocale(input.account)],
    ["cookie", normalizeLocale(input.cookie)],
    ["browser", localeFromAcceptLanguage(input.browser)],
    ["country", localeFromCountry(input.country)],
  ] as const;
  const resolved = candidates.find(([, locale]) => locale);
  return { locale: resolved?.[1] ?? defaultLocale, source: resolved?.[0] ?? "fallback" };
}

export function localeFromPath(pathname: string): Locale | undefined {
  return normalizeLocale(pathname.split("/")[1]);
}

export function stripLocalePrefix(pathname: string) {
  const parts = pathname.split("/");
  if (!isLocale(parts[1])) return pathname || "/";
  const stripped = `/${parts.slice(2).join("/")}`;
  return stripped === "/" ? "/" : stripped.replace(/\/$/, "");
}

/**
 * The URL that serves `path` in `locale`.
 *
 * English is served from the unprefixed root, and `/en/*` is a permanent
 * redirect to it. Root and `/en/` were byte-identical English trees, both
 * `index,follow` and both self-canonical, which is why Search Console reported
 * 105 URLs where it had chosen a different canonical. Returning the unprefixed
 * path for the default locale removes `/en/*` from every internal link,
 * hreflang cluster, canonical and sitemap in one place -- there is no second
 * English URL left to emit.
 */
export function localePath(path: string, locale: Locale) {
  if (!path.startsWith("/") || path.startsWith("//") || /^\/(?:api|_next)(?:\/|$)/.test(path)) return path;
  const splitAt = [path.indexOf("?"), path.indexOf("#")].filter((index) => index >= 0).sort((a, b) => a - b)[0] ?? path.length;
  const pathname = stripLocalePrefix(path.slice(0, splitAt));
  if (locale === defaultLocale) return `${pathname}${path.slice(splitAt)}`;
  return `/${locale}${pathname === "/" ? "" : pathname}${path.slice(splitAt)}`;
}

/**
 * The complete reciprocal hreflang cluster for `pathname`.
 *
 * Every locale is listed, including the page's own, because a cluster Google
 * will honour must be self-referential and reciprocal in both directions --
 * a one-way declaration is silently discarded. `en` and `x-default` both
 * resolve to the unprefixed URL, so the cluster is eight entries over seven
 * distinct URLs.
 */
export function localeAlternates(pathname: string): Record<string, string> {
  const path = stripLocalePrefix(pathname);
  return Object.fromEntries([
    ...supportedLocales.map((locale) => [locale, localePath(path, locale)]),
    ["x-default", path],
  ]) as Record<string, string>;
}

/**
 * The same cluster as absolute URLs, for sitemap `xhtml:link` blocks and any
 * consumer that cannot resolve a relative href.
 */
export function localeAlternateEntries(pathname: string): { hreflang: string; path: string }[] {
  return Object.entries(localeAlternates(pathname)).map(([hreflang, path]) => ({ hreflang, path }));
}

export function canonicalForRequest(basePath: string, publicPath: string, locale: Locale) {
  return localeFromPath(publicPath) ? localePath(basePath, locale) : stripLocalePrefix(basePath);
}

/**
 * Routes whose content is actually translated.
 *
 * This is the one input that decides how a page canonicalises, and it exists
 * because the rule was previously accidental -- it held only because of which
 * metadata helper each author happened to reach for.
 *
 * - **Listed here:** the page serves real localized content, so each locale's
 *   URL canonicalises to *itself* and advertises the others via hreflang.
 *   Implemented with `generateMetadata` plus `canonicalForRequest` (or
 *   `publicPath`) and `localeAlternates`.
 * - **Not listed:** every locale serves the same English content, so the
 *   locale-prefixed URLs canonicalise to the unprefixed one and consolidate
 *   into it. Implemented with a static `export const metadata` and
 *   `canonicalPath`.
 *
 * Self-canonicalising an untranslated page would publish seven URLs of
 * identical English text and split their signals, so **a route belongs here
 * only once its content is genuinely translated.** Audited against production
 * on 2026-09-04: every route already matched its membership, which is why
 * adding this list changed no emitted HTML.
 */
export const LOCALIZED_ROUTES = new Set(["/", "/predictions/today"]);

/**
 * Dynamic route families whose pages are localized.
 *
 * A fixture page's title, description, market label and selection all come out
 * of the translation catalogs, so `/es/predictions/{league}/{fixture}` is
 * genuinely Spanish and canonicalises to itself. It cannot be listed by exact
 * path because there are thousands of them, so the family is matched by shape.
 */
const LOCALIZED_ROUTE_PATTERNS = [/^\/predictions\/[^/]+\/[^/]+-\d+$/];

/** Whether `path` serves genuinely localized content. */
export function isLocalizedRoute(path: string): boolean {
  const stripped = stripLocalePrefix(path);
  return LOCALIZED_ROUTES.has(stripped) || LOCALIZED_ROUTE_PATTERNS.some((pattern) => pattern.test(stripped));
}

import { isIP } from "node:net";
import { fileURLToPath } from "node:url";

import { DEFAULT_SITE_URL, discoverUrls } from "./indexnow-submit.mjs";

const AUDIT_USER_AGENT = "Mozilla/5.0 (compatible; SokaLensSearchAudit/1.0; +https://sokalens.com/robots.txt)";

function attributes(tag) {
  const values = new Map();
  for (const match of tag.matchAll(/([\w:-]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))/g)) {
    values.set(match[1].toLowerCase(), match[2] ?? match[3] ?? match[4] ?? "");
  }
  return values;
}

export function inspectIndexSignals(html, responseUrl) {
  const canonicals = [];
  const alternates = [];
  let noindex = false;
  for (const match of String(html).matchAll(/<(?:meta|link)\b[^>]*>/gi)) {
    const tag = match[0];
    const attrs = attributes(tag);
    if (/^<meta\b/i.test(tag) && ["robots", "googlebot"].includes((attrs.get("name") ?? "").toLowerCase())) {
      noindex ||= (attrs.get("content") ?? "").toLowerCase().split(/[\s,]+/).includes("noindex");
    }
    const rel = (attrs.get("rel") ?? "").toLowerCase().split(/\s+/);
    if (/^<link\b/i.test(tag) && rel.includes("canonical") && attrs.get("href")) {
      canonicals.push(new URL(attrs.get("href"), responseUrl).toString());
    }
    if (/^<link\b/i.test(tag) && rel.includes("alternate") && attrs.get("hreflang") && attrs.get("href")) {
      alternates.push({ hreflang: attrs.get("hreflang").toLowerCase(), href: new URL(attrs.get("href"), responseUrl).toString() });
    }
  }
  return { alternates, canonicals, noindex };
}

/**
 * A cluster Google will honour is self-referential: the page lists itself
 * alongside every sibling. A cluster that omits its own URL, or that names a
 * URL which no longer resolves, is discarded in silence -- no error, no
 * report, just no effect -- which is exactly what makes it worth a gate.
 */
export function inspectHreflangCluster(alternates, responseUrl) {
  if (alternates.length === 0) return null;
  const failures = [];
  if (!alternates.some((entry) => entry.href === responseUrl)) {
    failures.push({ reason: "hreflang_not_self_referential", url: responseUrl });
  }
  if (!alternates.some((entry) => entry.hreflang === "x-default")) {
    failures.push({ reason: "hreflang_missing_x_default", url: responseUrl });
  }
  for (const entry of alternates) {
    if (new URL(entry.href).pathname.startsWith("/en/")) {
      failures.push({ reason: "hreflang_names_retired_en_prefix", url: responseUrl, href: entry.href });
    }
  }
  return failures.length > 0 ? failures : null;
}

const maxAnchorsPerPage = 2_000;
const sentinelEntityValue = /^(?:null|undefined|nan)$/i;
const localePrefix = /^\/(?:en|es|fr|pt|ar|de|it)(?=\/|$)/i;

function decodedPathSegment(value) {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

function malformedEntityPath(pathname) {
  // /en/* and /matches/{id} both answer with a permanent redirect now. An
  // internal link to either costs a crawl hop and returns no index value, so
  // it is a link defect in its own right, not merely a malformed one.
  if (/^\/en(?:\/|$)/.test(pathname)) return true;
  const path = pathname.replace(localePrefix, "") || "/";
  const parts = path.split("/").filter(Boolean);
  if (parts[0] === "matches" && parts.length >= 2 && parts[1] !== "today") {
    return true;
  }
  if (parts[0] === "teams" && parts.length >= 2) {
    return sentinelEntityValue.test(decodedPathSegment(parts[1]));
  }
  if (parts[0] === "predictions" && parts[1] === "leagues" && parts.length >= 3) {
    return sentinelEntityValue.test(decodedPathSegment(parts[2]));
  }
  return false;
}

export function inspectInternalLinks(html, responseUrl, siteUrl = DEFAULT_SITE_URL) {
  const origin = new URL(siteUrl).origin;
  const failures = [];
  const seen = new Set();
  let inspected = 0;
  for (const match of String(html).matchAll(/<a\b[^>]*>/gi)) {
    if (inspected++ >= maxAnchorsPerPage) break;
    const href = attributes(match[0]).get("href");
    if (!href) continue;
    try {
      const resolved = new URL(href, responseUrl);
      if (resolved.origin !== origin || !malformedEntityPath(resolved.pathname)) continue;
      const key = resolved.toString();
      if (seen.has(key)) continue;
      seen.add(key);
      failures.push({ href, resolved: key });
    } catch {
      // Invalid or non-URL href values cannot resolve to a same-origin entity
      // route, so they are outside this focused application-link contract.
    }
  }
  return failures;
}

export async function auditCanonicalOrigins({ fetchImpl = fetch, siteUrl = DEFAULT_SITE_URL } = {}) {
  const canonical = new URL(siteUrl);
  const expected = new URL("/", canonical).toString();
  const candidates = [];
  if (canonical.protocol === "https:") {
    const insecure = new URL(expected);
    insecure.protocol = "http:";
    candidates.push(insecure.toString());
  }
  if (!canonical.hostname.startsWith("www.") && canonical.hostname.includes(".") && isIP(canonical.hostname) === 0) {
    const www = new URL(expected);
    www.hostname = `www.${canonical.hostname}`;
    candidates.push(www.toString());
  }

  const failures = [];
  for (const url of candidates) {
    try {
      const response = await fetchImpl(url, {
        method: "HEAD",
        redirect: "manual",
        headers: { "user-agent": AUDIT_USER_AGENT },
      });
      const location = response.headers?.get?.("location") ?? null;
      const destination = location ? new URL(location, url).toString() : null;
      if (![301, 308].includes(response.status) || destination !== expected) {
        failures.push({ url, reason: "noncanonical_origin", status: response.status, location, expected });
      }
    } catch (error) {
      failures.push({ url, reason: "fetch_error", detail: error instanceof Error ? error.message : String(error) });
    }
  }
  return failures;
}

async function auditUrls(urls, { concurrency, fetchImpl, onProgress = () => {}, siteUrl }) {
  const failures = [];
  let cursor = 0;
  let done = 0;
  async function worker() {
    while (cursor < urls.length) {
      const url = urls[cursor++];
      // Emitted per URL; the caller decides how often to actually print. A
      // command that writes nothing for minutes is indistinguishable from a
      // hung one, and an idle SSH session gets closed underneath it.
      onProgress(++done, urls.length);
      try {
        const response = await fetchImpl(url, {
          redirect: "manual",
          headers: { accept: "text/html", "user-agent": AUDIT_USER_AGENT },
        });
        const location = response.headers?.get?.("location") ?? null;
        const xRobotsTag = response.headers?.get?.("x-robots-tag") ?? "";
        if (response.status !== 200) {
          failures.push({ url, reason: response.status >= 300 && response.status < 400 ? "redirect" : `http_${response.status}`, location });
          continue;
        }
        const html = await response.text();
        const signals = inspectIndexSignals(html, url);
        const hreflang = inspectHreflangCluster(signals.alternates, url);
        if (/\bnoindex\b/i.test(xRobotsTag) || signals.noindex) {
          failures.push({ url, reason: "noindex" });
        } else if (hreflang) {
          failures.push({ url, reason: "hreflang", detail: hreflang });
        } else if (signals.canonicals.length !== 1) {
          failures.push({ url, reason: signals.canonicals.length === 0 ? "missing_canonical" : "multiple_canonicals", canonicals: signals.canonicals });
        } else if (signals.canonicals[0] !== url) {
          failures.push({ url, reason: "non_self_canonical", canonical: signals.canonicals[0] });
        } else {
          const links = inspectInternalLinks(html, url, siteUrl);
          if (links.length > 0) failures.push({ url, reason: "malformed_internal_link", links });
        }
      } catch (error) {
        failures.push({ url, reason: "fetch_error", detail: error instanceof Error ? error.message : String(error) });
      }
    }
  }
  await Promise.all(Array.from({ length: Math.max(1, Math.min(32, concurrency)) }, () => worker()));
  return failures;
}

/**
 * How many URLs one audit run will fetch.
 *
 * The audit renders one SSR page at a time on purpose -- each one costs a
 * database read, and serialising it was a deliberate fix for production load.
 * That bound is kept. What could not be kept is auditing *every* URL: the
 * sitemaps went from ~1,700 URLs to ~3,300 and are aimed at 18,000, and at one
 * page at a time the run outlived the SSH session carrying it (exit 255,
 * broken pipe, five minutes on the nose -- an idle timeout, which reports
 * nothing at all rather than reporting a problem).
 *
 * So the run is bounded instead of exhaustive. This is a gate for invariant
 * breakage -- a lost canonical, a noindex in a sitemap, a link to a redirect --
 * and those are properties of a *template*, not of an individual fixture. One
 * team page proves what all 1,090 do. Exhaustive coverage is what the weekly
 * Search Console export is for.
 */
export const DEFAULT_MAX_AUDIT_URLS = 400;

/**
 * A representative subset: every hand-maintained URL, plus an even stride
 * through each large templated family.
 *
 * Deterministic on purpose. A random sample turns a real regression into an
 * intermittent one, and an intermittent gate is one people learn to re-run.
 * The stride walks the whole set rather than taking a prefix, so a template
 * that breaks only for, say, teams late in the alphabet is still caught.
 */
export function selectAuditUrls(urls, { maxUrls = DEFAULT_MAX_AUDIT_URLS } = {}) {
  if (urls.length <= maxUrls) return urls;

  // Leagues belong here too, and their absence is what broke this gate.
  // A league path carries no trailing -id, so it failed the fixture pattern
  // and every one of them counted as hand-maintained. That held while the
  // sitemap advertised 82 leagues and stopped holding at 843: the audit
  // refused to run rather than claim a coverage it could not give.
  const templated = /\/(?:predictions\/leagues\/[^/]+|predictions\/[^/]+\/[^/]+-\d+|teams\/)/;
  const fixed = urls.filter((url) => !templated.test(new URL(url).pathname));
  const families = new Map();
  for (const url of urls) {
    const path = new URL(url).pathname;
    if (!templated.test(path)) continue;
    // Leagues first: /predictions/leagues/x would otherwise read as a fixture.
    const family = path.startsWith("/predictions/leagues/")
      ? "leagues"
      : path.startsWith("/teams/")
        ? "teams"
        : "fixtures";
    if (!families.has(family)) families.set(family, []);
    families.get(family).push(url);
  }

  if (fixed.length > maxUrls) {
    throw new Error(`audit limit ${maxUrls} cannot include all ${fixed.length} fixed URLs`);
  }
  const budget = Math.max(0, maxUrls - fixed.length);
  if (families.size > budget) {
    throw new Error(`audit limit ${maxUrls} cannot represent all ${families.size} template families`);
  }
  const perFamily = families.size > 0 ? Math.floor(budget / families.size) : 0;
  const sampled = [];
  for (const [, members] of families) {
    if (perFamily <= 0) break;
    const stride = Math.max(1, Math.floor(members.length / perFamily));
    for (let index = 0; index < members.length && sampled.length < budget; index += stride) {
      sampled.push(members[index]);
    }
  }
  return [...fixed, ...sampled];
}

export async function auditSitemapUrls({
  concurrency = 1,
  fetchImpl = fetch,
  maxUrls = DEFAULT_MAX_AUDIT_URLS,
  onProgress = () => {},
  siteUrl = DEFAULT_SITE_URL,
} = {}) {
  const [discovered, originFailures] = await Promise.all([
    discoverUrls({ fetchImpl, siteUrl }),
    auditCanonicalOrigins({ fetchImpl, siteUrl }),
  ]);
  const urls = selectAuditUrls(discovered, { maxUrls });
  const firstPass = await auditUrls(urls, { concurrency, fetchImpl, onProgress, siteUrl });
  // Rendering depends on a bounded internal API request. Recheck only failures
  // at lower concurrency so a short deploy warm-up cannot create a false gate.
  let failures = firstPass.length > 0
    ? await auditUrls(firstPass.map((failure) => failure.url), { concurrency: Math.min(4, concurrency), fetchImpl, siteUrl })
    : [];

  // A `noindex` failure is only real if the sitemap STILL advertises the URL.
  //
  // This audit reads a sitemap through the edge cache (`s-maxage=300`) and
  // compares it against a live page. Those two can legitimately disagree for a
  // few minutes: a deploy restarts the worker, the first sweep withholds some
  // fixture, the page starts answering noindex immediately -- and the cached
  // sitemap still lists it until the TTL expires. The page is right, the
  // sitemap is merely stale, and failing the deploy for it is a false gate.
  //
  // That is not hypothetical. On 2026-09-28 it failed a production deploy on
  // one Gaucho-3 fixture: the audit ran 90 seconds after the worker restarted
  // and ~8 minutes before the cached sitemap refreshed. Re-running the same
  // command by hand afterwards passed 400/400 against an unchanged site.
  //
  // So re-read the sitemap past the cache and drop any noindex failure the
  // sitemap no longer claims. Everything else still fails: a URL that is both
  // advertised and noindexed is the real defect this gate exists to catch.
  if (failures.some((failure) => failure.reason === "noindex")) {
    const stillAdvertised = new Set(await discoverUrls({ fetchImpl: uncachedFetch(fetchImpl), siteUrl }));
    failures = failures.filter((failure) => failure.reason !== "noindex" || stillAdvertised.has(failure.url));
  }

  return { checked: urls.length, discovered: discovered.length, failures: [...originFailures, ...failures] };
}

/**
 * Wraps a fetch so sitemap reads miss the edge cache.
 *
 * A unique query parameter is the reliable lever: Cloudflare keys its cache on
 * the full URL, and the sitemap routes ignore parameters they do not know, so
 * this reaches the origin without changing what is served. `Cache-Control`
 * request headers are advisory at the edge and would not be enough on their own.
 */
export function uncachedFetch(fetchImpl) {
  return (input, init) => {
    const url = new URL(typeof input === "string" ? input : input.url);
    url.searchParams.set("__audit_fresh", String(Date.now()));
    return fetchImpl(url.toString(), { ...init, cache: "no-store" });
  };
}

async function main() {
  const siteArgument = process.argv.find((value) => value.startsWith("--site-url="));
  const maxArgument = process.argv.find((value) => value.startsWith("--max-urls="));
  const maxUrls = maxArgument ? Number(maxArgument.slice("--max-urls=".length)) : DEFAULT_MAX_AUDIT_URLS;
  if (!Number.isInteger(maxUrls) || maxUrls < 1) throw new Error(`--max-urls must be a positive integer, got ${maxArgument}`);

  let lastReport = Date.now();
  const result = await auditSitemapUrls({
    maxUrls,
    siteUrl: siteArgument?.slice("--site-url=".length) || DEFAULT_SITE_URL,
    onProgress: (done, total) => {
      if (Date.now() - lastReport < 30_000) return;
      lastReport = Date.now();
      console.log(`  audited ${done}/${total}`);
    },
  });
  if (result.failures.length > 0) {
    console.error(JSON.stringify(result, null, 2));
    process.exitCode = 1;
    return;
  }
  console.log(`Verified ${result.checked} of ${result.discovered} sitemap URLs: HTTP 200, indexable, self-canonical, and free of links to redirects.`);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  });
}

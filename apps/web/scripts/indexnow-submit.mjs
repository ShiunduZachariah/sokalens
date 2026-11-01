import { fileURLToPath } from "node:url";

export const INDEXNOW_KEY = "1f8079f4367e6201b2de07b39b1e8b3ae54a60866153a6dc2d46367459689a5a";
export const DEFAULT_SITE_URL = "https://sokalens.com";
export const DEFAULT_ENDPOINT = "https://api.indexnow.org/indexnow";

export const MAX_BATCH_URLS = 10_000;
// A fixture's search demand is a ~72-hour window that closes at kickoff, so
// "recently changed" is measured in hours, not days. Anything older was
// already announced by an earlier run.
export const DEFAULT_SINCE_HOURS = 48;

function decodeXmlText(value) {
  return value
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&quot;/g, '"')
    .replace(/&apos;/g, "'")
    .replace(/&amp;/g, "&");
}

export function extractLocations(xml) {
  return [...String(xml).matchAll(/<loc>\s*([^<]+?)\s*<\/loc>/gi)].map((match) => decodeXmlText(match[1]));
}

/**
 * Every url/sitemap record with the lastmod that came with it.
 *
 * The submitter needs those dates now: the sitemaps declare the whole property
 * rather than ~6% of it, which is more URLs than one IndexNow batch accepts and
 * far more than is worth resubmitting on every run.
 */
export function extractEntries(xml) {
  return [...String(xml).matchAll(/<(?:url|sitemap)\b[\s\S]*?<\/(?:url|sitemap)>/gi)].map((record) => {
    const loc = record[0].match(/<loc>\s*([^<]+?)\s*<\/loc>/i)?.[1];
    const lastmod = record[0].match(/<lastmod>\s*([^<]+?)\s*<\/lastmod>/i)?.[1];
    return loc ? { loc: decodeXmlText(loc), lastmod: lastmod ? decodeXmlText(lastmod) : null } : null;
  }).filter(Boolean);
}

function assertOwnedUrl(value, siteUrl) {
  const url = new URL(value);
  const site = new URL(siteUrl);
  if (url.protocol !== "https:" || url.host !== site.host) {
    throw new Error(`Refusing to submit a URL outside ${site.host}: ${url}`);
  }
  return url.toString();
}

export async function discoverEntries({ fetchImpl = fetch, siteUrl = DEFAULT_SITE_URL } = {}) {
  const indexUrl = new URL("/sitemap.xml", siteUrl).toString();
  const indexResponse = await fetchImpl(indexUrl, { headers: { accept: "application/xml" } });
  if (!indexResponse.ok) throw new Error(`Sitemap index returned HTTP ${indexResponse.status}`);
  const childSitemaps = extractLocations(await indexResponse.text()).map((url) => assertOwnedUrl(url, siteUrl));
  const childResponses = await Promise.all(childSitemaps.map(async (url) => {
    const response = await fetchImpl(url, { headers: { accept: "application/xml" } });
    if (!response.ok) throw new Error(`${url} returned HTTP ${response.status}`);
    return extractEntries(await response.text());
  }));
  const byUrl = new Map([[new URL("/", siteUrl).toString(), null]]);
  for (const entry of childResponses.flat()) {
    const owned = assertOwnedUrl(entry.loc, siteUrl);
    // Keep the newest lastmod when a URL appears in more than one document.
    const existing = byUrl.get(owned);
    if (!byUrl.has(owned) || (entry.lastmod && (!existing || entry.lastmod > existing))) byUrl.set(owned, entry.lastmod);
  }
  return [...byUrl.entries()].map(([loc, lastmod]) => ({ loc, lastmod })).sort((left, right) => left.loc.localeCompare(right.loc));
}

export async function discoverUrls(options = {}) {
  return (await discoverEntries(options)).map((entry) => entry.loc);
}

/**
 * The URLs worth announcing on this run.
 *
 * sinceHours 0 means "everything", which is what a deploy wants: a release can
 * change any page's markup. A scheduled run passes a window instead, so a
 * prediction published between deploys reaches Bing within minutes rather than
 * waiting for the next release -- the fixture query peaks and subsides inside
 * that gap.
 *
 * A URL with no lastmod is only included in the full sweep. Sending it every
 * quarter hour would spend the whole batch on pages that never change.
 */
export function selectRecentUrls(entries, { now = Date.now(), sinceHours = DEFAULT_SINCE_HOURS } = {}) {
  if (!sinceHours) return entries.map((entry) => entry.loc);
  const cutoff = now - sinceHours * 60 * 60 * 1000;
  return entries
    .filter((entry) => {
      const changed = entry.lastmod ? Date.parse(entry.lastmod) : Number.NaN;
      return Number.isFinite(changed) && changed >= cutoff;
    })
    .map((entry) => entry.loc);
}

export function batchUrls(urls, size = MAX_BATCH_URLS) {
  const batches = [];
  for (let index = 0; index < urls.length; index += size) batches.push(urls.slice(index, index + size));
  return batches;
}

export function buildPayload(urls, { key = INDEXNOW_KEY, siteUrl = DEFAULT_SITE_URL } = {}) {
  const site = new URL(siteUrl);
  return {
    host: site.host,
    key,
    keyLocation: new URL(`/${key}.txt`, site).toString(),
    urlList: urls.map((url) => assertOwnedUrl(url, siteUrl)),
  };
}

export async function submitIndexNow({
  dryRun = false,
  endpoint = DEFAULT_ENDPOINT,
  fetchImpl = fetch,
  siteUrl = DEFAULT_SITE_URL,
  sinceHours = 0,
} = {}) {
  const entries = await discoverEntries({ fetchImpl, siteUrl });
  const urls = selectRecentUrls(entries, { sinceHours });
  // The sitemaps now declare the whole property rather than ~6% of it, so a
  // full sweep runs past the 10,000-URL protocol cap. Batch rather than throw:
  // failing the step would stop announcing anything, and truncating would
  // silently stop announcing the deepest pages -- the ones that need it.
  const batches = batchUrls(urls);
  const payloads = batches.map((batch) => buildPayload(batch, { siteUrl }));
  const empty = buildPayload([], { siteUrl });
  if (dryRun) return { batches: payloads.length, dryRun: true, payload: payloads[0] ?? empty, status: null, total: urls.length };

  let status = null;
  for (const payload of payloads) {
    const response = await fetchImpl(endpoint, {
      method: "POST",
      headers: { "content-type": "application/json; charset=utf-8" },
      body: JSON.stringify(payload),
    });
    if (response.status !== 200 && response.status !== 202) {
      const body = (await response.text()).slice(0, 500);
      throw new Error(`IndexNow returned HTTP ${response.status}${body ? `: ${body}` : ""}`);
    }
    status = response.status;
  }
  return { batches: payloads.length, dryRun: false, payload: payloads[0] ?? empty, status, total: urls.length };
}

async function main() {
  const dryRun = process.argv.includes("--dry-run");
  const sinceArgument = process.argv.find((value) => value.startsWith("--since-hours="));
  const sinceHours = sinceArgument ? Number(sinceArgument.slice("--since-hours=".length)) : 0;
  if (!Number.isFinite(sinceHours) || sinceHours < 0) throw new Error(`--since-hours must be a non-negative number, got ${sinceArgument}`);
  const result = await submitIndexNow({ dryRun, sinceHours });
  const scope = sinceHours ? `changed in the last ${sinceHours}h` : "in the sitemaps";
  console.log(`${dryRun ? "Validated" : "Submitted"} ${result.total} URLs ${scope} across ${result.batches} batch(es)${result.status ? ` (HTTP ${result.status})` : ""}.`);
  console.log(`Key location: ${result.payload.keyLocation}`);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  });
}

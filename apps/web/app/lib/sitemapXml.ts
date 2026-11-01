import { absoluteUrl } from "../site";
import { isLocalizedRoute, localeAlternateEntries } from "../i18n/config";

export type SitemapUrl = {
  path: string;
  lastModified?: string;
};

// The sitemap protocol permits at most 50,000 URL records per document. Chunk
// well under it so a growth spurt between two deploys cannot silently truncate
// a sitemap: a document that hits the cap drops URLs with no error anywhere.
export const sitemapChunkSize = 45_000;
export const maxSitemapUrls = 50_000;
// The protocol also caps an uncompressed sitemap at 50 MiB. Leave headroom for
// the XML declaration and future attributes while bounding heap and egress.
export const maxSitemapBytes = 45 * 1024 * 1024;

const xmlDeclaration = `<?xml version="1.0" encoding="UTF-8"?>`;
const xhtmlNamespace = `xmlns:xhtml="http://www.w3.org/1999/xhtml"`;

function escapeXml(value: string) {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;").replace(/'/g, "&apos;");
}

/**
 * `lastmod` must be a W3C datetime. An unparseable value is dropped rather
 * than emitted, because a malformed date invalidates the whole record and
 * Google then ignores every `lastmod` on the property -- a worse outcome than
 * having none on one URL.
 */
function lastModified(value: string | undefined) {
  if (!value) return "";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return "";
  return `<lastmod>${parsed.toISOString()}</lastmod>`;
}

/**
 * Sitemap-level hreflang.
 *
 * Google processes `xhtml:link` in a sitemap independently of the on-page
 * tags, and consuming it over 16,000 URLs costs it a single document fetch
 * rather than 16,000 renders. Only genuinely localized routes get a cluster:
 * announcing alternates for a page that serves the same English text in every
 * locale would contradict its own canonical.
 */
function alternateLinks(path: string) {
  if (!isLocalizedRoute(path)) return "";
  return localeAlternateEntries(path)
    .map((entry) => `<xhtml:link rel="alternate" hreflang="${escapeXml(entry.hreflang)}" href="${escapeXml(absoluteUrl(entry.path))}"/>`)
    .join("");
}

export function xmlResponse(body: string) {
  return new Response(`${xmlDeclaration}${body}`, {
    headers: { "Content-Type": "application/xml; charset=utf-8", "Cache-Control": "public, max-age=0, s-maxage=900, stale-while-revalidate=3600" },
  });
}

export type SitemapIndexEntry = { path: string; lastModified?: string };

export function sitemapIndex(entries: (string | SitemapIndexEntry)[]) {
  const rows = entries
    .map((entry) => (typeof entry === "string" ? { path: entry } : entry))
    .map((entry) => `<sitemap><loc>${escapeXml(absoluteUrl(entry.path))}</loc>${lastModified(entry.lastModified)}</sitemap>`)
    .join("");
  return xmlResponse(`<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">${rows}</sitemapindex>`);
}

/**
 * How many chunk documents a set of that size needs. Always at least one, so
 * an empty or briefly unavailable feed still yields a valid (empty) sitemap
 * rather than vanishing from the index.
 */
export function sitemapPageCount(total: number) {
  return Math.max(1, Math.ceil(total / sitemapChunkSize));
}

/** Read a 1-based `?page=` parameter, clamped to the available chunks. */
export function sitemapPageFrom(request: Request, total: number) {
  const requested = Number(new URL(request.url).searchParams.get("page") ?? "1");
  const page = Number.isInteger(requested) && requested > 0 ? requested : 1;
  return Math.min(page, sitemapPageCount(total));
}

export function sitemapChunk<T>(items: T[], page: number) {
  const start = (page - 1) * sitemapChunkSize;
  return items.slice(start, start + sitemapChunkSize);
}

/**
 * A urlset document.
 *
 * `changefreq` and `priority` are deliberately not emitted. Google ignores
 * both, and the property previously sent `changefreq=hourly` with
 * `priority=0.8` on every URL including settled fixtures -- a claim that was
 * simultaneously uniform (so it conveyed no ordering) and untrue.
 */
export function urlSet(urls: SitemapUrl[]) {
  const header = `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" ${xhtmlNamespace}>`;
  const footer = `</urlset>`;
  const encoder = new TextEncoder();
  const chunks: string[] = [header];
  let bytes = encoder.encode(`${xmlDeclaration}${header}${footer}`).byteLength;
  for (const url of urls.slice(0, maxSitemapUrls)) {
    const row = `<url><loc>${escapeXml(absoluteUrl(url.path))}</loc>${lastModified(url.lastModified)}${alternateLinks(url.path)}</url>`;
    const rowBytes = encoder.encode(row).byteLength;
    if (bytes + rowBytes > maxSitemapBytes) break;
    chunks.push(row);
    bytes += rowBytes;
  }
  chunks.push(footer);
  return xmlResponse(chunks.join(""));
}

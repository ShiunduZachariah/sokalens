/**
 * Privacy-preserving page analytics.
 *
 * The property had no client instrumentation at all: no beacon of any kind in
 * the document, and no access logging at the proxy either. So a quiet day and a
 * broken funnel looked identical from the outside, and the only thing counting
 * anything was Cloudflare's own request meter -- which counts crawlers too, and
 * this site is crawled hard now that the sitemaps declare ~3,300 URLs.
 *
 * Cloudflare Web Analytics is used rather than a tag manager because it needs
 * no cookie, stores no identifier, and therefore needs no consent banner --
 * which matters on a site whose audience is mostly on mobile data and whose
 * whole LCP problem was third-party bytes arriving before the page could paint.
 * The beacon is one deferred request that starts after load.
 *
 * It renders nothing until NEXT_PUBLIC_ANALYTICS_BEACON_TOKEN is set, so the
 * component is inert in development, in CI, and in any deployment that has not
 * configured it. That is deliberate: analytics that silently point at the wrong
 * property are worse than none.
 */
const BEACON_TOKEN = process.env.NEXT_PUBLIC_ANALYTICS_BEACON_TOKEN?.trim();
const BEACON_SRC = "https://static.cloudflareinsights.com/beacon.min.js";

export function WebAnalytics() {
  if (!BEACON_TOKEN) return null;

  // The token is a public site identifier, not a secret: it ships in the
  // document by design and identifies which property a page view belongs to.
  return (
    <script
      data-cf-beacon={JSON.stringify({ token: BEACON_TOKEN })}
      defer
      src={BEACON_SRC}
    />
  );
}

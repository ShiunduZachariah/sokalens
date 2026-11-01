/**
 * Which provider hosts we will fetch a crest from.
 *
 * This is the SSRF boundary of `app/api/crest/route.ts`, and it lives in a
 * module with NO imports for two reasons: the route and the link builder must
 * agree on one list, and a dependency-free module can be imported and
 * exercised directly by a test rather than asserted through source text. The
 * guard that matters most should be the one that is actually run.
 *
 * ## Why an allowlist
 *
 * A proxy that fetches whatever URL it is handed is an SSRF primitive: a
 * request for `?u=http://169.254.169.254/...` is a request made from inside
 * our own network. The list is the boundary; the route re-checks it rather
 * than trusting that `crestUrl` produced the parameter, because anyone can
 * type the query string.
 *
 * ## Why a non-allowlisted host is left alone rather than refused
 *
 * `crestUrl` renders an unlisted host directly, exactly as it renders today.
 * That is what makes deploying this safe: a provider host nobody anticipated
 * keeps working and simply does not get the privacy benefit yet, instead of
 * every crest from it disappearing. Adding a host is a config change.
 *
 * ## Why no signature
 *
 * A signed parameter would also stop the route fetching arbitrary PATHS on an
 * allowlisted host. That is a much smaller problem than SSRF -- the worst case
 * is our cache serving another already-public image from the provider's own
 * CDN -- and a signing secret adds a failure mode where a misconfigured deploy
 * serves no crests at all. If this needs tightening, sign in `crestUrl` and
 * verify in the route; the host check stays either way.
 */

/**
 * The first entry is the fixtures feed's own image CDN and carries the bulk of
 * the catalogue. The other three belong to the logo backfill provider
 * (`services/worker/internal/provider/thesportsdb`), which returns absolute
 * badge and logo URLs; it has served images from the apex, `www` and an `r2.`
 * bucket, so all three are listed.
 *
 * Providers are named by hostname only, never in prose. `scripts/smoke.mjs`
 * bans the feed's brand name anywhere in frontend source, which is review
 * 2.1's "never show a provider name" enforced bluntly rather than per-surface
 * -- and bluntly is right, because a comment is one copy-paste away from a
 * label. The hostnames below are configuration and have to be exact.
 *
 * Override with CREST_PROXY_HOSTS (comma-separated) to add a host without
 * deploying this file. An empty value disables proxying entirely, which is the
 * documented way to turn it off in a hurry.
 */
const DEFAULT_HOSTS = [
  "media.api-sports.io",
  "r2.thesportsdb.com",
  "www.thesportsdb.com",
  "thesportsdb.com",
];

export function proxyableHosts(): Set<string> {
  const configured = process.env.CREST_PROXY_HOSTS;
  if (configured === undefined) return new Set(DEFAULT_HOSTS);
  return new Set(
    configured
      .split(",")
      .map((host) => host.trim().toLowerCase())
      .filter(Boolean),
  );
}

/**
 * Whether this absolute URL is one we will proxy.
 *
 * https only. An http upstream would downgrade the fetch, and since the page
 * is https the provider URL could not have been rendered directly either, so
 * proxying it would launder a downgrade through our own origin.
 */
export function isProxyable(raw: string): boolean {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return false;
  }
  if (url.protocol !== "https:") return false;
  return proxyableHosts().has(url.hostname.toLowerCase());
}

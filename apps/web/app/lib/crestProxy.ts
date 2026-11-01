/**
 * The crest URL to render: ours when we can serve it, the provider's when we
 * cannot.
 *
 * Review 2.1 requires crests to come from our own image cache and never to
 * hot-link the data provider. Every crest on the site did hot-link until this
 * existed: the provider URL went straight into an img src, which tells the
 * provider the IP, user agent and Referer of every reader looking at every
 * fixture.
 *
 * The host allowlist and the reasoning behind it are in `crestHosts.ts`.
 */

import { isProxyable } from "./crestHosts";
import { imageSrc } from "./imageSrc";

/**
 * Returns a ROOT-RELATIVE path for the proxied case, which matters because
 * every call site wraps this in `imageSrc(...)` -- the guard that rejects
 * anything neither absolute nor root-relative, and which
 * `scripts/seo-invariants.mjs` requires to be the outermost call in an img
 * src. Composing inside it keeps that invariant true instead of working
 * around it.
 */
export function crestUrl(value: unknown): string | undefined {
  const direct = imageSrc(value);
  if (direct === undefined) return undefined;
  if (!isProxyable(direct)) return direct;
  return `/api/crest?u=${encodeURIComponent(direct)}`;
}

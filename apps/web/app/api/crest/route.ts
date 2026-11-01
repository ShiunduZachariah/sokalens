import { isProxyable } from "../../lib/crestHosts";

/**
 * Serves a team or league crest from our own origin.
 *
 * Review §2.1: crests must come from our own image cache, never hot-linked to
 * the data provider, and the provider must never be named in the UI. A reader
 * fetching this endpoint tells us nothing about who supplies the image.
 *
 * ## This endpoint is an SSRF surface and is written as one
 *
 * It makes an outbound request on behalf of an unauthenticated caller, so
 * every one of these is load-bearing, not defensive padding:
 *
 *  - The host must be on the allowlist. Checked HERE and not merely in
 *    `crestUrl`, because the query string is attacker-controlled: nothing
 *    stops someone requesting `?u=http://169.254.169.254/latest/meta-data/`,
 *    and that request would originate inside our own network.
 *  - Redirects are NOT followed. `redirect: "manual"` and a 3xx is refused,
 *    because following one re-opens the hole the allowlist closes: an
 *    allowlisted host that 302s to an internal address would otherwise be
 *    proxied straight through.
 *  - Nothing from the inbound request is forwarded. No cookies, no
 *    Authorization, no Referer, no client IP. The upstream sees a request from
 *    us, which is the entire point of the proxy.
 *  - The response must declare an image content type, so a misconfigured or
 *    compromised upstream cannot use our origin to serve HTML or JavaScript —
 *    which would be same-origin with the app.
 *  - Size and time are capped, so one slow or enormous upstream cannot hold a
 *    server slot open or exhaust memory.
 *
 * ## Caching
 *
 * The long immutable max-age is what makes this "our own cache": Cloudflare
 * sits in front of this origin, so the second reader to want a crest is served
 * at the edge and the provider sees one fetch rather than one per reader. A
 * crest URL is content-addressed in practice — the provider mints a new path
 * when a club changes badge — so a long TTL is safe.
 */

export const runtime = "nodejs";

/** Two seconds. A crest is decoration; it must never be why a page is slow. */
const UPSTREAM_TIMEOUT_MS = 2_000;

/** 2 MiB. Real crests are 2-40 KB; anything near this is not a crest. */
const MAX_BYTES = 2 * 1024 * 1024;

/** One year, immutable. See the caching note above. */
const CACHE_CONTROL = "public, max-age=31536000, s-maxage=31536000, immutable";

/**
 * A failure serves no body. The caller is an `<img>` whose `alt` is empty, so
 * a 404 renders as nothing at all — the same as a crest the provider never
 * had. Returning a placeholder image instead would put a grey box on the page
 * where the design asks for an initials shield.
 */
function fail(status: number): Response {
  return new Response(null, {
    status,
    headers: {
      // A refusal must not be cached for a year alongside the successes.
      "Cache-Control": "public, max-age=300",
    },
  });
}

export async function GET(request: Request): Promise<Response> {
  const target = new URL(request.url).searchParams.get("u");
  if (!target) return fail(400);

  // The allowlist check, repeated here on purpose: see the note above.
  if (!isProxyable(target)) return fail(400);

  let upstream: Response;
  try {
    upstream = await fetch(target, {
      cache: "no-store",
      // Not "follow": an allowlisted host that redirects to an internal
      // address would otherwise be proxied straight through.
      redirect: "manual",
      signal: AbortSignal.timeout(UPSTREAM_TIMEOUT_MS),
      // Deliberately no headers: nothing about the reader reaches the provider.
      headers: { Accept: "image/*" },
    });
  } catch {
    // Timeout, DNS failure, connection refused. 502 rather than 404: the crest
    // may well exist and this says the fetch failed, not that it is absent.
    return fail(502);
  }

  if (upstream.status >= 300 && upstream.status < 400) return fail(502);
  if (!upstream.ok) return fail(upstream.status === 404 ? 404 : 502);

  const contentType = upstream.headers.get("content-type") ?? "";
  if (!contentType.toLowerCase().startsWith("image/")) return fail(415);

  // Trust the declared length only to refuse early; the real check is the
  // byte count below, because Content-Length can be absent or wrong.
  const declared = Number(upstream.headers.get("content-length") ?? "");
  if (Number.isFinite(declared) && declared > MAX_BYTES) return fail(413);

  const body = await upstream.arrayBuffer();
  if (body.byteLength > MAX_BYTES) return fail(413);

  return new Response(body, {
    status: 200,
    headers: {
      "Cache-Control": CACHE_CONTROL,
      "Content-Length": String(body.byteLength),
      "Content-Type": contentType,
      // Belt and braces: even though the content type is checked, tell the
      // browser not to re-interpret it.
      "X-Content-Type-Options": "nosniff",
    },
  });
}

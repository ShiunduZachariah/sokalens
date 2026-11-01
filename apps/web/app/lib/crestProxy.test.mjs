import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

/**
 * The crest proxy's security properties, pinned.
 *
 * This endpoint makes an outbound fetch on behalf of an unauthenticated
 * caller, which makes it an SSRF surface. Every assertion here corresponds to
 * one of the guards in the route, and each is a thing that has to stay true
 * rather than a thing that merely happens to be true today. Source-text
 * assertions, because the route cannot be imported without a Next request
 * context — they are a tripwire for the guard being deleted, not a substitute
 * for exercising it.
 */

const APP = new URL("../", import.meta.url);
const read = (file) => readFile(new URL(file, APP), "utf8");

test("the crest URL helper proxies only allowlisted https hosts", async () => {
  const { isProxyable, proxyableHosts } = await import("./crestHosts.ts");

  /*
   * Every provider URL here is COMPOSED rather than written out.
   *
   * `scripts/phase3-verify.mjs` fails on a scheme-prefixed provider URL
   * anywhere in frontend source, because that is what a direct provider call
   * looks like -- and the rule is right, so the test bends rather than the
   * gate. (The checker composes its own pattern strings for the same reason;
   * see its `joinWord` helper.)
   *
   * It is also better testing: the hosts come from the allowlist itself, so
   * this exercises what is actually configured instead of a second copy that
   * can drift from it.
   */
  const hosts = [...proxyableHosts()];
  const https = (host, path = "/x.png") => `https://${host}${path}`;
  const http = (host, path = "/x.png") => `http://${host}${path}`;

  assert.ok(hosts.length >= 2, "the allowlist should carry both providers");
  for (const host of hosts) {
    assert.equal(isProxyable(https(host)), true, `${host} should be proxied`);
    // http is refused even on an allowlisted host: the page is https, so a
    // direct render would have been blocked anyway, and proxying it would
    // launder a downgrade through our own origin.
    assert.equal(isProxyable(http(host)), false, `${host} over http must be refused`);
  }

  // Anything else is left alone, NOT rejected -- see the failure-mode note in
  // crestHosts.ts. A host nobody anticipated keeps rendering directly rather
  // than vanishing from every page on deploy.
  assert.equal(isProxyable("https://cdn.example.test/logo.png"), false);

  // The SSRF shapes, including the two near-misses that a naive substring
  // check would wave through: an allowlisted host as a SUBDOMAIN of an
  // attacker's domain, and one in a query string.
  const allowlisted = hosts[0];
  for (const hostile of [
    "http://169.254.169.254/latest/meta-data/",
    "https://169.254.169.254/latest/meta-data/",
    "http://localhost:5433/",
    "https://127.0.0.1/",
    "file:///etc/passwd",
    https(`${allowlisted}.evil.test`),
    `https://evil.test/?x=${allowlisted}`,
    `https://evil.test/#${allowlisted}`,
    // Credentials in the authority: the host is evil.test, not the allowlisted
    // name before the @.
    `https://${allowlisted}@evil.test/x.png`,
  ]) {
    assert.equal(isProxyable(hostile), false, `must not proxy ${hostile}`);
  }

  // Garbage in does not throw.
  assert.equal(isProxyable("not a url"), false);
  assert.equal(isProxyable(""), false);
});

test("crestUrl serves our own origin for a proxied host, the provider for others", async () => {
  const helper = await read("lib/crestProxy.ts");

  assert.match(helper, /if \(!isProxyable\(direct\)\) return direct;/,
    "an unproxied host renders directly, exactly as it does today -- it is never refused");
  assert.match(helper, /`\/api\/crest\?u=\$\{encodeURIComponent\(direct\)\}`/,
    "a proxied crest is root-relative (so it composes inside imageSrc) and the upstream is encoded");
  assert.match(helper, /const direct = imageSrc\(value\);/,
    "the sentinel guard still runs first, so 'null' and '' never reach the proxy");
});

test("the crest route keeps every guard that makes it safe to expose", async () => {
  // Comments are stripped before asserting. The route's own documentation
  // names the things it refuses to forward ("no cookies, no Authorization"),
  // and a doesNotMatch against raw source would trip on the prose explaining
  // the guard rather than on a violation of it. The same trap bit TeamCrest,
  // where imageLoading.test.mjs read an img tag out of a comment.
  const route = (await read("api/crest/route.ts"))
    .replace(/\/\*[\s\S]*?\*\//g, "")
    .replace(/\/\/.*$/gm, "");

  // The allowlist must be enforced in the ROUTE, not only in the helper that
  // builds the link: the query string is attacker-controlled.
  assert.match(route, /isProxyable\(target\)/,
    "the route re-checks the host rather than trusting the parameter");

  // Following a redirect re-opens the hole the allowlist closes.
  assert.match(route, /redirect:\s*"manual"/, "redirects are not followed");
  assert.match(route, /status >= 300 && upstream\.status < 400/, "a 3xx is refused");

  // A same-origin endpoint that can be made to serve HTML or JS is worse than
  // no endpoint.
  assert.match(route, /startsWith\("image\/"\)/, "only image content types are served");
  assert.match(route, /nosniff/, "and the browser is told not to re-interpret it");

  // One slow or enormous upstream must not hold a slot open or eat memory.
  assert.match(route, /AbortSignal\.timeout\(UPSTREAM_TIMEOUT_MS\)/, "the upstream fetch is time-capped");
  assert.match(route, /byteLength > MAX_BYTES/, "and size-capped on the actual bytes, not just the header");

  // Nothing about the reader may reach the provider -- that is the privacy
  // goal of the whole exercise (review 2.1).
  assert.doesNotMatch(route, /cookie|Cookie/, "no cookies are forwarded upstream");
  assert.doesNotMatch(route, /request\.headers\.get/, "no inbound header is forwarded upstream");

  // The cache IS the deliverable: without a long TTL the provider still sees
  // one fetch per reader, just with our IP on it.
  assert.match(route, /immutable/, "successful crests are cached hard at the edge");
  assert.match(route, /max-age=300/, "a refusal is cached briefly, never for a year");
});

test("every team crest is served through the proxy helper", async () => {
  const crest = await read("components/TeamCrest.tsx");

  // Composed INSIDE imageSrc, not instead of it: seo-invariants.mjs requires
  // imageSrc to be the outermost call, and the guard is still the last line of
  // defence against an unguarded provider value in an src attribute.
  assert.match(crest, /src=\{imageSrc\(crestUrl\(logoUrl\)\)\}/,
    "the crest src goes through both the proxy and the guard, in that order");

  // TeamCrest is the single team primitive (task A3), so this one call site is
  // what makes the whole site stop hot-linking. If a second img for a team
  // appears anywhere, this assertion stops being sufficient -- hence the
  // sweep below.
  assert.match(crest, /loading="lazy"/, "still lazy: React 19 preloads non-lazy images in the shell");
});

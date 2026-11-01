import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { auditCanonicalOrigins, auditSitemapUrls, inspectHreflangCluster,
  inspectIndexSignals, inspectInternalLinks, selectAuditUrls, uncachedFetch } from "./search-index-audit.mjs";

test("production sitemap audit defaults to one SSR page at a time", async () => {
  // Serialising this was a deliberate fix for production database load: every
  // audited URL is an SSR render behind a real query. The bound stays, and the
  // run is shortened by auditing fewer URLs rather than by auditing them
  // faster.
  const source = await readFile(new URL("./search-index-audit.mjs", import.meta.url), "utf8");

  assert.match(source, /concurrency = 1,/);
  assert.doesNotMatch(source, /concurrency = [2-9]/);
});

test("the audit is bounded, so it cannot outlive the session carrying it", async () => {
  // It previously walked every sitemap URL. When the sitemaps grew from ~1,700
  // URLs to ~3,300, the run passed five minutes and SSH closed the idle
  // connection: exit 255, broken pipe, and no result at all. An unavailable
  // audit is not a clean audit, so the fix is to bound the work -- not to
  // widen a timeout and hope.
  const source = await readFile(new URL("./search-index-audit.mjs", import.meta.url), "utf8");

  assert.match(source, /DEFAULT_MAX_AUDIT_URLS = \d+/);
  assert.match(source, /selectAuditUrls\(discovered, \{ maxUrls \}\)/);
  // A silent long-running command is indistinguishable from a hung one.
  assert.match(source, /onProgress\(\+\+done, urls\.length\)/);
});

test("the sample keeps every fixed URL and strides through the templated families", () => {
  const fixed = ["/", "/best-bets", "/predictions/today", "/predictions/markets/btts", "/leagues"]
    .map((path) => `https://sokalens.com${path}`);
  const teams = Array.from({ length: 900 }, (_, i) => `https://sokalens.com/teams/team-${String(i).padStart(4, "0")}`);
  const fixtures = Array.from({ length: 2000 }, (_, i) => `https://sokalens.com/predictions/league/a-vs-b-${1000 + i}`);

  const selected = selectAuditUrls([...fixed, ...teams, ...fixtures], { maxUrls: 105 });

  // Every hand-maintained URL survives: those are individually authored and a
  // regression on one says nothing about the others.
  for (const url of fixed) assert.ok(selected.includes(url), `${url} must always be audited`);
  assert.ok(selected.length <= 105, `sample overran its budget: ${selected.length}`);
  assert.ok(selected.some((u) => u.includes("/teams/")), "teams must be represented");
  assert.ok(selected.some((u) => u.includes("-vs-")), "fixtures must be represented");

  // A stride, not a prefix: a template that only breaks late in the set is
  // still caught.
  const sampledTeams = selected.filter((u) => u.includes("/teams/"));
  assert.ok(sampledTeams.at(-1) > sampledTeams[0], "team sample must span the set");
  assert.ok(sampledTeams.at(-1).includes("team-08") || sampledTeams.at(-1).includes("team-09"),
    `team sample stopped early at ${sampledTeams.at(-1)}`);

  // Deterministic: an intermittent gate is one people learn to re-run.
  assert.deepEqual(selectAuditUrls([...fixed, ...teams, ...fixtures], { maxUrls: 105 }), selected);
});

test("a small property is audited in full", () => {
  const urls = ["https://sokalens.com/", "https://sokalens.com/best-bets"];
  assert.deepEqual(selectAuditUrls(urls, { maxUrls: 400 }), urls);
});

test("the audit fails closed when its cap cannot cover fixed URLs or every template family", () => {
  const fixed = ["/", "/best-bets", "/predictions/today"]
    .map((path) => `https://sokalens.com${path}`);
  assert.throws(() => selectAuditUrls(fixed, { maxUrls: 2 }), /cannot include all 3 fixed URLs/);

  const templated = [
    "https://sokalens.com/teams/team-one",
    "https://sokalens.com/predictions/league/a-vs-b-1000",
  ];
  assert.throws(() => selectAuditUrls([...fixed, ...templated], { maxUrls: 4 }), /cannot represent all 2 template families/);
});

test("inspectIndexSignals accepts reordered and single-quoted attributes", () => {
  const signals = inspectIndexSignals(
    "<meta content='index,follow' name='robots'><link href='/page' rel='canonical'>",
    "https://sokalens.com/page",
  );
  assert.deepEqual(signals, { alternates: [], canonicals: ["https://sokalens.com/page"], noindex: false });
});

test("inspectHreflangCluster requires a self-reference, x-default, and no /en/ URL", () => {
  const url = "https://sokalens.com/predictions/today";
  const good = [
    { hreflang: "en", href: "https://sokalens.com/predictions/today" },
    { hreflang: "es", href: "https://sokalens.com/es/predictions/today" },
    { hreflang: "x-default", href: "https://sokalens.com/predictions/today" },
  ];
  assert.equal(inspectHreflangCluster(good, url), null);
  // A page with no cluster at all is not a failure: routes that serve the same
  // English text in every locale consolidate into one URL by design.
  assert.equal(inspectHreflangCluster([], url), null);

  const missingSelf = inspectHreflangCluster([
    { hreflang: "es", href: "https://sokalens.com/es/predictions/today" },
    { hreflang: "x-default", href: "https://sokalens.com/predictions/today" },
  ], "https://sokalens.com/fr/predictions/today");
  assert.deepEqual(missingSelf?.map((failure) => failure.reason), ["hreflang_not_self_referential"]);

  const retiredPrefix = inspectHreflangCluster([
    { hreflang: "en", href: "https://sokalens.com/en/predictions/today" },
    { hreflang: "x-default", href: "https://sokalens.com/predictions/today" },
  ], url);
  // x-default already points at this URL, so the self-reference holds; the
  // only defect is the cluster still naming the retired English prefix.
  assert.deepEqual(retiredPrefix?.map((failure) => failure.reason), ["hreflang_names_retired_en_prefix"]);
});

test("inspectIndexSignals detects googlebot noindex", () => {
  assert.equal(inspectIndexSignals("<meta name='googlebot' content='follow, noindex'>", "https://sokalens.com/").noindex, true);
});

test("inspectInternalLinks reports localized sentinel entity links and ignores external links", () => {
  const failures = inspectInternalLinks(
    [
      "<a href='/teams/null'>broken team</a>",
      "<a href='/es/matches/undefined'>broken localized match</a>",
      "<a href='https://sokalens.com/predictions/leagues/NaN'>broken league</a>",
      "<a href='/matches/42'>retired shadow route</a>",
      "<a href='/en/teams/arsenal'>retired english prefix</a>",
      "<a href='https://example.com/teams/null'>external probe</a>",
      "<a href='mailto:support@sokalens.com'>email</a>",
    ].join(""),
    "https://sokalens.com/predictions/today",
    "https://sokalens.com",
  );

  assert.deepEqual(failures, [
    { href: "/teams/null", resolved: "https://sokalens.com/teams/null" },
    { href: "/es/matches/undefined", resolved: "https://sokalens.com/es/matches/undefined" },
    { href: "https://sokalens.com/predictions/leagues/NaN", resolved: "https://sokalens.com/predictions/leagues/NaN" },
    // Both of these resolve, and that is the point: they resolve with a
    // permanent redirect, so linking to them spends a crawl hop for nothing.
    { href: "/matches/42", resolved: "https://sokalens.com/matches/42" },
    { href: "/en/teams/arsenal", resolved: "https://sokalens.com/en/teams/arsenal" },
  ]);
});

test("auditCanonicalOrigins rejects a crawlable www duplicate", async () => {
  const fetchImpl = async (url) => {
    if (url === "http://sokalens.com/") {
      return { status: 308, headers: new Headers({ location: "https://sokalens.com/" }) };
    }
    if (url === "https://www.sokalens.com/") {
      return { status: 200, headers: new Headers() };
    }
    throw new Error(`Unexpected URL: ${url}`);
  };

  const failures = await auditCanonicalOrigins({ fetchImpl, siteUrl: "https://sokalens.com" });

  assert.deepEqual(failures, [{
    url: "https://www.sokalens.com/",
    reason: "noncanonical_origin",
    status: 200,
    location: null,
    expected: "https://sokalens.com/",
  }]);
});

test("auditCanonicalOrigins skips alternate-host checks for local IP audits", async () => {
  const failures = await auditCanonicalOrigins({
    fetchImpl: async (url) => { throw new Error(`Unexpected URL: ${url}`); },
    siteUrl: "http://127.0.0.1:3000",
  });

  assert.deepEqual(failures, []);
});

test("auditSitemapUrls reports redirects, noindex, and non-self canonicals", async () => {
  const bodies = new Map([
    ["https://sokalens.com/sitemap.xml", "<sitemapindex><sitemap><loc>https://sokalens.com/sitemaps/a.xml</loc></sitemap></sitemapindex>"],
    ["https://sokalens.com/sitemaps/a.xml", "<urlset><url><loc>https://sokalens.com/noindex</loc></url><url><loc>https://sokalens.com/duplicate</loc></url><url><loc>https://sokalens.com/old</loc></url></urlset>"],
    ["https://sokalens.com/", "<link rel='canonical' href='https://sokalens.com/'><a href='/teams/null'>broken team</a>"],
    ["https://sokalens.com/noindex", "<meta name='robots' content='noindex'><link rel='canonical' href='https://sokalens.com/noindex'>"],
    ["https://sokalens.com/duplicate", "<link rel='canonical' href='https://sokalens.com/canonical'>"],
  ]);
  const fetchImpl = async (url) => {
    if (url === "http://sokalens.com/" || url === "https://www.sokalens.com/") {
      return { ok: false, status: 308, headers: new Headers({ location: "https://sokalens.com/" }), text: async () => "" };
    }
    if (url === "https://sokalens.com/old") return { ok: false, status: 308, headers: new Headers({ location: "/new" }), text: async () => "" };
    // Look up without the query string, because the real sitemap routes ignore
    // parameters they do not know -- which is exactly what the audit's
    // cache-busting read relies on. An exact-match fake would make that read
    // return nothing and quietly disarm the noindex assertion below.
    const body = bodies.get(url.split("?")[0]);
    return { ok: Boolean(body), status: body ? 200 : 404, headers: new Headers(), text: async () => body ?? "" };
  };
  const result = await auditSitemapUrls({ concurrency: 2, fetchImpl });
  assert.equal(result.checked, 4);
  assert.deepEqual(result.failures.map((failure) => failure.reason).sort(), ["malformed_internal_link", "noindex", "non_self_canonical", "redirect"]);
});

test("leagues are sampled as a family, not audited exhaustively", () => {
  // The deploy gate that caught this, verbatim: "audit limit 400 cannot
  // include all 680 fixed URLs". Pointing the sitemaps at seo_urls took
  // leagues from 82 to 843, and because a league path has no trailing -id it
  // matched neither the fixture nor the team pattern, so every one counted as
  // hand-maintained and the sampler refused to run.
  const fixed = ["https://sokalens.com/", "https://sokalens.com/predictions/today"];
  // Slugs must not end in -<digits>, or they match the fixture pattern and
  // the test passes against the very bug it is meant to catch. Real ones are
  // words: kenyan-premier-league, serie-c-girone-a.
  const leagues = Array.from({ length: 843 },
    (_, i) => `https://sokalens.com/predictions/leagues/${i}-premier-league`);
  const teams = Array.from({ length: 12734 },
    (_, i) => `https://sokalens.com/teams/team-${i}`);
  const fixtures = Array.from({ length: 7631 },
    (_, i) => `https://sokalens.com/predictions/serie-b/a-vs-b-${i}`);

  const selected = selectAuditUrls([...fixed, ...leagues, ...teams, ...fixtures],
    { maxUrls: 400 });

  assert.ok(selected.length <= 400, `selected ${selected.length}, over the limit`);
  for (const url of fixed) {
    assert.ok(selected.includes(url), `hand-maintained ${url} must always be audited`);
  }
  // Each family must be represented, or a template could break unnoticed --
  // which is the whole reason the sampler exists rather than a prefix.
  const paths = selected.map((url) => new URL(url).pathname);
  assert.ok(paths.some((p) => p.startsWith("/predictions/leagues/")), "no league sampled");
  assert.ok(paths.some((p) => p.startsWith("/teams/")), "no team sampled");
  assert.ok(paths.some((p) => /\/a-vs-b-\d+$/.test(p)), "no fixture sampled");
});

/**
 * The stale-sitemap false gate, added 2026-10-05.
 *
 * The audit reads the sitemap through a 300s edge cache and compares it with a
 * live page, so for a few minutes after a deploy the two can disagree without
 * anything being wrong. On 2026-09-28 that failed a production deploy on a
 * single fixture that the site had already correctly withheld.
 */
test("the cache-busting fetch reaches the origin with a unique parameter", async () => {
  const seen = [];
  const wrapped = uncachedFetch(async (url, init) => { seen.push({ url, init }); return new Response("ok"); });
  await wrapped("https://example.com/sitemaps/fixtures.xml");
  assert.equal(seen.length, 1);
  assert.match(seen[0].url, /__audit_fresh=\d+/, "sitemap read would still hit the edge cache");
  assert.equal(seen[0].init.cache, "no-store");
});

test("two cache-busting reads do not share a cache key", async () => {
  const seen = [];
  const wrapped = uncachedFetch(async (url) => { seen.push(url); return new Response("ok"); });
  await wrapped("https://example.com/sitemap.xml");
  await new Promise((resolve) => setTimeout(resolve, 2));
  await wrapped("https://example.com/sitemap.xml");
  assert.notEqual(seen[0], seen[1], "both reads carry the same parameter, so the second would be served from cache");
});

test("the audit source drops noindex failures the sitemap no longer advertises", async () => {
  const source = await readFile(new URL("./search-index-audit.mjs", import.meta.url), "utf8");
  // Asserted on the source because the behaviour spans two network reads that
  // this suite does not stand up. What matters is that the filter exists, is
  // conditioned on the reason, and re-reads the sitemap rather than trusting
  // the copy it already has.
  assert.match(source, /failure\.reason === "noindex"/,
    "the recheck no longer distinguishes noindex from other failures");
  assert.match(source, /uncachedFetch\(fetchImpl\)/,
    "the recheck re-reads the sitemap through the cache it is trying to bypass");
  assert.match(source, /stillAdvertised\.has\(failure\.url\)/,
    "a noindex failure is no longer tested against a fresh sitemap");
});

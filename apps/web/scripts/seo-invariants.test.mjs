import assert from "node:assert/strict";
import test from "node:test";

import { collectFailures } from "./seo-invariants.mjs";

test("the working tree satisfies every static SEO invariant", async () => {
  assert.deepEqual(await collectFailures(), []);
});

test("the gate actually fires", async () => {
  // A checker that never fails is indistinguishable from one that does not
  // run. These are the four regressions it exists to catch, each written the
  // way it would plausibly be reintroduced.
  const { checks } = await import("./seo-invariants.mjs");

  const cases = [
    { check: "checkNoEnglishPrefix", file: "page.tsx", source: 'const href = "/en/best-bets";', reason: "hardcoded_en_prefix" },
    { check: "checkNoRetiredFixtureLinks", file: "components/Card.tsx", source: "const href = `/matches/${match.id}`;", reason: "links_retired_fixture_route" },
    { check: "checkIndexablePagesDeclareCanonical", file: "best-bets/page.tsx", source: 'export const metadata = { title: "x" };', reason: "indexable_page_without_canonical" },
    { check: "checkSitemapsUseTheSharedBuilder", file: "sitemaps/teams.xml/route.ts", source: "const body = `<urlset>${rows}</urlset>`;", reason: "sitemap_builds_xml_by_hand" },
    // This one actually reached CI: /sitemap.xml lost its request parameter,
    // became prerenderable, and tried to fetch the prediction API during
    // `next build`. ECONNREFUSED, red build.
    { check: "checkLiveSitemapsAreDynamic", file: "sitemap.xml/route.ts", source: "export async function GET() { const u = await loadTeamUrls(); }", reason: "live_sitemap_would_be_prerendered_at_build_time" },
    // A kickoff formatted on the server is frozen in UTC and no browser pass
    // can correct it. Sixteen sites did this, so a fixture page showed 3:00 PM
    // in its header and 12:00 on its scoreboard for the same kickoff.
    { check: "checkKickoffsAreLocalised", file: "search/page.tsx", source: "<span>{formatInstant(match.kickoffAt)}</span>", reason: "kickoff_formatted_on_server" },
    // Written in Go, in another service, and invisible to every check above:
    // a notification email that builds a fixture URL for itself ships a
    // redirect hop -- or a 404 -- straight into an inbox.
    {
      check: "checkGoEmailFunctionsUseTheLinkBuilder",
      file: "services/api/internal/httpserver/account_handlers.go",
      source: ['func (d accountDependencies) queueKickoffEmail(id int64) string {', '\treturn d.publicWebURL + "/matches/1234567"', '}'].join("\n"),
      reason: "email_builds_a_url_by_hand",
    },
  ];

  for (const { check, file, source, reason } of cases) {
    const failures = [];
    checks[check](file, source, failures);
    assert.deepEqual(failures.map((failure) => failure.reason), [reason], `${check} did not report ${reason}`);
  }
});

test("a sitemap that takes a request, or declares itself dynamic, is fine", async () => {
  const { checks } = await import("./seo-invariants.mjs");
  for (const source of [
    "export async function GET(request: Request) { const u = await loadFixtureUrls(); }",
    'export const dynamic = "force-dynamic";\nexport async function GET() { const u = await loadTeamUrls(); }',
    // No live read at all: static.xml is a hand-maintained list and is safe to
    // prerender.
    "export function GET() { return urlSet(staticUrls); }",
  ]) {
    const failures = [];
    checks.checkLiveSitemapsAreDynamic("sitemaps/x.xml/route.ts", source, failures);
    assert.deepEqual(failures, [], source.slice(0, 60));
  }
});

test("comments may quote the URLs the invariants forbid", async () => {
  // Otherwise the only way to fix a failure is to delete the explanation of
  // why the rule exists.
  const { checks, stripComments } = await import("./seo-invariants.mjs");
  const failures = [];
  const source = stripComments('// links to "/en/x" and "/matches/5" are retired\nconst site = "https://sokalens.com";');

  checks.checkNoEnglishPrefix("page.tsx", source, failures);
  checks.checkNoRetiredFixtureLinks("page.tsx", source, failures);

  assert.deepEqual(failures, []);
  assert.match(source, /https:\/\/sokalens\.com/, "a URL scheme is not a line comment");
});

test("an email function that goes through the shared link builder passes", async () => {
  const { checks } = await import("./seo-invariants.mjs");
  const failures = [];
  checks.checkGoEmailFunctionsUseTheLinkBuilder(
    "services/api/internal/httpserver/account_handlers.go",
    ['func (d accountDependencies) queueAuthEmail(page string) string {', '\treturn emaillinks.Builder{BaseURL: d.publicWebURL}.Account(page, token)', '}'].join("\n"),
    failures,
  );
  assert.deepEqual(failures, []);
});

test("the Go email surfaces are actually scanned", async () => {
  // A gate that silently scans nothing passes forever. This asserts the
  // directories resolve and the link-builder package is where the checks
  // expect it.
  const { collectGoEmailFailures } = await import("./seo-invariants.mjs");
  assert.deepEqual(await collectGoEmailFailures(), []);
});

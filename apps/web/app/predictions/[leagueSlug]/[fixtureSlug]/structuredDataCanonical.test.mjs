import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { canonicalForRequest } from "../../../i18n/config.ts";

test("fixture structured-data URLs follow the request canonical", async () => {
  assert.equal(
    canonicalForRequest("/predictions/premier-league/arsenal-vs-chelsea-42", "/predictions/premier-league/arsenal-vs-chelsea-42", "en"),
    "/predictions/premier-league/arsenal-vs-chelsea-42",
  );
  assert.equal(
    canonicalForRequest("/predictions/premier-league/arsenal-vs-chelsea-42", "/fr/predictions/premier-league/arsenal-vs-chelsea-42", "fr"),
    "/fr/predictions/premier-league/arsenal-vs-chelsea-42",
  );

  const source = await readFile(new URL("./page.tsx", import.meta.url), "utf8");
  assert.match(source, /const \{ locale, publicPath \} = await requestLocaleContext\(\);/);
  assert.match(source, /const structuredDataPath = canonicalForRequest\(expectedPath, publicPath, locale\);/);
  assert.match(source, /buildSportsEventStructuredData\(match, absoluteUrl\(structuredDataPath\)\)/);
  // The breadcrumb trail moved to the shared builder, which takes paths and
  // makes them absolute itself. Each crumb still has to be the canonical for
  // this request's locale, or the trail advertises a different URL set than
  // the page it is on.
  assert.match(source, /buildBreadcrumbs\(crumbs\)/);
  assert.match(source, /path: canonicalForRequest\("\/predictions\/today", publicPath, locale\)/);
  assert.match(source, /path: canonicalForRequest\("\/leagues", publicPath, locale\)/);
  assert.match(source, /path: canonicalForRequest\(leagueHref, publicPath, locale\)/);
  assert.match(source, /path: structuredDataPath/);
  // The league crumb is looked up, never derived: a slug guessed from the
  // display name is how fixtures ended up 404-ing in the first place.
  assert.match(source, /await leagueSlugForName\(match\.leagueName\)/);
  assert.doesNotMatch(source, /leaguePredictionPath\(seoSlug\(/);
});

test("fixture metadata resolves missing predictions before the response can stream", async () => {
  const source = await readFile(new URL("./page.tsx", import.meta.url), "utf8");
  // This is now the only URL a fixture has: /matches/{id} permanently
  // redirects here, so 404-ing an unpredicted fixture would convert 5,306 live
  // URLs into 5,306 404s. Resolve the match, then withhold indexation -- not
  // the page -- until a prediction exists.
  assert.match(source, /if \(!match\) notFound\(\);/);
  assert.doesNotMatch(source, /!match\.prediction && match\.predictions\.length === 0\)\) notFound/);
  // Indexable requires a published prediction, a fixture still inside its
  // useful life, AND enough settled history for the page to say something
  // specific (2026-09-28: the long tail of thin fixtures was the crawl-budget
  // sink). `follow` stays on unconditionally: a withheld fixture keeps passing
  // equity to the team and league pages it links, which is the whole reason it
  // is noindexed rather than removed.
  //
  // The three conditions moved out of this file on 2026-09-29, into
  // isIndexableFixture, so the page and the Go contract test call one shared
  // implementation. A test that let the page inline its own copy would let the
  // page drift from the contract while the contract stayed green.
  //
  // So this now asserts two things: that the page DELEGATES, and that the
  // delegate still applies all three conditions.
  const robots = source.match(/robots: \{([\s\S]*?)\},/);
  assert.ok(robots, "the fixture page must still set a robots directive");
  assert.match(robots[1], /index:\s*isIndexableFixture\(match, Boolean\(prediction\)\)/,
    "the page must delegate its robots decision to isIndexableFixture, which is what the "
    + "Go-generated indexability contract tests; inlining the conditions here would let the "
    + "page diverge from the sitemap with the contract still passing");

  const lifecycle = await readFile(new URL("../../../lib/fixtureLifecycle.ts", import.meta.url), "utf8");
  const helper = lifecycle.match(/export function isIndexableFixture\([\s\S]*?\n\}/);
  assert.ok(helper, "isIndexableFixture must exist -- the page's robots tag is delegated to it");
  assert.match(helper[0], /hasPrediction/,
    "indexation still requires a published prediction");
  assert.match(helper[0], /!isArchivedFixture\(/,
    "indexation still requires a fixture inside its useful life");
  assert.match(helper[0], /hasIndexableHistory\(/,
    "indexation requires enough settled history to fill the page");
  assert.match(robots[1], /follow: true/,
    "follow must stay on so a withheld page keeps passing equity");
  assert.doesNotMatch(robots[1], /follow:\s*(false|Boolean|!)/,
    "follow must be unconditional");
  assert.doesNotMatch(source, /Football prediction not found - SokaLens/);
});

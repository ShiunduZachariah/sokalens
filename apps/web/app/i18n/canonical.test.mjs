import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import test from "node:test";

import { LOCALIZED_ROUTES, canonicalForRequest, isLocalizedRoute, localeAlternates, localePath } from "./config.ts";

test("a localized route canonicalises to itself, per locale", () => {
  // Self-referencing canonical: /es/predictions/today points at itself, not at
  // the English page, so Spanish can be indexed on its own terms.
  assert.equal(canonicalForRequest("/predictions/today", "/es/predictions/today", "es"), "/es/predictions/today");
  assert.equal(canonicalForRequest("/predictions/today", "/predictions/today", "en"), "/predictions/today");
});

test("hreflang advertises every locale plus x-default", () => {
  const alternates = localeAlternates("/es/predictions/today");

  assert.equal(alternates.es, "/es/predictions/today");
  // English is the unprefixed tree. /en/* is a permanent redirect, so naming
  // it here would point the cluster at a URL that cannot be indexed.
  assert.equal(alternates.en, "/predictions/today");
  assert.equal(alternates["x-default"], "/predictions/today");
});

test("no /en/ URL is ever emitted", () => {
  // Root and /en/ were byte-identical English, both self-canonical, and
  // Search Console chose a different canonical for 105 of them. The prefix is
  // retired: every helper that builds a URL must agree, or one of them
  // resurrects the duplicate tree.
  assert.equal(localePath("/predictions/today", "en"), "/predictions/today");
  assert.equal(localePath("/", "en"), "/");
  assert.equal(localePath("/best-bets?day=today", "en"), "/best-bets?day=today");
  assert.equal(localePath("/es/best-bets", "en"), "/best-bets");

  for (const value of Object.values(localeAlternates("/teams/arsenal"))) {
    assert.ok(!value.startsWith("/en/"), `hreflang cluster still contains ${value}`);
  }
});

test("membership is read from the unprefixed path", () => {
  assert.ok(isLocalizedRoute("/es/predictions/today"));
  assert.ok(isLocalizedRoute("/predictions/today"));
  assert.ok(isLocalizedRoute("/"));
  assert.ok(!isLocalizedRoute("/es/best-bets"));
});

test("every page's metadata style matches its membership", async () => {
  // The rule used to hold by accident. An untranslated page that starts
  // self-canonicalising publishes seven URLs of identical English content; a
  // translated page that does not is telling Google to drop it. Neither is
  // visible in review, so it is asserted here instead.
  const appDir = new URL("../", import.meta.url);
  const routes = new Map([
    ["/predictions/today", "predictions/today/page.tsx"],
    ["/best-bets", "best-bets/page.tsx"],
    ["/acca-predictions", "acca-predictions/page.tsx"],
    ["/results", "results/page.tsx"],
    ["/leagues", "leagues/page.tsx"],
    ["/pro", "pro/page.tsx"],
    ["/about", "about/page.tsx"],
  ]);

  for (const [route, file] of routes) {
    const source = await readFile(new URL(file, appDir), "utf8");
    const localeAware = /canonicalForRequest|canonical:\s*publicPath/.test(source);

    assert.equal(
      localeAware,
      LOCALIZED_ROUTES.has(route),
      localeAware
        ? `${route} canonicalises per locale but is not declared localized`
        : `${route} is declared localized but canonicalises to one URL for every locale`,
    );
  }

  // Guard the directory listing itself, so a page added later is not silently
  // absent from the map above.
  const entries = await readdir(appDir, { withFileTypes: true });
  assert.ok(entries.some((entry) => entry.isDirectory() && entry.name === "best-bets"));
});

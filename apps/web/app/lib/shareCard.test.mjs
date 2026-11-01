import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const APP = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

/**
 * A shared link is often the only impression a page makes. Every URL on this
 * site rendered the same static picture, and that picture said "SokaStats" -- a
 * brand the site does not use -- so a fixture link announced the wrong name and
 * told the reader nothing about the match.
 */

test("the retired SokaStats artwork is gone and nothing points at it", () => {
  assert.ok(!existsSync(path.join(APP, "public", "og.png")), "the stale card is back in public/");

  for (const file of ["layout.tsx", "lib/structuredData.ts"]) {
    const source = readFileSync(path.join(APP, file), "utf8");
    const code = source
      .split("\n")
      .filter((line) => !line.trim().startsWith("//") && !line.trim().startsWith("*"))
      .join("\n");
    assert.ok(!code.includes('"/og.png"'), `${file} still references the retired card`);
  }
});

test("the layout does not pin an image and beat the file convention", () => {
  // `opengraph-image.tsx` supplies og:image and twitter:image per route, and a
  // page overrides the default simply by having the file. Naming an image in
  // the layout's metadata wins over that, which is how every match link came to
  // show the site's own card.
  const layout = readFileSync(path.join(APP, "layout.tsx"), "utf8");
  assert.ok(!/images:\s*\[/.test(layout), "the layout pins an og:image");
  assert.ok(!/twitter:\s*\{[^}]*images/.test(layout), "the layout pins a twitter:image");
});

test("the pages worth sharing have their own card", () => {
  for (const route of [
    "opengraph-image.tsx",
    "predictions/[leagueSlug]/[fixtureSlug]/opengraph-image.tsx",
    "teams/[slug]/opengraph-image.tsx",
  ]) {
    assert.ok(existsSync(path.join(APP, route)), `${route} is missing`);
  }
});

test("a card never fails a link", () => {
  // An unreachable API must produce a branded image, not a broken preview.
  for (const route of [
    "predictions/[leagueSlug]/[fixtureSlug]/opengraph-image.tsx",
    "teams/[slug]/opengraph-image.tsx",
  ]) {
    const source = readFileSync(path.join(APP, route), "utf8");
    assert.match(source, /catch\(\(\) => null\)/, `${route} does not tolerate a failed lookup`);
    assert.match(source, /brandShareCard/, `${route} has no fallback card`);
  }
});

test("shared slips get their own card", () => {
  // A slip is the most deliberately shared thing here -- someone pastes their
  // ticket into a group to argue about it -- and both kinds were unfurling as
  // the generic page card.
  //
  // Neither can use the `opengraph-image` file convention: that receives only
  // route params, and a builder ticket lives in `?ticket=` while an ACCA is
  // identified by a fragment the server never sees. Both are therefore claimed
  // in generateMetadata and drawn by a route handler.
  assert.ok(existsSync(path.join(APP, "og/slip/route.tsx")), "the slip card route is missing");

  for (const page of ["multi-match-builder/page.tsx", "acca-predictions/page.tsx"]) {
    const source = readFileSync(path.join(APP, page), "utf8");
    assert.match(source, /generateMetadata/, `${page} cannot claim a per-slip card`);
    assert.match(source, /\/og\/slip\?/, `${page} does not point at the slip card`);
    // A shared slip is one reader's ticket, not a page to compete with the
    // canonical one it was built from.
    assert.match(source, /robots: \{ index: false, follow: true \}/, `${page} would index a shared slip`);
  }
});

test("an ACCA link carries what its card needs", () => {
  // The fragment still scrolls to the right slip; the query parameter is what
  // lets the card describe it.
  const explorer = readFileSync(path.join(APP, "acca-predictions/AccaExplorer.tsx"), "utf8");
  assert.match(explorer, /encodeSlipToken/, "the share link carries no slip summary");
  assert.match(explorer, /#acca-\$\{encodeURIComponent\(bundle\.id\)\}/, "the deep-link fragment was dropped");
});

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

/**
 * The site publishes betting-market predictions, so three things belong on
 * every page: an age marker, a route to the responsible-gambling page, and the
 * statement that SokaLens is not a bookmaker and holds no stakes.
 *
 * Two of the three were present. The third -- "not a bookmaker" -- appeared
 * only in structured data and on /about, which is to say it was legible to
 * crawlers and not to readers, and there was no age marker anywhere on the
 * property at all.
 *
 * This is asserted against the root layout rather than a rendered page because
 * the root layout is what makes it unskippable: a new page type cannot ship
 * without it. That is the failure being guarded against, not the wording.
 */
const layout = readFileSync(
  path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..", "layout.tsx"),
  "utf8",
);

test("every page carries an age marker", () => {
  assert.match(layout, /18\+/, "no age marker in the root layout");
  // Screen readers announce "eighteen plus", not "one eight plus".
  assert.match(layout, /aria-label="Eighteen plus only"/);
});

test("every page routes to the responsible-gambling page", () => {
  assert.match(layout, /localePath\("\/responsible-gambling", locale\)/);
});

test("every page states that SokaLens is not a bookmaker", () => {
  assert.match(layout, /not a\s+bookmaker and accepts no stakes/);
});

test("the treatment sits in the layout, not in a single page", () => {
  // If this ever moves into a page component, it stops being a guarantee.
  const footer = layout.slice(layout.indexOf("site-footer__bottom"));
  assert.ok(footer.includes("18+"), "the age marker left the shared footer");
});

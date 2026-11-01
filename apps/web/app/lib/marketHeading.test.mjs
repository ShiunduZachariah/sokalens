import assert from "node:assert/strict";
import test from "node:test";

import { marketHeadingParts, marketSeoDefinitions } from "./seo.ts";

/**
 * The market hub renders `title` then `accent`. Nothing enforced that the two
 * did not overlap, so every heading ending in "predictions" was rendered with
 * the word twice -- "Over 2.5 goals predictions predictions" -- in the h1 of
 * every market hub on the site. The <title> was composed separately and stayed
 * correct, which is exactly why it went unnoticed.
 */

test("no market heading renders its last word twice", () => {
  for (const definition of Object.values(marketSeoDefinitions)) {
    const { title, accent } = marketHeadingParts(definition.heading);
    const rendered = `${title} ${accent}`.replace(/\s+/g, " ").trim();
    assert.doesNotMatch(
      rendered,
      /\b(\w+)\s+\1\b/i,
      `"${definition.heading}" renders as "${rendered}", which repeats a word`,
    );
  }
});

test("every market heading still ends in the accent word", () => {
  // The accent is what makes it a predictions page rather than a market name.
  // Dropping it would leave the h1 reading "Over 2.5 goals".
  for (const definition of Object.values(marketSeoDefinitions)) {
    const { title, accent } = marketHeadingParts(definition.heading);
    assert.equal(accent, "predictions");
    assert.ok(title.length > 0, `"${definition.heading}" produced an empty title`);
    assert.match(`${title} ${accent}`.toLowerCase(), /predictions$/);
  }
});

test("a heading that does not already end in the word still gets it", () => {
  // "Both teams to score and match result" is the one heading in the set that
  // relies on the accent to complete the phrase, so a blanket strip would have
  // broken it. It is pinned here by shape rather than by name.
  const parts = marketHeadingParts("Both teams to score and match result");
  assert.equal(parts.title, "Both teams to score and match result");
  assert.equal(parts.accent, "predictions");
});

test("the word is removed when it is already present", () => {
  const parts = marketHeadingParts("Over 2.5 goals predictions");
  assert.equal(parts.title, "Over 2.5 goals");
  assert.equal(parts.accent, "predictions");
});

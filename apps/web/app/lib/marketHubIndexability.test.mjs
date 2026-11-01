import assert from "node:assert/strict";
import test from "node:test";

import { marketHubIsIndexable } from "./seo.ts";

/**
 * A hub with nothing published is a heading and an empty state, so it should
 * not be indexed. A hub whose inventory could not be read is a different thing
 * entirely, and conflating the two cost real pages.
 *
 * The prediction API was failing 56% of requests. The inventory read caught
 * those failures as an empty list, an empty list counted as zero, and zero
 * meant `noindex` -- cached for ten minutes. Six hubs sat in the sitemap
 * answering noindex, which is the one shape that teaches Google to discount a
 * whole sitemap rather than one row.
 */
test("a hub with published selections is indexable", () => {
  assert.equal(marketHubIsIndexable(1), true);
  assert.equal(marketHubIsIndexable(42), true);
});

test("a hub with nothing published is not indexable", () => {
  assert.equal(marketHubIsIndexable(0), false);
});

test("a market with a durable guide stays indexable during an empty feed", () => {
  assert.equal(marketHubIsIndexable(0, "over-2-5-goals"), true);
  assert.equal(marketHubIsIndexable(0, "btts"), true);
  assert.equal(marketHubIsIndexable(0, "corners"), false);
});

test("a hub whose inventory could not be read stays indexable", () => {
  // Unknown is not zero. A page wrongly left indexable is corrected by the
  // next crawl; one wrongly noindexed has to be rediscovered.
  assert.equal(marketHubIsIndexable(null), true);
});

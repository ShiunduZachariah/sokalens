import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";

import { searchHitHref, hasOwnDestination } from "./searchHref.ts";

test("a match hit in the dropdown opens the match, not the results page", () => {
  assert.equal(searchHitHref({ type: "match", id: 3735044, name: "Manchester City vs Sunderland" }), "/matches/3735044");
});

// The bug QA reported: a prediction row in the dropdown fell through to the
// `/search?q=…` fallback, so clicking it looked like nothing had opened.
test("a prediction hit opens its fixture instead of bouncing to /search", () => {
  const href = searchHitHref({
    type: "prediction",
    id: 101,
    match_id: 3735044,
    name: "Manchester City vs Sunderland",
  });
  assert.equal(href, "/matches/3735044");
  assert.ok(!href.startsWith("/search?"), "a prediction must not link back to the search page");
});

test("an acca hit opens its slip", () => {
  assert.equal(
    searchHitHref({ type: "acca", key: "slip-7", name: "5-leg ACCA" }),
    "/acca-predictions#acca-slip-7",
  );
});

test("teams and leagues keep their catalog routes", () => {
  assert.equal(searchHitHref({ type: "team", slug: "team-england-sunderland", name: "Sunderland" }), "/teams/team-england-sunderland");
  assert.equal(
    searchHitHref({ type: "league", slug: "premier-league", name: "Premier League" }),
    "/predictions/leagues/premier-league",
  );
});

test("a hit with no usable identifier still lands somewhere real", () => {
  assert.equal(searchHitHref({ type: "team", name: "Sunderland" }), "/search?q=Sunderland");
  assert.equal(searchHitHref({ type: "match", id: 0, name: "Broken" }), "/search?q=Broken");
  assert.equal(searchHitHref({ type: "country", code: "GB", name: "United Kingdom" }), "/search?q=United%20Kingdom");
});

test("hasOwnDestination distinguishes a real page from the search fallback", () => {
  assert.equal(hasOwnDestination({ type: "match", id: 12, name: "A vs B" }), true);
  assert.equal(hasOwnDestination({ type: "country", code: "GB", name: "United Kingdom" }), false);
});

// The two callers drifting apart is what caused the bug in the first place.
test("both search surfaces route through the one resolver", () => {
  for (const path of ["../components/SearchBox.tsx", "../search/page.tsx"]) {
    const source = readFileSync(new URL(path, import.meta.url), "utf8");
    assert.match(source, /searchHitHref/, `${path} must use the shared resolver`);
    assert.doesNotMatch(
      source,
      /case "team":\s*\n\s*return teamPath/,
      `${path} has its own copy of the href switch again`,
    );
  }
});

// A dropdown row has to be a real anchor so it works on a touch tap, with the
// keyboard, and on middle-click — not a div with an onClick.
test("dropdown rows are anchors with an href", () => {
  const source = readFileSync(new URL("../components/SearchBox.tsx", import.meta.url), "utf8");
  assert.match(source, /<Link[\s\S]*?href=\{localePath\(searchHitHref\(suggestion\), locale\)\}/);
  // Closing on blur raced the click on every browser that does not focus an
  // anchor when you press it.
  assert.doesNotMatch(source, /onBlur=/, "the dropdown must not close on blur");
  assert.match(source, /pointerdown/, "the dropdown should close on an outside pointer press");
});

import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";

const componentDir = import.meta.dirname;

const readSource = (name) => readFile(path.join(componentDir, name), "utf8");

test("every tab panel stays in the DOM, hidden rather than unmounted", async () => {
  // This is an SEO invariant, not a rendering preference. The panels hold the
  // timeline and the full prediction set -- most of what the page has to rank
  // on. Mounting only the active tab would put all of it behind an interaction
  // a crawler does not perform, and the page would quietly rank on whichever
  // tab happened to be the default.
  //
  // The tell is that panels are mapped unconditionally and hidden with the
  // `hidden` attribute. A conditional like `active === tab.id && (...)` around
  // the panel body is the regression this catches.
  const source = await readSource("MatchTabs.tsx");

  assert.match(source, /hidden=\{tab\.id !== activeId\}/,
    "panels must be hidden with the `hidden` attribute, not conditionally mounted");
  assert.match(source, /\{tab\.content\}/,
    "every panel must render its content unconditionally");
  assert.doesNotMatch(source, /activeId === tab\.id \?\s*tab\.content/,
    "rendering only the active panel hides the page's content from crawlers");
});

test("the tablist is reachable and operable from a keyboard", async () => {
  // A tablist is expected to move with arrow keys, and to be a single tab stop
  // via roving tabindex. Without it a reader has to tab through every tab
  // control to reach the last one, which is the reason these are buttons in a
  // role="tablist" rather than links.
  const source = await readSource("MatchTabs.tsx");

  assert.match(source, /role="tablist"/);
  assert.match(source, /role="tab"/);
  assert.match(source, /role="tabpanel"/);
  assert.match(source, /ArrowRight/, "arrow keys must move between tabs");
  assert.match(source, /tabIndex=\{selected \? 0 : -1\}/, "roving tabindex keeps the list one tab stop");
  assert.match(source, /aria-controls=/, "each tab must point at its panel");
  assert.match(source, /aria-selected=\{selected\}/);
});

test("the match page no longer explains its own polling to the reader", async () => {
  // "Live from the provider, refreshed every 15 seconds." described our
  // plumbing, not their match.
  const source = await readSource("MatchDetail.tsx");
  assert.doesNotMatch(source, /Live from the provider/);
});

test("the in-play tab is never empty in any phase, and Stats only appears with data", async () => {
  const source = await readSource("MatchDetail.tsx");
  const markets = await readSource("inplay/InPlayMarkets.tsx");

  /*
   * This used to assert the tab was gated on `live`, "so a finished fixture does
   * not offer an empty live view". That reason was right and the mechanism was
   * crude: it also meant a reader two hours before kick-off got no tab at all,
   * and the markets component told them no markets were published "yet".
   *
   * The 2026-10-02 canvas gives the surface a voice per phase, so the tab is now
   * present always and the invariant moves to where it belongs: every phase has
   * its own copy, including its own empty state. An empty tab is still a promise
   * the page cannot keep -- it just is not prevented by hiding the tab.
   */
  assert.match(source, /id: "in-play"/);
  // Anonymous visibility is exercised by inplay/publicInPlay.test.mjs.
  assert.match(source, /phase=\{phase\}/, "the board must be told which phase to speak in");
  assert.match(
    source,
    /const phase: MatchPhase = live \? "live" : isFinishedStatus\(match\.status\) \? "finished" : "prematch";/,
    "the phase is no longer derived from the match status",
  );

  for (const phase of ["prematch", "live", "finished"]) {
    assert.match(markets, new RegExp(`${phase}: \\{`), `the board has no copy for the ${phase} phase`);
  }
  // The empty state is the one that was wrong before: four `empty` keys would mean
  // a phase shares another's wording, which is how "published yet" reached a
  // fixture that had not started.
  assert.equal(
    (markets.match(/^\s{4}empty:/gm) ?? []).length,
    3,
    "each phase needs its own empty state; sharing one is how a pre-match fixture was told markets were not published yet",
  );

  // Stats was absent because "the data does not exist yet, and an empty tab is a
  // promise the page cannot keep", with a note that adding it needs its own
  // test. The first half stopped being true on 2026-10-06 -- fixture_statistics
  // holds 5,715 fixtures in production -- and the second half is matchStats.test.mjs.
  //
  // The concern behind the original assertion is kept, and is the thing worth
  // guarding: the tab must be conditional on there being something in it.
  assert.match(source, /\.\.\.\(match\.statistics/,
    "the Stats tab is no longer gated on there being statistics, so a match without them gets an empty tab");
});

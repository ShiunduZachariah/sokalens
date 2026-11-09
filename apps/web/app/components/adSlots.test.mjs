import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import path from "node:path";

const appDir = path.resolve(import.meta.dirname, "..");
const read = (relative) => readFile(path.join(appDir, relative), "utf8");

/*
 * The 2026-09-30 ad placement spec, asserted.
 *
 * Canvas version 1790716143-3381 added four canonical slots to every template,
 * and one rule that matters more than the sizes: an unfilled slot leaves
 * NOTHING behind -- no border, no label, no reserved height. The canvas ships
 * "Ads unfilled (collapsed)" boards purely to show that state.
 *
 * Ads are also the one part of this UI nobody notices is broken. They are
 * invisible by design when unfilled; disabled providers render nothing,
 * and a missing slot costs revenue silently
 * rather than failing. Hence a test rather than a visual check.
 */

/*
 * Updated 2026-10-15 for section 4 of the ad placement spec.
 *
 * This used to assert, flatly, that "an unfilled slot renders no element at
 * all" -- it pinned the literal guard
 *
 *   if (!viewportEligible || status !== "ready" || !access?.adsAllowed) return null;
 *
 * and required it to precede every `<aside>` in the file. Section 4 contradicts
 * that for exactly one slot and gives the reason: Mediavine's CLS guidance is to
 * reserve a box where the ad loads, and the design resolves the conflict with
 * collapsing BY SLOT POSITION. Above the fold, `top` reserves its height and
 * then collapses only on no-fill. Below the fold, everything else stays at zero
 * until it is filled.
 *
 * So the assertion is split rather than dropped. The collapse-to-nothing rule
 * still holds for in-feed, rail and adhesion, and the one exception is pinned
 * with its own behaviour -- a reserved height, a 150ms collapse, and an unmount
 * after it -- so `top` cannot quietly become "renders a grey box forever" and
 * no other format can quietly acquire a reserved height.
 */
test("every slot but the top one renders no element until it is filled", async () => {
  const source = await read("components/AdSlot.tsx");

  // Only the top slot may render before a creative exists.
  assert.match(source, /const reservingTop = format === "top" && !collapsed && state !== "filled";/,
    "the reserve-before-fill exception must be scoped to the top slot by name");
  const eligibilityGuard = 'if (!reservingTop && (!viewportEligible || !resolved || !allowed)) return null;';
  assert.ok(source.includes(eligibilityGuard),
    "hidden responsive slots must collapse before provider markup");
  assert.match(source, /!decision\?\.allowed \|\| decision\.providers\.length === 0/);
  assert.match(source, /setDelivery\("exhausted"\)/);
  const guard = source.indexOf(
    'if (!reservingTop && ((delivery !== "attempting" && delivery !== "success") || !attempt)) return null;',
  );
  const markup = source.indexOf("<aside", guard);
  assert.ok(guard > 0 && markup > guard,
    "disabled and exhausted provider delivery must return null before provider markup");
  assert.ok(source.indexOf(eligibilityGuard) < markup,
    "the eligibility guard must precede the provider markup");
  // An ad blocker, a refused consent and an access API that cannot answer are
  // all section 4's "empty".
  assert.match(source, /if \(resolved && !allowed\) return "empty";/);
});

test("the top slot reserves a height, then collapses over 150ms and unmounts", async () => {
  const source = await read("components/AdSlot.tsx");
  const watch = await read("components/ads/creativeWatch.ts");
  const css = await read("globals.css");

  /*
   * 22px of label plus a 90px creative (72px with the 320x50 mobile creative),
   * and it must be on the BASE rule rather than on the reserved state.
   *
   * Reserving only while unfilled is the subtle wrong version of this: the box
   * is 112px while loading and then grows by `.ad-slot--ready`'s border and
   * padding when the creative lands, which is a shift ON FILL. Section 4 allows
   * this slot exactly one movement and it is the collapse, on no-fill.
   */
  assert.match(css, /\.ad-slot--top \{[^}]*min-height: 112px;/,
    "the reserved height must be on the base rule, so filled and reserved are the same box");
  assert.match(css, /@media \(max-width: 900px\)[\s\S]*?\.ad-slot--top \{ min-height: 72px; \}/,
    "mobile reserves 22px of label plus a 320x50 creative");
  assert.doesNotMatch(css, /\.ad-slot--top\.ad-slot--ready \{[^}]*border: 1px/,
    "a filled top slot must not gain a border, which would make it taller than the reserve");

  // Flat ground, and explicitly not a dashed placeholder: "No dashed border in
  // production."
  for (const selector of [".ad-slot--reserved {", ".ad-slot--top.ad-slot--ready {"]) {
    const block = css.slice(css.indexOf(selector));
    const rules = block.slice(0, block.indexOf("}"));
    assert.match(rules, /background: var\(--sl-bg\)/,
      `section 4: ${selector} background uses var(--bg), in both themes`);
    assert.match(rules, /border: 0/, `no dashed or panel border on ${selector}`);
  }

  // The transition is declared on the base rule, not on the end state: a
  // transition that first appears alongside its end state does not animate.
  assert.match(css, /\.ad-slot--top \{[^}]*transition: min-height 150ms/,
    "the 150ms collapse must be transitionable before the class changes");
  assert.match(css, /\.ad-slot--collapsing \{[^}]*height: 0;/);
  assert.equal(watch.match(/COLLAPSE_TRANSITION_MS = (\d+)/)[1], "150",
    "section 4 gives the collapse a 150ms height transition");
  // And then it leaves the document, because acceptance criterion 1 is that
  // nothing is VISIBLE -- a retained 0-height aside passes the pixels and not
  // the criterion.
  assert.match(source, /setTimeout\(\(\) => setCollapsed\(true\), COLLAPSE_TRANSITION_MS\)/);

  // No other format gets a reserved height. This is the half of section 4 that
  // regresses by someone being helpful about CLS on the in-feed slot.
  for (const format of ["in-feed", "rail", "adhesion"]) {
    assert.doesNotMatch(css, new RegExp(`\\.ad-slot--${format}\\.ad-slot--reserved`),
      `only the top slot may reserve height; ${format} must stay out of flow until filled`);
  }
  /*
   * The in-feed slot is the opposite case: no height until it fills, so the
   * expansion happens off-screen and costs no CLS.
   *
   * Section 4 writes that as `height: 0; overflow: hidden`, and the literal
   * declaration is NOT what is asserted here, deliberately. AdSense refuses to
   * fill a container with no available height -- serving into one is against
   * its policy, and `FeedAdBlock`'s own notes have said so since it was written
   * -- so a mounted slot pinned to `height: 0` is a slot that never fills.
   *
   * The guarantee is split instead, and both halves are pinned: the mounted
   * slot reserves nothing, and nothing is mounted at all until the block is one
   * viewport away (`rootMargin: 100%`, asserted on FeedAdBlock below). Between
   * them the reader never sees an expansion and never sees a gap.
   */
  assert.match(css, /\.ad-slot--in-feed\.ad-slot--pending \{[^}]*min-height: 0;/);
  assert.match(css, /\.ad-slot--in-feed\.ad-slot--pending \{[^}]*padding: 0;/);
  assert.doesNotMatch(css, /\.ad-slot--in-feed\.ad-slot--pending \{[^}]*[^-]\bheight: 0;/,
    "a mounted in-feed slot pinned to height: 0 cannot be filled by AdSense");
  const block = await read("components/FeedAdBlock.tsx");
  assert.match(block, /nearViewport \?[\s\S]{0,120}<AdSlot format="in-feed"/,
    "no in-feed slot may be mounted before the block approaches the viewport");
});

/*
 * Section 4: "Confirm whether they collapse unfilled in-content units. If not,
 * SokaLens must do it: a MutationObserver on the container, collapsing it if
 * there's no iframe after 3s."
 *
 * This is the branch no provider exercises in a test environment and every
 * provider eventually hits in production -- a container filled with nothing.
 */
test("a container the provider fills with nothing is collapsed after 3s", async () => {
  const watch = await read("components/ads/creativeWatch.ts");
  const source = await read("components/AdSlot.tsx");
  assert.equal(watch.match(/EMPTY_CREATIVE_GRACE_MS = (\d)_000/)[1], "3");
  assert.match(watch, /new MutationObserver\(read\)/);
  assert.match(watch, /node\.querySelector\("iframe"\)/);
  assert.match(source, /if \(kind === "empty"\) setDelivery\("exhausted"\);/,
    "an empty container must reach the same state as a no-fill");
});

/*
 * Section 6, and UI-01: the in-feed native container.
 *
 * All three causes of the live defect are asserted, because each of them was
 * invisible on its own and the combination is what shipped: a slot narrower
 * than its column, a hardcoded light ground in a dark page, and words split
 * mid-title.
 */
test("the native in-feed container is the column's width, on tokens, and splits no words", async () => {
  const css = await read("globals.css");
  const frame = await read("components/ads/NativeFeedFrame.tsx");

  // Container queries, not media queries: the rule is written against container
  // width and this slot sits in columns of different widths on different pages.
  assert.match(css, /\.ad-native \{[^}]*container-type: inline-size;/);
  assert.match(css, /@container ad-native \(min-width: 600px\)/, "4 tiles from 600px");
  assert.match(css, /@container ad-native \(max-width: 399\.98px\)/, "the list layout below 400px");
  assert.doesNotMatch(css, /@media[^{]*\{\s*\.ad-native\b/,
    "the native layout must not be driven by a viewport media query");

  // Minimum tile width 160px, and 4 / 2 / 1 columns driven by one property.
  assert.match(css, /grid-template-columns: repeat\(var\(--ad-native-columns, 1\), minmax\(160px, 1fr\)\)/);
  assert.match(css, /--ad-native-columns: 4;/);
  assert.match(css, /--ad-native-columns: 2;/);
  assert.match(css, /--ad-native-columns: 1;/);

  // Full column width, never a fixed box inside a wide column. The old rule
  // centred the unit and capped it at 336px, which IS the defect.
  assert.doesNotMatch(css, /\.feed-ads \.ad-slot--in-feed \{[^}]*max-width: 336px/);
  assert.match(css, /\.feed-ads \.ad-slot--in-feed \{[^}]*width: 100%/);
  assert.doesNotMatch(css, /\.feed-ads\[data-columns/,
    "the in-feed block renders one unit, so there are no column variants left");

  // Theme tokens, both themes. "Ads must not change the page theme."
  const nativeBlock = css.slice(css.indexOf('.ad-native[data-ad-creative="native"] {'));
  const nativeRules = nativeBlock.slice(0, nativeBlock.indexOf("}"));
  assert.match(nativeRules, /background: var\(--sl-surface\)/);
  assert.match(nativeRules, /color: var\(--sl-text\)/);
  assert.doesNotMatch(nativeRules, /#[0-9a-fA-F]{3,6}/, "no hardcoded colour in the native frame");

  // No word is ever split. Named verbatim by section 6.
  assert.match(css, /\.ad-native \*\s*\{[^}]*word-break: normal/);
  assert.match(css, /\.ad-native \*\s*\{[^}]*overflow-wrap: normal/);
  assert.match(css, /\.ad-native \*\s*\{[^}]*hyphens: none/);
  // Two lines then an ellipsis.
  assert.match(css, /-webkit-line-clamp: 2;/);

  // The fallback: too narrow for even one minimum-width tile collapses the slot
  // rather than squeezing the widget, which is what UI-01 was.
  assert.match(frame, /MIN_NATIVE_FRAME_WIDTH = 160 \+ 16 \* 2/);
  assert.match(frame, /onUnfitRef\.current\?\.\(\)/);
});

test("a native unit is labelled Sponsored and a display unit Advertisement", async () => {
  const source = await read("components/AdSlot.tsx");
  // Section 4: label every filled display ad "Advertisement" and every native
  // unit "Sponsored". The kind comes from the DOM rather than from provider
  // configuration, so a provider-inserted unit is labelled correctly too.
  assert.match(source, /const resolvedLabel = creative === "native" \? "Sponsored" : label;/);
  assert.match(source, /label = "Advertisement"/);
  // And the label goes when the slot collapses: it is only rendered on `filled`.
  assert.match(source, /\{visible \? <span className="ad-slot__label">\{resolvedLabel\}<\/span> : null\}/);
});

test("the canonical slot formats exist and keep the legacy ones", async () => {
  const source = await read("components/ads/types.ts");
  for (const slot of ["top", "in-feed", "rail", "adhesion"]) {
    assert.match(source, new RegExp(`"${slot}"`), `AdFormat is missing "${slot}"`);
  }
  // The un-refreshed pages (/results, /leagues, ...) still pass these. Dropping
  // them would leave those pages with no slots rather than with old ones.
  for (const slot of ["banner", "sidebar", "inline", "footer"]) {
    assert.match(source, new RegExp(`"${slot}"`), `AdFormat dropped legacy "${slot}"`);
  }
});

test("every canonical format has a sizing rule and an AdSense shape", async () => {
  const css = await read("globals.css");
  const unit = await read("components/AdSenseUnit.tsx");
  for (const slot of ["top", "in-feed", "rail", "adhesion"]) {
    assert.match(css, new RegExp(`\\.ad-slot--${slot}\\b`),
      `globals.css has no rule for .ad-slot--${slot}`);
    // An unmapped format would make AdSense fall back to an arbitrary shape.
    assert.match(unit, new RegExp(`(^|\\s|")${slot}"?:`, "m"),
      `AdSenseUnit has no display format for ${slot}`);
  }
});

test("each refreshed page carries the slots the spec gives it", async () => {
  /*
   * Updated 2026-10-15: the expected in-feed block count is now per page rather
   * than per file.
   *
   * It used to require exactly one `<FeedAdBlock />` in every file in this
   * table, including `PredictionListShell`. Section 3.1 moved the prediction
   * list's in-feed position INTO the feed -- "after the first competition
   * group" -- which `PredictionExplorer` renders, so the shell's own trailing
   * block was a second in-feed unit on the same page and, on a short slate,
   * within eight rows of the explorer's own. The shell now carries none and the
   * explorer carries it, so the count is declared per file.
   */
  const pages = {
    "page.tsx": { slots: ["top"], feedBlocks: 1 },
    "components/MatchDetail.tsx": { slots: ["top"], feedBlocks: 1 },
    "acca-predictions/page.tsx": { slots: ["top"], feedBlocks: 1 },
    // The five prediction list pages do not declare slots of their own: they
    // render PredictionListShell, which carries top and rail for all of them
    // and delegates the in-feed position to PredictionExplorer. Asserted here,
    // plus the routing assertion that follows -- so a list page cannot quietly
    // stop using the shell and lose its slots without a test saying so.
    "components/PredictionListShell.tsx": { slots: ["top", "rail"], feedBlocks: 0 },
    "components/PredictionExplorer.tsx": { slots: [], feedBlocks: 1 },
    "components/BestBetExplorer.tsx": { slots: [], feedBlocks: 1 },
  };

  for (const [file, { feedBlocks, slots }] of Object.entries(pages)) {
    const source = await read(file);
    for (const slot of slots) {
      assert.match(source, new RegExp(`format="${slot}"`),
        `${file} is missing its ${slot} slot`);
    }
    const blocks = source.match(/<FeedAdBlock\b/g) ?? [];
    assert.equal(blocks.length, feedBlocks,
      `${file} has ${blocks.length} in-feed ad blocks, want exactly ${feedBlocks}`);
  }
});

/*
 * Section 3.1 (where the in-feed unit goes on each page) and 3.2 (where no ad
 * ever goes).
 *
 * Asserted on the source order rather than visually, because a misplaced ad
 * slot is not an error -- it renders perfectly well in the wrong place, and the
 * two violations this pins were both live for weeks.
 */
test("the top slot sits directly under the site header, above the page title", async () => {
  // Section 3: "Directly under the site header, above the page title." All four
  // of these rendered it BELOW their masthead or hero, which on mobile put it a
  // full screen down and sharing that screen with the adhesion band.
  const shells = {
    "page.tsx": '<section className="hm-hero">',
    "acca-predictions/page.tsx": '<section className="ac-hero">',
    "components/ContentPageShell.tsx": "<PageHeader",
    "components/PredictionListShell.tsx": "<PredictionSubnav",
    "multi-match-builder/BuilderClient.tsx": "<PageHeader",
  };

  for (const [file, firstContent] of Object.entries(shells)) {
    const source = await read(file);
    const slot = source.indexOf('<AdSlot format="top"');
    const content = source.indexOf(firstContent, source.indexOf('id="content"'));
    assert.ok(slot > 0, `${file} has no top slot`);
    assert.ok(content > 0, `${file} no longer renders ${firstContent}; this test needs updating`);
    assert.ok(slot < content,
      `${file} renders its top slot after ${firstContent}; section 3 puts it directly under the site header`);
  }
});

test("the match page moves its top slot below the scoreboard once a match is live", async () => {
  /*
   * Section 3's one exception to "directly under the site header": "Live match
   * on mobile: under the scoreboard + tabs, so the score stays at the top of
   * the screen." A reader checking a live score should not scroll past an
   * advert to find it.
   *
   * MatchDetail is deliberately absent from the shells table above, because it
   * is the only page where the correct position depends on state. Without this
   * test that rule would be unpinned: the generic order assertion does not
   * cover this file, so a future edit could collapse the two branches back into
   * one unconditional slot above the hero and nothing would fail.
   *
   * The position is decided by match phase on the server, not by viewport, and
   * that over-applies the exception to live desktop pages on purpose. The
   * alternatives: a second slot hidden by media query still REQUESTS an ad, so
   * it spends an impression to show nothing; and reordering with flex would
   * make `.mp__inner` a flex column at mobile, where children carry their own
   * margins -- margins do not collapse between flex items, so every gap on the
   * mobile match page would move to reposition one advert.
   */
  const match = await read("components/MatchDetail.tsx");

  const hero = match.indexOf('<section className="mp-hero">');
  assert.ok(hero > 0, "the match page no longer renders mp-hero; this test needs updating");

  const prematchSlot = match.indexOf('{phase === "prematch" ? <AdSlot format="top"');
  const liveSlot = match.indexOf('{phase === "prematch" ? null : <AdSlot format="top"');

  assert.ok(prematchSlot > 0, "a pre-match page must render the top slot above the hero");
  assert.ok(liveSlot > 0, "a live or finished page must render the top slot below the hero");
  assert.ok(prematchSlot < hero,
    "the pre-match top slot belongs above the scoreboard, under the site header");
  assert.ok(liveSlot > hero,
    "the live top slot belongs below the scoreboard, so the score is the first thing on screen");

  // Exactly one of the two ever renders. Two mounted slots would request two
  // ads for one position, which is both a wasted impression and a density
  // breach -- the thing the hidden-slot alternative was rejected for.
  const slots = match.match(/<AdSlot format="top"/g) ?? [];
  assert.equal(slots.length, 2, "exactly two branches, one per phase");
  const guards = match.match(/\{phase === "prematch" \?/g) ?? [];
  assert.equal(guards.length, 2,
    "both top slots stay behind a phase guard, so only one is ever mounted");
});

test("the Builder has no rail ad and no adhesion band", async () => {
  const builder = await read("multi-match-builder/BuilderClient.tsx");
  const adhesion = await read("components/AdhesionSlot.tsx");

  /*
   * Section 3.2, first bullet: never place an ad "inside or overlapping the
   * Builder ticket (sidebar or bottom bar)", and the 3.1 table gives the
   * Builder no rail at all.
   *
   * A 300x600 was pinned to the bottom of the ticket aside -- the page's
   * primary control, sticky on desktop and a bottom bar on mobile. It rendered
   * correctly, which is why it survived; it was an ad inside the ticket.
   */
  assert.doesNotMatch(builder, /format="rail"/,
    "the Builder gets no rail: an ad there sits inside the ticket (section 3.2)");
  assert.doesNotMatch(builder, /format="sidebar"/, "nor the pre-refresh sidebar name");
  // The adhesion exclusion already held and must keep holding: the ticket bar
  // owns the bottom of the screen on mobile.
  assert.match(adhesion, /multi-match-builder/);

  // And the one in-feed unit sits before "Show more matches", per 3.1.
  const ad = builder.indexOf("<FeedAdBlock />");
  const loadMore = builder.indexOf('className="bd-loadmore"');
  assert.ok(ad > 0 && loadMore > ad,
    "section 3.1: the Builder's in-feed unit goes after the match cards, before 'Show more matches'");
});

test("the in-feed unit sits where section 3.1 puts it on each page", async () => {
  // Home: directly above "How it works". Section 3.1 reads "between Featured
  // ACCAs and How it works" while V31's home.html puts Featured ACCAs after
  // the method section -- the two disagree on the order of those blocks and
  // agree exactly on this edge, so this is the edge that is pinned.
  const home = await read("page.tsx");
  const homeAd = home.indexOf("<FeedAdBlock />");
  const howItWorks = home.indexOf('aria-labelledby="how-it-works"');
  assert.ok(homeAd > 0 && howItWorks > homeAd,
    "the home in-feed unit must sit directly above the How it works section");

  // Match page: after Markets, before Form & goals.
  const match = await read("components/MatchDetail.tsx");
  const markets = match.indexOf('aria-labelledby="markets-title"');
  const matchAd = match.indexOf("<FeedAdBlock />");
  const numbers = match.indexOf('aria-labelledby="numbers-title"');
  assert.ok(markets > 0 && matchAd > markets && numbers > matchAd,
    "section 3.1: after Markets, before Form & goals");

  // ACCA list: after the tickets, before How to read. One unit, page level --
  // the in-grid blocks between slips are gone, so there is nothing between a
  // ticket and the next ticket.
  const accas = await read("acca-predictions/page.tsx");
  const tickets = accas.indexOf('id="published-accas"');
  const accaAd = accas.indexOf("<FeedAdBlock />");
  const howToRead = accas.indexOf('aria-label="How to read this page"');
  assert.ok(tickets > 0 && accaAd > tickets && howToRead > accaAd,
    "section 3.1: after the tickets, before How to read");
  const explorer = await read("acca-predictions/AccaExplorer.tsx");
  assert.doesNotMatch(explorer, /<FeedAdBlock/,
    "the ACCA list has one in-feed unit, at page level; blocks between slips are a second and third");

  // Best Bets: after the 5th ranked row, hero included.
  const bestBets = await read("components/BestBetExplorer.tsx");
  assert.match(bestBets, /BEST_BETS_FIRST_AD_AFTER_ROW = 4/,
    "section 3.1: after the 5th ranked row, and the hero is rank 1");
  assert.match(bestBets, /firstAfterIndex: BEST_BETS_FIRST_AD_AFTER_ROW/);

  // Prediction lists: after the first competition group, only with 2+ groups.
  const lists = await read("components/PredictionExplorer.tsx");
  assert.match(lists, /groups\.length >= 2 && firstGroupRows > 0 \? firstGroupRows - 1 : undefined/,
    "section 3.1: after the first competition group, only if there are 2+ groups");
});

test("one in-feed unit per block, at the column's full width", async () => {
  const block = await read("components/FeedAdBlock.tsx");
  // Section 3: "One per page ... Always the full width of the content column."
  // This mounted up to three 336px units side by side at >= 980px, measured
  // with a ResizeObserver -- a row of narrow boxes that did not line up with
  // the cards above and below, which is UI-01 with extra steps.
  const slots = block.match(/<AdSlot\b/g) ?? [];
  assert.equal(slots.length, 1, "an in-feed block mounts exactly one unit");
  assert.doesNotMatch(block, /new ResizeObserver/,
    "there is nothing left to measure: one full-width unit, and container queries inside it");
  assert.doesNotMatch(block, /data-columns/);
  // Section 4: the request starts a viewport early so the expansion happens
  // off-screen. `100%` and not a pixel value, which is more than a viewport on
  // a phone and less than one on a desktop.
  assert.match(block, /rootMargin: "100%"/,
    "section 4: request 1 viewport ahead with IntersectionObserver rootMargin: 100%");
});

test("every prediction list page gets its slots through the shared shell", async () => {
  /*
   * The slots are the reason this assertion exists rather than being implied.
   *
   * Four of these five pages had no slots at all before the 2026-10-01 refresh
   * -- /predictions/weekend and the fifteen market hubs were still on the old
   * markup -- and the way that regresses is a page being rewritten back to its
   * own layout. Routing through the shell is what earns it top, in-feed and
   * rail, so the routing is the thing worth pinning.
   */
  const pages = [
    "predictions/today/page.tsx",
    "predictions/weekend/page.tsx",
    "predictions/markets/[marketSlug]/page.tsx",
  ];
  for (const file of pages) {
    const source = await read(file);
    assert.match(source, /<PredictionListShell[\s/>]/,
      `${file} no longer renders PredictionListShell, so it has lost its ad slots`);
  }

  // Tomorrow reaches the shell through the Today page rather than directly.
  const tomorrow = await read("predictions/tomorrow/page.tsx");
  assert.match(tomorrow, /<TodayPredictionsPage[\s/>]/,
    "predictions/tomorrow no longer delegates to the Today page, so it has lost its ad slots");
});

test("the adhesion band is global, mobile-only, and excludes the Builder", async () => {
  const layout = await read("layout.tsx");
  const slot = await read("components/AdhesionSlot.tsx");
  const css = await read("globals.css");

  // In the layout so a new page cannot ship without it.
  assert.match(layout, /<AdhesionSlot \/>/, "the adhesion band is not in the root layout");

  // Ordered before the nav, whose higher z-index must keep it on top.
  assert.ok(
    layout.indexOf("<AdhesionSlot />") < layout.indexOf("<MobileBottomNav />"),
    "the adhesion band must render before the tab bar",
  );

  // The spec excludes the Builder: on small screens its ticket is itself a
  // sticky bottom bar and owns that edge.
  assert.match(slot, /multi-match-builder/, "the Builder is not excluded");
  assert.match(slot, /acca-generator/,
    "the generator route is not excluded; the design merges it into the Builder");

  // Hidden by default and shown only on mobile, or it would pin a band across
  // the foot of every desktop page.
  assert.match(css, /\.ad-slot--adhesion\s*\{[^}]*display:\s*none/,
    "the adhesion band must be hidden until a mobile breakpoint enables it");
  assert.match(css, /@media \(max-width: 767px\)[\s\S]*?\.ad-slot--adhesion \{ display: flex; \}/,
    "the adhesion band is never enabled at a mobile width");
});

test("the adhesion offset and the shell's padding read one value", async () => {
  const css = await read("globals.css");

  // The band sits one nav-height up from the bottom, and the shell pads itself
  // by the same amount. Hard-coding either is how they drift and the band
  // either floats over the tab bar or leaves a gap under it.
  assert.match(css, /\.app-shell \{[\s\S]*?padding-bottom: var\(--sl-nav-height/,
    ".app-shell must read --sl-nav-height rather than a literal");
  assert.match(css, /\.ad-slot--adhesion \{[\s\S]*?bottom: var\(--sl-nav-height\)/,
    "the adhesion band must read --sl-nav-height rather than a literal");
  assert.match(css, /--sl-nav-height:\s*\d+px/, "--sl-nav-height is not defined");
});

/*
 * The zone migration and the adapter have to agree, and nothing else makes
 * them. `infra/deploy/ads/adsterra-zones.sql` is the only place the Adsterra
 * zone ids exist -- an operator script rather than a migration, because the
 * architecture gate requires the ad catalogue to stay disabled and
 * unconfigured after any migration. If a zone is
 * dropped, mistyped, or given a box the validator rejects, the slot renders
 * nothing and no other test notices -- an unfilled ad slot is invisible by
 * design. So this reads the migration and puts every zone in it through the
 * validator the browser will use.
 */
test("every Adsterra zone in the operator script validates", async () => {
  const { validateAdsterraConfig } = await import("../lib/adDelivery.ts");
  const script = await readFile(
    path.join(appDir, "..", "..", "..", "infra", "deploy", "ads", "adsterra-zones.sql"),
    "utf8",
  );

  const origin = script.match(/'script_origin',\s*'([^']+)'/);
  assert.ok(origin, "the operator script must pin a script origin");

  const zones = [...script.matchAll(
    /'([a-z-]+)',\s*jsonb_build_object\('kind',\s*'([a-z]+)',\s*'zone_id',\s*'([0-9a-f]{32})'(?:,\s*'width',\s*(\d+),\s*'height',\s*(\d+))?\)/g,
  )];
  assert.ok(zones.length >= 8, `expected a zone for each slot format, found ${zones.length}`);

  const config = { script_origin: origin[1], zones: {} };
  for (const [, format, kind, zoneId, width, height] of zones) {
    config.zones[format] = width === undefined
      ? { kind, zone_id: zoneId }
      : { kind, zone_id: zoneId, width: Number(width), height: Number(height) };
  }

  // Every canonical placement of the refreshed design, and the four
  // pre-refresh names the un-refreshed pages still render.
  for (const format of ["top", "in-feed", "rail", "adhesion", "banner", "sidebar", "inline", "footer"]) {
    const resolved = validateAdsterraConfig(config, format);
    assert.ok(resolved, `the operator script has no usable Adsterra zone for the "${format}" slot`);
    assert.equal(resolved.scriptOrigin, origin[1]);
  }
});

test("the Adsterra adapter is wired and Monetag still fails closed", async () => {
  const registry = await read("components/ads/providerRegistry.tsx");
  assert.match(registry, /adsterra: AdsterraUnit/);
  // Monetag holds an ownership meta tag and no zone, so it can only ever
  // spend a slot's timeout. Wiring it before a zone exists is the mistake
  // this asserts against.
  assert.match(registry, /monetag: UnsupportedProvider/);
});

test("the Monetag ownership meta is rendered site-wide", async () => {
  const site = await read("site.ts");
  const layout = await read("layout.tsx");
  assert.match(site, /monetagVerification: "[0-9a-f]{32}"/);
  assert.match(layout, /monetag: siteConfig\.monetagVerification/);
});

/*
 * A repeating slot cannot hold a unit that is keyed by a DOM id.
 *
 * `FeedAdBlock` renders up to two slots per block and `feedAdPositions` allows
 * six blocks, so one page can carry twelve `in-feed` slots. Adsterra's native
 * widget and its social bar both write into an element they find by an id
 * derived from the zone, so a second copy on the page is a duplicate id that
 * never fills -- silently, after burning its attempt timeout. Banners are
 * iframes with their own document and repeat safely.
 *
 * This failed in production: in-feed was mapped to the native widget.
 */
test("every repeatable slot format is mapped to a banner, not a keyed unit", async () => {
  const script = await readFile(
    path.join(appDir, "..", "..", "..", "infra", "deploy", "ads", "adsterra-zones.sql"),
    "utf8",
  );
  const kindFor = (format) => {
    const match = script.match(
      new RegExp(`'${format}',\\s*jsonb_build_object\\('kind',\\s*'([a-z]+)'`),
    );
    return match && match[1];
  };

  // Formats a single page can render more than once.
  for (const format of ["in-feed", "inline"]) {
    assert.equal(kindFor(format), "banner",
      `"${format}" can appear several times on one page, so it must be a banner`);
  }
  // And the keyed units must only sit on formats that render once.
  const singletonFormats = ["top", "rail", "sidebar", "adhesion", "banner", "footer"];
  const keyed = singletonFormats.filter((format) => kindFor(format) !== "banner");
  assert.ok(keyed.length <= 1,
    `at most one keyed unit should be configured, found: ${keyed.join(", ")}`);
});

test("a filled slot drops the placeholder chrome and uses theme tokens", async () => {
  const css = await read("globals.css");
  const block = css.slice(css.indexOf(".ad-slot--ready {"));
  assert.ok(block.startsWith(".ad-slot--ready {"), "the --ready rules must exist");
  const rules = block.slice(0, block.indexOf("/* Homepage */"));
  // The hardcoded light placeholder palette must not survive into a filled slot.
  assert.doesNotMatch(rules, /#f2f6f3|#b8c8bf|#829087/);
  assert.match(rules, /background: var\(--sl-surface\)/);
  assert.match(rules, /border: 1px solid var\(--sl-line\)/);
  // `overflow-wrap: anywhere` broke the creative one letter per line.
  assert.match(rules, /overflow-wrap: normal/);
});

test("an odd number of ACCA cards fills the row instead of leaving a hole", async () => {
  const css = await read("globals.css");
  assert.match(css, /\.ac-grid > \.ac-card:last-child:nth-child\(odd\) \{ grid-column: 1 \/ -1; \}/);
});

/*
 * The HTML `hidden` attribute loses to a class selector.
 *
 * Every leg past the preview carries `hidden`, which is enforced only by the
 * user-agent rule `[hidden] { display: none }`. `.ac-leg { display: grid }`
 * outranks it, so the legs rendered anyway and a seventeen-leg ticket pulled
 * every card in the grid to its height. Found on production, after the
 * two-leg preview had already shipped.
 */
test("a hidden ACCA leg is actually hidden", async () => {
  const css = await read("globals.css");
  const rule = css.indexOf(".ac-leg[hidden] { display: none; }");
  const base = css.indexOf(".ac-leg {");
  assert.ok(rule >= 0, "`.ac-leg[hidden]` must override the display the class sets");
  assert.ok(rule < base || /\.ac-leg\[hidden\]/.test(css.slice(base)),
    "the override must exist alongside the base rule");
});

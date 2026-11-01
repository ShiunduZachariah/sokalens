import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import path from "node:path";

const webRoot = path.resolve(import.meta.dirname, "../..");
const css = await readFile(path.join(webRoot, "app/globals.css"), "utf8");
const client = await readFile(
  path.join(webRoot, "app/multi-match-builder/BuilderClient.tsx"),
  "utf8",
);

function rule(selector, { within } = {}) {
  const source = within ?? css;
  const match = source.match(
    new RegExp(`${selector.replace(/[.[\]="]/g, (c) => "\\" + c)}\\s*\\{([^}]*)\\}`),
  );
  return match ? match[1] : null;
}

function mediaBlock(width) {
  const opener = new RegExp(`@media \\(max-width: ${width}px\\)\\s*\\{`, "g");
  for (const match of css.matchAll(opener)) {
    let depth = 1;
    let i = match.index + match[0].length;
    for (; i < css.length && depth > 0; i += 1) {
      if (css[i] === "{") depth += 1;
      else if (css[i] === "}") depth -= 1;
    }
    const body = css.slice(match.index + match[0].length, i - 1);
    if (body.includes(".bd-bar")) return body;
  }
  return null;
}

/**
 * The bar is fixed above a fixed bottom nav, so its offset is a hard-coded copy
 * of the nav's height. Nothing links the two: change `.mobile-bottom-nav__inner`
 * to 72px and the bar silently covers the nav's top edge on every phone, which
 * no test and no type checker would notice.
 *
 * The nav's occupied height is the inner row PLUS the 1px top rule, which sits
 * on the fixed parent rather than the row. Asserting against the row alone is
 * how this shipped 1px over that rule in the first place -- it passed, and only
 * a measured render in a browser found the overlap.
 */
function navOccupiedHeight() {
  const inner = rule(".mobile-bottom-nav__inner");
  assert.ok(inner, ".mobile-bottom-nav__inner rule not found");
  const row = Number(inner.match(/height:\s*(\d+)px/)[1]);

  const parent = rule(".mobile-bottom-nav");
  assert.ok(parent, ".mobile-bottom-nav rule not found");
  const border = Number(parent.match(/border-top:\s*(\d+)px/)?.[1] ?? 0);

  return row + border;
}

test("the ticket bar clears the bottom nav it sits above", () => {
  const navHeight = navOccupiedHeight();

  const phone = mediaBlock(767);
  assert.ok(phone, "no 767px block carrying .bd-bar");
  const offset = Number(rule(".bd-bar", { within: phone }).match(/bottom:\s*calc\((\d+)px/)[1]);

  assert.equal(
    offset,
    navHeight,
    `the bar sits ${offset}px up but the nav occupies ${navHeight}px (row + top rule) -- they overlap`,
  );
});

test("the bar is hidden where the ticket rail is already sticky", () => {
  // Declared outside any media query, so the wide default is "not shown".
  const base = css.slice(0, css.indexOf("@media (max-width: 1023px)"));
  assert.match(
    rule(".bd-bar", { within: base }) ?? "",
    /display:\s*none/,
    "the bar must default to hidden -- on a wide screen the rail is visible and the bar is noise",
  );
});

/**
 * The open ticket must be PINNED above the bar, never left in document flow.
 *
 * In flow it sits below a lazy-loading match list, so bringing it into view
 * means scrolling toward it -- which appends more matches above it and pushes it
 * further away. Measured on production: the rail's top went 24518 -> 3669 ->
 * 73856 as the page chased it, 74,000px down a list that kept growing. Pinned,
 * there is nothing to chase.
 */
test("an open ticket is pinned above the bar rather than scrolled to", () => {
  const tablet = mediaBlock(1023);
  const phone = mediaBlock(767);

  const open = rule('.bd-ticket[data-open="true"]', { within: tablet });
  assert.match(open, /position:\s*fixed/, "the open ticket must be fixed, not in flow");
  assert.match(open, /overflow-y:\s*auto/, "a pinned sheet taller than its box must scroll itself");

  // The base rule sets `top` for the sticky desktop rail. A fixed box with both
  // top and bottom is stretched between them, which silently returns the sheet
  // to full height with dead space and lifts its bottom edge off the bar.
  assert.match(open, /top:\s*auto/, "`top` must be released or the sheet stretches");
  assert.match(open, /padding:/, "a fixed sheet has left the page grid and needs its own padding");
  assert.doesNotMatch(
    open,
    /margin-bottom/,
    "a margin means it is back in flow, below the match list, and the chase returns",
  );

  const barHeight = Number(rule(".bd-bar", { within: tablet }).match(/height:\s*(\d+)px/)[1]);
  assert.equal(
    Number(open.match(/bottom:\s*(\d+)px/)[1]),
    barHeight,
    "the sheet must sit exactly on top of the bar",
  );

  assert.equal(
    Number(rule('.bd-ticket[data-open="true"]', { within: phone }).match(/bottom:\s*calc\((\d+)px/)[1]),
    barHeight + navOccupiedHeight(),
    "on a phone the sheet must clear both the bar and the nav",
  );
});

/**
 * The component must not try to scroll the ticket into view. That was the
 * original fix and it is the bug: see the test above.
 */
test("nothing scrolls the ticket into view", () => {
  assert.doesNotMatch(
    client,
    /scrollIntoView/,
    "scrolling to the ticket chases a lazy-loading list that keeps growing",
  );
});

test("the bar is a button that announces the rail it controls", () => {
  assert.match(client, /aria-controls="builder-ticket"/);
  assert.match(client, /aria-expanded=\{ticketOpen\}/);
  assert.match(client, /id="builder-ticket"/, "the rail must carry the id the bar points at");
  assert.match(client, /type="button"/, "an untyped button inside a form submits it");
});

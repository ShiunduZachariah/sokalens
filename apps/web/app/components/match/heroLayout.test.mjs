import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

/**
 * The match hero's kick-off figure overlapped both crests on mobile.
 *
 * Measured on production at 390px: the crests sat at 54-118 and 272-336, and
 * the figure spanned 80-310 -- 38px over each of them, at the same vertical
 * position. The time was drawn straight through both flags.
 *
 * Two causes, and the guard needs both because either alone reproduces it:
 *
 *  1. `.mp-teams` used three equal `minmax(0, 1fr)` columns. The middle one
 *     shrinks below its content, and `.mp-figure` is `white-space: nowrap`, so
 *     the figure overflowed into the side columns instead of being contained.
 *
 *  2. `.mp-figure` is `clamp(56px, 8vw, 88px)`. 8vw only beats the 56px floor
 *     above ~700px, so a 390px screen rendered a desktop-sized figure and no
 *     mobile rule overrode it.
 *
 * This asserts the CSS rather than the rendering, deliberately and with a known
 * limit: CI runs `node --test`, lint and build -- it does not run Playwright --
 * so a browser assertion here would never execute. The actual fix was verified
 * by measuring both the design export and the running page in Chromium at
 * 390px; this file exists so the two properties that caused it cannot silently
 * revert between those manual passes.
 */

const css = await readFile(new URL("../../globals.css", import.meta.url), "utf8");

function block(selector) {
  const start = css.indexOf(`${selector} {`);
  assert.notEqual(start, -1, `${selector} is gone from globals.css`);
  return css.slice(start, css.indexOf("}", start));
}

test("the hero centre column sizes to its own content", () => {
  const teams = block(".mp-teams");
  const cols = /grid-template-columns:\s*([^;]+);/.exec(teams);
  assert.ok(cols, ".mp-teams no longer declares grid-template-columns");
  assert.match(
    cols[1],
    /minmax\(0,\s*1fr\)\s+auto\s+minmax\(0,\s*1fr\)/,
    "the centre column is not `auto`, so the kick-off figure can shrink below its "
      + "own width and overflow into the crests -- which is the bug this guards",
  );
  assert.doesNotMatch(
    cols[1],
    /repeat\(3,\s*minmax\(0,\s*1fr\)\)/,
    "three equal columns are back; the figure will overlap both crests on mobile",
  );
});

test("the kick-off figure is overridden below the clamp floor on mobile", () => {
  // The floor is the whole problem: without an explicit mobile size the figure
  // never gets smaller than 56px, whatever the viewport.
  const base = block(".mp-figure");
  const floor = /font-size:\s*clamp\((\d+)px/.exec(base);
  assert.ok(floor, ".mp-figure no longer uses a clamp; re-check the mobile override below");

  const override = /\.mp-figure \{ font-size: (\d+)px; \}/.exec(css);
  assert.ok(override, "no mobile font-size override for .mp-figure; it will render at the clamp floor at 390px");
  assert.ok(
    Number(override[1]) < Number(floor[1]),
    `the mobile override (${override[1]}px) is not below the clamp floor (${floor[1]}px), so it changes nothing`,
  );
});

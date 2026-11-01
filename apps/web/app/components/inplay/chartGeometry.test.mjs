import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import path from "node:path";

const webRoot = path.resolve(import.meta.dirname, "../../..");
const css = await readFile(path.join(webRoot, "app/globals.css"), "utf8");
const chart = await readFile(
  path.join(webRoot, "app/components/inplay/WinProbabilityChart.tsx"),
  "utf8",
);

/**
 * The plot's shape is declared in two places that cannot see each other.
 *
 * The viewBox lives in TSX because CSS cannot set one; the reserved box lives in
 * CSS because the height has to be right on the first paint, before matchMedia
 * has answered. If the two ratios drift apart the SVG letterboxes inside its own
 * container -- which renders as a correct-looking chart with unexplained dead
 * space, not as a visible break. Nothing else in the build catches that.
 */

const view = Object.fromEntries(
  [...chart.matchAll(/(wide|narrow):\s*\{\s*w:\s*(\d+),\s*h:\s*(\d+)\s*\}/g)].map((m) => [
    m[1],
    { w: Number(m[2]), h: Number(m[3]) },
  ]),
);

test("the chart declares both a wide and a narrow viewBox", () => {
  assert.deepEqual(Object.keys(view).sort(), ["narrow", "wide"]);
  assert.ok(
    view.narrow.h / view.narrow.w > view.wide.h / view.wide.w,
    "the narrow box must be proportionally taller -- it exists so a phone gets more than ~100px of plot",
  );
});

test("each viewBox has a matching aspect-ratio reserved in CSS", () => {
  const ratios = [...css.matchAll(/\.wp__svg\s*\{[^}]*aspect-ratio:\s*(\d+)\s*\/\s*(\d+)/g)].map(
    (m) => `${m[1]}/${m[2]}`,
  );
  assert.deepEqual(
    ratios,
    [`${view.wide.w}/${view.wide.h}`, `${view.narrow.w}/${view.narrow.h}`],
    "globals.css must reserve the wide ratio in the base rule and the narrow one in the override, in that order",
  );
});

/**
 * globals.css has several `max-width: 600px` blocks, so the override has to be
 * located by brace-matching from each one rather than by taking the first match.
 */
function mediaBlocksAt(width) {
  const blocks = [];
  const opener = new RegExp(`@media \\(max-width: ${width}px\\)\\s*\\{`, "g");
  for (const match of css.matchAll(opener)) {
    let depth = 1;
    let i = match.index + match[0].length;
    for (; i < css.length && depth > 0; i += 1) {
      if (css[i] === "{") depth += 1;
      else if (css[i] === "}") depth -= 1;
    }
    blocks.push({ start: match.index, body: css.slice(match.index + match[0].length, i - 1) });
  }
  return blocks;
}

test("the TSX media query and the CSS breakpoint are the same width", () => {
  const fromChart = chart.match(/NARROW_QUERY\s*=\s*"\(max-width:\s*(\d+)px\)"/);
  assert.ok(fromChart, "NARROW_QUERY not found");

  const blocks = mediaBlocksAt(fromChart[1]);
  assert.ok(blocks.length > 0, `no @media (max-width: ${fromChart[1]}px) block in globals.css`);

  const holder = blocks.filter((b) => /\.wp__svg\s*\{[^}]*aspect-ratio/.test(b.body));
  assert.equal(
    holder.length,
    1,
    "the narrow aspect-ratio must appear exactly once, inside the breakpoint the component switches on",
  );
  assert.match(holder[0].body, new RegExp(`aspect-ratio:\\s*${view.narrow.w}\\s*/\\s*${view.narrow.h}`));
});

/**
 * The base rule has to come first. Writing the override above it silently loses
 * the cascade -- the same mistake that twice shipped a mobile style that never
 * applied.
 */
test("the base aspect-ratio is declared before the media override", () => {
  const base = css.search(
    new RegExp(`\\.wp__svg\\s*\\{[^}]*aspect-ratio:\\s*${view.wide.w}\\s*/\\s*${view.wide.h}`),
  );
  const override = css.search(
    new RegExp(`\\.wp__svg\\s*\\{[^}]*aspect-ratio:\\s*${view.narrow.w}\\s*/\\s*${view.narrow.h}`),
  );
  assert.ok(base !== -1, "no base .wp__svg aspect-ratio");
  assert.ok(override !== -1, "no narrow .wp__svg aspect-ratio");
  assert.ok(base < override, "the override precedes the base rule and will not apply");
});

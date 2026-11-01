import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

/**
 * The timeline's three-column shape, and the two things that silently undid it.
 *
 * The row is mirrored about the minute: home on the left, away on the right,
 * the clock as the spine. That geometry IS the information -- which side an
 * event belongs to is read from where it sits, not from a team name in the
 * copy -- so a rule that folds the columns costs the timeline its point.
 */

const css = await readFile(new URL("../globals.css", import.meta.url), "utf8");
const markup = await readFile(new URL("./MatchTimeline.tsx", import.meta.url), "utf8");

test("the clock is the centre column, not the icon", () => {
  // The row used to put the icon in the middle and the clock inside the body,
  // which is a different layout wearing the same class names.
  assert.match(markup, /<span className="match-timeline__clock">\{event\.clock\}<\/span>\s*\n\s*<span className="match-timeline__body match-timeline__body--away">/,
    "the clock is no longer the centre column between the two bodies");
  assert.doesNotMatch(markup, /className="match-timeline__marker"/,
    "the marker column is back; the icon belongs beside the name, not in the centre");
});

test("the row keeps three columns at every width", () => {
  // Two rules folded this to one or two columns below 560/720px, each reasoning
  // that "the three columns cannot each hold a name". The export answers that by
  // narrowing the clock column instead -- 44px at 390 -- and this asserts no
  // such fold has returned.
  const folds = [...css.matchAll(/\.match-timeline__event[^{]*\{[^}]*grid-template-columns:\s*([^;]+);/g)]
    .map((m) => m[1].trim());
  assert.ok(folds.length >= 3, "the timeline no longer declares its three breakpoint widths");
  for (const value of folds) {
    const tracks = value.split(/\s+(?![^(]*\))/).length;
    assert.equal(tracks, 3, `grid-template-columns: ${value} is not three columns; the mirrored row has folded`);
  }
});

test("responsive overrides come after the rules they override", () => {
  // Twice, a media query was written above the base rule and lost the cascade at
  // equal specificity -- the label stayed 15px at 390 and the body kept its 10px
  // gap, both with the override apparently in place and doing nothing.
  const base = css.indexOf(".match-timeline__label {");
  const override = css.indexOf(".match-timeline__label { font-size: 13px; }");
  assert.ok(base !== -1 && override !== -1, "the label rules are gone");
  assert.ok(override > base,
    "the mobile label size is declared before the base rule, so it loses the cascade and does nothing");

  const bodyBase = css.indexOf(".match-timeline__body {");
  const bodyOverride = css.indexOf(".match-timeline__body { gap: 6px; }");
  assert.ok(bodyOverride > bodyBase,
    "the mobile body gap is declared before the base rule, so it loses the cascade and does nothing");
});

test("long names wrap at a space rather than mid-word", () => {
  // `anywhere` split "Tomas Viera" into "Toma / s / Viera" in the narrow column
  // a goal row leaves beside its score chip.
  assert.doesNotMatch(css, /\.match-timeline__label,\s*\n\s*\.match-timeline__detail,\s*\n\s*\.match-timeline__replaced \{\s*\n\s*overflow-wrap: anywhere;/,
    "timeline text is back on overflow-wrap: anywhere, which breaks names mid-word");
});

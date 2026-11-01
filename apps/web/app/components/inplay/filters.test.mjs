import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

/**
 * The in-play filter row, and the property that makes it safe.
 *
 * Every section is server-rendered and stays in the document; the filter only
 * sets a data attribute that CSS reads. If that becomes conditional rendering
 * instead, three things break at once: the page stops working without
 * JavaScript, the live board and both lists leave the HTML a crawler sees, and
 * switching starts costing a re-render. None of those are visible by looking at
 * the page in a browser with JavaScript on, which is why they are asserted here.
 */

const component = await readFile(new URL("./InPlayFilters.tsx", import.meta.url), "utf8");
const page = await readFile(new URL("../../in-play/page.tsx", import.meta.url), "utf8");
const css = await readFile(new URL("../../globals.css", import.meta.url), "utf8");

test("sections are filtered by CSS, never by conditional render", () => {
  assert.match(component, /data-filter=\{active\}/, "the filter no longer drives a data attribute");
  for (const section of ["live", "soon", "finished"]) {
    assert.match(page, new RegExp(`data-section="${section}"`),
      `the ${section} section lost its data-section marker, so the filter cannot address it`);
    assert.match(css, new RegExp(`data-filter="${section}"`), `no CSS rule filters for ${section}`);
  }
});

test("the counts come from the server, not a second count in the client", () => {
  assert.match(component, /counts: \{ live: number; soon: number; finished: number \}/,
    "the filter recounts the lists itself; two independent counts of one list drift");
  assert.match(page, /counts=\{\{ live: liveMatches\.length, soon: soon\.length, finished: finished\.length \}\}/,
    "the page no longer passes the same counts the stat strip above shows");
});

test("the board still has a heading", () => {
  assert.match(page, /<h2>Live now<\/h2>/,
    "the Live now heading is gone; the board floats under the honesty note again");
});

import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

// A day on this strip can take over two seconds to load. Each of these was once
// missing, and the page looked dead or jumped away for the whole of that wait.
const source = await readFile(new URL("./DateTabs.tsx", import.meta.url), "utf8");
const css = await readFile(new URL("../globals.css", import.meta.url), "utf8");

test("the tapped day lights up before its data arrives", () => {
  assert.match(source, /useOptimistic\(selectedDate\)/);
  assert.match(source, /day\.isoDate === shownDate/);
  assert.doesNotMatch(source, /day\.isoDate === selectedDate/);
});

test("changing day keeps the reader where they are on the page", () => {
  assert.match(source, /router\.push\(href, \{ scroll: false \}\)/);
});

test("the days stay real links, and only a plain navigation is intercepted", () => {
  assert.match(source, /<Link/);
  assert.match(source, /onNavigate=/);
  assert.doesNotMatch(source, /onClick=/);
});

test("the wait is visible and announced", () => {
  assert.match(source, /data-pending=\{isPending \? "true" : undefined\}/);
  assert.match(source, /aria-busy=\{isPending\}/);
  assert.match(source, /role="status"/);
  assert.match(css, /\.td-strip\[data-pending="true"\] ~ \.td-body/);
  assert.match(css, /\.date-tabs\[data-pending="true"\] ~ \.section/);
});

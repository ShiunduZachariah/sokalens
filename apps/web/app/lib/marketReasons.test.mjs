import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import path from "node:path";

const libDir = import.meta.dirname;

// matchPreview.ts is TypeScript, so these assert on the source. The behavioural
// cases live with the preview's own suite; what matters here is that the two
// deliberate departures from marketSentences stay departures, because both are
// easy to "tidy" back into a bug.
test("marketReasons keeps the headline market and does not dedupe evidence", async () => {
  const source = await readFile(path.join(libDir, "matchPreview.ts"), "utf8");
  const fn = source.slice(source.indexOf("export function marketReasons"));
  const body = fn.slice(0, fn.indexOf("\n}"));

  assert.doesNotMatch(body, /headlineKey/,
    "the headline market must stay in the map: its card needs a reason too");
  assert.doesNotMatch(body, /cited/,
    "evidence must not be de-duplicated across markets; a card read on its own would lose its line");
  assert.match(body, /reasons\.has\(key\)/,
    "one reason per market: a re-publication can briefly leave two picks on a market");
  assert.match(body, /read\?\.evidence/,
    "a market with no evidence must be absent, not present and empty");
});

test("every market card reason comes from readPick, not from parsed prose", async () => {
  const source = await readFile(path.join(libDir, "matchPreview.ts"), "utf8");
  assert.match(source, /export function marketReasons/);
  // The regression this guards: sourcing card copy by splitting the rendered
  // sentences, which silently empties every card when the template changes.
  const fn = source.slice(source.indexOf("export function marketReasons"));
  const body = fn.slice(0, fn.indexOf("\n}"));
  assert.doesNotMatch(body, /\.split\(|marketSentences|indexOf\(/,
    "reasons must be built from readPick directly");
});

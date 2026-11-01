import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import test from "node:test";

const APP_DIR = new URL("../", import.meta.url);

async function clientComponents() {
  const entries = await readdir(APP_DIR, { recursive: true });
  const files = entries
    .map((entry) => entry.replaceAll("\\", "/"))
    .filter((entry) => entry.endsWith(".tsx") && !entry.includes(".test."))
    .sort();

  const out = [];
  for (const file of files) {
    const source = await readFile(new URL(file, APP_DIR), "utf8");
    if (/^["']use client["']/m.test(source)) out.push({ file, source });
  }
  return out;
}

/**
 * Browser capability checks belong in an effect, never in render.
 *
 * `typeof navigator.share === "function"` is false on the server and true in
 * Chrome, so a component that branched on it during render produced different
 * markup on each side. React answered with "Minified React error #418" on every
 * page carrying that control and threw the server render away to re-render the
 * whole tree in the browser -- paying for SSR and discarding it, on a site whose
 * Core Web Vitals are the point.
 *
 * It hid for a while because the control lives in PredictionExplorer and not in
 * BestBetExplorer: /best-bets and the fixture pages were clean while
 * /predictions/today, /tomorrow and /weekend were not, which reads like a
 * problem with those pages rather than with one shared button.
 *
 * The safe shape is `useState(false)` plus an effect, so the first client render
 * matches the server and the capability is applied a tick later.
 */
const CAPABILITY_READS = [
  String.raw`navigator\s*\.\s*share`,
  String.raw`navigator\s*\.\s*clipboard`,
  String.raw`navigator\s*\.\s*userAgent`,
  String.raw`window\s*\.\s*matchMedia`,
  String.raw`window\s*\.\s*innerWidth`,
  String.raw`localStorage`,
];

/**
 * Lines where a capability read decides what JSX to emit.
 *
 * Targeted at the shape the bug actually takes -- `{navigator.share ? <button/>
 * : null}` -- rather than at every mention of the API. A helper function that
 * reads localStorage when a button is clicked is fine; the same read choosing
 * between two trees during render is not. Scanning for the combination keeps
 * this specific enough to stay useful instead of being suppressed.
 */
const RENDERS_JSX = /[?&]{1,2}\s*<|\?\s*\(?\s*<|&&\s*\(?\s*</;

test("no client component branches on a browser capability during render", async () => {
  const offenders = [];
  for (const { file, source } of await clientComponents()) {
    for (const [index, line] of source.split("\n").entries()) {
      const trimmed = line.trim();
      if (trimmed.startsWith("//") || trimmed.startsWith("*")) continue;
      if (!RENDERS_JSX.test(line)) continue;
      for (const pattern of CAPABILITY_READS) {
        const match = line.match(new RegExp(pattern));
        if (match) offenders.push(`${file}:${index + 1}: ${match[0]}`);
      }
    }
  }

  assert.deepEqual(
    offenders,
    [],
    "these decide what to render from a browser capability, so the server and " +
      "the browser emit different markup and React discards the server tree; " +
      "read the capability in an effect instead: " + offenders.join(" | "),
  );
});

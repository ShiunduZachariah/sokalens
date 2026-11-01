import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const pageSource = await readFile(new URL("./page.tsx", import.meta.url), "utf8");

/*
 * What this guards, and why it is written structurally rather than against
 * component names.
 *
 * The homepage's two data-bearing regions -- the hero's strongest-signal card
 * and the grid of today's predictions -- both read one feed. If HomePage awaits
 * that feed, the whole document waits on the prediction API before any byte
 * ships, and the page's LCP becomes the API's p95. The feed must therefore be
 * created as a promise and handed to Suspense boundaries that each render a
 * layout-stable fallback, so the shell streams immediately and neither region
 * collapses to zero height and shoves the page around when it resolves.
 *
 * The previous version of this test asserted the exact JSX of four named
 * components. The names changed in the 2026-09-27 design refresh while every
 * one of those properties held, so it failed for a rename rather than a
 * regression. These assertions are on the shape instead: the same guarantees,
 * without pinning what the components are called.
 */

test("the prediction feed is handed to Suspense as a promise, never awaited in HomePage", () => {
  // Allows extra arguments (viewer country was added for region ranking).
  assert.match(pageSource, /const feedPromise = loadHomePredictionFeed\(locale[,)]/,
    "the feed must be created as a promise");
  assert.doesNotMatch(pageSource, /const feedPromise = await /,
    "awaiting the feed here blocks the whole document on the prediction API");

  const homePage = pageSource.slice(pageSource.indexOf("export default async function HomePage"));
  assert.doesNotMatch(homePage, /await (fetchTodayPredictionPool|loadHomePredictionFeed|feedPromise)/,
    "HomePage must not await the feed; that is what the Suspense boundaries are for");
});

test("both streamed regions render a layout-stable fallback", () => {
  // Every consumer of the feed must sit behind its own boundary, and that
  // boundary must render something with height -- `fallback={null}` collapses
  // the region and shifts the page when the data lands.
  const consumers = [...pageSource.matchAll(/<(\w+)\s+feedPromise=\{feedPromise\}\s*\/>/g)]
    .map((match) => match[1]);
  assert.ok(consumers.length >= 2,
    `expected the hero card and the grid to both take the feed, found ${consumers.length}`);

  for (const name of consumers) {
    const boundary = new RegExp(
      `<Suspense fallback=\\{([\\s\\S]*?)\\}>\\s*<${name}\\s+feedPromise=`,
    );
    const match = pageSource.match(boundary);
    assert.ok(match, `${name} must be wrapped in its own Suspense boundary`);
    assert.notEqual(match[1].trim(), "null",
      `${name}'s fallback must occupy space, or the page shifts when it resolves`);
  }
});

test("the grid fallback renders as many skeletons as the grid renders cards", () => {
  // Six is the number the feed is sliced to. A shorter fallback is a layout
  // shift; a longer one is a taller page than the data will fill.
  assert.match(pageSource, /\.slice\(0, 6\)/,
    "the feed is sliced to six cards");
  assert.match(pageSource, /Array\.from\(\{ length: 6 \}/,
    "the fallback must render six skeletons to match");
});

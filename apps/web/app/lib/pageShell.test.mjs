import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";

const appRoot = path.resolve(import.meta.dirname, "..");

async function pageFiles(dir) {
  const found = [];
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    if (entry.name === "node_modules" || entry.name.startsWith(".")) continue;
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) found.push(...(await pageFiles(full)));
    else if (entry.name === "page.tsx") found.push(full);
  }
  return found;
}

test("every page that lays out a content grid is wrapped in page-shell", async () => {
  // /in-play shipped without it. `.page-shell` sets
  // `width: min(100% - gutter, max)` and `margin: 0 auto`, so without the
  // wrapper the grid spans the viewport and the page renders flush against the
  // left edge -- which is how it reached production and stayed there, because
  // nothing about the JSX looks wrong in review and every other page had it.
  //
  // It also carries id="content", the skip link's target, so a page missing it
  // breaks keyboard navigation as well as the layout.
  //
  // Matched on the className rather than the bare word: the first version of
  // this test checked `source.includes("page-shell")` and passed with the
  // wrapper deleted, because the comment explaining the wrapper still said
  // "page-shell". A guard its own documentation satisfies is not a guard.
  const offenders = [];
  for (const file of await pageFiles(appRoot)) {
    const source = await readFile(file, "utf8");
    if (!source.includes('className="content-grid')) continue;
    if (source.includes('className="page-shell')) continue;
    offenders.push(path.relative(appRoot, file).split(path.sep).join("/"));
  }
  assert.deepEqual(
    offenders,
    [],
    `these pages use content-grid without page-shell and will render full-bleed: ${offenders.join(", ")}`,
  );
});

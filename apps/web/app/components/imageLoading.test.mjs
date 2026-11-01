import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import test from "node:test";

const APP_DIR = new URL("../", import.meta.url);

async function sourceFiles() {
  const entries = await readdir(APP_DIR, { recursive: true });
  return entries
    .map((entry) => entry.replaceAll("\\", "/"))
    // Share cards are rasterised into a PNG on the server by satori. There is
    // no document, no preload scanner and no LCP to lose a race to, so the
    // lazy-loading rule is meaningless there -- and `loading` is not a
    // property satori understands.
    .filter((entry) => entry.endsWith(".tsx") && !entry.includes(".test."))
    .filter((entry) => !entry.endsWith("opengraph-image.tsx") && !entry.endsWith("lib/shareCard.tsx"))
    .sort();
}

test("every crest and badge is lazy, so React does not preload it", async () => {
  // React 19 emits <link rel="preload" as="image"> for images rendered in the
  // initial shell -- unless they are marked lazy. The homepage was preloading
  // eight 28x28 decorative crests at high priority from a third-party origin,
  // each taking 690-850ms, ahead of the stylesheet the <h1> needs in order to
  // paint. That is the LCP element losing a race to decoration.
  //
  // Measured on production before the fix: /leagues, whose crests were already
  // lazy, emitted zero image preloads; / and the fixture pages, whose crests
  // were not, emitted them.
  //
  // Every <img> in this app is a small decorative logo, crest or flag. None is
  // ever the largest contentful paint, so none should be preloaded.
  const offenders = [];
  for (const file of await sourceFiles()) {
    const source = await readFile(new URL(file, APP_DIR), "utf8");
    for (const tag of source.match(/<img\b[^>]*>/g) ?? []) {
      if (!/loading="lazy"/.test(tag)) offenders.push(`${file}: ${tag.slice(0, 90)}`);
    }
  }
  assert.deepEqual(offenders, [], `these images would be preloaded ahead of the LCP element:\n${offenders.join("\n")}`);
});

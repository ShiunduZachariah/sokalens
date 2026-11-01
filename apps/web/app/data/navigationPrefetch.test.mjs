import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const appRoot = new URL("../", import.meta.url);

test("high-fanout navigation leaves links crawlable without prefetching hidden page images", async () => {
  const paths = [
    "components/PublicHeader.tsx",
    "components/BrandLogo.tsx",
    "components/DateTabs.tsx",
    "components/PredictionSubnav.tsx",
    "components/MobileBottomNav.tsx",
  ];

  for (const path of paths) {
    const contents = await readFile(new URL(path, appRoot), "utf8");
    assert.match(contents, /prefetch=\{false\}/, `${path} must disable speculative route prefetch`);
    assert.match(contents, /<Link/, `${path} must retain a real crawlable link`);
  }
});

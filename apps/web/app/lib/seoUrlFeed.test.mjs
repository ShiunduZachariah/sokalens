import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { stripTypeScriptTypes } from "node:module";
import test from "node:test";

// Exercise the real loader and HTTP client; only the network boundary is mocked.
const client = await readFile(new URL("../data/apiClient.ts", import.meta.url), "utf8");
const feed = (await readFile(new URL("./seoUrlFeed.ts", import.meta.url), "utf8"))
  .replace(/^import .*;\r?\n/gm, "");
const { loadSeoUrls } = await import(`data:text/javascript,${encodeURIComponent(stripTypeScriptTypes(`${client}\n${feed}`))}`);
const page = (count, start = 0) => Array.from({ length: count }, (_, i) => ({ path: `/teams/team-${start + i}` }));
function respond(t, payloads) {
  let calls = 0;
  t.mock.method(globalThis, "fetch", async () => {
    const payload = payloads[calls++];
    return payload instanceof Response ? payload : Response.json(payload);
  });
}

test("a genuine empty inventory remains empty", async (t) => {
  respond(t, [{ total: 0, urls: [] }]);
  assert.deepEqual(await loadSeoUrls("team"), []);
});

test("complete pages retain every URL and supplied lastmod", async (t) => {
  const last = { path: "/teams/last", last_modified: "2026-10-06T08:00:00Z" };
  respond(t, [{ total: 10001, urls: page(10000) }, { total: 10001, urls: [last] }]);
  const urls = await loadSeoUrls("team");
  assert.equal(urls.length, 10001);
  assert.deepEqual(urls.at(-1), { path: last.path, lastModified: last.last_modified });
});

for (const failedPage of [0, 1]) {
  test(`HTTP failure on page ${failedPage + 1} cannot become an empty or partial sitemap`, async (t) => {
    respond(t, [...(failedPage ? [{ total: 10001, urls: page(10000) }] : []), new Response("unavailable", { status: 503 })]);
    await assert.rejects(loadSeoUrls("team"));
  });
}

for (const payload of [null, {}, { total: 0 }, { total: "1", urls: page(1) }, { total: -1, urls: [] }, { total: 1.5, urls: [] }, { total: 1, urls: [null] }, { total: 1, urls: [{}] }, { total: 1, urls: [{ path: "" }] }]) {
  test(`malformed inventory is rejected: ${JSON.stringify(payload)}`, async (t) => {
    respond(t, [payload]);
    await assert.rejects(loadSeoUrls("team"));
  });
}

test("a short page cannot silently omit the reported inventory", async (t) => {
  respond(t, [{ total: 2, urls: page(1) }]);
  await assert.rejects(loadSeoUrls("team"));
});

test("a malformed later page cannot publish the first page", async (t) => {
  respond(t, [{ total: 10001, urls: page(10000) }, { total: 10001 }]);
  await assert.rejects(loadSeoUrls("team"));
});

test("changing totals cannot publish an inconsistent inventory", async (t) => {
  respond(t, [{ total: 10001, urls: page(10000) }, { total: 10002, urls: page(2, 10000) }]);
  await assert.rejects(loadSeoUrls("team"));
});

test("the pagination ceiling cannot publish a truncated inventory", async (t) => {
  respond(t, Array.from({ length: 12 }, (_, i) => ({ total: 120001, urls: page(10000, i * 10000) })));
  await assert.rejects(loadSeoUrls("team"));
});

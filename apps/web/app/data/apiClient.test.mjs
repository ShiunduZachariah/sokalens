import test from "node:test";
import assert from "node:assert/strict";

import { fetchPredictionApi, predictionApiBaseUrl, responseAgeSeconds } from "./apiClient.ts";

test("prediction API client trims the configured base URL", () => {
  const previous = process.env.PREDICTION_API_BASE_URL;
  process.env.PREDICTION_API_BASE_URL = "http://api.internal:8080/";
  try {
    assert.equal(predictionApiBaseUrl(), "http://api.internal:8080");
  } finally {
    if (previous === undefined) delete process.env.PREDICTION_API_BASE_URL;
    else process.env.PREDICTION_API_BASE_URL = previous;
  }
});

test("prediction API client applies revalidation and a request deadline", async () => {
  const previousBaseUrl = process.env.PREDICTION_API_BASE_URL;
  const previousFetch = globalThis.fetch;
  process.env.PREDICTION_API_BASE_URL = "http://api.internal:8080";
  let request;

  globalThis.fetch = async (input, init) => {
    request = { input, init };
    return new Response("{}", { status: 200 });
  };

  try {
    await fetchPredictionApi("v1/predictions/today?limit=6", { revalidate: 60 });
    assert.equal(request.input, "http://api.internal:8080/v1/predictions/today?limit=6");
    assert.equal(request.init.headers.Accept, "application/json");
    assert.equal(request.init.next.revalidate, 60);
    assert.ok(request.init.signal instanceof AbortSignal);
  } finally {
    globalThis.fetch = previousFetch;
    if (previousBaseUrl === undefined) delete process.env.PREDICTION_API_BASE_URL;
    else process.env.PREDICTION_API_BASE_URL = previousBaseUrl;
  }
});

async function withStubbedFetch(responses, run) {
  const previousBaseUrl = process.env.PREDICTION_API_BASE_URL;
  const previousFetch = globalThis.fetch;
  process.env.PREDICTION_API_BASE_URL = "http://api.internal:8080";
  const requests = [];
  globalThis.fetch = async (input, init) => {
    requests.push({ input, init });
    const next = responses.shift();
    if (next instanceof Error) throw next;
    return next;
  };
  try {
    return await run(requests);
  } finally {
    globalThis.fetch = previousFetch;
    if (previousBaseUrl === undefined) delete process.env.PREDICTION_API_BASE_URL;
    else process.env.PREDICTION_API_BASE_URL = previousBaseUrl;
  }
}

function apiResponse(body, ageSeconds, status = 200) {
  const headers = ageSeconds === null ? {} : { Date: new Date(Date.now() - ageSeconds * 1000).toUTCString() };
  return new Response(JSON.stringify(body), { status, headers });
}

test("a cached copy inside the stale window is served without a second request", async () => {
  await withStubbedFetch([apiResponse({ status: "upcoming" }, 20)], async (requests) => {
    const response = await fetchPredictionApi("/v1/matches/17529008", { revalidate: 15, staleWhileRevalidate: 30 });
    assert.deepEqual(await response.json(), { status: "upcoming" });
    assert.equal(requests.length, 1);
    assert.equal(requests[0].init.next.revalidate, 15);
  });
});

// The production defect: Next's data cache returns an expired entry and
// refreshes it in the background, with no upper bound on how old the entry is.
// A fixture page last rendered before kickoff showed its first visitor after
// full time "Upcoming" with every market pending.
test("a cached copy older than the stale window is replaced by an uncached read", async () => {
  const hoursOld = apiResponse({ status: "upcoming", home_score: null }, 4 * 60 * 60);
  const current = apiResponse({ status: "finished", home_score: 1 }, 0);
  await withStubbedFetch([hoursOld, current], async (requests) => {
    const response = await fetchPredictionApi("/v1/matches/17529008", { revalidate: 15, staleWhileRevalidate: 30 });
    assert.deepEqual(await response.json(), { status: "finished", home_score: 1 });
    assert.equal(requests.length, 2);
    assert.equal(requests[1].input, "http://api.internal:8080/v1/matches/17529008");
    assert.equal(requests[1].init.cache, "no-store");
    assert.equal(requests[1].init.next, undefined);
    assert.ok(requests[1].init.signal instanceof AbortSignal);
  });
});

test("a response with no readable age is not trusted as young", async () => {
  await withStubbedFetch([apiResponse({ v: "unknown age" }, null), apiResponse({ v: "current" }, 0)], async (requests) => {
    const response = await fetchPredictionApi("/v1/leagues/x", { revalidate: 60, staleWhileRevalidate: 60 });
    assert.deepEqual(await response.json(), { v: "current" });
    assert.equal(requests.length, 2);
  });
});

test("an unreachable API falls back to the cached copy rather than an error page", async () => {
  await withStubbedFetch([apiResponse({ v: "old" }, 600), new Error("timeout")], async () => {
    const response = await fetchPredictionApi("/v1/matches/1", { revalidate: 15, staleWhileRevalidate: 30 });
    assert.deepEqual(await response.json(), { v: "old" });
  });
});

test("a failing API falls back to the cached copy, but a not-found is an answer", async () => {
  await withStubbedFetch([apiResponse({ v: "old" }, 600), apiResponse({ error: "down" }, 0, 503)], async () => {
    const response = await fetchPredictionApi("/v1/matches/1", { revalidate: 15, staleWhileRevalidate: 30 });
    assert.equal(response.status, 200);
    assert.deepEqual(await response.json(), { v: "old" });
  });
  await withStubbedFetch([apiResponse({ v: "old" }, 600), apiResponse({ error: "gone" }, 0, 404)], async () => {
    const response = await fetchPredictionApi("/v1/matches/1", { revalidate: 15, staleWhileRevalidate: 30 });
    assert.equal(response.status, 404);
  });
});

test("reads without a stale window keep Next's unbounded behaviour", async () => {
  await withStubbedFetch([apiResponse({ v: "old" }, 24 * 60 * 60)], async (requests) => {
    const response = await fetchPredictionApi("/v1/seo/urls?type=fixture", { revalidate: 300 });
    assert.deepEqual(await response.json(), { v: "old" });
    assert.equal(requests.length, 1);
  });
});

test("response age reads the API's Date header", () => {
  const now = Date.parse("2026-11-09T04:25:00Z");
  const response = new Response("{}", { headers: { Date: "Mon, 09 Nov 2026 00:25:00 GMT" } });
  assert.equal(responseAgeSeconds(response, now), 4 * 60 * 60);
  assert.equal(responseAgeSeconds(new Response("{}"), now), null);
  const future = new Response("{}", { headers: { Date: "Mon, 09 Nov 2026 04:25:05 GMT" } });
  assert.equal(responseAgeSeconds(future, now), 0);
});

// Every read that carries match status, score or settlement must bound its
// staleness. Dropping the option from one of them brings the defect back for
// that page without failing anything else.
test("every match-state read bounds how stale its cached copy may be", async () => {
  const { readFile } = await import("node:fs/promises");
  const files = [
    "app/data/liveMatches.ts",
    "app/data/livePredictions.ts",
    "app/data/catalog.ts",
    "app/data/predictionResults.ts",
    "app/data/accaApi.ts",
    "app/api/accas/route.ts",
  ];
  const stateBearing = /\/v1\/(matches|leagues|teams|predictions\/today|predictions\/results|accas\?)|fetchPredictionApi\(path,/;
  let checked = 0;
  for (const file of files) {
    const source = await readFile(new URL(`../../${file}`, import.meta.url), "utf8");
    for (const call of source.split("fetchPredictionApi(").slice(1)) {
      const head = call.slice(0, call.indexOf(");"));
      const invocation = `fetchPredictionApi(${head}`;
      if (!stateBearing.test(invocation)) continue;
      if (/revalidate:\s*0\b/.test(head)) continue;
      checked += 1;
      assert.match(head, /staleWhileRevalidate:/, `${file}: ${invocation.split("\n")[0]}`);
    }
  }
  assert.ok(checked >= 9, `expected to check the match-state reads, checked ${checked}`);
});

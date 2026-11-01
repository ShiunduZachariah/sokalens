import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";
import ts from "typescript";

const require = createRequire(import.meta.url);
const source = readFileSync(new URL("./[...path]/route.ts", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText;
const route = { exports: {} };
class NextResponse extends Response {
  static json(body, init) { return Response.json(body, init); }
}
new Function("require", "module", "exports", compiled)((name) => ({
  "next/server": { NextResponse },
  "../../../data/apiClient": { predictionApiBaseUrl: () => "http://qa-api" },
  "../proxyHeaders": { accountProxyHeaders: () => new Headers() },
  "../requestBody": { AccountBodyError: class AccountBodyError extends Error {}, readAccountBody: async () => null },
  "../../../lib/nullBodyStatus": { nullBodyStatus: () => false },
})[name] ?? require(name), route, route.exports);

const request = { method: "GET", headers: new Headers(), nextUrl: new URL("http://qa-web/api/account/v1/inplay/fixtures/42") };
const context = (id) => ({ params: Promise.resolve({ path: ["v1", "inplay", "fixtures", id] }) });

test("guest fixture refresh reaches the public in-play API", async (t) => {
  const urls = [];
  t.mock.method(globalThis, "fetch", async (url) => {
    urls.push(String(url));
    return Response.json({ fixture: { fixture_id: 42, markets: [] } });
  });
  const response = await route.exports.GET(request, context("42"));
  assert.equal(response.status, 200);
  assert.deepEqual(urls, ["http://qa-api/v1/inplay/fixtures/42"]);
  assert.equal((await response.json()).fixture.fixture_id, 42);
});

test("the fixture proxy rejects non-numeric paths before upstream fetch", async (t) => {
  const calls = [];
  t.mock.method(globalThis, "fetch", async () => { calls.push(true); return Response.json({}); });
  const response = await route.exports.GET(request, context("latest"));
  assert.equal(response.status, 404);
  assert.deepEqual(calls, []);
});

/**
 * The probability graph goes through the same proxy, and it did not.
 *
 * The allowlist entry above is anchored -- `/v1/inplay/fixtures/{id}$` -- so
 * `/v1/inplay/fixtures/{id}/series` fell outside it and the browser got a 404
 * while the API answered 200 to a direct call. Verifying the endpoint is not
 * the same as verifying the path a reader actually takes, and only a browser
 * on the real page found it.
 */
const seriesRequest = {
  method: "GET",
  headers: new Headers(),
  nextUrl: new URL("http://qa-web/api/account/v1/inplay/fixtures/42/series"),
};
const seriesContext = (...segments) => ({
  params: Promise.resolve({ path: ["v1", "inplay", "fixtures", ...segments] }),
});

test("the probability series reaches the in-play API", async (t) => {
  const urls = [];
  t.mock.method(globalThis, "fetch", async (url) => {
    urls.push(String(url));
    return Response.json({ series: [{ sequence: 1, minute: 0 }] });
  });
  const response = await route.exports.GET(seriesRequest, seriesContext("42", "series"));
  assert.equal(response.status, 200, "the series path is not allowed through the proxy");
  assert.deepEqual(urls, ["http://qa-api/v1/inplay/fixtures/42/series"]);
});

test("the allowlist stays an allowlist under the fixture prefix", async (t) => {
  // Loosening the entry to a prefix would have been the quicker fix and would
  // forward anything the API ever mounts under a fixture -- including routes
  // written later with different assumptions about who may call them.
  const calls = [];
  t.mock.method(globalThis, "fetch", async () => { calls.push(true); return Response.json({}); });
  for (const segments of [["42", "admin"], ["42", "series", "raw"], ["0", "series"], ["abc", "series"]]) {
    const response = await route.exports.GET(seriesRequest, seriesContext(...segments));
    assert.equal(response.status, 404, `/${segments.join("/")} was forwarded upstream`);
  }
  assert.deepEqual(calls, [], "a rejected path still reached the API");
});

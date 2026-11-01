import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import test from "node:test";
import ts from "typescript";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";

const require = createRequire(import.meta.url);
const app = path.resolve(import.meta.dirname, "../..");
function load(relative, dependencies = {}) {
  const filename = path.join(app, relative);
  const compiled = ts.transpileModule(readFileSync(filename, "utf8"), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  const module = { exports: {} };
  const resolve = (name) => {
    if (name in dependencies) return dependencies[name];
    if (!name.startsWith(".")) return require(name);
    const base = path.resolve(path.dirname(filename), name);
    const resolved = [base, `${base}.ts`, `${base}.tsx`].find(existsSync);
    return load(path.relative(app, resolved), dependencies);
  };
  new Function("require", "module", "exports", compiled)(resolve, module, module.exports);
  return module.exports;
}

for (const status of ["loading", "ready"]) {
  test(`anonymous in-play content renders while account status is ${status}`, () => {
    const { InPlayGate } = load("components/InPlayGate.tsx", {
      "./account/AccountContext": { useAccount: () => ({ status, access: { authenticated: false } }) },
      "./locale/LocaleProvider": { useLocale: () => ({ locale: "en" }) },
      "../i18n/config": { localePath: (value) => value },
      "next/link": "a",
    });
    const html = renderToStaticMarkup(React.createElement(InPlayGate, null, React.createElement("p", null, "Live markets")));
    assert.equal(html, "<p>Live markets</p>");
  });
}

test("anonymous overview, fixture and changes requests reach the existing public proxy", async (t) => {
  const requests = [];
  t.mock.method(globalThis, "fetch", async (url, init) => {
    requests.push([url, init]);
    return Response.json(url.endsWith("/fixtures/1")
      ? { fixture: { fixture_id: 1, markets: [{ key: "next_goal", prediction: { selection: "home", probability_bp: 6400 } }] } }
      : { fixtures: [], snapshots: [], events: [], cursor: 42, entitled: true, locked_count: 0 });
  });
  const { fetchInPlayOverview, fetchInPlayFixture, fetchInPlayChanges } = load("data/inplay.ts");
  assert.equal((await fetchInPlayOverview()).cursor, 42);
  assert.equal((await fetchInPlayFixture(1)).markets[0].key, "next_goal");
  assert.equal((await fetchInPlayChanges(42)).cursor, 42);
  assert.deepEqual(requests.map(([url]) => url), ["/api/account/v1/inplay/overview", "/api/account/v1/inplay/fixtures/1", "/api/account/v1/inplay/changes?since=42"]);
  for (const [, init] of requests) {
    assert.equal(init.headers.has("Authorization"), false);
    assert.equal(init.headers.has("Cookie"), false);
    assert.equal(init.cache, "no-store");
  }
});

function renderMarkets(health, locked = false, markets = [{ key: "next_goal", label: "Next goal", locked, prediction: { selection: "home", probabilityBp: 6400 } }]) {
  const state = {
    fixture: { stale: health === "stale", markets },
    snapshot: { modelHealth: health },
    lockedCount: locked ? 1 : 0,
    unlock: { reason: "auth_required", cta: "Sign in" },
  };
  let stateIndex = 0;
  const { InPlayMarkets } = load("components/inplay/InPlayMarkets.tsx", {
    react: { ...React, useState: (initial) => [stateIndex++ === 0 ? state : stateIndex === 2 ? "ready" : initial, () => {}] },
    "../locale/LocaleProvider": { useLocale: () => ({ locale: "en" }) },
    "../../i18n/config": { localePath: (value) => value },
    "next/link": "a",
  });
  return renderToStaticMarkup(React.createElement(InPlayMarkets, { fixtureId: 1, phase: "live" }));
}

test("public markets show available figures without an account offer", () => {
  const html = renderMarkets("ok");
  assert.match(html, /64%/);
  assert.doesNotMatch(html, /Sign in|subscription|href="\/pro"/);
});

test("live market headings show family names instead of wire keys or ladder lines", () => {
  const markets = [
    ["btts_in_play", "Both teams to score"],
    ["match_result_in_play", "Match result"],
    ["total_cards_1.5", "Total cards"],
    ["total_goals_match_0.5", "Total goals in match"],
    ["total_goals_remaining_0.5", "Goals remaining"],
    ["corners_total_8.5", "Total corners"],
  ].map(([key]) => ({ key, label: key, locked: false, prediction: { selection: "over", probabilityBp: 6400 } }));
  const html = renderMarkets("ok", false, markets);
  for (const [, label] of [
    ["btts_in_play", "Both teams to score"], ["match_result_in_play", "Match result"],
    ["total_cards_1.5", "Total cards"], ["total_goals_match_0.5", "Total goals in match"],
    ["total_goals_remaining_0.5", "Goals remaining"], ["corners_total_8.5", "Total corners"],
  ]) assert.match(html, new RegExp(`<h4>${label}</h4>`));
  assert.doesNotMatch(html, /<h4>[^<]*_(?:in_play|\d)/);
});

test("a legacy locked response stays withheld without advertising a login or upgrade", () => {
  const html = renderMarkets("ok", true);
  assert.doesNotMatch(html, /64%|Sign in|auth_required|subscription|>Pro</);
});

test("public access preserves stale labelling and suspended suppression", () => {
  assert.match(renderMarkets("stale"), /last figures we had/);
  assert.doesNotMatch(renderMarkets("suspended"), /64%/);
});

import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";
import { createRequire } from "node:module";
import path from "node:path";
import test from "node:test";
import ts from "typescript";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";

const require = createRequire(import.meta.url);
function load(filename, mocks) {
  const module = { exports: {} };
  const js = ts.transpileModule(readFileSync(filename, "utf8"), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
  }).outputText;
  new Function("require", "module", "exports", js)((name) => {
    if (name in mocks) return mocks[name];
    if (!name.startsWith(".")) return require(name);
    const base = path.resolve(path.dirname(filename), name);
    return load([base, `${base}.ts`, `${base}.tsx`].find(existsSync), mocks);
  }, module, module.exports);
  return module.exports;
}
const pick = (matchId, selection = "over_2.5") => ({ matchId, homeTeam: "Home", awayTeam: "Away", leagueName: "Premier League", selection, confidence: 72, marketKey: "total_goals", kickoffAt: "2026-10-07T12:00:00Z" });
const common = {
  "next/link": { __esModule: true, default: ({ children, prefetch, ...props }) => React.createElement("a", props, children) },
  "next/headers": { cookies: async () => ({ get: () => ({ value: "Africa/Nairobi" }) }) },
  "next/navigation": { notFound: () => { throw new Error("404"); } },
  "../../../i18n/server": { requestLocaleContext: async () => ({ locale: "en" }) },
  "../../i18n/server": { requestLocaleContext: async () => ({ locale: "en" }) },
  "../../../components/account/FavoriteButton": { FavoriteButton: () => null },
  "../../../data/predictionItems": { fromLivePrediction: (item) => ({ league: item.leagueName }) },
  "../../../components/PredictionListShell": { PredictionListShell: ({ leadContent, items, emptyState }) => React.createElement("main", null, leadContent, items.length ? null : emptyState) },
  "../today/page": { __esModule: true, default: ({ leadContent }) => React.createElement("main", null, leadContent) },
  "../../components/TopPickCard": { TopPickCard: ({ pick }) => React.createElement("a", { href: pick.href }, pick.homeTeam) },
};
async function market(slug, rows) {
  const { default: Page } = load(path.join(import.meta.dirname, "markets/[marketSlug]/page.tsx"), {
    ...common,
    "../../../data/livePredictions": { fetchMarketPredictions: async () => { if (rows instanceof Error) throw rows; return rows; } },
  });
  return renderToStaticMarkup(await Page({ params: Promise.resolve({ marketSlug: slug }) }));
}

test("Over 2.5 explains its result boundary and links to distinct fixture analysis", async () => {
  const html = await market("over-2-5-goals", [pick(1), pick(1), pick(2, "under_2.5")]);
  assert.match(html, /2–1 and 3–0/);
  assert.match(html, /1 fixture/);
  assert.match(html, /7 October 2026/);
  assert.match(html, /href="\/predictions\/premier-league\/home-vs-away-1"/);
  assert.match(html, /href="\/predictions\/markets\/btts"/);
});

test("BTTS explains both selections and does not equate BTTS with Over 2.5", async () => {
  const html = await market("btts", [{ ...pick(3, "no"), marketKey: "btts" }]);
  assert.match(html, /1–1/);
  assert.match(html, /3–0/);
  assert.match(html, /BTTS No/);
});

test("market failure remains unknown rather than claiming zero qualifying fixtures", async () => {
  const html = await market("btts", new Error("upstream"));
  assert.match(html, /Predictions are temporarily unavailable/);
  assert.doesNotMatch(html, /No qualifying fixtures right now|0 fixtures/);
});

test("a genuinely empty market retains useful interpretation and a truthful empty state", async () => {
  const html = await market("over-2-5-goals", []);
  assert.match(html, /No qualifying fixtures right now/);
  assert.match(html, /2–1 and 3–0/);
  assert.doesNotMatch(html, /Every settled selection/);
});

test("Tomorrow describes its date/source scope and does not promise ten available picks or a complete archive", async () => {
  const { default: Page, metadata } = load(path.join(import.meta.dirname, "tomorrow/page.tsx"), {
    ...common,
    "../../data/livePredictions": { fetchAllTodayPredictionsWithProvider: async () => ({ engine: [pick(1), pick(1)], provider: [], total: 1 }) },
  });
  const html = renderToStaticMarkup(await Page());
  assert.match(html, /Africa\/Nairobi/);
  assert.match(html, /1 fixture/);
  assert.match(html, /href="\/predictions\/markets\/over-2-5-goals"/);
  assert.doesNotMatch(html, /Every settled prediction is published|Every settled result is published/);
  assert.doesNotMatch(metadata.title, /Top 10/);
});

for (const unavailable of [false, true]) {
  test(`Tomorrow ${unavailable ? "unavailable" : "empty"} feed preserves useful guidance without an invented shortlist`, async () => {
    const { default: Page } = load(path.join(import.meta.dirname, "tomorrow/page.tsx"), {
      ...common,
      "../../data/livePredictions": { fetchAllTodayPredictionsWithProvider: async () => {
        if (unavailable) throw new Error("upstream");
        return { engine: [], provider: [], total: 0 };
      } },
    });
    const html = renderToStaticMarkup(await Page());
    assert.match(html, unavailable ? /model feed is temporarily unavailable/ : /0 fixtures with model selections/);
    assert.doesNotMatch(html, /id="tomorrow-top-picks"/);
    if (unavailable) assert.doesNotMatch(html, /0 fixtures/);
    const schema = JSON.parse(html.match(/<script[^>]*>(.*?)<\/script>/s)[1])[0];
    const visible = html.replace(/<script[^>]*>.*?<\/script>/gs, "").replace(/<[^>]+>/g, "").replaceAll("&#x27;", "'");
    for (const question of schema.mainEntity) {
      assert.ok(visible.includes(question.name), "FAQ question must be visible");
      assert.ok(visible.includes(question.acceptedAnswer.text), "FAQ answer must match visible copy");
    }
  });
}

test("the targeted market guide does not spread to unrelated markets", async () => {
  assert.doesNotMatch(await market("cards", []), /id="market-reading-guide"/);
});

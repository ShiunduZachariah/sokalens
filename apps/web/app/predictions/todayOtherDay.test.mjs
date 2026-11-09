import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
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

const isoDay = (offset) => new Date(Date.now() + offset * 86_400_000).toLocaleDateString("en-CA", { timeZone: "Africa/Nairobi" });

let shell;
const today = load(path.join(import.meta.dirname, "today/page.tsx"), {
  "next/link": { __esModule: true, default: ({ children, prefetch, ...props }) => React.createElement("a", props, children) },
  "next/headers": { cookies: async () => ({ get: () => ({ value: "Africa/Nairobi" }) }) },
  "../../i18n/server": { requestLocaleContext: async () => ({ locale: "en", publicPath: "/predictions/today" }) },
  "../../components/DateTabs": { DateTabs: () => null },
  "../../components/PredictionListShell": { PredictionListShell: (props) => { shell = props; return null; } },
  "../../data/liveMatches": { fetchMatchCount: async () => 0 },
  "../../data/livePredictions": { fetchAllTodayPredictionsWithProvider: async () => ({ engine: [], provider: [], total: 0 }) },
  "../../data/predictionItems": { fromLivePrediction: () => ({}), fromProviderLivePrediction: () => ({}) },
});

async function render(searchParams, forcedDay) {
  renderToStaticMarkup(await today.default({ searchParams: Promise.resolve(searchParams), forcedDay }));
  return shell;
}
const metadata = (searchParams) => today.generateMetadata({ searchParams: Promise.resolve(searchParams) });

test("another day replaces every mention of today", async () => {
  const date = isoDay(3);
  const label = new Intl.DateTimeFormat("en-GB", { day: "numeric", month: "long", timeZone: "UTC", weekday: "long" }).format(new Date(`${date}T12:00:00Z`));
  const props = await render({ date });
  assert.equal(props.title, `Football predictions for ${label}`);
  assert.match(props.summary, new RegExp(label));
  assert.equal(props.subnavNoCurrent, true);
  const empty = renderToStaticMarkup(props.emptyState);
  assert.match(empty, new RegExp(`No published predictions for ${label}`));
  assert.doesNotMatch(empty, /Today.s prediction list/);
  assert.equal((await metadata({ date })).title, `Football predictions for ${label} - SokaLens`);
});

test("today keeps its own copy, whether or not the URL names the date", async () => {
  for (const searchParams of [{}, { date: isoDay(0) }]) {
    const props = await render(searchParams);
    assert.equal(props.title, "Today's football predictions");
    assert.equal(props.subnavNoCurrent, false);
    assert.equal((await metadata(searchParams)).title, "Today's football predictions - SokaLens");
  }
});

test("a nonsense date in the URL cannot break the page or reach the heading", async () => {
  const props = await render({ date: "not-a-date" });
  assert.equal(props.title, "Today's football predictions");
  assert.equal((await metadata({ date: "not-a-date" })).title, "Today's football predictions - SokaLens");
});

test("Tomorrow keeps its own heading", async () => {
  const props = await render({}, "tomorrow");
  assert.equal(props.title, "Tomorrow's football predictions");
  assert.equal(props.subnavNoCurrent, false);
});

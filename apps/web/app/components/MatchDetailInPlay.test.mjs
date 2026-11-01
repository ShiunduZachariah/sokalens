import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";
import ts from "typescript";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { isFinishedStatus, isLiveStatus, matchStatusLabel } from "../lib/matchStatus.ts";

const require = createRequire(import.meta.url);
const source = readFileSync(new URL("./MatchDetail.tsx", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
}).outputText;
// Isolate the real page's status-to-panel decision from unrelated catalogue reads
// and visual components. Render the actual in-play tab content selected by it.
const dependencies = {
  "../lib/matchStatus": { isFinishedStatus },
  "../lib/fixtureLifecycle": { isLiveFixture: isLiveStatus },
  "../data/liveMatches": { matchScore: () => "–", matchStatusLabel: (match) => matchStatusLabel(match.status), matchClockLabel: () => "Live" },
  "../i18n/server": { requestLocaleContext: async () => ({ locale: "en" }) },
  "../lib/matchPreview": { buildMatchPreview: () => null, marketReasons: () => new Map() },
  "../lib/relatedFixtures": { relatedFixtures: () => [] },
  "./InPlayGate": { InPlayGate: ({ children }) => children },
  "./inplay/InPlayMarkets": { InPlayMarkets: ({ phase }) => React.createElement("span", { "data-market-phase": phase }, "Market board") },
};
const module = { exports: {} };
new Function("require", "module", "exports", compiled)((name) => {
  if (name === "react/jsx-runtime") return require(name);
  return dependencies[name] ?? new Proxy({}, { get: () => () => null });
}, module, module.exports);

async function panel(status) {
  const tree = await module.exports.MatchDetail({ match: {
    id: 1, status, homeTeam: "Home", awayTeam: "Away", predictions: [], timeline: [], odds: [],
    kickoffAt: "2026-10-06T12:00:00Z", homeForm: [], awayForm: [],
  } });
  function find(node) {
    if (!node || typeof node !== "object") return null;
    const tab = node.props?.tabs?.find((item) => item.id === "in-play");
    if (tab) return tab.content;
    for (const child of React.Children.toArray(node.props?.children)) {
      const result = find(child);
      if (result) return result;
    }
    return null;
  }
  const content = find(tree);
  assert.ok(content, "the public in-play tab remains available");
  return renderToStaticMarkup(content);
}

for (const status of ["unknown", "canceled", "postponed", "abandoned", "suspended", "interrupted", "awarded", "walkover"]) {
  test(`${status} does not claim a final-whistle in-play read`, async () => {
    const html = await panel(status);
    assert.doesNotMatch(html, /data-market-phase/);
    assert.match(html, /In-play predictions are unavailable/);
  });
}

for (const [status, phase] of [["scheduled", "prematch"], ["live", "live"], ["halftime", "live"], ["finished", "finished"]]) {
  test(`${status} keeps the public ${phase} market board`, async () => {
    assert.match(await panel(status), new RegExp(`data-market-phase="${phase}"`));
  });
}

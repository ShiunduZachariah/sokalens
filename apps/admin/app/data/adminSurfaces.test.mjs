import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../", import.meta.url);

async function source(path) {
  return readFile(new URL(path, root), "utf8");
}

test("admin navigation and pages expose the account and prediction operations surfaces", async () => {
  const [shell, activity, predictions, providers] = await Promise.all([
    source("components/AdminShell.tsx"),
    source("user-activity/page.tsx"),
    source("predictions/page.tsx"),
    source("provider/page.tsx"),
  ]);

  assert.match(shell, /href: "\/user-activity"/);
  assert.match(activity, /\/v1\/admin\/user-activity\?limit=200/);
  assert.match(predictions, /Published predictions/);
  assert.match(predictions, /Match results/);
  assert.match(providers, /\/v1\/admin\/provider-requests/);
  assert.match(providers, /fetchProviderStatusSnapshot/);
});

test("published prediction feed reserves space for provider fallback rows", async () => {
  const livePredictions = await source("data/livePredictions.ts");
  assert.match(livePredictions, /providerLimit/);
  assert.match(livePredictions, /providerPredictions\.slice\(0, providerLimit\)/);
  assert.match(livePredictions, /provider fallback/);
});

import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { seoFixturePath, seoLeaguePath, seoSlug } from "./seo.ts";

/**
 * The web app and outbound email must produce byte-identical URLs.
 *
 * On-site links come from `seo.ts`; email bodies are built in Go, in
 * `services/api/internal/emaillinks`, because the notification outbox lives on
 * the backend. Two implementations of one URL shape drift silently: the Go
 * side folds a diacritic differently, the reader gets a 404 or a redirect hop,
 * and Search Console reports it as a duplicate six weeks later.
 *
 * Both sides assert this one table, so drift fails a test in whichever
 * language moved.
 */
const cases = JSON.parse(await readFile(new URL("../../../../packages/contracts/seo/email-link-cases.json", import.meta.url), "utf8"));

test("seoSlug matches the shared email-link contract", () => {
  for (const { input, expected } of cases.slugs) {
    assert.equal(seoSlug(input), expected, `seoSlug(${JSON.stringify(input)})`);
  }
});

test("fixture paths match the shared email-link contract", () => {
  for (const item of cases.fixtures) {
    if (!item.fixtureId || !item.leagueName || !item.homeTeam || !item.awayTeam) continue;
    assert.equal(
      seoFixturePath({ id: item.fixtureId, leagueName: item.leagueName, homeTeam: item.homeTeam, awayTeam: item.awayTeam }),
      item.expected,
    );
  }
});

test("league paths match the shared email-link contract", () => {
  for (const { leagueName, expected } of cases.leagues) {
    assert.equal(seoLeaguePath(seoSlug(leagueName)), expected);
  }
});

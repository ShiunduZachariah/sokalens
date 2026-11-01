import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { isIndexableFixture } from "./fixtureLifecycle.ts";

/**
 * The web half of the indexability contract. Go is the source of truth.
 *
 * `packages/contracts/seo-indexability.golden.json` is generated from
 * `services/worker/internal/seourl.Decide`, which decides what the sitemap
 * advertises. This asserts the fixture page's robots tag agrees with it.
 *
 * Why it exists: the two decisions are necessarily separate -- Go on a
 * schedule, TypeScript on a request, neither able to call the other -- and
 * nothing structural kept them in step. On 2026-09-28 the history gate was
 * flagged on the sweep but unconditional on the page, and the sitemap
 * advertised 136 URLs the page served as noindex. The shared CONSTANTS were
 * already pinned; the shared DECISION was not, and that is where it broke.
 *
 * Note this calls `isIndexableFixture`, the same function `generateMetadata`
 * calls. That is the point. A test that re-implemented the page's expression
 * would agree with the golden file forever while the page quietly diverged.
 */

const HERE = path.dirname(fileURLToPath(import.meta.url));
const GOLDEN = path.resolve(HERE, "../../../../packages/contracts/seo-indexability.golden.json");
const golden = JSON.parse(readFileSync(GOLDEN, "utf8"));

test("the golden file is present and populated", () => {
  // Guarding the guard. An empty or unregenerated corpus would make every
  // assertion below vacuous, and a guard that cannot fail is worse than none
  // because it also reads as a claim that someone checked.
  assert.ok(Array.isArray(golden.cases) && golden.cases.length > 0, "golden file has no cases");
  assert.ok(golden.now, "golden file has no fixed `now`; decisions would drift with the clock");
  for (const c of golden.cases) {
    assert.notEqual(c.indexable, null,
      `case "${c.name}" has no generated expectation -- regenerate the golden file from Go`);
  }
});

test("the page's robots decision matches Go for every case", () => {
  const now = new Date(golden.now);
  const skipped = [];

  for (const c of golden.cases) {
    if (c.goOnly) { skipped.push(c.name); continue; }

    const actual = isIndexableFixture(
      {
        kickoffAt: c.fixture.kickoffAt,
        status: c.fixture.status,
        homeHistoryCount: c.fixture.homeHistory,
        awayHistoryCount: c.fixture.awayHistory,
      },
      c.fixture.hasPrediction,
      now,
    );

    assert.equal(
      actual,
      c.indexable,
      `"${c.name}": Go says indexable=${c.indexable}`
        + `${c.reason ? ` (${c.reason})` : ""}, the page says ${actual}. `
        + "Go is the source of truth -- bring the page into line, or change Decide "
        + "and regenerate the golden file.",
    );
  }

  // Every go-only case must be one the page genuinely cannot evaluate. If that
  // list ever grows silently it becomes a hole in the contract.
  assert.deepEqual(skipped, ["a duplicate is never advertised whatever else is true of it"],
    "the set of cases exempt from the page contract changed; confirm the page really cannot decide them");
});

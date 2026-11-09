import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");

// The percentage on public pages is the engine's composite confidence score: data
// completeness, sample size, model agreement and similar. Scores break probability
// rules, for example a 64 on an away win beside a 56 on draw or away, so calling
// one a probability tells a reader something false about their pick.
const pages = [
  "../components/MatchDetail.tsx",
  "../best-bets/page.tsx",
  "../predictions/tomorrow/page.tsx",
  "../predictions/markets/[marketSlug]/page.tsx",
  "../about/page.tsx",
  "../acca-predictions/page.tsx",
  "../components/MarketReadingGuide.tsx",
  "./seo.ts",
];

test("no page calls the confidence score a probability", () => {
  for (const page of pages) {
    const source = read(page);
    assert.doesNotMatch(source, /confidence = model probability/i, page);
    assert.doesNotMatch(source, /confidence (is|describes) the model(&apos;|')s? \w* ?probability/i, page);
    assert.doesNotMatch(source, /model(&rsquo;|')s probability for that outcome/i, page);
    assert.doesNotMatch(source, /carries the model probability|the model probability is shown/i, page);
  }
});

test("the pages that define confidence say it is not the chance of winning", () => {
  assert.match(read("../components/MatchDetail.tsx"), /% is confidence, not win chance/);
  assert.match(read("../best-bets/page.tsx"), /not the chance it wins/);
  assert.match(read("../predictions/tomorrow/page.tsx"), /It is not the chance the selection wins/);
  assert.match(read("../about/page.tsx"), /It is not the chance/);
});

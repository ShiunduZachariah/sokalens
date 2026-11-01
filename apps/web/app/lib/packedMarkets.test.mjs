import assert from "node:assert/strict";
import test from "node:test";

const { emptyMarketTable, packMarkets, unpackMarkets } = await import("./packedMarkets.ts");

const slate = [
  [
    { market: "Match Result", prediction: "Home win", confidence: 61 },
    { market: "Total Goals", prediction: "Over 2.5 Goals", confidence: 54 },
  ],
  [
    { market: "Match Result", prediction: "Away win", confidence: 48 },
    { market: "Total Goals", prediction: "Over 2.5 Goals", confidence: 52 },
    { market: "Both Teams to Score", prediction: "Both teams to score: Yes", confidence: 58 },
  ],
];

test("packing then unpacking returns the original market entries", () => {
  const { packed, table } = packMarkets(slate);
  assert.deepEqual(packed.map((entry) => unpackMarkets(entry, table)), slate);
});

test("labels are stored once no matter how often they repeat", () => {
  const { table } = packMarkets(slate);
  // Three distinct markets and four distinct predictions across five entries.
  assert.deepEqual(table.marketLabels, ["Match Result", "Total Goals", "Both Teams to Score"]);
  assert.equal(table.predictionLabels.length, 4);
});

test("the market label table is exactly the slate's distinct market set", () => {
  // /predictions/today renders `marketLabels.length` as its "Markets" stat,
  // which has to keep matching the set it replaced.
  const { table } = packMarkets(slate);
  const distinct = new Set(slate.flatMap((entries) => entries.map((entry) => entry.market)));
  assert.equal(table.marketLabels.length, distinct.size);
  assert.deepEqual(new Set(table.marketLabels), distinct);
});

test("the packed form is dramatically smaller on a realistic slate", () => {
  // 460 fixtures x 13 markets, the shape production serves.
  const fixtures = Array.from({ length: 460 }, (_, fixture) =>
    Array.from({ length: 13 }, (_, market) => ({
      market: `Market number ${market}`,
      prediction: `A reasonably long selection label ${market % 7}`,
      confidence: 40 + ((fixture + market) % 50),
    })),
  );
  const before = JSON.stringify(fixtures).length;
  const { packed, table } = packMarkets(fixtures);
  const after = JSON.stringify(packed).length + JSON.stringify(table).length;
  assert.ok(after * 5 < before, `expected >5x reduction, got ${before} -> ${after}`);
});

test("an empty or absent market list packs and unpacks to nothing", () => {
  const { packed, table } = packMarkets([undefined, []]);
  assert.deepEqual(packed, [[], []]);
  assert.equal(unpackMarkets(packed[0], table), undefined);
  assert.equal(unpackMarkets(undefined, table), undefined);
});

test("an entry pointing outside the table is dropped, not rendered as undefined", () => {
  const entries = unpackMarkets([0, 0, 50, 99, 0, 50], { marketLabels: ["Total Goals"], predictionLabels: ["Over 2.5 Goals"] });
  assert.deepEqual(entries, [{ confidence: 50, market: "Total Goals", prediction: "Over 2.5 Goals" }]);
});

test("the empty table is a stable identity so it does not rerun memos", () => {
  assert.deepEqual(emptyMarketTable, { marketLabels: [], predictionLabels: [] });
});

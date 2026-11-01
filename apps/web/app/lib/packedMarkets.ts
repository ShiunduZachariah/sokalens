/**
 * Compact wire form for the per-item market lists on /predictions/today.
 *
 * Measured on production (2026-09-20, 461 fixtures / 5,734 market entries):
 * the page's inline RSC payload was 725 KB and 442 KB of it -- 61% -- was
 * these arrays, repeating a handful of labels over and over:
 *
 *   {"market":"Both Teams to Score","prediction":"Both teams to score: Yes",
 *    "confidence":61}
 *
 * There are only ~13 distinct market labels and ~42 distinct prediction
 * labels in a whole day's slate, so the labels go into two shared tables and
 * each entry becomes three integers. Roughly 78 bytes per entry becomes
 * roughly 10.
 *
 * This is a transport detail only: `unpackMarkets` rebuilds the exact objects
 * the explorer already renders, so nothing downstream changes shape.
 */

export type MarketEntry = { market: string; prediction: string; confidence: number };

export type MarketTable = {
  marketLabels: string[];
  predictionLabels: string[];
};

/** Flat triplets: [marketIndex, predictionIndex, confidence, ...]. */
export type PackedMarkets = number[];

export const emptyMarketTable: MarketTable = { marketLabels: [], predictionLabels: [] };

class LabelTable {
  private readonly index = new Map<string, number>();
  readonly labels: string[] = [];

  idFor(label: string) {
    const existing = this.index.get(label);
    if (existing !== undefined) return existing;
    const id = this.labels.length;
    this.index.set(label, id);
    this.labels.push(label);
    return id;
  }
}

export function packMarkets(itemMarkets: ReadonlyArray<MarketEntry[] | undefined>): {
  packed: PackedMarkets[];
  table: MarketTable;
} {
  const markets = new LabelTable();
  const predictions = new LabelTable();
  const packed = itemMarkets.map((entries) => {
    const flat: number[] = [];
    for (const entry of entries ?? []) {
      flat.push(markets.idFor(entry.market), predictions.idFor(entry.prediction), entry.confidence);
    }
    return flat;
  });
  return { packed, table: { marketLabels: markets.labels, predictionLabels: predictions.labels } };
}

export function unpackMarkets(packed: PackedMarkets | undefined, table: MarketTable): MarketEntry[] | undefined {
  if (!packed || packed.length === 0) return undefined;
  const entries: MarketEntry[] = [];
  for (let index = 0; index + 2 < packed.length; index += 3) {
    const market = table.marketLabels[packed[index]];
    const prediction = table.predictionLabels[packed[index + 1]];
    // A label the table does not carry means the two halves disagree; drop
    // the entry rather than render "undefined" into a market filter.
    if (market === undefined || prediction === undefined) continue;
    entries.push({ confidence: packed[index + 2], market, prediction });
  }
  return entries.length ? entries : undefined;
}

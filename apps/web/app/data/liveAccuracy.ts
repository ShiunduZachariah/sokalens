import { fetchPredictionApi } from "./apiClient";

export type MarketHitRate = {
  marketKey: string;
  settled: number;
  won: number;
  lost: number;
  void: number;
  hitRate: number;
  denominator: number;
};

export type HitRateReport = {
  settled: number;
  won: number;
  lost: number;
  void: number;
  hitRate: number;
  denominator: number;
  markets: MarketHitRate[];
};

type HitRatePayload = {
  settled?: unknown;
  won?: unknown;
  lost?: unknown;
  void?: unknown;
  hit_rate?: unknown;
  denominator?: unknown;
  markets?: Array<Record<string, unknown>>;
};

function finiteNumber(value: unknown) {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

export async function fetchHitRateReport(): Promise<HitRateReport | null> {
  try {
    const response = await fetchPredictionApi("/v1/predictions/hit-rate", {
      revalidate: 60,
    });
    if (!response.ok) {
      return null;
    }

    const payload = (await response.json()) as HitRatePayload;
    return {
      settled: finiteNumber(payload.settled),
      won: finiteNumber(payload.won),
      lost: finiteNumber(payload.lost),
      void: finiteNumber(payload.void),
      hitRate: finiteNumber(payload.hit_rate),
      denominator: finiteNumber(payload.denominator),
      markets: (payload.markets ?? []).map((market) => ({
        marketKey: String(market.market_key ?? "unknown"),
        settled: finiteNumber(market.settled),
        won: finiteNumber(market.won),
        lost: finiteNumber(market.lost),
        void: finiteNumber(market.void),
        hitRate: finiteNumber(market.hit_rate),
        denominator: finiteNumber(market.denominator),
      })),
    };
  } catch {
    return null;
  }
}

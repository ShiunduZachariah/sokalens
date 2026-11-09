import { fetchPredictionApi } from "./apiClient";

export type PredictionResultRow = {
  predictionId: number; matchId: number; matchTitle: string;
  /** The two sides, beside matchTitle rather than instead of it: a crest needs
   *  a name to hash its fallback shield from, and matchTitle is still the
   *  single-line fallback and the share text. */
  homeTeam: string; awayTeam: string; homeTeamLogo: string; awayTeamLogo: string;
  leagueName: string; country: string;
  kickoffAt: string; matchStatus: string; marketKey: string; selection: string; actual: string;
  outcome: "won" | "lost" | "void" | "pending"; confidence: number;
};

export type HitRateReport = {
  settled: number; won: number; lost: number; void: number; hitRate: number;
  markets: Array<{ marketKey: string; settled: number; won: number; lost: number; void: number; hitRate: number }>;
};

export async function fetchPredictionResults(date: string, timezone: string) {
  const params = new URLSearchParams({ date, timezone, limit: "100", page: "1" });
  const response = await fetchPredictionApi(`/v1/predictions/results?${params}`, { revalidate: 15, staleWhileRevalidate: 30 });
  if (!response.ok) throw new Error(`Prediction results request failed with ${response.status}`);
  const payload = await response.json() as { results?: Array<Record<string, unknown>>; total?: number };
  return {
    total: Number(payload.total ?? 0),
    rows: (payload.results ?? []).map((row) => ({
      predictionId: Number(row.prediction_id), matchId: Number(row.match_id), matchTitle: String(row.match_title ?? "Match"),
      homeTeam: String(row.home_team ?? ""), awayTeam: String(row.away_team ?? ""),
      homeTeamLogo: String(row.home_team_logo ?? ""), awayTeamLogo: String(row.away_team_logo ?? ""),
      leagueName: String(row.league_name ?? "Competition"), country: String(row.country ?? "International"),
      kickoffAt: String(row.kickoff_at ?? ""), matchStatus: String(row.match_status ?? "finished"),
      marketKey: String(row.market_key ?? ""), selection: String(row.selection ?? ""), actual: String(row.actual ?? ""),
      outcome: String(row.outcome ?? "pending") as PredictionResultRow["outcome"], confidence: Number(row.confidence ?? 0),
    })),
  };
}

export async function fetchHitRateReport(): Promise<HitRateReport> {
  const response = await fetchPredictionApi("/v1/predictions/hit-rate", { revalidate: 300 });
  if (!response.ok) throw new Error(`Hit-rate request failed with ${response.status}`);
  const payload = await response.json() as Record<string, unknown>;
  return {
    settled: Number(payload.settled ?? 0), won: Number(payload.won ?? 0), lost: Number(payload.lost ?? 0),
    void: Number(payload.void ?? 0), hitRate: Number(payload.hit_rate ?? 0),
    markets: Array.isArray(payload.markets) ? payload.markets.map((value) => {
      const market = value as Record<string, unknown>;
      return { marketKey: String(market.market_key ?? ""), settled: Number(market.settled ?? 0),
        won: Number(market.won ?? 0), lost: Number(market.lost ?? 0), void: Number(market.void ?? 0), hitRate: Number(market.hit_rate ?? 0) };
    }) : [],
  };
}

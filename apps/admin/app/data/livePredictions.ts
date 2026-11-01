type TodayPredictionsResponse = {
  predictions?: Array<{
    id?: number;
    match_id?: number;
    market_key?: string;
    selection?: string;
    confidence?: number;
    published_version?: number;
    match_title?: string;
    source?: string;
  }>;
  provider_predictions?: Array<{
    match_id?: number;
    match_title?: string;
    market_key?: string;
    selection?: string;
    confidence?: number;
    published_version?: number;
    source?: string;
  }>;
};

type MarketHitRateResponse = {
  market_key?: string;
  settled?: number;
  won?: number;
  lost?: number;
  void?: number;
  hit_rate?: number;
};

type PredictionHitRateResponse = {
  settled?: number;
  won?: number;
  lost?: number;
  void?: number;
  hit_rate?: number;
  markets?: MarketHitRateResponse[];
};

export type LivePredictionRow = {
  match: string;
  market: string;
  pick: string;
  confidence: string;
  status: string;
  model: string;
  inputVersion: string;
  predictionVersion: string;
  settlement: string;
};

export type LivePredictionStatRow = {
  market: string;
  published: number;
  review: number;
  hitRate: string;
  drift: string;
};

function predictionApiBaseUrl() {
  return (
    process.env.ADMIN_API_BASE_URL?.trim() ||
    process.env.PREDICTION_API_BASE_URL?.trim() ||
    process.env.API_BASE_URL?.trim() ||
    "http://localhost:8080"
  );
}

function marketLabel(marketKey: string) {
  const marketLabels: Record<string, string> = {
    "1x2": "1X2",
    double_chance: "Double Chance",
    btts: "BTTS",
    ou_0_5: "Over/Under 0.5",
    ou_1_5: "Over/Under 1.5",
    ou_2_5: "Over/Under 2.5",
    ou_3_5: "Over/Under 3.5",
    ou_4_5: "Over/Under 4.5",
    ou_5_5: "Over/Under 5.5",
    team_ou_goals: "Team Goals O/U",
    ht_ft: "Half-Time/Full-Time",
    corners: "Corners",
    cards: "Cards",
    fouls: "Fouls",
    throw_ins: "Throw-Ins",
  };
  return marketLabels[marketKey] ?? marketKey;
}

function selectionLabel(selection: string) {
  return selection
    .replaceAll("_", " ")
    .replace(/\b\w/g, (character) => character.toUpperCase());
}

function toPercent(value: number) {
  return `${(value * 100).toFixed(1)}%`;
}

export async function fetchLivePredictionRows(limit = 100): Promise<LivePredictionRow[]> {
  const baseUrl = predictionApiBaseUrl().replace(/\/$/, "");
  const response = await fetch(`${baseUrl}/v1/predictions/today`, {
    cache: "no-store",
  });
  if (!response.ok) {
    return [];
  }

  const payload = (await response.json()) as TodayPredictionsResponse;
  const enginePredictions = payload.predictions ?? [];
  const providerPredictions = payload.provider_predictions ?? [];
  const providerLimit = providerPredictions.length > 0
    ? Math.min(providerPredictions.length, Math.max(1, Math.floor(limit * 0.2)))
    : 0;
  const combined = [
    ...enginePredictions.slice(0, Math.max(0, limit - providerLimit)),
    ...providerPredictions.slice(0, providerLimit),
  ];
  return combined.map((prediction) => {
    const matchID = Number(prediction.match_id);
    const confidence = Number(prediction.confidence);
    const version = Number(prediction.published_version);
    return {
      match: prediction.match_title || (Number.isFinite(matchID) ? `Match #${matchID}` : "Match"),
      market: marketLabel(String(prediction.market_key ?? "")),
      pick: selectionLabel(String(prediction.selection ?? "")),
      confidence: Number.isFinite(confidence) ? `${Math.round(confidence)}%` : "-",
      status: prediction.source === "provider" ? "provider fallback" : "published",
      model: prediction.source === "provider" ? "provider" : "SokaLens engine",
      inputVersion: "live",
      predictionVersion: Number.isFinite(version) ? `v${version}` : "-",
      settlement: "pending",
    };
  });
}

export async function fetchLivePredictionStats(): Promise<{
  rows: LivePredictionStatRow[];
  settled: number;
  source: string;
}> {
  const token = process.env.ADMIN_API_BEARER_TOKEN?.trim();
  if (!token) {
    return {
      rows: [],
      settled: 0,
      source: "Fallback sample data (ADMIN_API_BEARER_TOKEN not set)",
    };
  }

  const baseUrl = predictionApiBaseUrl().replace(/\/$/, "");
  const response = await fetch(`${baseUrl}/v1/admin/predictions/hit-rate`, {
    headers: { Authorization: `Bearer ${token}` },
    cache: "no-store",
  });

  if (!response.ok) {
    return {
      rows: [],
      settled: 0,
      source: `Fallback sample data (live status ${response.status})`,
    };
  }

  const payload = (await response.json()) as PredictionHitRateResponse;
  const rows = (payload.markets ?? []).map((market) => ({
    market: marketLabel(String(market.market_key ?? "")),
    published: Number(market.settled ?? 0),
    review: 0,
    hitRate: toPercent(Number(market.hit_rate ?? 0)),
    drift: "-",
  }));

  return {
    rows,
    settled: Number(payload.settled ?? 0),
    source: "Live hit-rate API",
  };
}

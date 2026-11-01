import { NextRequest, NextResponse } from "next/server";
import { fetchPredictionApi } from "../../data/apiClient";

export async function GET(request: NextRequest) {
  if (process.env.ACCA_INVENTORY_V2_ENABLED !== "true" || (process.env.ACCA_INVENTORY_READ_SOURCE ?? "v2") !== "v2") return NextResponse.json({ error: "ACCA inventory unavailable" }, { status: 503 });
  const params = new URLSearchParams();
  for (const key of ["tab", "market", "selection", "period", "country", "league", "legs", "min_fair_odds", "max_fair_odds", "cursor", "limit", "seed", "generate"]) {
    const value = request.nextUrl.searchParams.get(key);
    if (value) params.set(key === "market" ? "tab" : key, value);
  }
  if (!params.has("limit")) params.set("limit", "10");
  try {
    const upstream = await fetchPredictionApi(`/v1/accas?${params}`, { revalidate: 15 });
    return new NextResponse(await upstream.text(), { status: upstream.status, headers: { "Content-Type": "application/json", "Cache-Control": "public, s-maxage=15, stale-while-revalidate=30" } });
  } catch {
    return NextResponse.json({ error: "ACCA feed temporarily unavailable" }, { status: 502 });
  }
}

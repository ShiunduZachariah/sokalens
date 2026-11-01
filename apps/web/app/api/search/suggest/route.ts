import { NextResponse } from "next/server";
import { predictionApiBaseUrl } from "../../../data/apiClient";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

// Same-origin proxy for public search suggestions so the browser never talks
// to the Go API directly (no CORS, no public API base URL, no provider call).
export async function GET(request: Request) {
  const url = new URL(request.url);
  const query = url.searchParams.get("q")?.trim() ?? "";
  if (query.length < 3) {
    return NextResponse.json({ results: [] });
  }
  const limitParam = url.searchParams.get("limit") ?? "8";
  const limit = Math.min(20, Math.max(1, Number.parseInt(limitParam, 10) || 8));

  // No `include_recommendations`. It adds two joins across engine_predictions
  // and the ACCA inventory and measured at +250-350ms on production — five
  // times the cost of the entity lookup itself, on the one request that has to
  // keep up with typing. The /search page still asks for them; a dropdown of
  // four rows does not need a market and a slip in it.
  const target = `${predictionApiBaseUrl()}/v1/search/suggest?q=${encodeURIComponent(query)}&limit=${limit}`;
  try {
    const upstream = await fetch(target, {
      headers: { Accept: "application/json" },
      signal: AbortSignal.timeout(2_500),
    });
    if (!upstream.ok) {
      return NextResponse.json({ results: [] });
    }
    const payload = await upstream.json();
    return NextResponse.json(payload);
  } catch {
    return NextResponse.json({ results: [] });
  }
}

import { NextRequest, NextResponse } from "next/server";
import { accountProxyHeaders } from "../../../account/proxyHeaders";
import { predictionApiBaseUrl } from "../../../../data/apiClient";
import { copyUpstreamCookies } from "../../proxyResponse";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export async function GET(
  request: NextRequest,
  context: { params: Promise<{ placement: string }> },
) {
  const { placement } = await context.params;
  if (!/^[a-z0-9][a-z0-9_-]{0,79}$/.test(placement)) {
    return NextResponse.json({ code: "invalid_placement" }, { status: 400 });
  }
  try {
    const upstream = await fetch(
      `${predictionApiBaseUrl()}/v1/ads/placements/${encodeURIComponent(placement)}`,
      {
        cache: "no-store",
        headers: accountProxyHeaders(request.headers),
        redirect: "manual",
        signal: AbortSignal.timeout(2_800),
      },
    );
    const response = new NextResponse(await upstream.arrayBuffer(), { status: upstream.status });
    response.headers.set("Cache-Control", "private, no-store");
    response.headers.set("Content-Type", upstream.headers.get("content-type") ?? "application/json");
    copyUpstreamCookies(upstream.headers, response.headers);
    return response;
  } catch {
    return NextResponse.json(
      { code: "ads_api_unavailable" },
      { status: 503, headers: { "Cache-Control": "private, no-store" } },
    );
  }
}

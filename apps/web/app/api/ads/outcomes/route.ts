import { NextRequest, NextResponse } from "next/server";
import { adFailureReasons, type AdFailureReason } from "../../../lib/adDelivery";
import { predictionApiBaseUrl } from "../../../data/apiClient";
import { accountProxyHeaders } from "../../account/proxyHeaders";
import { copyUpstreamCookies } from "../proxyResponse";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

type OutcomeBody = {
  attempt_token: string;
  duration_ms: number;
  failure_reason?: AdFailureReason;
  outcome: "success" | "failure";
};

function validBody(value: unknown): value is OutcomeBody {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const body = value as Record<string, unknown>;
  if (typeof body.attempt_token !== "string" || body.attempt_token.length > 2048 ||
      !/^[A-Za-z0-9._~-]+$/.test(body.attempt_token)) return false;
  if (!Number.isInteger(body.duration_ms) || Number(body.duration_ms) < 0 || Number(body.duration_ms) > 60_000) return false;
  if (body.outcome === "success") return body.failure_reason === undefined;
  return body.outcome === "failure" && typeof body.failure_reason === "string" &&
    adFailureReasons.has(body.failure_reason as AdFailureReason);
}

export async function POST(request: NextRequest) {
  let body: unknown;
  try {
    if (Number(request.headers.get("content-length") ?? "0") > 4_096) throw new Error("too_large");
    body = await request.json();
  } catch {
    return NextResponse.json({ code: "invalid_outcome" }, { status: 400 });
  }
  if (!validBody(body)) return NextResponse.json({ code: "invalid_outcome" }, { status: 400 });

  try {
    const headers = accountProxyHeaders(request.headers);
    headers.set("Content-Type", "application/json");
    const upstream = await fetch(`${predictionApiBaseUrl()}/v1/ads/outcomes`, {
      body: JSON.stringify(body),
      cache: "no-store",
      headers,
      method: "POST",
      redirect: "manual",
      signal: AbortSignal.timeout(2_800),
    });
    const responseBody = upstream.status === 204 ? null : await upstream.arrayBuffer();
    const response = new NextResponse(responseBody, { status: upstream.status });
    response.headers.set("Cache-Control", "private, no-store");
    if (responseBody) response.headers.set("Content-Type", upstream.headers.get("content-type") ?? "application/json");
    copyUpstreamCookies(upstream.headers, response.headers);
    return response;
  } catch {
    return NextResponse.json(
      { code: "ads_api_unavailable" },
      { status: 503, headers: { "Cache-Control": "private, no-store" } },
    );
  }
}

import { NextRequest, NextResponse } from "next/server";
import { predictionApiBaseUrl } from "../../../data/apiClient";
import { accountProxyHeaders } from "../proxyHeaders";
import { AccountBodyError, readAccountBody } from "../requestBody";
import { nullBodyStatus } from "../../../lib/nullBodyStatus";

export const dynamic = "force-dynamic";
export const runtime = "nodejs";

const allowedMethods = new Set(["DELETE", "GET", "PATCH", "POST", "PUT"]);
const passwordHashingPaths = new Set([
  "/v1/auth/register",
  "/v1/auth/login",
  "/v1/auth/password-reset/confirm",
  "/v1/me/password",
]);

function isAllowedPath(path: string) {
  return (
    path.startsWith("/v1/auth/") ||
    path === "/v1/access" ||
    path === "/v1/access/context" ||
    path === "/v1/best-bets" ||
    // The bounded in-play JSON reads, including one fixture's current market
    // publication. The SSE stream has its own route because this proxy reads
    // each upstream body into a buffer and has a short timeout.
    path === "/v1/inplay/overview" ||
    path === "/v1/inplay/changes" ||
    /^\/v1\/inplay\/fixtures\/[1-9]\d*$/.test(path) ||
    // One fixture's probability graph. Anchored the same way as the line above
    // rather than loosened to a prefix: this list is an allowlist, and
    // `startsWith("/v1/inplay/fixtures/")` would forward anything the API ever
    // mounts under a fixture, including routes written later with different
    // assumptions about who may call them.
    /^\/v1\/inplay\/fixtures\/[1-9]\d*\/series$/.test(path) ||
    /^\/v1\/analysis\/\d+$/.test(path) ||
    path === "/v1/plans" ||
    path === "/v1/growth/events" ||
    path === "/v1/share-events" ||
    path === "/v1/checkout" ||
    path === "/v1/checkout-sessions" ||
    path === "/v1/recommendations" ||
    path.startsWith("/v1/recommendations/") ||
    path === "/v1/me" ||
    path.startsWith("/v1/me/")
  );
}

async function proxy(request: NextRequest, context: { params: Promise<{ path: string[] }> }) {
  const method = request.method.toUpperCase();
  if (!allowedMethods.has(method)) {
    return NextResponse.json({ message: "Method not allowed." }, { status: 405 });
  }

  const { path: segments } = await context.params;
  if (segments.some((segment) => segment === "." || segment === ".." || !/^[A-Za-z0-9:._~-]+$/.test(segment))) {
    return NextResponse.json({ message: "Account route not found." }, { status: 404 });
  }
  const path = `/${segments.join("/")}`;
  if (!isAllowedPath(path)) {
    return NextResponse.json({ message: "Account route not found." }, { status: 404 });
  }

  const target = new URL(`${predictionApiBaseUrl()}${path}`);
  request.nextUrl.searchParams.forEach((value, key) => target.searchParams.append(key, value));

  const headers = accountProxyHeaders(request.headers);

  try {
    const upstream = await fetch(target, {
      body: method === "GET" ? undefined : await readAccountBody(request),
      cache: "no-store",
      headers,
      method,
      redirect: "manual",
      signal: AbortSignal.timeout(passwordHashingPaths.has(path) ? 8_500 : 2_800),
    });
    // A 204/205/304 may not carry a body: handing one to NextResponse throws,
    // the catch below turns that into a 503, and the caller is told its
    // request failed when the upstream actually succeeded. That is what made
    // removing a favorite -- the only DELETE the web app issues, and the one
    // that answers 204 -- report "Account services are temporarily
    // unavailable" every single time while the row was in fact deleted.
    const body = nullBodyStatus(upstream.status) ? null : await upstream.arrayBuffer();
    const response = new NextResponse(body, {
      status: upstream.status,
    });
    response.headers.set("Cache-Control", "no-store, private");
    response.headers.set("Content-Type", upstream.headers.get("content-type") ?? "application/json");

    const upstreamHeaders = upstream.headers as Headers & { getSetCookie?: () => string[] };
    const cookies = upstreamHeaders.getSetCookie?.() ?? [];
    if (cookies.length > 0) {
      cookies.forEach((cookie) => response.headers.append("Set-Cookie", cookie));
    } else {
      const cookie = upstream.headers.get("set-cookie");
      if (cookie) response.headers.append("Set-Cookie", cookie);
    }
    return response;
  } catch (error) {
    if (error instanceof AccountBodyError) {
      return NextResponse.json(
        { code: "invalid_account_body", message: error.message },
        { status: error.status, headers: { "Cache-Control": "no-store, private" } },
      );
    }
    return NextResponse.json(
      { code: "account_api_unavailable", message: "Account services are temporarily unavailable." },
      { status: 503, headers: { "Cache-Control": "no-store, private" } },
    );
  }
}

export const DELETE = proxy;
export const GET = proxy;
export const PATCH = proxy;
export const POST = proxy;
export const PUT = proxy;

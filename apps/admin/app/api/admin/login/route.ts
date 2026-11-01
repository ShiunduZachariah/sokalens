import { NextRequest, NextResponse } from "next/server";

const apiBase = () => process.env.ADMIN_API_BASE_URL?.trim() || "http://127.0.0.1:8080";

export async function POST(request: NextRequest) {
  const origin = request.headers.get("origin");
  if (origin && new URL(origin).host !== request.headers.get("host")) {
    return NextResponse.json({ error: "invalid_origin" }, { status: 403 });
  }
  const body = await request.text();
  if (body.length > 16_384) {
    return NextResponse.json({ error: "payload_too_large" }, { status: 413 });
  }
  const upstream = await fetch(`${apiBase()}/v1/admin/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body,
    cache: "no-store",
    signal: AbortSignal.timeout(2500),
  }).catch(() => null);
  if (!upstream?.ok) {
    if (!upstream) {
      return NextResponse.json({ error: "admin_auth_unavailable" }, { status: 503 });
    }
    if (upstream.status === 429) {
      return NextResponse.json({ error: "admin_auth_rate_limited" }, { status: 429 });
    }
    if (upstream.status >= 500) {
      return NextResponse.json({ error: "admin_auth_unavailable" }, { status: 503 });
    }
    return NextResponse.json({ error: "invalid_admin_credentials" }, { status: 401 });
  }
  const result = (await upstream.json()) as { token?: string; expires_at?: string };
  if (!result.token) {
    return NextResponse.json({ error: "invalid_admin_session" }, { status: 502 });
  }
  const response = NextResponse.json({ authenticated: true });
  response.cookies.set("sokalens_admin_session", result.token, {
    httpOnly: true,
    secure: process.env.NODE_ENV === "production",
    sameSite: "strict",
    path: "/",
    expires: result.expires_at ? new Date(result.expires_at) : undefined,
  });
  return response;
}

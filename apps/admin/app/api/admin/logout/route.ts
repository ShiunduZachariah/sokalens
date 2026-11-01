import { cookies, headers } from "next/headers";
import { NextResponse } from "next/server";

const apiBase = () => process.env.ADMIN_API_BASE_URL?.trim() || "http://127.0.0.1:8080";

export async function POST() {
  const requestHeaders = await headers();
  const origin = requestHeaders.get("origin");
  if (origin && new URL(origin).host !== requestHeaders.get("host")) {
    return NextResponse.json({ error: "invalid_origin" }, { status: 403 });
  }
  const jar = await cookies();
  const token = jar.get("sokalens_admin_session")?.value;
  if (token) {
    await fetch(`${apiBase()}/v1/admin/auth/logout`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}` },
      cache: "no-store",
      signal: AbortSignal.timeout(2500),
    }).catch(() => null);
  }
  const forwardedHost = requestHeaders.get("x-forwarded-host") || requestHeaders.get("host") || "localhost:3001";
  const forwardedProto = requestHeaders.get("x-forwarded-proto") || (forwardedHost.startsWith("localhost") ? "http" : "https");
  const publicURL = process.env.ADMIN_PUBLIC_URL || `${forwardedProto}://${forwardedHost}`;
  const response = NextResponse.redirect(new URL("/login", publicURL), 303);
  response.cookies.set("sokalens_admin_session", "", { httpOnly: true, sameSite: "strict", path: "/", maxAge: 0 });
  return response;
}

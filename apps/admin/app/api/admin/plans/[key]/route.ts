import { cookies, headers } from "next/headers";
import { NextRequest, NextResponse } from "next/server";

const apiBase = () => process.env.ADMIN_API_BASE_URL?.trim() || "http://127.0.0.1:8080";

export async function PUT(request: NextRequest, context: { params: Promise<{ key: string }> }) {
  const requestHeaders = await headers();
  const origin = requestHeaders.get("origin");
  if (origin && new URL(origin).host !== requestHeaders.get("host")) return NextResponse.json({ error: "invalid_origin" }, { status: 403 });
  const token = (await cookies()).get("sokalens_admin_session")?.value;
  if (!token) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  const { key } = await context.params;
  if (!/^pro_(monthly|yearly)$/.test(key)) return NextResponse.json({ error: "not_found" }, { status: 404 });
  const body = await request.text();
  if (body.length > 32_768) return NextResponse.json({ error: "payload_too_large" }, { status: 413 });
  const upstream = await fetch(`${apiBase()}/v1/admin/plans/${key}`, { method: "PUT", headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" }, body, cache: "no-store", signal: AbortSignal.timeout(2500) }).catch(() => null);
  return new NextResponse(upstream?.body ?? null, { status: upstream?.status ?? 503, headers: { "Content-Type": "application/json" } });
}

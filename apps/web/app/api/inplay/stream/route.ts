import { NextRequest, NextResponse } from "next/server";
import { accountProxyHeaders } from "../../account/proxyHeaders";
import { predictionApiBaseUrl } from "../../../data/apiClient";

/**
 * The in-play event stream, proxied.
 *
 * `/api/account/[...path]` cannot carry this: it aborts at 2.8 seconds and hands
 * the upstream body to `NextResponse` in one piece, which is the opposite of what
 * an SSE connection needs. So the stream gets its own route, and the account
 * proxy's allowlist deliberately excludes it — a test asserts that, because
 * "fixing" it by adding the path there would produce a stream that dies after
 * under three seconds and looks like a flaky network.
 *
 * ## Why a stream at all, when 15-second polling is equally fresh
 *
 * It is not about freshness. The provider refreshes `/fixtures` every 15 seconds,
 * so polling at 15 seconds sees everything a stream sees. It is about request
 * volume: a poll is four requests a minute per open match page, and at the
 * concurrency this platform is being built for that is the difference between
 * tens of thousands of requests a second and the same number of held connections.
 * Polling remains the path that always works and the client falls back to it on any
 * failure, because an intermediary that buffers or strips SSE is a real
 * possibility we do not control.
 *
 * ## What this route must not do
 *
 * **No timeout.** The contract bounds connections server-side (30 minutes by
 * default) and announces `event: reconnect` with the cursor before closing, so a
 * deliberate close is distinguishable from a network drop. A timeout here would
 * turn every clean handover into an indistinguishable drop.
 *
 * **No buffering.** The upstream body is passed through untouched. Reading it into
 * memory first would hold every frame until the connection closed, which is the
 * one failure mode that looks exactly like the stream working and then not.
 *
 * **No body transformation.** The `id:` of each frame IS the cursor a client
 * resumes from. Rewriting frames risks changing it.
 */
export const dynamic = "force-dynamic";
export const runtime = "nodejs";

export async function GET(request: NextRequest) {
  const target = new URL(`${predictionApiBaseUrl()}/v1/inplay/stream`);
  // `since` seeds the first request. A valid `Last-Event-ID` takes precedence
  // on automatic reconnect, where EventSource reuses that initial URL.
  const since = request.nextUrl.searchParams.get("since");
  if (since) target.searchParams.set("since", since);

  const headers = accountProxyHeaders(request.headers);
  headers.set("Accept", "text/event-stream");
  const lastEventId = request.headers.get("last-event-id");
  if (lastEventId) headers.set("Last-Event-ID", lastEventId);

  let upstream: Response;
  try {
    upstream = await fetch(target, {
      cache: "no-store",
      headers,
      // Tied to the client's own connection: when the reader navigates away, the
      // abort propagates upstream instead of leaving a stream nobody is reading.
      signal: request.signal,
      redirect: "manual",
    });
  } catch {
    // The client falls back to polling on any failure, so a 503 here is a
    // complete answer rather than a dead end.
    return NextResponse.json({ code: "inplay_stream_unavailable" }, { status: 503 });
  }

  if (!upstream.ok || !upstream.body) {
    // Preserve upstream availability errors so the public client can fall back
    // to polling without disguising a failure as an open stream.
    return NextResponse.json({ code: "inplay_stream_unavailable" }, { status: upstream.status || 503 });
  }

  return new NextResponse(upstream.body, {
    headers: {
      "Cache-Control": "private, no-cache, no-store, no-transform",
      // `no-transform` above and this below are the two that matter for an
      // intermediary: without them a proxy may buffer the stream into a single
      // response, which presents as a connection that opens and then says nothing.
      "Content-Type": upstream.headers.get("content-type") ?? "text/event-stream",
      "X-Accel-Buffering": "no",
      Connection: "keep-alive",
    },
    status: 200,
  });
}

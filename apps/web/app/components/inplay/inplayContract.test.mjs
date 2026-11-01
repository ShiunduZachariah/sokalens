import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import path from "node:path";

const appDir = path.resolve(import.meta.dirname, "../..");
const read = (relative) => readFile(path.join(appDir, relative), "utf8");

/*
 * The in-play client's side of `docs/integrations/in-play-data-contract.md`.
 *
 * Every rule below fails silently if broken. A board reading the retained
 * `probability` float instead of `probability_bp` renders plausible numbers that
 * disagree with Flutter by a point. A board presenting stale figures as current
 * looks correct and is wrong. A board that drops locked markets looks like a
 * shorter market list, not like a bug. None of this shows up in a screenshot, so
 * it is asserted here.
 */

test("probabilities are read as basis points, never as the retained float", async () => {
  const data = await read("data/inplay.ts");
  const board = await read("components/inplay/InPlayMarkets.tsx");

  assert.match(data, /probability_bp/, "the parser no longer reads probability_bp");

  // The float is retained on the wire only for the existing client. Reading it
  // reintroduces exactly the rounding divergence basis points exist to prevent,
  // so the parser must refuse a market that has no basis points rather than
  // falling back.
  assert.match(
    data,
    /if \(!selection \|\| probabilityBp === undefined \|\| !Number\.isFinite\(probabilityBp\)\) return undefined;/,
    "a market without probability_bp is no longer rejected; it may now fall back to the float",
  );
  assert.ok(
    !/\bprobability\b(?!_bp)/.test(board),
    "the board reads the `probability` float; read probabilityBp",
  );
});

test("stale is never presented as current, and suspended shows no figures", async () => {
  const data = await read("data/inplay.ts");
  const board = await read("components/inplay/InPlayMarkets.tsx");

  // An unrecognised health must degrade to stale, not to ok: guessing upward
  // presents an unknown state as current, which is the one reading the contract
  // forbids.
  assert.match(
    data,
    /MODEL_HEALTHS\.has\(value\) \? \(value as ModelHealth\) : "stale"/,
    "an unknown model_health no longer degrades to stale",
  );

  // Suspended withholds every probability, enforced at the one place that draws
  // them rather than by remembering to check at each call site.
  assert.match(board, /const showFigures = health !== "suspended";/, "suspended no longer withholds figures");
  assert.match(board, /!showFigures/, "showFigures is computed but never applied");
  assert.match(board, /HEALTH_NOTE/, "the health states are no longer explained to the reader");
});

test("the cursor comes from the payload and a gap is not a lost message", async () => {
  const data = await read("data/inplay.ts");
  const board = await read("components/inplay/InPlayMarkets.tsx");

  // The server may truncate the lists, so a cursor computed from them would
  // re-request what was already applied, forever.
  assert.match(data, /cursor: readNumber\(root, "cursor"\) \?\? 0/, "the cursor is no longer read from the payload");
  assert.match(board, /cursor\.current = changes\.cursor/, "the board no longer advances to the payload's cursor");

  // `more` means the limit truncated the set: ask again immediately rather than
  // waiting out the interval and falling further behind.
  assert.match(board, /if \(changes\.more/, "a truncated changeset no longer triggers an immediate re-poll");
});

test("a legacy locked market keeps its row without inventing a figure", async () => {
  const board = await read("components/inplay/InPlayMarkets.tsx");

  // A legacy upstream may still withhold a market. Public access must not
  // fabricate its probability or silently remove its row.
  assert.ok(
    !/\.filter\([^)]*!\s*\w+\.locked/.test(board),
    "locked markets are filtered out rather than shown as unavailable",
  );
  assert.match(board, /iv-line--locked/, "a locked row no longer has its own treatment");
});

// Standing snapshot preservation is exercised by inplayStream.test.mjs.

test("the polling interval matches the provider's own refresh", async () => {
  const board = await read("components/inplay/InPlayMarkets.tsx");
  // 15s is the provider's refresh for /fixtures and /fixtures/events, confirmed
  // in the scale plan. Polling faster returns identical bytes and spends quota.
  assert.match(board, /const POLL_MS = 15_000;/, "the poll interval no longer matches the 15s provider refresh");
});

test("the in-play endpoints are reachable and the stream is not proxied", async () => {
  const proxy = await read("api/account/[...path]/route.ts");

  assert.match(proxy, /path === "\/v1\/inplay\/overview"/, "the overview endpoint is not allowed through the proxy");
  assert.match(proxy, /path === "\/v1\/inplay\/changes"/, "the changes endpoint is not allowed through the proxy");

  // The proxy aborts at 2.8s and hands the upstream body to NextResponse whole,
  // which an SSE connection cannot survive. Allowing it here would produce a
  // stream that dies after under three seconds and looks like a flaky network.
  assert.ok(
    !/\/v1\/inplay\/stream/.test(proxy),
    "the SSE stream is allowed through a proxy that aborts at 2.8s; it needs its own streaming route",
  );
});

test("the stream route neither times out nor buffers", async () => {
  const route = await read("api/inplay/stream/route.ts");

  // A timeout turns every clean 30-minute handover -- which the contract announces
  // with `event: reconnect` -- into an indistinguishable network drop.
  assert.ok(
    !/AbortSignal\.timeout/.test(route),
    "the stream route has a timeout; the contract bounds connections server-side and announces the close",
  );
  // Tied to the client's own connection instead, so navigating away propagates.
  assert.match(route, /signal: request\.signal/, "an abandoned stream is not cancelled upstream");

  // The body must be handed to NextResponse untouched. Reading it first holds
  // every frame until the connection closes, which looks exactly like a stream
  // that worked and then stopped.
  assert.match(route, /new NextResponse\(upstream\.body/, "the stream route no longer passes the body through");
  assert.ok(
    !/await upstream\.(text|json|arrayBuffer)\(\)/.test(route),
    "the stream route reads the body into memory, which buffers every frame",
  );

  // Two headers an intermediary reads. Without them a proxy may coalesce the
  // stream into one response: the connection opens and then says nothing.
  assert.match(route, /"X-Accel-Buffering": "no"/);
  assert.match(route, /no-transform/);
});

test("the client falls back to polling and never back again", async () => {
  const board = await read("components/inplay/InPlayMarkets.tsx");

  // An intermediary that strips or buffers SSE is a real possibility nobody here
  // controls, so polling is the path that always works. A client that kept retrying a
  // stream it cannot have would sit showing a stale board.
  assert.match(board, /setTransport\("poll"\)/, "there is no fallback to polling");
  assert.ok(
    !/setTransport\("stream"\)/.test(board.slice(board.indexOf("const [transport"))),
    "the client switches back to the stream after falling back; it must stay fallen back",
  );

  // A failure AFTER frames have arrived is a transient drop: EventSource
  // reconnects by itself with Last-Event-ID. Only a failure before the first frame
  // means the transport is unavailable here.
  assert.match(board, /if \(!delivered\)/, "any stream error now triggers a permanent fallback");

  // One reducer for both transports. Two would be the obvious place for the paths
  // to drift, and they share one server-side query precisely so they cannot.
  assert.match(board, /const apply = useCallback/, "the transports no longer share one reducer");
});

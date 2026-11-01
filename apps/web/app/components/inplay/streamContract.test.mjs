import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

/**
 * The SSE event name is a contract across two languages, and it was broken.
 *
 * Go writes `event: changes`; the browser listened for `message`. Under the SSE
 * spec a named event is delivered only to listeners registered for that name --
 * `message` receives frames sent WITHOUT a name -- so every frame was dropped.
 *
 * It hid behind a second defect. The endpoint answered 501 because it was
 * wrapped in http.TimeoutHandler, so EventSource raised `error` before any
 * frame arrived and the client fell back to polling, which works. Users saw
 * live updates and nothing looked wrong.
 *
 * That is why this guard reads BOTH files. Fixing either side alone is worse
 * than fixing neither: with the stream connecting and the listener still wrong,
 * frames arrive, nothing handles them, and no `error` fires -- so the polling
 * fallback never triggers and the board goes quietly stale.
 */

const client = await readFile(new URL("./InPlayMarkets.tsx", import.meta.url), "utf8");
const server = await readFile(
  new URL("../../../../../services/api/internal/httpserver/inplay_stream.go", import.meta.url),
  "utf8",
);

function serverEventNames(source) {
  return [...source.matchAll(/event:\s*([a-z_]+)\\n/g)].map((m) => m[1]);
}

test("the browser listens for the event name the server actually writes", () => {
  const names = serverEventNames(server);
  assert.ok(names.includes("changes"),
    "inplay_stream.go no longer writes an `event: changes` frame; this contract has moved");

  for (const name of new Set(names)) {
    // `error` is also EventSource's own connection-failure event, which the
    // client already handles; the rest must be listened for by name.
    if (name === "error") continue;
    assert.match(client, new RegExp(`addEventListener\\("${name}"`),
      `the server sends \`event: ${name}\` but InPlayMarkets.tsx never listens for it, `
        + "so those frames are discarded");
  }
});

test("the client does not rely on the default message event", () => {
  assert.doesNotMatch(client, /addEventListener\("message"/,
    "the server names every frame, so a `message` listener receives nothing");
});

test("a stream that never delivers still falls back to polling", () => {
  // The safety net that made the original defect survivable. Losing it turns
  // any future stream regression into a silently stale board.
  assert.match(client, /setTransport\("poll"\)/,
    "the polling fallback is gone; a broken stream would now show stale data with no error");
  assert.match(client, /if \(!delivered\)/,
    "the fallback no longer distinguishes a connection that never delivered a frame");
});

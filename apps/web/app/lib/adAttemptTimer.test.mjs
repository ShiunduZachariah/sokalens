import assert from "node:assert/strict";
import test from "node:test";
import { createAdAttemptTimer } from "./adAttemptTimer.ts";

function fakeClock() {
  let now = 0;
  let nextId = 0;
  const tasks = new Map();
  return {
    timer: {
      set(callback, delayMs) {
        const id = ++nextId;
        tasks.set(id, { callback, at: now + delayMs });
        return id;
      },
      clear(id) { tasks.delete(id); },
    },
    advance(ms) {
      const end = now + ms;
      while (true) {
        const next = [...tasks].sort((a, b) => a[1].at - b[1].at)[0];
        if (!next || next[1].at > end) break;
        now = next[1].at;
        tasks.delete(next[0]);
        next[1].callback();
      }
      now = end;
    },
  };
}

test("a banner queued behind another gets its full fill budget when it starts", () => {
  const clock = fakeClock();
  let timedOut = 0;
  const queued = createAdAttemptTimer(5_000, () => timedOut++, clock.timer);

  clock.advance(8_000); // Earlier banners occupy the shared script queue.
  assert.equal(timedOut, 0);
  assert.equal(queued.start(), true);
  clock.advance(4_999);
  assert.equal(timedOut, 0);
  clock.advance(1);
  assert.equal(timedOut, 1);
  assert.equal(queued.start(), false);
});

test("settling or cancelling an attempt prevents its pending timeout", () => {
  const clock = fakeClock();
  let timedOut = 0;
  const attempt = createAdAttemptTimer(5_000, () => timedOut++, clock.timer);
  attempt.start();
  clock.advance(3_000);
  attempt.stop();
  clock.advance(10_000);
  assert.equal(timedOut, 0);
  assert.equal(attempt.start(), false);
});

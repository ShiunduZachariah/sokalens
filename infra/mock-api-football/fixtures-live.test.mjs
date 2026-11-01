import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

/**
 * Guards the mock's `fixtures?live=all` payload.
 *
 * This exists because the payload once served three fixtures with status `FT`.
 * Nothing failed. The live poll ran, events were written, the smoke test
 * passed, and `fixture_live_snapshots` stayed empty for days -- because the
 * snapshot writer skips any fixture that is not in play, correctly, and a
 * finished match is not in play. The mock was wrong and everything downstream
 * was right, which is the hardest shape of bug to see.
 *
 * Real API-Football returns only matches on the pitch from `live=all`. A mock
 * that returns finished ones is not a simplification, it is a different
 * endpoint wearing the same name.
 */

const payload = JSON.parse(
  readFileSync(new URL("./fixtures/fixtures-live.json", import.meta.url), "utf8"),
);

// The provider short codes that mean "happening right now". Kept in step with
// inPlayProviderStatuses in services/worker/internal/ingestion/fixture_jobs.go.
const IN_PLAY = new Set(["1H", "HT", "2H", "ET", "BT", "P", "LIVE"]);

test("every fixture in the live payload is actually in play", () => {
  assert.ok(Array.isArray(payload.response) && payload.response.length > 0, "payload has no fixtures");
  for (const fixture of payload.response) {
    const short = fixture?.fixture?.status?.short;
    assert.ok(
      IN_PLAY.has(short),
      `fixture ${fixture?.fixture?.id} has status ${short}; live=all returns only in-play matches, ` +
        `and a non-in-play status silently stops the snapshot writer without failing anything`,
    );
  }
});

test("the payload still carries the events the timeline is built from", () => {
  // The events ride inside this same response, which is why the timeline costs
  // no extra provider request. A payload without them exercises nothing.
  const total = payload.response.reduce((sum, fixture) => sum + (fixture.events?.length ?? 0), 0);
  assert.ok(total > 0, "no events in the live payload");
});

test("the payload spans both halves so the period split is exercised", () => {
  const minutes = payload.response.flatMap((fixture) =>
    (fixture.events ?? []).map((event) => event?.time?.elapsed).filter((m) => Number.isFinite(m)),
  );
  assert.ok(
    minutes.some((m) => m <= 45) && minutes.some((m) => m > 45),
    "every event is in one half; the first/second half split renders as a single group",
  );
});

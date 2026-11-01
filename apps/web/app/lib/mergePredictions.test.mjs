import assert from "node:assert/strict";
import test from "node:test";

import { mergeByMatchId } from "./mergePredictions.ts";

const engineRow = (matchId, kickoffAt) => ({ id: matchId, matchId, kickoffAt });
const providerRow = (matchId, kickoffAt) => ({ matchId, kickoffAt });
const convert = (row) => ({ id: -Math.abs(row.matchId), matchId: row.matchId, kickoffAt: row.kickoffAt });

test("one row per fixture, and the engine row wins", () => {
  const merged = mergeByMatchId(
    [engineRow(1, "2026-09-04T18:00:00Z")],
    [providerRow(1, "2026-09-04T18:00:00Z"), providerRow(2, "2026-09-04T19:00:00Z")],
    convert,
  );

  assert.equal(merged.length, 2);
  // Provider fallbacks carry a synthesized negative id; a positive id here
  // proves the engine row survived rather than being overwritten.
  assert.ok(merged.find((row) => row.matchId === 1).id > 0);
  assert.ok(merged.find((row) => row.matchId === 2).id < 0);
});

test("duplicate engine rows for one fixture collapse to the first", () => {
  const merged = mergeByMatchId(
    [engineRow(1, "2026-09-04T18:00:00Z"), engineRow(1, "2026-09-04T18:00:00Z")],
    [],
    convert,
  );

  assert.equal(merged.length, 1);
});

test("sorted by kickoff ascending", () => {
  const merged = mergeByMatchId(
    [
      engineRow(3, "2026-09-04T19:00:00Z"),
      engineRow(1, "2026-09-04T12:00:00Z"),
      engineRow(2, "2026-09-04T15:00:00Z"),
    ],
    [providerRow(4, "2026-09-04T13:00:00Z")],
    convert,
  );

  assert.deepEqual(merged.map((row) => row.matchId), [1, 4, 2, 3]);
});

test("an unparseable kickoff does not throw or drop the row", () => {
  const merged = mergeByMatchId([engineRow(1, "not-a-date"), engineRow(2, "2026-09-04T12:00:00Z")], [], convert);

  assert.equal(merged.length, 2);
});

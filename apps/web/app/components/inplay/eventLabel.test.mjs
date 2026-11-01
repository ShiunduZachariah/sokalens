import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import path from "node:path";
import ts from "typescript";

/**
 * `eventLabel` turns a provider incident into the words under the marker.
 *
 * The component is a client component with React imports, so the function is
 * transpiled and evaluated on its own rather than importing the module.
 */
const source = await readFile(
  path.join(import.meta.dirname, "WinProbabilityChart.tsx"),
  "utf8",
);
const fn = source.match(/export function eventLabel\([\s\S]*?\n}/);
assert.ok(fn, "eventLabel not found -- has it been renamed or inlined?");

const eventLabel = new Function(
  `${ts.transpileModule(fn[0].replace(/^export /, ""), {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText}; return eventLabel;`,
)();

test("a plain goal does not repeat itself", () => {
  // The provider writes "Normal Goal" for the ordinary case. Appending it gives
  // "Goal (Normal Goal)", which tells a reader nothing and reads as a bug.
  assert.equal(
    eventLabel({ type: "Goal", detail: "Normal Goal", player: "D. Silva" }),
    "Goal — D. Silva",
  );
});

test("a detail that changes the meaning is kept", () => {
  // A penalty and an own goal are materially different from a goal, and a
  // straight red is different from a second booking.
  assert.equal(
    eventLabel({ type: "Goal", detail: "Penalty", player: "M. Bolivar" }),
    "Goal (Penalty) — M. Bolivar",
  );
  assert.equal(
    eventLabel({ type: "Goal", detail: "Own Goal", player: "A. Smith" }),
    "Goal (Own Goal) — A. Smith",
  );
  assert.equal(
    eventLabel({ type: "Card", detail: "Red Card", player: "J. Doe" }),
    "Card (Red Card) — J. Doe",
  );
});

test("an unnamed player leaves the incident standing alone", () => {
  // The feed does not always name someone. "Goal — " with nothing after the
  // dash is worse than just "Goal".
  assert.equal(eventLabel({ type: "Goal", detail: null, player: null }), "Goal");
  assert.equal(
    eventLabel({ type: "Goal", detail: "Penalty", player: null }),
    "Goal (Penalty)",
  );
});

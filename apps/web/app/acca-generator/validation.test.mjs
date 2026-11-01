import assert from "node:assert/strict";
import test from "node:test";
import { validateAccaGeneratorInput } from "./validation.ts";

test("generator accepts the inclusive 2 through 20 leg range", () => {
  assert.deepEqual(validateAccaGeneratorInput("2", "1.5", "1000"), { legs: 2, minOdds: 1.5, maxOdds: 1000 });
  assert.deepEqual(validateAccaGeneratorInput("20", "2", "25"), { legs: 20, minOdds: 2, maxOdds: 25 });
});

test("generator rejects invalid legs and fair-odds ranges", () => {
  for (const legs of ["1", "21", "2.5", "", "five"]) {
    assert.throws(() => validateAccaGeneratorInput(legs, "1.5", "1000"), /whole number between 2 and 20/);
  }
  assert.throws(() => validateAccaGeneratorInput("5", "0", "10"), /Minimum fair odds/);
  assert.throws(() => validateAccaGeneratorInput("5", "10", "5"), /Maximum fair odds/);
  assert.throws(() => validateAccaGeneratorInput("5", "1.5", "1001"), /cannot exceed 1000/);
});

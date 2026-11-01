import assert from "node:assert/strict";
import test from "node:test";
import { formatOdds, isOddsFormat } from "./odds.ts";

test("odds formatter supports decimal fractional and American systems", () => {
  assert.equal(formatOdds(1.5), "1.50");
  assert.equal(formatOdds(1.5, "fractional"), "1/2");
  assert.equal(formatOdds(1.5, "american"), "-200");
  assert.equal(formatOdds(2.5, "american"), "+150");
});

test("odds preference accepts only supported formats", () => {
  assert.equal(isOddsFormat("decimal"), true);
  assert.equal(isOddsFormat("fractional"), true);
  assert.equal(isOddsFormat("american"), true);
  assert.equal(isOddsFormat("moneyline-ish"), false);
});

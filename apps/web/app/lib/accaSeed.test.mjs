import assert from "node:assert/strict";
import test from "node:test";

import { ACCA_SEED_COOKIE, isValidSeed, mintSeed, resolveAccaSeed } from "./accaSeed.ts";

test("a minted seed is opaque, short and URL-safe", () => {
  const seed = mintSeed();
  assert.match(seed, /^[0-9a-f]{12}$/, "12 hex chars needs no encoding in a query string");
  assert.equal(isValidSeed(seed), true);
});

test("minted seeds differ, so different visitors see different slips", () => {
  const seeds = new Set(Array.from({ length: 50 }, () => mintSeed()));
  assert.ok(seeds.size > 45, `expected mostly unique seeds, got ${seeds.size}/50`);
});

test("an existing cookie is reused, so one visitor keeps one order", () => {
  // Stability matters more than novelty here: a new seed on page 2 would
  // reorder the catalogue mid-scroll and break cursor pagination.
  const existing = "abc123def456";
  assert.equal(resolveAccaSeed(existing), existing);
  assert.equal(resolveAccaSeed(existing), existing, "stable across calls");
});

test("a malformed or absent cookie is replaced rather than trusted", () => {
  for (const bad of [undefined, null, "", "  ", "not-hex!", "ABC", "a".repeat(64), "'; DROP TABLE"]) {
    const seed = resolveAccaSeed(bad);
    assert.match(seed, /^[0-9a-f]{12}$/, `${String(bad)} must be replaced with a fresh seed`);
  }
});

test("the cookie name is stable", () => {
  assert.equal(ACCA_SEED_COOKIE, "sokalens.acca.seed");
});

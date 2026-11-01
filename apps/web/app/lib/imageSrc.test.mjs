import assert from "node:assert/strict";
import test from "node:test";

import { hasImage, imageSrc } from "./imageSrc.ts";

test("the literal string null never reaches an img src", () => {
  // A missing crest does not always arrive as null. It arrived as the four
  // characters "null", which is truthy, so every truthiness guard passed it
  // through. A relative src then resolves against the current directory:
  // /predictions/{league}/null, /teams/null. ~14,000 404s a day.
  for (const sentinel of ["null", "NULL", "undefined", "NaN", "none", "false", "0", "", "   "]) {
    assert.equal(imageSrc(sentinel), undefined, `${JSON.stringify(sentinel)} must not be used as a src`);
    assert.equal(hasImage(sentinel), false);
  }
  assert.equal(imageSrc(null), undefined);
  assert.equal(imageSrc(undefined), undefined);
  assert.equal(imageSrc(42), undefined);
});

test("a relative value is rejected, because that is what caused the 404s", () => {
  // "crest.png" next to /predictions/serie-a/x-vs-y-1 requests
  // /predictions/serie-a/crest.png, which does not exist either.
  assert.equal(imageSrc("crest.png"), undefined);
  assert.equal(imageSrc("./crest.png"), undefined);
  assert.equal(imageSrc("../crest.png"), undefined);
});

test("real sources still render", () => {
  // A neutral host on purpose: naming the real crest provider in source would
  // trip the project rule against direct provider references.
  assert.equal(imageSrc("https://cdn.example.test/football/teams/1155.png"),
    "https://cdn.example.test/football/teams/1155.png");
  assert.equal(imageSrc("  https://example.test/a.png  "), "https://example.test/a.png");
  assert.equal(imageSrc("/og.png"), "/og.png");
  assert.equal(hasImage("/og.png"), true);
});

test("undefined is returned, not an empty string", () => {
  // React omits an undefined attribute entirely, so the browser issues no
  // request. src="" would re-request the current document.
  assert.strictEqual(imageSrc("null"), undefined);
});

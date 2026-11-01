import assert from "node:assert/strict";
import test from "node:test";

import * as catalogRoutes from "./catalogSlug.ts";

const { validCatalogSlug } = catalogRoutes;

test("catalog links reject missing values serialized as slugs", () => {
  assert.equal(validCatalogSlug("manchester-united"), true);
  assert.equal(validCatalogSlug("null"), false);
  assert.equal(validCatalogSlug(" undefined "), false);
  assert.equal(validCatalogSlug(null), false);
  assert.equal(validCatalogSlug("nan"), false);
  assert.equal(validCatalogSlug(" manchester-united "), false);
  assert.equal(validCatalogSlug("manchester/united"), false);
  assert.equal(validCatalogSlug(".."), false);
  assert.equal(validCatalogSlug("manchester-united?tab=matches"), false);
  assert.equal(validCatalogSlug("manchester-united#matches"), false);
});

test("catalog route builders reject unsafe entity references", () => {
  assert.equal(typeof catalogRoutes.teamPath, "function");
  assert.equal(typeof catalogRoutes.leaguePredictionPath, "function");
  assert.equal(catalogRoutes.teamPath("manchester-united"), "/teams/manchester-united");
  assert.equal(catalogRoutes.teamPath("null"), null);
  assert.equal(catalogRoutes.teamPath(undefined), null);
  assert.equal(catalogRoutes.leaguePredictionPath("premier-league"), "/predictions/leagues/premier-league");
  assert.equal(catalogRoutes.leaguePredictionPath("undefined"), null);
});

test("match route builder accepts only positive safe integer IDs", () => {
  assert.equal(typeof catalogRoutes.matchPath, "function");
  assert.equal(catalogRoutes.matchPath(42), "/matches/42");
  assert.equal(catalogRoutes.matchPath("42"), "/matches/42");
  for (const value of [null, undefined, "", "null", "undefined", "NaN", 0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY]) {
    assert.equal(catalogRoutes.matchPath(value), null, String(value));
  }
});

test("entity boundary helpers reject invalid IDs and blank route text", () => {
  assert.equal(typeof catalogRoutes.validEntityId, "function");
  assert.equal(typeof catalogRoutes.validRouteText, "function");
  assert.equal(catalogRoutes.validEntityId(42), true);
  assert.equal(catalogRoutes.validEntityId(Number.NaN), false);
  assert.equal(catalogRoutes.validEntityId(0), false);
  assert.equal(catalogRoutes.validRouteText("Arsenal"), true);
  assert.equal(catalogRoutes.validRouteText("   "), false);
});

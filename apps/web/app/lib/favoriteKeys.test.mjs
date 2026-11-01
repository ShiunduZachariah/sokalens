import assert from "node:assert/strict";
import test from "node:test";

const { favoriteKey, favoriteMutation, normalizeFavoriteKeys } = await import("./favoriteKeys.ts");

test("keys are case-folded to match what the API stores", () => {
  // accounts.Service.AddFavorite lowercases entity_type and entity_id, so a
  // button holding the original slug has to fold the same way or it will read
  // its own saved entity back as unsaved.
  assert.equal(favoriteKey("Team", "Manchester-United"), "team:manchester-united");
  assert.equal(favoriteKey("team", "manchester-united"), favoriteKey("TEAM", "Manchester-United"));
});

test("numeric entity ids key the same as their string form", () => {
  assert.equal(favoriteKey("match", 2014779), "match:2014779");
  assert.equal(favoriteKey("match", "2014779"), favoriteKey("match", 2014779));
});

test("keys come back from the lightweight view payload", () => {
  const keys = normalizeFavoriteKeys({
    favorites: [
      { entity_type: "match", entity_id: "2014779" },
      { entity_type: "league", entity_id: "premier-league" },
    ],
  });
  assert.deepEqual(keys, ["match:2014779", "league:premier-league"]);
});

test("keys also come back from the full favorites payload", () => {
  // The provider must tolerate the richer /v1/me/favorites body, which is what
  // an older API build or a cached response would return.
  const keys = normalizeFavoriteKeys({
    favorites: [{ id: 7, entity_type: "team", entity_id: "arsenal", name: "Arsenal", href: "/teams/arsenal", matches: [] }],
  });
  assert.deepEqual(keys, ["team:arsenal"]);
});

test("entries missing a type or id are skipped rather than keyed as blanks", () => {
  const keys = normalizeFavoriteKeys({ favorites: [{ entity_type: "team" }, { entity_id: "arsenal" }, {}, "nonsense"] });
  assert.deepEqual(keys, []);
});

test("saving posts the entity with its display name", () => {
  const mutation = favoriteMutation("match", "2014779", "Bournemouth vs Liverpool", true);
  assert.equal(mutation.method, "POST");
  assert.deepEqual(JSON.parse(mutation.body), {
    entity_id: "2014779",
    entity_type: "match",
    name: "Bournemouth vs Liverpool",
  });
});

test("unsaving deletes the same entity", () => {
  // This is the defect: the control only ever issued the POST above, so a
  // saved item could not be unsaved anywhere except the favorites page.
  const mutation = favoriteMutation("match", "2014779", "Bournemouth vs Liverpool", false);
  assert.equal(mutation.method, "DELETE");
  assert.deepEqual(JSON.parse(mutation.body), { entity_id: "2014779", entity_type: "match" });
});

test("a save and the unsave that follows it address the same entity", () => {
  const save = JSON.parse(favoriteMutation("league", "premier-league", "Premier League", true).body);
  const remove = JSON.parse(favoriteMutation("league", "premier-league", "Premier League", false).body);
  assert.equal(save.entity_type, remove.entity_type);
  assert.equal(save.entity_id, remove.entity_id);
});

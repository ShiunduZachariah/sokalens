import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const buttonSource = await readFile(new URL("./FavoriteButton.tsx", import.meta.url), "utf8");
const providerSource = await readFile(new URL("./FavoritesContext.tsx", import.meta.url), "utf8");
const listSource = await readFile(new URL("./FavoritesList.tsx", import.meta.url), "utf8");

test("saved state is read from the shared set, not seeded to false", () => {
  // The defect: `const [saved, setSaved] = useState(false)` meant an entity
  // the viewer had already saved still rendered as "Save", and the only way
  // the control ever became "Saved" was an interaction in that same session.
  assert.doesNotMatch(buttonSource, /useState\(false\)/);
  assert.match(buttonSource, /const saved = isFavorite\(entityType, entityId\);/);
});

test("a saved button stays clickable", () => {
  // The other half of the defect: `disabled={busy || saved || ...}` latched
  // the control off the moment it succeeded, so unsaving was impossible.
  const disabled = buttonSource.match(/disabled=\{([^}]*)\}/);
  assert.ok(disabled, "expected the button to declare a disabled expression");
  assert.doesNotMatch(disabled[1], /\bsaved\b/);
});

test("the click handler toggles instead of only saving", () => {
  assert.match(buttonSource, /const next = !saved;/);
  assert.match(buttonSource, /favoriteMutation\(entityType, entityId, label, next\)/);
});

test("the toggle is optimistic and rolls back when the request fails", () => {
  assert.match(buttonSource, /setFavorite\(entityType, entityId, next\);[\s\S]{0,400}accountApiRequest/);
  assert.match(buttonSource, /if \(!result\.ok\) \{\s*setFavorite\(entityType, entityId, !next\);/);
});

test("the control reports its pressed state to assistive technology", () => {
  assert.match(buttonSource, /aria-pressed=\{saved\}/);
});

test("the provider loads the cheap keys view, not the full favorites body", () => {
  // /predictions/today renders hundreds of these buttons; the full GET does a
  // per-favorite catalog fan-out that has no business running on every page.
  assert.match(providerSource, /"\/v1\/me\/favorites\?view=keys"/);
  assert.doesNotMatch(providerSource, /"\/v1\/me\/favorites"/);
});

test("a failed load does not claim the viewer has nothing saved", () => {
  assert.match(providerSource, /if \(result\.ok\) setKeys\(new Set\(normalizeFavoriteKeys\(result\.data\)\)\);/);
});

test("removing from the favorites page updates the shared set too", () => {
  assert.match(listSource, /setFavorite\(item\.entityType, item\.entityId, false\);/);
});

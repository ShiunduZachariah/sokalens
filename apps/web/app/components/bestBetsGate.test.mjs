import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import path from "node:path";

const appDir = path.resolve(import.meta.dirname, "..");
const read = (relative) => readFile(path.join(appDir, relative), "utf8");

/*
 * The Best Bets sign-up gate, and the one thing about it that must not drift.
 *
 * `/best-bets` is in `staticUrls` and indexable, and Googlebot is always
 * anonymous -- so whatever an anonymous reader sees is what gets indexed. The
 * design puts the hero pick OUTSIDE the gate for exactly that reason: one
 * complete, readable, ranked pick with its confidence keeps the page worth
 * ranking, and the gate starts at number two.
 *
 * Moving the hero inside the gate would leave a blurred page and a sign-up card
 * as the indexable content of a page that currently ranks. Nothing would fail, no
 * test would go red, and the traffic would go away over several weeks.
 */

test("the hero pick is rendered outside the gate", async () => {
  const source = await read("components/BestBetExplorer.tsx");

  const heroAt = source.indexOf("showHero ? (");
  // Anchored on the component, not on `locked ? (` -- that matches `!locked ? (`
  // as a substring, which put the gate earlier in the file than it is and made
  // this assertion pass or fail for the wrong reason.
  const gateAt = source.indexOf("<BestBetsLocked");
  assert.ok(heroAt > 0, "the hero branch is gone");
  assert.ok(gateAt > 0, "the locked branch is gone");
  assert.ok(
    heroAt < gateAt,
    "the hero is no longer rendered before the gate; an anonymous reader and a crawler must still get one complete pick",
  );

  // And it must not be gated by its own condition.
  assert.ok(
    !/locked[^\n]*&&[^\n]*showHero|showHero[^\n]*&&[^\n]*!?locked/.test(source),
    "the hero's visibility now depends on the lock",
  );
});

test("an unresolved account state does not lock the page", async () => {
  const source = await read("components/BestBetExplorer.tsx");

  // The account context resolves asynchronously. Locking during that window
  // flashes a sign-up card at every signed-in reader on every visit.
  assert.match(
    source,
    /const locked = status === "ready" && !access\?\.authenticated;/,
    "the lock no longer waits for the account state to resolve",
  );
});

test("the gate asks for a free account, not an upgrade", async () => {
  const locked = await read("components/BestBetsLocked.tsx");

  // The design's own copy: "Best Bets are free with a SokaLens account." It is a
  // registration gate. Pointing it at /pro would misdescribe the product and send
  // people to a payment page for something free.
  assert.match(locked, /Create free account/);
  assert.match(locked, /\/register/, "the primary action must go to registration");
  assert.match(locked, /\/login/, "an existing account needs a way in");
  assert.ok(!/\/pro\b/.test(locked), "the gate points at an upgrade; Best Bets are free with an account");
});

test("the blurred rows are hidden from assistive technology and the keyboard", async () => {
  const locked = await read("components/BestBetsLocked.tsx");
  const css = await read("globals.css");

  // A blurred row is decoration. Left reachable, a screen reader reads out the
  // picks the page just decided to withhold, and a keyboard tabs into them.
  assert.match(locked, /aria-hidden="true" className="bb-locked__teaser" inert/,
    "the teaser is reachable by a screen reader or the keyboard");
  assert.match(css, /\.bb-locked__teaser \{[^}]*filter: blur/, "the teaser is not blurred");
  assert.match(css, /\.bb-locked__teaser \{[^}]*user-select: none/, "the teaser can be selected and copied");
});

test("filters are not offered over a list the reader cannot read", async () => {
  const source = await read("components/BestBetExplorer.tsx");
  // A country select over four blurred rows is a dead end dressed as a feature.
  assert.match(source, /\{locked \? null : \(\s*<div className="bb-controls">/,
    "the filter controls are still rendered while locked");
});

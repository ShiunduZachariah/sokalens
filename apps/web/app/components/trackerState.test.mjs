import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import path from "node:path";

const appDir = path.resolve(import.meta.dirname, "..");
const read = (relative) => readFile(path.join(appDir, relative), "utf8");

/*
 * The in-play tracker contract, asserted on this side of it.
 *
 * `docs/integrations/in-play-data-contract.md` §8 is a contract between the
 * backend and *two* clients, and the whole reason `tracker_state` is computed
 * server-side is that web and Flutter must not grade the same pick differently:
 * "over 1.5 at 2-0 is won to one implementation and still on_track to another,
 * and a reader who checks both sees the platform contradict itself."
 *
 * Nothing in a type-checker stops someone adding `if (homeScore > awayScore)` to
 * the chip, and nothing in a rendered page looks wrong when they do -- it looks
 * like a working tracker right up until it disagrees with the mobile app or with
 * the settlement the same pick later gets. Hence a test.
 */

test("every state the contract defines is handled, and none is invented", async () => {
  const source = await read("components/TrackerChip.tsx");
  const types = await read("data/liveMatches.ts");

  // The six values the contract lists. A seventh appearing here means the
  // contract moved and this client guessed at the new one.
  const contractStates = ["on_track", "off_track", "won", "lost", "void", "unknown"];
  for (const state of contractStates) {
    assert.match(types, new RegExp(`"${state}"`), `TrackerState is missing "${state}"`);
    assert.match(source, new RegExp(`\\b${state}\\b`), `TrackerChip does not handle "${state}"`);
  }

  // The parser must reject anything else rather than passing a string through to
  // a class name, which would render an unstyled chip.
  assert.match(
    types,
    /TRACKER_STATES\.has\(value\)/,
    "tracker_state is no longer checked against the known set before being accepted",
  );
});

test("the tracker state is read, never derived", async () => {
  const chip = await read("components/TrackerChip.tsx");
  const parser = await read("data/liveMatches.ts");

  // Scores and minutes are what a client-side derivation would need. Their
  // absence from both files is the assertion.
  for (const [file, source] of [["TrackerChip.tsx", chip], ["liveMatches.ts (tracker parsing)", parser.slice(parser.indexOf("function toTrackerState"), parser.indexOf("export type LiveMatchPrediction"))]]) {
    for (const signal of ["homeScore", "awayScore", "statusElapsed", "home_score", "away_score"]) {
      assert.ok(
        !source.includes(signal),
        `${file} reads ${signal}; the tracker state must come from the server, not be derived (contract §8)`,
      );
    }
  }
});

test("unknown and a null verdict both render nothing", async () => {
  const source = await read("components/TrackerChip.tsx");

  // The contract: "Show no tracker for `unknown`, not a colour." A label or a
  // modifier for it would put a chip on a pick the engine could not judge.
  assert.match(
    source,
    /unknown:\s*null/,
    "`unknown` has a presentation again; the contract says show no tracker for it",
  );

  // No verdict must short-circuit before the lookup, or `PRESENTATION[undefined]`
  // throws on a fixture nothing has judged -- which is most of them. The API sends
  // `tracker_state: null` for that since 2026-10-02 and omitted the key before;
  // the parser maps both to undefined, so this guard covers both wire forms.
  assert.match(
    source,
    /const presentation = state \? PRESENTATION\[state\] : null;/,
    "a null tracker_state no longer short-circuits the presentation lookup",
  );
  assert.match(source, /if \(!presentation\) return null;/, "TrackerChip no longer returns null for a state it cannot draw");
});

test("the chip never replaces a settled grade", async () => {
  const card = await read("matches/[id]/MatchPredictionCard.tsx");

  // tracker_state and outcome are independent records, so both can be present:
  // a pick reads `won` on the tracker while `outcome` is still `pending`.
  // Rendering both produces "Pending · Won" on one card. Settlement is the
  // answer once it exists; before that the tracker replaces "Pending".
  assert.match(card, /settled \?/, "the card no longer prefers a settled grade over the live tracker");
  assert.match(
    card,
    /prediction\.outcome === "won" \|\| prediction\.outcome === "lost" \|\| prediction\.outcome === "void"/,
    "the settled check no longer names the three graded outcomes",
  );
  assert.match(card, /<TrackerChip state=\{prediction\.trackerState\} \/>/, "the card no longer renders the tracker chip");
});

test("every tracker modifier has a style rule", async () => {
  const css = await read("globals.css");
  // An unstyled modifier is an invisible chip, which looks like a pick with no
  // tracker rather than like a bug.
  for (const modifier of ["on", "risk", "won", "lost", "void"]) {
    assert.match(css, new RegExp(`\\.mp-track--${modifier}\\b`), `globals.css has no rule for .mp-track--${modifier}`);
  }
  assert.match(css, /\.mp-track \{/, "the base .mp-track rule is gone");
});

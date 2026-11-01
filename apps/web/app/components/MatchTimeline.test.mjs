import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";

const readSource = (name) => readFile(path.join(import.meta.dirname, name), "utf8");

test("the side a row sits on is the side the event benefited", async () => {
  // Position is an assertion. The provider attributes an own goal to the
  // beneficiary, so the same mapped side places the row and credits the goal.
  // The helper keeps that interpretation shared with the running score.
  const source = await readSource("MatchTimeline.tsx");

  assert.match(source, /const side = benefitingSide\(event\)/,
    "row placement must come from benefitingSide, not the raw provider side");
  assert.doesNotMatch(source, /const side = event\.side/);
});

test("an event with no mapped team is not filed under the away side", async () => {
  // The provider does not always map a team id. Such an event has no side, and
  // dropping it into the away column would attribute it to a team on no
  // evidence. It gets its own modifier so the stylesheet can place it
  // distinctly rather than silently implying a side.
  const source = await readSource("MatchTimeline.tsx");
  assert.match(source, /match-timeline__event--\$\{side \?\? "unknown"\}/);

  const css = await readFile(
    path.join(import.meta.dirname, "..", "globals.css"),
    "utf8",
  );
  assert.match(css, /\.match-timeline__event--unknown\s*\{/,
    "the unknown-side row needs its own layout, or it reads as an away event");
});

test("event icons are decorative and never announced", async () => {
  // The label beside the icon already says "Goal (Penalty)" or "Second
  // yellow". An icon with an accessible name makes a screen reader say the
  // same thing twice, so every glyph is aria-hidden and the text carries the
  // meaning.
  const source = await readSource("MatchEventIcon.tsx");
  assert.match(source, /aria-hidden="true"/);
  assert.doesNotMatch(source, /<title>/, "a <title> would give the icon an accessible name");
  assert.doesNotMatch(source, /aria-label=/);
});

test("every event kind the timeline can render has a glyph that exists", async () => {
  // A kind with no mapping falls through to the neutral dot rather than
  // rendering nothing, so the marker column stays aligned when the provider
  // sends something this build has not seen. Losing that default would leave
  // holes in the column.
  //
  // This used to assert a `case "<kind>":` per kind, which pinned the switch
  // statement rather than the property. The icons moved to the SokaLens sprite
  // and the switch became a map; the property is unchanged, and the assertion is
  // now stronger — it resolves each mapped name against the sprite's real symbol
  // ids, so a typo renders an empty box in a browser and fails here instead.
  const source = await readSource("MatchEventIcon.tsx");
  const sprite = await readFile(
    new URL("../../public/icons/sokalens-icons-sprite.svg", import.meta.url),
    "utf8",
  );
  const symbols = new Set([...sprite.matchAll(/id="sl-([a-z-]+)"/g)].map((match) => match[1]));
  assert.ok(symbols.size > 30, "the sprite has almost no symbols; it is probably the wrong file");

  for (const kind of [
    "goal",
    "own_goal",
    "penalty_goal",
    "penalty_missed",
    "yellow_card",
    "red_card",
    "second_yellow_card",
    "substitution",
    "var_retraction",
    "var_other",
  ]) {
    const mapped = source.match(new RegExp(`\\b${kind}: "([a-z-]+)"`));
    assert.ok(mapped, `no glyph mapping for ${kind}`);
    assert.ok(
      symbols.has(mapped[1]),
      `${kind} maps to "${mapped[1]}", which is not a symbol in the sprite`,
    );
  }

  // An unknown kind must still render a marker.
  assert.match(source, /if \(!name\)/, "an unknown kind no longer falls back to the neutral dot");
});

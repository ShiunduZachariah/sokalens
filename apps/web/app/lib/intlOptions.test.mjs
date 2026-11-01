import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { VIEWER_TIME_ZONE, formatInstant } from "./format.ts";

/**
 * `Intl.DateTimeFormat` throws `TypeError: Invalid option : option` when
 * `dateStyle` or `timeStyle` is combined with any component option --
 * `timeZoneName`, `weekday`, `hour`, and the rest. The two families are
 * mutually exclusive and the runtime rejects the mixture rather than ignoring
 * the surplus key.
 *
 * That is a runtime throw with no type error and no build failure, and every
 * page on this site renders a kickoff time. Adding `timeZoneName: "short"`
 * beside `timeStyle` to name the reader's zone put production into a 500 on
 * every route for sixteen minutes.
 *
 * So this is checked two ways: the exported formatters are actually called,
 * and the source is scanned, because the next occurrence will be in a
 * component that no unit test happens to render.
 */

const STYLE_OPTION = /\b(dateStyle|timeStyle)\s*:/;

const COMPONENT_OPTIONS = [
  "weekday", "era", "year", "month", "day", "dayPeriod",
  "hour", "minute", "second", "fractionalSecondDigits", "timeZoneName",
];

test("the shared formatters produce a string rather than throwing", () => {
  const instant = "2026-09-12T18:30:00.000Z";
  const calls = [
    formatInstant(instant),
    formatInstant(instant, { timeOnly: true }),
    // The two zones the component actually uses: the server pass and a reader
    // whose browser resolved its own.
    formatInstant(instant, { timeZone: "UTC" }),
    formatInstant(instant, { timeOnly: true, timeZone: "America/New_York" }),
  ];
  for (const value of calls) {
    assert.equal(typeof value, "string");
    assert.ok(value.length > 0);
  }
  assert.equal(formatInstant("not a date"), null);
});

test("no DateTimeFormat mixes dateStyle/timeStyle with component options", () => {
  const appDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

  const files = [];
  const walk = (dir) => {
    for (const entry of readdirSync(dir)) {
      const full = path.join(dir, entry);
      if (statSync(full).isDirectory()) walk(full);
      else if (/\.(ts|tsx|mjs)$/.test(entry) && !entry.endsWith(".test.mjs")) files.push(full);
    }
  };
  walk(appDir);

  // A regex cannot do this: the options literals here contain nested braces
  // (`...(includeDate ? { dateStyle } : {})`), and a non-nesting character
  // class stops at the first inner `}` and reads an empty object. That version
  // of this test passed while the outage was live in the very file it scanned.
  const optionsLiteral = (source, from) => {
    const open = source.indexOf("{", from);
    if (open === -1) return null;
    let depth = 0;
    for (let index = open; index < source.length; index += 1) {
      if (source[index] === "{") depth += 1;
      else if (source[index] === "}") {
        depth -= 1;
        if (depth === 0) return source.slice(open + 1, index);
      }
    }
    return null;
  };

  // Anchored on the option keys rather than on `DateTimeFormat(`, because the
  // options object is not always written at the call site: the pre-paint
  // script in LocalTime builds `var o={timeStyle:'short'}` and passes `o`, so
  // anchoring on the call would step straight past the one occurrence that
  // runs in the browser, where nothing on the server would ever catch it.
  // `dateStyle` and `timeStyle` are Intl-only keys, so they are a safe anchor.
  const offenders = [];
  for (const file of files) {
    const source = readFileSync(file, "utf8");
    for (const match of source.matchAll(/\{[^\n]*?\b(?:dateStyle|timeStyle)\s*:/g)) {
      const options = optionsLiteral(source, match.index);
      if (!options || !STYLE_OPTION.test(options)) continue;
      // Built from a String.raw template: in an ordinary one `\b` is a
      // backspace character and `\s` is a literal "s", so the pattern silently
      // matches nothing and the test passes on everything.
      const mixed = COMPONENT_OPTIONS.filter((option) => new RegExp(String.raw`\b${option}\s*:`).test(options));
      if (mixed.length > 0) offenders.push(`${path.relative(appDir, file)}: ${mixed.join(", ")}`);
    }
  }

  assert.deepEqual(offenders, [], `Intl.DateTimeFormat throws on these combinations:\n${offenders.join("\n")}`);
});

test("the viewer's own zone is reachable, and is not what an absent option means", () => {
  // Regression: LocalTime's effect passed `timeZone: undefined` meaning "the
  // reader's zone". An absent key falls back to the parameter default, which is
  // UTC, so every time the pre-paint pass did not reach stayed in UTC on the
  // reader's screen -- a 12:00Z kickoff read "12:00 PM" in Nairobi.
  const instant = "2026-09-12T12:00:00.000Z";

  const asUtc = formatInstant(instant, { timeOnly: true, timeZone: "UTC" });
  const asNairobi = formatInstant(instant, { timeOnly: true, timeZone: "Africa/Nairobi" });
  const asViewer = formatInstant(instant, { timeOnly: true, timeZone: VIEWER_TIME_ZONE });
  const defaulted = formatInstant(instant, { timeOnly: true });

  assert.notEqual(asUtc, asNairobi, "fixture must differ between the two zones");
  assert.equal(defaulted, asUtc, "an absent timeZone is the server zone");

  // The sentinel omits `timeZone`, so Intl resolves the environment's own. In
  // CI that environment is usually UTC, so assert the mechanism rather than a
  // value: it must match whatever zone this process actually resolves to.
  const resolved = Intl.DateTimeFormat().resolvedOptions().timeZone;
  assert.equal(asViewer, formatInstant(instant, { timeOnly: true, timeZone: resolved }));
});

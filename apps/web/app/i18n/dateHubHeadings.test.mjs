import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import path from "node:path";
import { supportedLocales } from "./config.ts";

/**
 * /predictions/tomorrow renders the Today page component with `forcedDay`.
 *
 * That is why both pages served one heading -- "Every signal. The prediction
 * behind it." -- on the two highest-intent date hubs on the property, while
 * their `<title>` tags were already correct and distinct. The inheritance is
 * invisible: nothing errors, the page just silently wears the wrong h1. These
 * assertions are the only thing standing between that and a repeat.
 *
 * The catalog is read as source rather than imported: `catalogs.ts` imports
 * `./config` without a file extension, which Node's TypeScript loader will not
 * resolve from a test. `styleContract.test.mjs` reads globals.css the same way.
 */

const catalogs = await readFile(path.join(import.meta.dirname, "catalogs.ts"), "utf8");

/** Values appear once per locale, in `supportedLocales` order. */
function byLocale(key) {
  const found = [
    ...catalogs.matchAll(new RegExp(`"${key.replace(".", "\\.")}":\\s*"((?:[^"\\\\]|\\\\.)*)"`, "g")),
  ].map((m) => m[1]);
  assert.equal(
    found.length,
    supportedLocales.length,
    `${key} is defined ${found.length} times but there are ${supportedLocales.length} locales`,
  );
  return Object.fromEntries(supportedLocales.map((locale, i) => [locale, found[i]]));
}

const SLOGAN = /Every signal|Cada señal|Chaque signal|Cada sinal|كل إشارة|Jedes Signal|Ogni segnale/;

test("every locale gives Today and Tomorrow their own heading", () => {
  const today = byLocale("today.title");
  const tomorrow = byLocale("tomorrow.title");

  for (const locale of supportedLocales) {
    assert.ok(today[locale] && tomorrow[locale], `${locale}: a date-hub heading is empty`);
    assert.notEqual(
      today[locale],
      tomorrow[locale],
      `${locale}: Tomorrow is inheriting Today's heading again`,
    );
  }
});

test("no date hub serves the brand slogan as its h1", () => {
  for (const key of ["today.title", "tomorrow.title"]) {
    const values = byLocale(key);
    for (const locale of supportedLocales) {
      assert.doesNotMatch(
        values[locale],
        SLOGAN,
        `${locale} ${key}: the masthead names the page, the home hero carries the slogan`,
      );
    }
  }
});

test("each heading matches the page its metadata title already describes", () => {
  // The <title> was right all along, so it is the reference: the h1 should be
  // the same phrase without the brand suffix.
  const heading = byLocale("today.title");
  const metaTitle = byLocale("today.metadata_title");

  for (const locale of supportedLocales) {
    assert.ok(
      metaTitle[locale].startsWith(heading[locale]),
      `${locale}: h1 "${heading[locale]}" does not lead its own <title> "${metaTitle[locale]}"`,
    );
  }
});

test("the Today page picks its heading from forcedDay", async () => {
  const source = await readFile(
    path.join(import.meta.dirname, "../predictions/today/page.tsx"),
    "utf8",
  );
  assert.match(
    source,
    /forcedDay === "tomorrow" \? "tomorrow\.title" : "today\.title"/,
    "the masthead no longer branches on forcedDay -- Tomorrow will inherit Today's heading",
  );
  assert.match(
    source,
    /forcedDay === "tomorrow" \? "tomorrow\.summary" : "today\.summary"/,
    "the summary under the masthead still hardcodes today's copy",
  );
});

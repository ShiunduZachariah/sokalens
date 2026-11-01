import assert from "node:assert/strict";
import test from "node:test";

import { countryFromLocale, resolveViewerCountry } from "./viewerCountry.ts";

const headers = (map) => new Map(Object.entries(map));

test("cf-ipcountry is used when Cloudflare supplies it", () => {
  assert.equal(resolveViewerCountry(headers({ "cf-ipcountry": "BR" }), "en"), "BR");
  assert.equal(resolveViewerCountry(headers({ "cf-ipcountry": "ke" }), "en"), "KE");
  assert.equal(resolveViewerCountry(headers({ "cf-ipcountry": " gb " }), "en"), "GB");
});

test("Cloudflare's placeholders are treated as unknown, not as a country", () => {
  // XX means Cloudflare could not resolve it; T1 is Tor. Neither is a country
  // and treating them as one would silently mis-rank the whole page.
  for (const value of ["XX", "T1", "", "ZZZ", "1"]) {
    assert.equal(
      resolveViewerCountry(headers({ "cf-ipcountry": value }), null),
      "",
      `${value} must not be treated as a country`,
    );
  }
});

test("locale supplies a fallback country when the header is absent", () => {
  assert.equal(countryFromLocale("pt-BR"), "BR");
  assert.equal(countryFromLocale("en-GB"), "GB");
  assert.equal(countryFromLocale("es-AR"), "AR");
});

test("a bare language is not a country", () => {
  // "en" tells us nothing about where the reader is.
  for (const locale of ["en", "es", "fr", "", null, undefined]) {
    assert.equal(countryFromLocale(locale), "", `${String(locale)} carries no region`);
  }
});

test("the header wins over the locale", () => {
  assert.equal(resolveViewerCountry(headers({ "cf-ipcountry": "KE" }), "pt-BR"), "KE");
});

test("unknown everything resolves to empty, never a guess", () => {
  assert.equal(resolveViewerCountry(headers({}), null), "");
  assert.equal(resolveViewerCountry(null, null), "");
});

import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { canonicalForRequest, localeAlternates, localePath, resolveLocale, stripLocalePrefix, supportedLocales } from "./config.ts";

test("locale resolution follows explicit, account, cookie, browser, country, English order", () => {
  assert.deepEqual(resolveLocale({ selected: "fr", account: "de", cookie: "es", browser: "pt-BR", country: "IT" }), { locale: "fr", source: "selected" });
  assert.deepEqual(resolveLocale({ account: "de", cookie: "es", browser: "pt-BR", country: "IT" }), { locale: "de", source: "account" });
  assert.deepEqual(resolveLocale({ cookie: "es", browser: "pt-BR", country: "IT" }), { locale: "es", source: "cookie" });
  assert.deepEqual(resolveLocale({ browser: "nl-NL,pt-BR;q=0.8", country: "IT" }), { locale: "pt", source: "browser" });
  assert.deepEqual(resolveLocale({ country: "IT" }), { locale: "it", source: "country" });
  assert.deepEqual(resolveLocale({ country: "KE" }), { locale: "en", source: "fallback" });
});

test("locale URLs never translate canonical sports entity slugs", () => {
  assert.equal(localePath("/", "fr"), "/fr");
  assert.equal(localePath("/teams/manchester-city", "ar"), "/ar/teams/manchester-city");
  assert.equal(localePath("/fr/predictions/premier-league/arsenal-vs-chelsea-42?market=btts", "es"), "/es/predictions/premier-league/arsenal-vs-chelsea-42?market=btts");
  assert.equal(canonicalForRequest("/predictions/premier-league/arsenal-vs-chelsea-42", "/predictions/premier-league/arsenal-vs-chelsea-42", "en"), "/predictions/premier-league/arsenal-vs-chelsea-42");
  assert.equal(canonicalForRequest("/predictions/premier-league/arsenal-vs-chelsea-42", "/de/predictions/premier-league/arsenal-vs-chelsea-42", "de"), "/de/predictions/premier-league/arsenal-vs-chelsea-42");
  assert.equal(stripLocalePrefix("/pt/teams/real-madrid"), "/teams/real-madrid");
});

test("localized pages advertise every locale and x-default", () => {
  const alternates = localeAlternates("/fr/predictions/today");
  assert.deepEqual(Object.keys(alternates), [...supportedLocales, "x-default"]);
  assert.equal(alternates.fr, "/fr/predictions/today");
  assert.equal(alternates["x-default"], "/predictions/today");
});

test("public localization keeps unsupported locale alternates out of sitemaps", async () => {
  const [proxy, layout, sitemap, catalogs] = await Promise.all([
    readFile(new URL("../../proxy.ts", import.meta.url), "utf8"),
    readFile(new URL("../layout.tsx", import.meta.url), "utf8"),
    readFile(new URL("../lib/sitemapXml.ts", import.meta.url), "utf8"),
    readFile(new URL("./catalogs.ts", import.meta.url), "utf8"),
  ]);
  assert.match(proxy, /NextResponse\.rewrite/);
  // The one redirect the proxy is allowed: /en/* is a duplicate of the root
  // tree and is permanently retired. 308 rather than 301 so a non-GET keeps
  // its method.
  assert.match(proxy, /pathLocale === defaultLocale/);
  assert.match(proxy, /NextResponse\.redirect\(url, 308\)/);
  assert.doesNotMatch(proxy, /admin/);
  assert.match(layout, /alternates:\s*\{ canonical: publicPath, languages: localeAlternates\(publicPath\) \}/);
  assert.match(layout, /localeDetails\[locale\]\.direction/);
  assert.match(layout, /application\/ld\+json/);
  // Sitemap-level hreflang is read independently of the on-page tags and is
  // far cheaper for Google to consume across ~17,000 URLs.
  assert.match(sitemap, /xmlns:xhtml/);
  assert.match(sitemap, /rel="alternate" hreflang=/);
  // ...but only for routes that actually serve localized content. Announcing
  // alternates for a page that is English in every locale contradicts its own
  // canonical.
  assert.match(sitemap, /if \(!isLocalizedRoute\(path\)\) return ""/);
  // Google ignores both, and uniform values conveyed nothing while being
  // untrue for settled fixtures.
  assert.doesNotMatch(sitemap, /<changefreq>/);
  assert.doesNotMatch(sitemap, /<priority>/);
  assert.match(sitemap, /sitemapChunkSize = 45_000/);
  assert.match(sitemap, /maxSitemapUrls = 50_000/);
  assert.match(sitemap, /maxSitemapBytes = 45 \* 1024 \* 1024/);
  assert.match(sitemap, /urls\.slice\(0, maxSitemapUrls\)/);
  assert.match(sitemap, /bytes \+ rowBytes > maxSitemapBytes/);
  assert.match(catalogs, /const criticalOverrides: Record<Exclude<Locale, "en">, Partial<Catalog>>/);
  assert.match(catalogs, /predictionLabel/);
  for (const key of ["home.hero_title", "today.title", "match.metadata_title", "auth.login.title", "auth.register.title", "auth.forgot.title", "auth.reset.title", "auth.verify.title"]) {
    assert.ok((catalogs.match(new RegExp(`"${key.replaceAll(".", "\\.")}"`, "g")) ?? []).length >= supportedLocales.length, `${key} must be populated for every launch locale`);
  }
});

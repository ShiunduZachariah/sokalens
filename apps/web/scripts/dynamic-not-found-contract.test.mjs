import assert from "node:assert/strict";
import { access, readFile, readdir } from "node:fs/promises";
import test from "node:test";

async function metadataSection(relativeUrl, nextMarker) {
  const source = await readFile(new URL(relativeUrl, import.meta.url), "utf8");
  const start = source.indexOf("export async function generateMetadata");
  const end = source.indexOf(nextMarker, start);
  assert.ok(start >= 0 && end > start, `metadata section missing in ${relativeUrl}`);
  return source.slice(start, end);
}

test("missing dynamic entities throw notFound during metadata resolution before streaming", async () => {
  const sections = await Promise.all([
    metadataSection("../app/teams/[slug]/page.tsx", "export default async function TeamPage"),
    metadataSection("../app/predictions/[leagueSlug]/[fixtureSlug]/page.tsx", "export default async function FixtureSeoPage"),
    metadataSection("../app/predictions/leagues/[leagueSlug]/page.tsx", "export default async function LeaguePredictionPage"),
    metadataSection("../app/leagues/[slug]/page.tsx", "export default async function LeaguePage"),
  ]);

  for (const section of sections) assert.match(section, /notFound\(\)/);
});

test("the /matches/{id} shadow route renders nothing and only redirects", async () => {
  // /matches/{id} and /predictions/{league}/{home}-vs-{away}-{id} rendered the
  // same fixture from the same data, and 5,306 URLs -- 64% of every
  // non-indexed page on the property -- sat on the first shape. It resolves
  // the fixture by id and hands off; it must never grow a body again.
  const source = await readFile(new URL("../app/matches/[id]/page.tsx", import.meta.url), "utf8");

  assert.doesNotMatch(source, /export async function generateMetadata/);
  assert.doesNotMatch(source, /return \(/, "the shadow route must not render markup");
  assert.match(source, /permanentRedirect\(seoFixturePath\(match\)\)/);
  assert.match(source, /if \(!match\) notFound\(\)/);
});

test("no page links to the retired /matches/{id} route", async () => {
  // An internal link to a redirect costs a crawl hop and returns no index
  // value. fixtureHref is the one place a fixture URL is built.
  const appDir = new URL("../app/", import.meta.url);
  const offenders = [];
  for (const file of await readdir(appDir, { recursive: true })) {
    if (!/\.tsx?$/.test(file) || /\.test\./.test(file)) continue;
    if (file.replaceAll("\\", "/").startsWith("matches/[id]/")) continue;
    const source = await readFile(new URL(file, appDir), "utf8");
    // `next=/matches/...` is an auth return target on a noindex page, not a
    // crawlable link, and `/matches/today` is a distinct hub page.
    const matches = source.match(/href=\{?[`"']\/matches\/\$?\{?(?!today)/g) ?? [];
    if (matches.length > 0) offenders.push(file);
  }
  assert.deepEqual(offenders, [], `these files still link to the retired route: ${offenders.join(", ")}`);
});

test("temporary catalog failures remain noindex instead of becoming indexable soft errors", async () => {
  const team = await metadataSection("../app/teams/[slug]/page.tsx", "export default async function TeamPage");
  const league = await metadataSection("../app/predictions/leagues/[leagueSlug]/page.tsx", "export default async function LeaguePredictionPage");

  assert.match(team, /if \(!result\.ok\)[\s\S]*robots:\s*\{\s*index:\s*false/);
  assert.match(league, /if \(!result\.ok\)[\s\S]*robots:\s*\{\s*index:\s*false/);
});

test("entity routes do not stream a loading shell before their HTTP status is known", async () => {
  for (const relativeUrl of [
    "../app/loading.tsx",
    "../app/matches/[id]/loading.tsx",
    "../app/leagues/[slug]/loading.tsx",
  ]) {
    await assert.rejects(access(new URL(relativeUrl, import.meta.url)), { code: "ENOENT" });
  }
});

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import test from "node:test";
import ts from "typescript";
import { localePath } from "../i18n/config.ts";
import { legacyTeamCanonicalSlug } from "../lib/legacyTeamSlugs.ts";

const require = createRequire(import.meta.url);
const source = readFileSync(new URL("./[slug]/page.tsx", import.meta.url), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, target: ts.ScriptTarget.ES2022 },
}).outputText;
function page(locale, result) {
  const mocks = {
    "next/navigation": { permanentRedirect: (target) => { throw Object.assign(new Error("redirect"), { target }); }, notFound: () => { throw new Error("not_found"); } },
    "../../data/catalog": { fetchTeam: async () => result },
    "../../i18n/server": { requestLocaleContext: async () => ({ locale }) },
    "../../i18n/config": { localePath },
    "../../lib/legacyTeamSlugs": { legacyTeamCanonicalSlug },
    "../../site": { canonicalPath: (value) => value },
    "../../lib/teamDepth": { teamForm: () => ({ matches: 0 }), isIndexableTeam: () => false },
  };
  const module = { exports: {} };
  new Function("require", "module", "exports", compiled)((name) => {
    if (name === "react/jsx-runtime") return require(name);
    return mocks[name] ?? new Proxy({}, { get: () => () => null });
  }, module, module.exports);
  return module.exports;
}
const canonical = { ok: true, data: { id: 10, slug: "canonical-team", name: "Team", recent_matches: [] } };
const params = (slug) => ({ params: Promise.resolve({ slug }) });

for (const locale of ["en", "sw"]) {
  test(`approved team alias permanently redirects preserving ${locale} locale`, async () => {
    await assert.rejects(page(locale, canonical).default(params("old-team")), (error) => error.target === localePath("/teams/canonical-team", locale));
  });
}
test("canonical team request renders without redirect and alias metadata names the canonical slug", async () => {
  const routes = page("en", canonical);
  assert.ok(await routes.default(params("canonical-team")));
  const metadata = await routes.generateMetadata(params("old-team"));
  assert.equal(metadata.alternates.canonical, "/teams/canonical-team");
});
test("unavailable team does not invent an alias redirect", async () => {
  assert.ok(await page("en", { ok: false, error: "unavailable" }).default(params("old-team")));
});

test("a reviewed former slug fetches the canonical team before metadata and redirects", async () => {
  const team = { ok: true, data: { id: 10, slug: "team-germany-westfalia-kinderhaus", name: "Westfalia Kinderhaus", recent_matches: [] } };
  const routes = page("en", team);
  const metadata = await routes.generateMetadata(params("team-westfalia-kinderhaus"));
  assert.equal(metadata.alternates.canonical, "/teams/team-germany-westfalia-kinderhaus");
  await assert.rejects(routes.default(params("team-westfalia-kinderhaus")),
    (error) => error.target === "/teams/team-germany-westfalia-kinderhaus");
});

test("ambiguous or unknown former slugs cannot nominate a different team", () => {
  assert.equal(legacyTeamCanonicalSlug("team-world-hang-yuen"), "team-world-hang-yuen");
  assert.equal(legacyTeamCanonicalSlug("team-unknown"), "team-unknown");
});

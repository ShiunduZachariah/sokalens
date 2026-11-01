import { readdir, readFile, stat } from "node:fs/promises";
import { fileURLToPath } from "node:url";

/**
 * Static SEO invariants.
 *
 * Every fix in the search-recovery backlog is one edit away from silently
 * regressing, and none of the regressions are visible in review: a page that
 * quietly drops its canonical, a card that starts linking the retired fixture
 * route again, a sitemap that reintroduces `priority`. Search Console reports
 * them six weeks later as a coverage number.
 *
 * These checks read source only -- no network, no build, no running site --
 * so they gate a pull request rather than a deploy. The live half (HTTP
 * status, rendered canonical, reciprocal hreflang, redirect chains) is
 * `search-index-audit.mjs`, which runs against production after release.
 *
 * Mirrors the structure of the existing `security-invariants` job.
 */

const APP_DIR = new URL("../app/", import.meta.url);
const REPO_DIR = new URL("../../../", import.meta.url);

/** Routes that are deliberately not indexable and so need no canonical. */
const NOINDEX_ROUTES = /^(account|favorites|login|register|forgot-password|reset-password|verify-email|notifications|odds-preference|onboarding|recommendations|search|subscription|pro)\b/;

/**
 * Files allowed to name the retired route.
 *
 * The route itself, obviously; and the two URL builders, whose whole job is to
 * own the one place the fallback is spelled out.
 */
const FIXTURE_URL_OWNERS = ["matches/[id]/page.tsx", "lib/fixtureHref.ts", "lib/catalogSlug.ts"];

/**
 * Comments explain these rules and necessarily quote the URLs they forbid.
 * Scanning them would make the gate unfixable except by deleting the reasoning
 * for it, so the checks read code only.
 */
function stripComments(source) {
  // A `//` that follows whitespace or starts a line is a comment; one that
  // follows `:` is the scheme separator in a URL literal.
  return source.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|\s)\/\/.*$/gm, "$1");
}

async function sourceFiles() {
  const entries = await readdir(APP_DIR, { recursive: true });
  return entries
    .map((entry) => entry.replaceAll("\\", "/"))
    .filter((entry) => /\.tsx?$/.test(entry) && !/\.test\./.test(entry))
    .sort();
}

function isPageFile(file) {
  return file.endsWith("/page.tsx") || file === "page.tsx";
}

function routeOf(file) {
  return file.replace(/\/?page\.tsx$/, "") || "/";
}

/**
 * No source builds a `/en/` URL.
 *
 * Root and `/en/` were byte-identical English trees, both `index,follow` and
 * both self-canonical. `localePath` now returns the unprefixed path for the
 * default locale, which removes the prefix from every href, canonical,
 * hreflang entry and sitemap at once -- but only while nothing hand-writes it.
 */
function checkNoEnglishPrefix(file, source, failures) {
  for (const match of source.matchAll(/["'`]\/en\//g)) {
    failures.push({ file, reason: "hardcoded_en_prefix", detail: source.slice(match.index, match.index + 40).split("\n")[0] });
  }
}

/**
 * Nothing links to `/matches/{id}`.
 *
 * It answers with a permanent redirect. An internal link to it costs a crawl
 * hop and returns no index value. `fixtureHref` is the single place a fixture
 * URL is built. `/matches/today` is a distinct hub and stays.
 */
function checkNoRetiredFixtureLinks(file, source, failures) {
  if (FIXTURE_URL_OWNERS.some((owner) => file.endsWith(owner))) return;
  for (const match of source.matchAll(/["'`]\/matches\/(?!today)[^"'`]*/g)) {
    // `?next=/matches/...` is an auth return target on a noindex page, not a
    // crawlable link.
    const line = source.slice(source.lastIndexOf("\n", match.index) + 1, source.indexOf("\n", match.index));
    if (/next=/.test(line)) continue;
    failures.push({ file, reason: "links_retired_fixture_route", detail: match[0].slice(0, 60) });
  }
}

/** A Go function whose name says it handles mail. */
const GO_EMAIL_FUNCTION = /func (?:\([^)]*\) )?([A-Za-z_0-9]*(?:[Ee]mail|[Mm]ail)[A-Za-z_0-9]*)\(/g;

/**
 * Go packages that build outbound email.
 *
 * Email is the only place SokaLens URLs are published outside the site, and
 * these checks read source in a different language and a different service to
 * everything above -- deliberately. The web gate cannot see a kickoff reminder
 * that links `/matches/1234567`, because that body is assembled in Go, and a
 * redirect hop shipped to every inbox is exactly the defect the fixture-route
 * retirement was for.
 */
const GO_EMAIL_DIRS = [
  "services/api/internal/httpserver",
  "services/api/internal/usernotifications",
  "services/worker/internal/notifications",
];

/** The one Go package allowed to spell a site path out. */
const GO_EMAIL_LINK_OWNER = "services/api/internal/emaillinks/links.go";

/**
 * Every indexable page declares a canonical.
 *
 * A page Google may index without one is a page whose canonical Google picks
 * for itself, which is how 105 URLs ended up canonicalised to something their
 * author did not choose.
 */
function checkIndexablePagesDeclareCanonical(file, source, failures) {
  if (!isPageFile(file)) return;
  const route = routeOf(file);
  if (NOINDEX_ROUTES.test(route)) return;
  if (/robots:\s*\{[^}]*index:\s*false[^}]*\}/.test(source) && !/index:\s*(?:true|Boolean|hasPrediction|hasContent)/.test(source)) return;
  // A route that only redirects renders no <head> of its own.
  if (/permanentRedirect\(/.test(source) && !/export (?:const metadata|async function generateMetadata)/.test(source)) return;
  if (!/export (?:const metadata|async function generateMetadata)/.test(source)) {
    failures.push({ file, reason: "indexable_page_without_metadata" });
    return;
  }
  if (!/alternates:\s*\{[\s\S]{0,400}?canonical/.test(source)) {
    failures.push({ file, reason: "indexable_page_without_canonical", detail: route });
  }
}

/** Sitemaps go through the shared builder, which is where the rules live. */
function checkSitemapsUseTheSharedBuilder(file, source, failures) {
  if (!/^sitemaps?[./]/.test(file) && !file.startsWith("sitemap.xml/")) return;
  if (/<changefreq>|<priority>/.test(source)) {
    failures.push({ file, reason: "sitemap_emits_changefreq_or_priority" });
  }
  if (/<urlset|<sitemapindex/.test(source) && !file.includes("lib/")) {
    failures.push({ file, reason: "sitemap_builds_xml_by_hand" });
  }
}

/**
 * A sitemap that reads live data is never prerendered.
 *
 * `next build` runs with no prediction API reachable. A route handler with no
 * `request` parameter is statically prerendered by default, so a data-reading
 * sitemap either fails the build (which is how this was caught) or, worse,
 * succeeds and bakes an empty document that tells Google the property has no
 * URLs. Routes that take `request` are already dynamic and need no marker.
 */
function checkLiveSitemapsAreDynamic(file, source, failures) {
  if (!/^(sitemaps\/[^/]+\/route\.ts|sitemap\.xml\/route\.ts)$/.test(file)) return;
  const readsLiveData = /\bload[A-Z]\w*Urls\b|\bindexableMarketHubs\b/.test(source);
  const takesRequest = /GET\(\s*request\b/.test(source);
  if (readsLiveData && !takesRequest && !/export const dynamic = "force-dynamic"/.test(source)) {
    failures.push({ file, reason: "live_sitemap_would_be_prerendered_at_build_time" });
  }
}

/**
 * Never put a provider value straight into an `<img src>`.
 *
 * A missing crest does not always arrive as null -- it can arrive as the four
 * characters `null`, which is truthy, so every truthiness guard passed it
 * through and `src="null"` reached the browser. A relative src resolves
 * against the current directory, so fixture pages requested
 * `/predictions/{league}/null` and team pages `/teams/null`: roughly 14,000
 * 404s a day, mostly Meta's crawler fetching link previews.
 *
 * `imageSrc` returns undefined for anything that is not an absolute URL or a
 * root-relative path, and React omits an undefined attribute entirely.
 */
function checkImagesUseTheGuardedSource(file, source, failures) {
  if (!/\.tsx$/.test(file)) return;
  // Share cards are rasterised into a PNG by satori, not rendered into the DOM.
  // They guard the value too -- through `imageSrc` in lib/shareCard.tsx -- but
  // they bind it via a variable, because a bad URL there fails the whole image
  // rather than one crest.
  if (isShareCard(file)) return;
  for (const match of source.matchAll(/<img[^>]*\ssrc=\{([^}]*)\}/g)) {
    if (!/^\s*imageSrc\(/.test(match[1])) {
      failures.push({ file, reason: "img_src_not_guarded", detail: match[1].trim().slice(0, 60) });
    }
  }
}

/**
 * A kickoff must reach the markup as a <LocalTime />, never as text the server
 * already formatted.
 *
 * Sixteen call sites rendered `formatKickoff(...)` straight into JSX. That
 * string is fixed at render time on a UTC server and no browser pass can
 * correct it, because nothing marks it as a time -- so those sites showed UTC
 * to every reader while the LocalTime ones showed the reader's own zone. One
 * fixture page printed 3:00 PM in its header and 12:00 on its scoreboard for
 * the same kickoff. The edge cache makes per-request formatting impossible, so
 * client-side correction is the only option and this is what keeps every site
 * on it.
 *
 * `formatInstant` is the one formatter, and LocalTime is its only caller.
 */
function checkKickoffsAreLocalised(file, source, failures) {
  if (!/\.tsx?$/.test(file)) return;
  if (file.endsWith("components/LocalTime.tsx") || file.endsWith("lib/format.ts")) return;
  // A share card is a PNG. It is rasterised once on the server and then cached
  // by whatever chat app the link was pasted into, so there is no client pass
  // that could ever correct it -- which is the entire mechanism this check
  // exists to protect. Formatting in UTC with the zone printed on the card is
  // the honest answer there; an unlabelled local time would be wrong for most
  // of the people who see it.
  if (isShareCard(file)) return;
  if (/formatInstant\s*\(/.test(source)) {
    failures.push({ file, reason: "kickoff_formatted_on_server", detail: "call <LocalTime /> instead of formatInstant()" });
  }
}

/**
 * `opengraph-image` routes and the renderer they share produce a PNG on the
 * server. DOM-level rules about loading and preloading do not apply to them.
 */
function isShareCard(file) {
  return /(^|\/)opengraph-image\.tsx$/.test(file) || /(^|\/)shareCard\.tsx$/.test(file);
}

/**
 * Extracts the body of a Go function: from its signature to the next top-level
 * declaration.
 *
 * Brace matching would be the obvious approach and is the wrong one here. A
 * brace inside a string literal -- []byte("{}"), a JSON template, a format
 * string -- unbalances the count, and the failure is silent in the direction
 * that matters: the extracted body ends early and a hand-written URL after that
 * point is never seen. A gate that misses what it exists to catch is worse than
 * no gate.
 *
 * gofmt puts every top-level declaration at column zero, so the next func,
 * type, var or const there is an unambiguous terminator that no string literal
 * can forge.
 */
function goFunctionBody(source, signatureIndex) {
  const rest = source.slice(signatureIndex);
  const next = rest.slice(1).search(/\n(?:func|type|var|const)\s/);
  return next === -1 ? rest : rest.slice(0, next + 1);
}

/**
 * No Go email function writes a site path by hand.
 *
 * `services/api/internal/emaillinks` owns every URL shape an email may use,
 * the same way `lib/fixtureHref.ts` owns them for the web app, and it is
 * asserted against `packages/contracts/seo/email-link-cases.json` so the two
 * languages cannot drift. A literal path here is a URL nothing validates: it
 * survives a slug change, a route retirement, and a canonical consolidation,
 * and the first evidence is a reader landing on a 404.
 *
 * The check keys on the function name rather than a registry, so a new
 * `queueDigestEmail` is covered the moment it is written.
 */
function checkGoEmailFunctionsUseTheLinkBuilder(file, source, failures) {
  for (const match of source.matchAll(GO_EMAIL_FUNCTION)) {
    const body = goFunctionBody(source, match.index);
    for (const literal of body.matchAll(/"(\/[a-z0-9][^"]*)"/g)) {
      failures.push({ file, reason: "email_builds_a_url_by_hand", detail: `${match[1]}: ${literal[1].slice(0, 60)}` });
    }
  }
}

/** Exposed so the suite can prove each check fires on the shape it forbids. */
export const checks = {
  checkGoEmailFunctionsUseTheLinkBuilder,
  checkImagesUseTheGuardedSource,
  checkKickoffsAreLocalised,
  checkIndexablePagesDeclareCanonical,
  checkLiveSitemapsAreDynamic,
  checkNoEnglishPrefix,
  checkNoRetiredFixtureLinks,
  checkSitemapsUseTheSharedBuilder,
};

export { stripComments };

export async function collectFailures() {
  const failures = [];
  for (const file of await sourceFiles()) {
    const raw = await readFile(new URL(file, APP_DIR), "utf8");
    const source = stripComments(raw);
    checkNoEnglishPrefix(file, source, failures);
    checkNoRetiredFixtureLinks(file, source, failures);
    checkIndexablePagesDeclareCanonical(file, source, failures);
    checkSitemapsUseTheSharedBuilder(file, source, failures);
    checkLiveSitemapsAreDynamic(file, source, failures);
    checkImagesUseTheGuardedSource(file, source, failures);
    checkKickoffsAreLocalised(file, source, failures);
  }

  const proxy = stripComments(await readFile(new URL("../proxy.ts", import.meta.url), "utf8"));
  if (!/pathLocale === defaultLocale/.test(proxy) || !/NextResponse\.redirect\(url, 308\)/.test(proxy)) {
    failures.push({ file: "proxy.ts", reason: "en_prefix_no_longer_redirects" });
  }

  const config = stripComments(await readFile(new URL("../app/i18n/config.ts", import.meta.url), "utf8"));
  if (!/if \(locale === defaultLocale\) return/.test(config)) {
    failures.push({ file: "app/i18n/config.ts", reason: "localePath_would_reemit_the_en_prefix" });
  }

  failures.push(...(await collectGoEmailFailures()));

  return failures;
}

async function main() {
  const failures = await collectFailures();
  if (failures.length > 0) {
    console.error(JSON.stringify({ failures }, null, 2));
    console.error(`\n${failures.length} SEO invariant failure(s).`);
    process.exitCode = 1;
    return;
  }
  console.log("SEO invariants hold: no /en/ URLs, no links to the retired fixture route, every indexable page declares a canonical, sitemaps use the shared builder and are never prerendered against a missing API.");
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  });
}

/** Scans the Go email surfaces. Kept separate so the suite can drive it. */
export async function collectGoEmailFailures() {
  const failures = [];
  try {
    await stat(new URL(GO_EMAIL_LINK_OWNER, REPO_DIR));
  } catch {
    failures.push({ file: GO_EMAIL_LINK_OWNER, reason: "email_link_owner_missing" });
  }
  for (const directory of GO_EMAIL_DIRS) {
    const base = new URL(`${directory}/`, REPO_DIR);
    let entries = [];
    try {
      entries = await readdir(base, { recursive: true });
    } catch {
      continue;
    }
    for (const entry of entries.map((value) => value.replaceAll("\\", "/")).sort()) {
      if (!entry.endsWith(".go") || entry.endsWith("_test.go")) continue;
      const source = stripComments(await readFile(new URL(entry, base), "utf8"));
      checkGoEmailFunctionsUseTheLinkBuilder(`${directory}/${entry}`, source, failures);
    }
  }
  return failures;
}

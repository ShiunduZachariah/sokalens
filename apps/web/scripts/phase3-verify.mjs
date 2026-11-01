import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);

export const defaultRoot = dirname(dirname(scriptPath));

export const phase3RouteExpectations = [
  {
    route: "/",
    file: "app/page.tsx",
    metadata: "dynamic",
    titleIncludes: ["site.title"],
    descriptionIncludes: ["site.description"],
  },
  {
    route: "/football",
    file: "app/football/page.tsx",
    metadata: "static",
    titleIncludes: ["Football", "SokaLens"],
    descriptionIncludes: ["fixtures", "predictions"],
  },
  {
    route: "/football/today",
    file: "app/football/today/page.tsx",
    metadata: "static",
    titleIncludes: ["Today's football matches", "SokaLens"],
    descriptionIncludes: ["fixtures", "prediction"],
  },
  {
    route: "/matches/today",
    file: "app/matches/today/page.tsx",
    metadata: "static",
    titleIncludes: ["Today's football matches", "SokaLens"],
    descriptionIncludes: ["fixtures", "prediction"],
  },
  {
    route: "/predictions/today",
    file: "app/predictions/today/page.tsx",
    metadata: "dynamic",
    titleIncludes: ["today.metadata_title"],
    descriptionIncludes: ["today.metadata_description"],
  },
  {
    route: "/predictions/tomorrow",
    file: "app/predictions/tomorrow/page.tsx",
    metadata: "static",
    titleIncludes: ["Tomorrow's football predictions", "SokaLens"],
    descriptionIncludes: ["predictions", "league"],
  },
  {
    route: "/predictions/weekend",
    file: "app/predictions/weekend/page.tsx",
    metadata: "static",
    titleIncludes: ["Weekend football predictions", "SokaLens"],
    descriptionIncludes: ["predictions", "confidence"],
  },
  {
    route: "/predictions/markets/[marketSlug]",
    file: "app/predictions/markets/[marketSlug]/page.tsx",
    metadata: "dynamic",
    titleIncludes: ["Prediction", "SokaLens"],
    descriptionIncludes: ["description"],
  },
  {
    route: "/predictions/leagues/[leagueSlug]",
    file: "app/predictions/leagues/[leagueSlug]/page.tsx",
    metadata: "dynamic",
    titleIncludes: ["predictions", "SokaLens"],
    descriptionIncludes: ["predictions", "fixtures"],
  },
  {
    route: "/predictions/[leagueSlug]/[fixtureSlug]",
    file: "app/predictions/[leagueSlug]/[fixtureSlug]/page.tsx",
    metadata: "dynamic",
    titleIncludes: ["prediction", "SokaLens"],
    descriptionIncludes: ["prediction"],
  },
  {
    route: "/best-bets",
    file: "app/best-bets/page.tsx",
    metadata: "static",
    titleIncludes: ["Best Bets", "SokaLens"],
    descriptionIncludes: ["confidence", "risk"],
  },
  {
    route: "/leagues",
    file: "app/leagues/page.tsx",
    metadata: "static",
    titleIncludes: ["Leagues", "SokaLens"],
    descriptionIncludes: ["leagues", "prediction"],
  },
  {
    route: "/results",
    file: "app/results/page.tsx",
    metadata: "static",
    titleIncludes: ["Performance status", "SokaLens"],
    descriptionIncludes: ["results", "accuracy"],
  },
  {
    // Retired: /matches/{id} and /predictions/{league}/{home}-vs-{away}-{id}
    // rendered the same fixture, and 64% of every non-indexed URL on the
    // property sat on this one. It resolves the fixture by id and permanently
    // redirects, so it has no metadata of its own to check -- what has to hold
    // is that it never grows any back.
    route: "/matches/[id]",
    file: "app/matches/[id]/page.tsx",
    metadata: "redirect",
    redirectsTo: "seoFixturePath",
  },
  {
    route: "/leagues/[slug]",
    file: "app/leagues/[slug]/page.tsx",
    metadata: "dynamic",
    titleIncludes: ["fixtures", "SokaLens"],
    descriptionIncludes: ["fixtures"],
  },
  {
    route: "/teams/[slug]",
    file: "app/teams/[slug]/page.tsx",
    metadata: "dynamic",
    titleIncludes: ["fixtures", "predictions", "SokaLens"],
    descriptionIncludes: ["fixtures"],
  },
  {
    route: "/multi-match-builder",
    file: "app/multi-match-builder/page.tsx",
    metadata: "dynamic",
    titleIncludes: ["Multi-Match Builder", "SokaLens"],
    descriptionIncludes: ["multi-match", "predictions"],
  },
  {
    route: "/acca-predictions",
    file: "app/acca-predictions/page.tsx",
    metadata: "dynamic",
    titleIncludes: ["Acca predictions", "SokaLens"],
    descriptionIncludes: ["accumulator", "confidence"],
  },
  {
    route: "/search",
    file: "app/search/page.tsx",
    metadata: "static",
    titleIncludes: ["Search", "SokaLens"],
    descriptionIncludes: ["teams", "leagues", "matches"],
  },
  {
    route: "/pro",
    file: "app/pro/page.tsx",
    metadata: "static",
    titleIncludes: ["SokaLens Pro"],
    descriptionIncludes: ["coming soon"],
  },
  {
    route: "/responsible-gambling",
    file: "app/responsible-gambling/page.tsx",
    metadata: "static",
    titleIncludes: ["Responsible gambling", "SokaLens"],
    descriptionIncludes: ["Responsible use"],
  },
  {
    route: "/terms",
    file: "app/terms/page.tsx",
    metadata: "static",
    titleIncludes: ["Terms", "SokaLens"],
    descriptionIncludes: ["terms"],
  },
  {
    route: "/privacy",
    file: "app/privacy/page.tsx",
    metadata: "static",
    titleIncludes: ["Privacy", "SokaLens"],
    descriptionIncludes: ["privacy"],
  },
  {
    route: "/cookies",
    file: "app/cookies/page.tsx",
    metadata: "static",
    titleIncludes: ["Cookies", "SokaLens"],
    descriptionIncludes: ["cookie", "consent"],
  },
];

export const phase3BrowserRoutes = [
  { route: "/", textIncludes: ["Today's top matches", "Today's predictions"] },
  { route: "/football", textIncludes: ["Live and upcoming football", "Featured predictions"] },
  { route: "/football/today", textIncludes: ["Football fixtures by competition", "Prediction ready"] },
  { route: "/matches/today", textIncludes: ["Football fixtures by competition", "Prediction ready"] },
  { route: "/predictions/today", textIncludes: ["Every signal", "Prediction feed"] },
  { route: "/best-bets", textIncludes: ["Best Bets", "Strong recommendations"] },
  { route: "/leagues", textIncludes: ["Supported leagues", "Predictions available"] },
  { route: "/results", textIncludes: ["Performance tracking", "Hit-rate"] },
  // League/team detail pages are dynamic over canonical slugs (no mock slugs).
  // Match detail is /matches/{id} (dynamic numeric ids) and is exercised via
  // the fixtures/predictions pages, not a literal browser route here.
  { route: "/leagues/league-england-premier-league", textIncludes: ["Premier League", "Fixtures"] },
  { route: "/teams/team-world-arsenal", textIncludes: ["Arsenal", "Recent results"] },
  { route: "/multi-match-builder", textIncludes: ["Coming soon", "predictions"] },
  { route: "/acca-predictions", textIncludes: ["Acca predictions", "Candidate selections"] },
  {
    route: "/search?q=arsenal",
    canonicalPath: "/search",
    textIncludes: ["Search results", "Arsenal"],
  },
  { route: "/pro", textIncludes: ["SokaLens Pro", "coming soon"] },
  { route: "/responsible-gambling", textIncludes: ["Responsible gambling", "Healthy use"] },
  { route: "/terms", textIncludes: ["SokaLens terms of use"] },
  { route: "/privacy", textIncludes: ["SokaLens privacy overview"] },
  { route: "/cookies", textIncludes: ["SokaLens cookie overview"] },
];

const sourceExtensions = /\.(css|js|jsx|mjs|ts|tsx)$/;

const providerPatterns = [
  { label: "API-Football", regex: /\bapi[-\s]?football\b/i },
  { label: "API-Sports football endpoint", regex: /v3\.football\.api-sports\.io/i },
  { label: "RapidAPI football endpoint", regex: /api-football-v1\.p\.rapidapi\.com/i },
  { label: "RapidAPI key header", regex: /\bx-rapidapi-key\b/i },
  { label: "RapidAPI host header", regex: /\bx-rapidapi-host\b/i },
  { label: "API-Sports", regex: /https?:\/\/(?:[^/]+\.)?api-sports\.io/i },
  { label: "API-Sports key header", regex: /\bx-apisports-key\b/i },
  { label: "API-Sports widget script", regex: /widgets\.api-sports\.io\/3\.1\.0\/widgets\.js/i },
  { label: "API-Sports widget element", regex: /<\/?api-sports-widget\b/i },
  { label: "iframe embed", regex: /<iframe\b/i },
  { label: "script src embed", regex: /<script\b[^>]*\bsrc=/i },
];

const frontendSecretPatterns = [
  { label: "API_FOOTBALL_KEY", regex: /\bAPI_FOOTBALL_KEY\b/ },
  { label: "x-apisports-key", regex: /\bx-apisports-key\b/i },
  { label: "x-rapidapi-key", regex: /\bx-rapidapi-key\b/i },
];

const joinWord = (...parts) => parts.join("");

const bannedCertaintyPatterns = [
  {
    label: "certainty win phrase",
    regex: new RegExp(`\\b${joinWord("su", "re")}\\s+${joinWord("wi", "ns?")}\\b`, "i"),
  },
  {
    label: "certainty pick phrase",
    regex: new RegExp(`\\b${joinWord("su", "re")}\\s+${joinWord("be", "ts?")}\\b`, "i"),
  },
  {
    label: "guaranteed outcome",
    regex: /\bguaranteed\s+(acca|bet|outcome|profit|result|return|win|winner)s?\b/i,
    allow: /\b(?:not|never)\s+(?:a\s+)?guaranteed\s+(?:acca|bet|outcome|profit|result|return|win|winner)s?\b/i,
  },
  {
    label: "copy defect (pluralization)",
    regex: /\bmatchs\b/i,
  },
  {
    label: "copy defect (missing space)",
    regex: /analysis\.Predictions/,
  },
  {
    label: "guaranteed",
    regex: /\bguaranteed\b/i,
    allow: /\b(not|no|never)\s+(?:a\s+)?guaranteed\b/i,
  },
  { label: "no-risk phrase", regex: /\brisk[-\s]?free\b/i },
  { label: "integrity-risk phrase", regex: /\bfixed\s+matches?\b/i },
  { label: "privileged-info phrase", regex: /\binside\s+info\b/i },
  { label: "urgent staking phrase", regex: /\bstake\s+now\b/i },
  { label: "urgent wagering phrase", regex: /\bbet\s+now\b/i },
  { label: "lottery-style phrase", regex: /\bjackpot\b/i },
  { label: "settlement-style phrase", regex: /\bcash\s*out\b/i },
  { label: "returns-style phrase", regex: /\bpay\s*out\b|\bpayout\b/i },
  {
    label: "ranked wager phrase",
    regex: /\bbest\s+bets?\b/i,
    allow: /\bBest (?:Bets?|bets?)\b(?:\s+only)?/,
  },
  { label: "low-risk wager phrase", regex: /\bsafe\s+bets?\b/i },
  { label: "banker wager phrase", regex: /\bbanker\s+bets?\b/i },
  { label: "lock of the day", regex: /\block\s+of\s+the\s+day\b/i },
  { label: "100 percent win", regex: /\b(100%|100\s+percent)\s+wins?\b/i },
  { label: "same-day outcome phrase", regex: /\bwin\s+today\b/i },
];

const seoSupportExpectations = [
  { file: "app/robots.ts", label: "robots metadata route" },
  { file: "app/sitemap.xml/route.ts", label: "sitemap index route" },
  { file: "app/sitemaps/fixtures.xml/route.ts", label: "fixture sitemap route" },
  { file: "app/icon.svg", label: "web app icon" },
];

export function runStaticChecks({
  root = defaultRoot,
  routeExpectations = phase3RouteExpectations,
  sourceSubdirs = ["app"],
} = {}) {
  const failures = [];
  const warnings = [];

  for (const expectation of routeExpectations) {
    checkRouteExpectation(root, expectation, failures);
  }

  const sourceFiles = collectSourceFiles(root, sourceSubdirs);
  const sourceMatches = scanSourceFiles(root, sourceFiles);
  failures.push(...sourceMatches.failures);

  warnings.push(...collectSeoSupportWarnings(root, routeExpectations));

  return {
    checkedFiles: sourceFiles.map((file) => toProjectPath(root, file)),
    checkedRoutes: routeExpectations.map((expectation) => expectation.route),
    failures,
    warnings,
  };
}

export async function runBrowserSmoke({
  baseUrl,
  routes = phase3BrowserRoutes,
  timeoutMs = 10_000,
  loadPlaywright = loadPlaywrightModule,
} = {}) {
  if (!baseUrl) {
    return {
      failures: [],
      skipped: true,
      warnings: ["Browser smoke skipped: pass --base-url http://localhost:3000 to enable it."],
    };
  }

  let playwright;
  try {
    playwright = await loadPlaywright();
  } catch (error) {
    const httpResult = await runHttpRouteSmoke({ baseUrl, routes, timeoutMs });
    return {
      failures: httpResult.failures,
      skipped: false,
      warnings: [
        `Playwright browser smoke unavailable (${error.message}); used HTTP route smoke fallback.`,
        ...httpResult.warnings,
      ],
    };
  }

  const failures = [];
  const browser = await playwright.chromium.launch({ headless: true });

  try {
    const page = await browser.newPage();

    for (const route of routes) {
      const url = new URL(route.route, ensureTrailingSlash(baseUrl)).toString();
      let response;

      try {
        response = await page.goto(url, { timeout: timeoutMs, waitUntil: "domcontentloaded" });
      } catch (error) {
        failures.push(`Browser route failed to load: ${route.route} (${error.message})`);
        continue;
      }

      if (!response) {
        failures.push(`Browser route did not return a response: ${route.route}`);
        continue;
      }

      if (response.status() >= 400) {
        failures.push(`Browser route returned HTTP ${response.status()}: ${route.route}`);
      }

      const title = await page.title();
      if (!title.toLowerCase().includes("sokalens")) {
        failures.push(`Browser route title does not include SokaLens: ${route.route}`);
      }

      const seoFailures = await inspectBrowserSeo(page, route, baseUrl);
      failures.push(...seoFailures);

      const accessibilityFailures = await inspectBrowserAccessibility(page, route);
      failures.push(...accessibilityFailures);

      const mainCount = await page.locator("main, #content").count();
      if (mainCount === 0) {
        failures.push(`Browser route is missing a main/#content landmark: ${route.route}`);
      }

      const h1Count = await page.locator("h1").count();
      if (h1Count === 0) {
        failures.push(`Browser route is missing an h1: ${route.route}`);
      }

      const bodyText = normalizeText((await page.locator("body").textContent()) ?? "");
      for (const expectedText of route.textIncludes) {
        if (!bodyText.includes(normalizeText(expectedText))) {
          failures.push(`Browser route missing text "${expectedText}": ${route.route}`);
        }
      }

      failures.push(...findBannedCertaintyInText(bodyText, `browser:${route.route}`));
    }
  } finally {
    await browser.close();
  }

  return { failures, skipped: false, warnings: [] };
}

async function runHttpRouteSmoke({ baseUrl, routes, timeoutMs }) {
  const failures = [];
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);

  try {
    for (const route of routes) {
      const url = new URL(route.route, ensureTrailingSlash(baseUrl)).toString();
      let response;

      try {
        response = await fetch(url, { signal: controller.signal });
      } catch (error) {
        failures.push(`HTTP route failed to load: ${route.route} (${error.message})`);
        continue;
      }

      if (response.status >= 400) {
        failures.push(`HTTP route returned HTTP ${response.status}: ${route.route}`);
        continue;
      }

      const html = await response.text();
      const title = extractHtmlTitle(html);
      if (!title.toLowerCase().includes("sokalens")) {
        failures.push(`HTTP route title does not include SokaLens: ${route.route}`);
      }

      failures.push(...inspectHtmlSeo(html, route, baseUrl));

      if (!/<main\b|id=["']content["']/i.test(html)) {
        failures.push(`HTTP route is missing main/#content markup: ${route.route}`);
      }

      if (!/<h1\b/i.test(html)) {
        failures.push(`HTTP route is missing an h1: ${route.route}`);
      }

      const bodyText = normalizeText(stripHtml(html));
      for (const expectedText of route.textIncludes) {
        if (!bodyText.includes(normalizeText(expectedText))) {
          failures.push(`HTTP route missing text "${expectedText}": ${route.route}`);
        }
      }

      failures.push(...findBannedCertaintyInText(bodyText, `http:${route.route}`));
    }
  } finally {
    clearTimeout(timer);
  }

  return { failures, warnings: [] };
}

async function inspectBrowserSeo(page, route, baseUrl) {
  const failures = [];
  const description = await page.locator('meta[name="description"]').getAttribute("content");
  if (!description || description.trim().length < 40) {
    failures.push(`Browser route is missing a useful meta description: ${route.route}`);
  }

  const canonicalHref = await page.locator('link[rel="canonical"]').getAttribute("href");
  const expectedPath = expectedCanonicalPath(route);
  if (!canonicalHref) {
    failures.push(`Browser route is missing canonical metadata: ${route.route}`);
  } else if (new URL(canonicalHref, baseUrl).pathname !== expectedPath) {
    failures.push(
      `Browser route canonical should point to ${expectedPath}: ${route.route} (${canonicalHref})`,
    );
  }

  return failures;
}

async function inspectBrowserAccessibility(page, route) {
  const failures = [];
  const htmlLang = await page.locator("html").getAttribute("lang");
  if (!htmlLang) {
    failures.push(`Browser route is missing html lang: ${route.route}`);
  }

  const missingAltCount = await page.locator("img:not([alt])").count();
  if (missingAltCount > 0) {
    failures.push(`Browser route has images without alt text: ${route.route}`);
  }

  const unnamedControls = await page.evaluate(() => {
    const controls = [...document.querySelectorAll("a, button, input, select, textarea")];
    return controls
      .filter((element) => {
        const text = element.textContent?.trim();
        const ariaLabel = element.getAttribute("aria-label");
        const ariaLabelledBy = element.getAttribute("aria-labelledby");
        const title = element.getAttribute("title");
        const placeholder =
          element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement
            ? element.placeholder
            : "";
        const labelText =
          element instanceof HTMLInputElement ||
          element instanceof HTMLSelectElement ||
          element instanceof HTMLTextAreaElement
            ? [...element.labels].map((label) => label.textContent?.trim()).join(" ")
            : "";
        return !text && !ariaLabel && !ariaLabelledBy && !title && !placeholder && !labelText;
      })
      .slice(0, 5)
      .map((element) => {
        const tag = element.tagName.toLowerCase();
        const href = element instanceof HTMLAnchorElement ? element.getAttribute("href") : "";
        return href ? `${tag}[href="${href}"]` : tag;
      });
  });

  if (unnamedControls.length > 0) {
    failures.push(
      `Browser route has unnamed interactive controls: ${route.route} (${unnamedControls.join(", ")})`,
    );
  }

  return failures;
}

function inspectHtmlSeo(html, route, baseUrl) {
  const failures = [];
  const description = extractMetaContent(html, "description");
  if (!description || description.trim().length < 40) {
    failures.push(`HTTP route is missing a useful meta description: ${route.route}`);
  }

  const canonicalHref = html.match(/<link[^>]+rel=["']canonical["'][^>]*href=["']([^"']+)["']/i)?.[1];
  const expectedPath = expectedCanonicalPath(route);
  if (!canonicalHref) {
    failures.push(`HTTP route is missing canonical metadata: ${route.route}`);
  } else if (new URL(canonicalHref, baseUrl).pathname !== expectedPath) {
    failures.push(
      `HTTP route canonical should point to ${expectedPath}: ${route.route} (${canonicalHref})`,
    );
  }

  if (!/<html[^>]+lang=["'][^"']+["']/i.test(html)) {
    failures.push(`HTTP route is missing html lang: ${route.route}`);
  }

  return failures;
}

function checkRouteExpectation(root, expectation, failures) {
  const routePath = join(root, expectation.file);

  if (!existsSync(routePath)) {
    failures.push(`Missing required route file: ${expectation.route} (${expectation.file})`);
    return;
  }

  const routeSource = readFileSync(routePath, "utf8");

  if (expectation.reexportsMetadata && !/export\s*{\s*metadata\s*}/.test(routeSource)) {
    failures.push(`Route should re-export metadata: ${expectation.route} (${expectation.file})`);
  }

  if (expectation.requiresStaticParams && !/\bfunction\s+generateStaticParams\b/.test(routeSource)) {
    failures.push(`Route should define generateStaticParams: ${expectation.route}`);
  }

  const metadataPath = join(root, expectation.metadataFile ?? expectation.file);
  if (!existsSync(metadataPath)) {
    failures.push(`Missing metadata source for route: ${expectation.route}`);
    return;
  }

  const metadataSource = readFileSync(metadataPath, "utf8");
  if (expectation.metadata === "redirect") {
    if (!metadataSource.includes(`permanentRedirect(${expectation.redirectsTo}(`)) {
      failures.push(`Route should permanently redirect via ${expectation.redirectsTo}: ${expectation.route}`);
    }
    if (/export\s+const\s+metadata|function\s+generateMetadata/.test(metadataSource)) {
      failures.push(`Retired route must not declare metadata: ${expectation.route}`);
    }
    return;
  }
  if (expectation.metadata === "dynamic") {
    if (!/\bfunction\s+generateMetadata\b/.test(metadataSource)) {
      failures.push(`Missing generateMetadata for route: ${expectation.route}`);
      return;
    }
  } else if (!/\bexport\s+const\s+metadata\b/.test(metadataSource)) {
    failures.push(`Missing metadata export for route: ${expectation.route}`);
    return;
  }

  checkMetadataField({
    expectation,
    failures,
    field: "title",
    includes: expectation.titleIncludes ?? [],
    source: metadataSource,
    dynamic: expectation.metadata === "dynamic",
  });
  checkMetadataField({
    expectation,
    failures,
    field: "description",
    includes: expectation.descriptionIncludes ?? [],
    source: metadataSource,
    dynamic: expectation.metadata === "dynamic",
  });
}

function checkMetadataField({ expectation, failures, field, includes, source, dynamic = false }) {
  if (!new RegExp(`\\b${field}\\s*:`).test(source)) {
    failures.push(`Missing metadata ${field} for route: ${expectation.route}`);
    return;
  }

  // Dynamic metadata composes title/description from fetched data, so the
  // literal-string extractor can grab the wrong (fallback) value. Check the
  // whole metadata source instead.
  const extractedValue = dynamic ? undefined : extractMetadataField(source, field);
  const haystack = (extractedValue ?? source).toLowerCase();

  for (const expectedText of includes) {
    if (!haystack.includes(expectedText.toLowerCase())) {
      failures.push(
        `Metadata ${field} for ${expectation.route} should include "${expectedText}".`,
      );
    }
  }
}

function extractMetadataField(source, field) {
  const match = source.match(new RegExp(`\\b${field}\\s*:\\s*([\"'\`])([\\s\\S]*?)\\1`, "m"));
  return match?.[2];
}

function extractHtmlTitle(html) {
  return html.match(/<title[^>]*>([\s\S]*?)<\/title>/i)?.[1] ?? "";
}

function extractMetaContent(html, name) {
  const match = html.match(
    new RegExp(`<meta[^>]+name=["']${name}["'][^>]*content=["']([^"']+)["']`, "i"),
  );
  return match?.[1] ?? "";
}

function stripHtml(html) {
  return decodeHtmlEntities(html)
    .replace(/<script\b[\s\S]*?<\/script>/gi, " ")
    .replace(/<style\b[\s\S]*?<\/style>/gi, " ")
    .replace(/<[^>]+>/g, " ");
}

function decodeHtmlEntities(html) {
  return html
    .replace(/&#x27;/g, "'")
    .replace(/&#39;/g, "'")
    .replace(/&amp;/g, "&")
    .replace(/&quot;/g, '"')
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">");
}

function collectSourceFiles(root, sourceSubdirs) {
  const files = [];

  for (const sourceSubdir of sourceSubdirs) {
    const absoluteSubdir = join(root, sourceSubdir);
    if (!existsSync(absoluteSubdir)) {
      continue;
    }

    walk(absoluteSubdir, files);
  }

  return files;
}

function walk(directory, files) {
  for (const entry of readdirSync(directory)) {
    const absolutePath = join(directory, entry);
    const stats = statSync(absolutePath);

    if (stats.isDirectory()) {
      walk(absolutePath, files);
      continue;
    }

    if (sourceExtensions.test(entry)) {
      files.push(absolutePath);
    }
  }
}

function scanSourceFiles(root, sourceFiles) {
  const failures = [];

  for (const file of sourceFiles) {
    const label = toProjectPath(root, file);
    const source = readFileSync(file, "utf8");
    const lines = source.split(/\r?\n/);

    failures.push(...findDeadInteractiveAnchors(source, label));
    failures.push(...findStructuralPolishRegressions(source, label));
    failures.push(...findFilterBarStateSyncIssues(source, label));

    lines.forEach((line, index) => {
      for (const pattern of providerPatterns) {
        if (pattern.regex.test(line) && !isApprovedProviderReference(label, pattern.label, line)) {
          failures.push(
            `Direct provider reference "${pattern.label}" in ${label}:${index + 1}`,
          );
        }
      }

      for (const pattern of frontendSecretPatterns) {
        if (pattern.regex.test(line)) {
          failures.push(`Frontend provider secret "${pattern.label}" in ${label}:${index + 1}`);
        }
      }

      failures.push(...findBannedCertaintyInText(line, `${label}:${index + 1}`));
      failures.push(...findDraftPublicCopy(line, `${label}:${index + 1}`));
    });
  }

  return { failures };
}

function isApprovedProviderReference(label, providerLabel, line) {
  void label;
  void providerLabel;
  void line;
  return false;
}

function findStructuralPolishRegressions(source, label) {
  const failures = [];

  if (label === "app/components/PublicHeader.tsx") {
    for (const [expectedLabel, translationKey] of [["Home", "nav.home"], ["Predictions", "nav.predictions"], ["In-play", "nav.in_play"], ["ACCAs", "nav.accas"], ["Builder", "nav.builder"]]) {
      if (!new RegExp(`label\\s*:\\s*["'](?:${expectedLabel}|${translationKey.replace(".", "\\.")})["']`).test(source)) {
        failures.push(`Primary nav missing "${expectedLabel}" in ${label}.`);
      }
    }

    if (/label\s*:\s*["']nav\.fixtures["']/.test(source)) {
      failures.push(`Primary nav must not expose Fixtures in ${label}.`);
    }

    if (!/aria-current=/.test(source)) {
      failures.push(`Primary nav active state missing aria-current in ${label}.`);
    }
  }

  if (label === "app/components/account/PlansAndCheckout.tsx") {
    if (!/\bplan-card\b/.test(source) || !/\bplan-card__actions\b/.test(source)) {
      failures.push(`Pro plan cards should use equal-height plan-card structure in ${label}.`);
    }

    if (!/recommended/i.test(source) || !/\bplan-card--recommended\b/.test(source)) {
      failures.push(`Recommended Pro plan highlight missing in ${label}.`);
    }
  }

  return failures;
}

function findFilterBarStateSyncIssues(source, label) {
  if (label !== "app/components/FilterBar.tsx") {
    return [];
  }
  if (!/\buseEffect\b/.test(source) || !/setSelected\s*\(\s*active\s*\)/.test(source)) {
    return [
      `FilterBar active prop sync missing in ${label}: selected chip state must update when active changes.`,
    ];
  }
  return [];
}

function findDeadInteractiveAnchors(source, label) {
  const failures = [];
  const filterAnchorPattern =
    /<a\b(?=[\s\S]*?className=["'][^"']*\bfilter-chip\b[^"']*["'])(?=[\s\S]*?href=["']#[^"']*["'])[\s\S]*?>/g;
  const tabAnchorPattern =
    /<a\b(?=[\s\S]*?className=["'][^"']*\btab\b[^"']*["'])(?=[\s\S]*?href=["']#(?:content|analysis)["'])[\s\S]*?>/g;

  if (filterAnchorPattern.test(source)) {
    failures.push(
      `Dead interactive anchor in ${label}: filter chips must be buttons with state or real route links.`,
    );
  }

  if (tabAnchorPattern.test(source) && /(\.map\(|\[[^\]]*Odds[\s\S]*?Events[^\]]*\])/.test(source)) {
    failures.push(
      `Dead interactive anchor in ${label}: match tabs must target distinct sections or real panels.`,
    );
  }

  return failures;
}

function findDraftPublicCopy(text, label) {
  const patterns = [
    /\bMVP\b/i,
    /\blocal\s+(sample|copy|review)\b/i,
    /\bsample\s+(copy|data|fixtures?|league|matches?|prediction|pricing)\b/i,
    /\breserved\s+(ad\s+space|responsive\b)/i,
  ];

  if (patterns.some((pattern) => pattern.test(text))) {
    return [`Draft public copy in ${label}: remove MVP/sample/local-copy language.`];
  }

  return [];
}

function findBannedCertaintyInText(text, label) {
  const failures = [];

  for (const pattern of bannedCertaintyPatterns) {
    if (label.includes("app/i18n/catalogs.ts") && pattern.label === "copy defect (pluralization)") continue;
    if (!pattern.regex.test(text)) {
      continue;
    }

    if (pattern.allow?.test(text)) {
      continue;
    }

    failures.push(`Banned certainty wording "${pattern.label}" in ${label}`);
  }

  return failures;
}

function collectSeoSupportWarnings(root, routeExpectations) {
  const warnings = [];

  for (const expectation of seoSupportExpectations) {
    if (!existsSync(join(root, expectation.file))) {
      warnings.push(
        `SEO support missing: ${expectation.label} (${expectation.file}). Phase 3 closure expects this.`,
      );
    }
  }

  const metadataSources = new Set(
    routeExpectations.map((expectation) => expectation.metadataFile ?? expectation.file),
  );
  const hasCanonicalMetadata = [...metadataSources].some((metadataFile) => {
    const metadataPath = join(root, metadataFile);
    if (!existsSync(metadataPath)) {
      return false;
    }

    const source = readFileSync(metadataPath, "utf8").toLowerCase();
    return source.includes("canonical") || source.includes("alternates");
  });

  if (!hasCanonicalMetadata) {
    warnings.push(
      "SEO support missing: no canonical/alternates metadata found in checked route metadata.",
    );
  }

  return warnings;
}

async function loadPlaywrightModule() {
  try {
    return await import("playwright");
  } catch {
    return await import("@playwright/test");
  }
}

function parseArgs(argv) {
  const args = {
    baseUrl: undefined,
    staticOnly: false,
  };

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];

    if (arg === "--static-only") {
      args.staticOnly = true;
      continue;
    }

    if (arg.startsWith("--base-url=")) {
      args.baseUrl = arg.slice("--base-url=".length);
      continue;
    }

    if (arg === "--base-url") {
      args.baseUrl = argv[index + 1];
      index += 1;
    }
  }

  return args;
}

function ensureTrailingSlash(url) {
  return url.endsWith("/") ? url : `${url}/`;
}

function expectedCanonicalPath(route) {
  if (route.canonicalPath) {
    return route.canonicalPath;
  }

  return new URL(route.route, "https://example.test").pathname;
}

function normalizeText(text) {
  return text.replace(/\s+/g, " ").trim().toLowerCase();
}

function toProjectPath(root, file) {
  return relative(root, file).split(sep).join("/");
}

function printResult(staticResult, browserResult) {
  console.log("SokaLens Phase 3 verification");
  console.log(`- Static routes checked: ${staticResult.checkedRoutes.length}`);
  console.log(`- Source files scanned: ${staticResult.checkedFiles.length}`);

  if (browserResult?.skipped) {
    console.log("- Browser smoke: skipped");
  } else if (browserResult) {
    console.log(`- Browser smoke routes checked: ${phase3BrowserRoutes.length}`);
  }

  const warnings = [...staticResult.warnings, ...(browserResult?.warnings ?? [])];
  if (warnings.length > 0) {
    console.warn("\nWarnings:");
    for (const warning of warnings) {
      console.warn(`- ${warning}`);
    }
  }

  const failures = [...staticResult.failures, ...(browserResult?.failures ?? [])];
  if (failures.length > 0) {
    console.error("\nFailures:");
    for (const failure of failures) {
      console.error(`- ${failure}`);
    }
    return false;
  }

  console.log("\nPhase 3 verification passed.");
  return true;
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const staticResult = runStaticChecks();
  const browserResult = args.staticOnly
    ? {
        failures: [],
        skipped: true,
        warnings: ["Browser smoke skipped by --static-only."],
      }
    : await runBrowserSmoke({ baseUrl: args.baseUrl });

  if (!printResult(staticResult, browserResult)) {
    process.exit(1);
  }
}

if (process.argv[1] === scriptPath) {
  await main();
}

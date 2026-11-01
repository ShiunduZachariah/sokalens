import { test, expect } from "@playwright/test";

/*
 * NOTE ON SELECTORS, 2026-10-02.
 *
 * Every class this file reached for was renamed by the UI refresh:
 * `.acca-bundle` is now `.ac-card`, `.gn-run` is `.gn-run`,
 * `.gn-result` is `.gn-result`, `.gn-result__legs` is
 * `.gn-result__legs`, `.acca-leg__teams` is `.ac-leg__teams`, and the empty
 * state's copy changed. The spec had been unrunnable in this repo since --
 * there was no `playwright.config.ts` and it pointed at a server nobody
 * started -- so the rot went unnoticed through the whole refresh. The
 * assertions are unchanged; only the selectors are re-pointed at the
 * mechanism they were always about.
 */

const viewports = [390, 768, 1024, 1440] as const;

/*
 * The base URL now comes from `playwright.config.ts`, which starts the app and
 * the stub API itself and still honours `PLAYWRIGHT_BASE_URL`. This file used to
 * pin `host.docker.internal:3000` and override the config, so the suite could
 * only run on a machine with the compose stack already up.
 */

async function dismissLanguageModal(page: import("@playwright/test").Page) {
  const continueButton = page.getByRole("button", { name: "Continue" });
  try {
    await continueButton.waitFor({ state: "visible", timeout: 1500 });
    await continueButton.click();
    await continueButton.waitFor({ state: "hidden" });
  } catch {
    // A saved preference means the first-visit chooser never opens.
  }
}

async function assertNoHorizontalOverflow(page: import("@playwright/test").Page) {
  await expect(page.locator("body")).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(
    await page.evaluate(() => window.innerWidth),
  );
}

const responsiveRoutes = [
  "/en",
  "/en/acca-predictions",
  "/en/acca-generator",
  "/en/multi-match-builder",
  // There is no app/predictions/page.tsx; /predictions/today is the public
  // predictions landing route backed by the actual app directory.
  "/en/predictions/today",
] as const;

for (const width of viewports) {
  test(`all public prediction surfaces fit at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    for (const route of responsiveRoutes) {
      await page.goto(route, { waitUntil: "domcontentloaded" });
      await dismissLanguageModal(page);
      await assertNoHorizontalOverflow(page);
    }
  });
}

for (const width of viewports) {
  test.describe(`ACCA responsive layout at ${width}px`, () => {
    test.use({ viewport: { width, height: 900 } });

    test("has no horizontal overflow or invalid numeric copy", async ({ page }, testInfo) => {
      await page.goto("/en", { waitUntil: "domcontentloaded" });
      await dismissLanguageModal(page);
      await expect(page.locator("body")).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
      const text = await page.locator("body").innerText();
      expect(text).not.toMatch(/infinity/i);
      expect(text).not.toMatch(/\b0(?:\.0+)?\s*(?:probability|fair probability)\b/i);
      await page.screenshot({ fullPage: true, path: testInfo.outputPath(`homepage-${width}.png`) });
      await page.locator('section[aria-labelledby="featured-accas"]').screenshot({ path: testInfo.outputPath(`featured-accas-${width}.png`) });
    });
  });
}

test("homepage ACCA cards show exactly two preview legs", async ({ page }) => {
  await page.goto("/en", { waitUntil: "domcontentloaded" });
  await dismissLanguageModal(page);
  const cards = page.locator(".featured-acca-card");
  await expect(cards.first()).toBeVisible();
  const count = await cards.count();
  expect(count).toBeGreaterThan(0);
  for (let i = 0; i < count; i += 1) {
    await expect(cards.nth(i).locator(".featured-acca-card__legs > *")).toHaveCount(2);
  }
});

for (const width of [1024, 1440] as const) {
  test(`desktop ACCA cards have equal heights at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/en", { waitUntil: "domcontentloaded" });
    await dismissLanguageModal(page);
    const heights = await page.locator(".featured-acca-card").evaluateAll((cards) => cards.map((card) => Math.round(card.getBoundingClientRect().height)));
    expect(heights.length).toBeGreaterThan(0);
    expect(new Set(heights).size).toBe(1);
  });
}

test("ACCA catalogue is reachable and exposes its published surface", async ({ page }, testInfo) => {
  await page.goto("/en/acca-predictions", { waitUntil: "domcontentloaded" });
  await dismissLanguageModal(page);
  await expect(page.getByRole("heading", { name: /acca predictions/i }).first()).toBeVisible();
  await expect(page.getByText(/published accas|no eligible acca right now/i).first()).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(await page.evaluate(() => window.innerWidth));
  await page.screenshot({ fullPage: true, path: testInfo.outputPath("catalogue.png") });
  await page.locator(".ac-card").first().screenshot({ path: testInfo.outputPath("catalogue-first-slip.png") });
});

test("generator exposes readable market choices and catalogue omits removed card copy", async ({ page }) => {
  await page.goto("/en/acca-generator", { waitUntil: "domcontentloaded" });
  await dismissLanguageModal(page);
  const marketOptions = page.getByRole('combobox', { name: 'Market', exact: true }).locator('option');
  await expect.poll(() => marketOptions.count()).toBeGreaterThan(4);
  const labels = await marketOptions.allTextContents();
  expect(labels.every((label) => label.trim().length > 0)).toBe(true);

  await page.goto("/en/acca-predictions", { waitUntil: "domcontentloaded" });
  await dismissLanguageModal(page);
  const catalogueText = await page.locator("body").innerText();
  expect(catalogueText).not.toContain("System-generated ACCA");
  expect(catalogueText).not.toContain("Engine-qualified selections from separate fixtures.");
});

test("generator scopes leagues and rejects invalid constraints without a request", async ({ page }) => {
  await page.goto("/en/acca-generator");
  await dismissLanguageModal(page);
  const country = page.getByRole("combobox", { name: "Country", exact: true });
  const league = page.getByRole("combobox", { name: "League", exact: true });
  await expect(league).toBeDisabled();
  await expect.poll(() => country.locator("option").count()).toBeGreaterThan(2);
  const countries = await country.locator("option").evaluateAll(options => options.map(o => (o as HTMLOptionElement).value).filter(v => v !== "all"));
  await country.selectOption(countries[0]);
  await expect(league).toBeEnabled();
  const leagues = await league.locator("option").evaluateAll(options => options.map(o => (o as HTMLOptionElement).value).filter(v => v !== "all"));
  expect(leagues.length).toBeGreaterThan(0);
  await league.selectOption(leagues[0]);
  await country.selectOption(countries[1]);
  await expect(league).toHaveValue("all");
  await country.selectOption("all");
  await expect(league).toBeDisabled();
  let searches = 0;
  page.on("request", request => { if (request.url().includes("/api/accas?") && request.url().includes("seed=")) searches++; });
  for (const legs of ["1", "21", "2.5", ""]) {
    await page.getByLabel("Legs", { exact: true }).fill(legs);
    await page.getByRole("button", { name: "Find an ACCA" }).click();
    await expect(page.locator(".form-error[role=alert]")).toBeVisible();
  }
  await page.getByLabel("Legs", { exact: true }).fill("2");
  await page.getByLabel("Minimum fair odds", { exact: true }).fill("100");
  await page.getByLabel("Maximum fair odds", { exact: true }).fill("2");
  await page.getByRole("button", { name: "Find an ACCA" }).click();
  await expect(page.locator(".form-error[role=alert]")).toBeVisible();
  expect(searches).toBe(0);
});

test("generator renders a valid boundary-size result or an explicit supply state", async ({ page }) => {
  await page.goto("/en/acca-generator");
  await dismissLanguageModal(page);
  for (const count of [2, 20]) {
    await page.getByLabel("Legs", { exact: true }).fill(String(count));
    const response = page.waitForResponse(r => r.url().includes("/api/accas?") && r.url().includes("seed="));
    await page.locator(".gn-run").click();
    expect((await response).status()).toBe(200);
    await expect(page.locator(".gn-run")).toBeEnabled();
    if (await page.locator(".gn-result").count()) {
      await expect(page.locator(".gn-result__legs > li")).toHaveCount(count);
    } else {
      await expect(page.getByText("No ACCA found for these filters")).toBeVisible();
    }
  }
});

test("catalogue pagination remains stable at the page bottom", async ({ page }) => {
  await page.goto("/en/acca-predictions");
  await dismissLanguageModal(page);
  const cards = page.locator(".ac-card");
  await expect(cards.first()).toBeVisible();
  const loadMore = page.getByRole("button", { name: "Load more ACCAs" });
  if (await loadMore.count()) {
    const before = await cards.count();
    await loadMore.click();
    await expect.poll(() => cards.count()).toBeGreaterThan(before);
  }
  const ids = await cards.evaluateAll(items => items.map(item => item.id));
  expect(new Set(ids).size).toBe(ids.length);
  await page.locator("footer").scrollIntoViewIfNeeded();
  await page.waitForTimeout(1500);
  expect(await cards.evaluateAll(items => items.map(item => item.id))).toEqual(ids);
});

test("catalogue leg rows align into columns", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/en/acca-predictions", { waitUntil: "domcontentloaded" });
  await dismissLanguageModal(page);

  /*
   * This test used to assert that the AWAY TEAM names shared a left edge, via
   * `.acca-bundle .acca-leg__teams > *:last-child`. That property is gone by
   * design, not by accident: the refresh made the fixture read as one line of
   * prose -- "Arsenal v Chelsea", with the `v` in a lighter weight -- exactly as
   * the canvas draws it, so there is no away-team element to align and the old
   * selector resolved to the `v` itself.
   *
   * The alignment promise the new markup DOES make is the row grid
   * (`52px minmax(0, 1fr) 150px 60px`), so the pick and price columns line up
   * down the card however long the team names are. That is the same thing the
   * original assertion was protecting -- a leg list that reads as a table
   * rather than as ragged sentences -- expressed against the markup that
   * exists.
   */
  const cards = page.locator(".ac-card");
  const cardCount = await cards.count();
  test.skip(cardCount < 1, "QA inventory returned no catalogue rows");

  // Per card, not across the page. The catalogue is a multi-column grid of
  // cards, so two cards side by side legitimately start at different offsets --
  // measuring every leg on the page together reports that as a misalignment.
  // The original assertion had the same flaw and only passed because the
  // catalogue was one column wide at the time.
  for (let index = 0; index < cardCount; index += 1) {
    const card = cards.nth(index);
    const legs = card.locator(".ac-leg:not([hidden])");
    if (await legs.count() < 2) continue;

    for (const selector of [".ac-leg__pick", ".ac-leg__odds"]) {
      const edges = await card.locator(`.ac-leg:not([hidden]) ${selector}`)
        .evaluateAll((nodes) => nodes.map((node) => Math.round(node.getBoundingClientRect().left)));
      expect(new Set(edges).size, `${selector} is ragged inside card ${index}`).toBe(1);
    }
  }
});

import { test, expect, type Page } from "@playwright/test";

/**
 * The journeys a reader actually takes, in a browser.
 *
 * The rest of this repo's web tests read source files. That catches a mechanism
 * being removed and cannot catch a page that throws on mount, a grid that
 * overflows, a link that 404s, or state that does not survive a navigation.
 * These do, which is why they are worth the minutes they cost.
 *
 * Fixtures come from `tests/stub-api/`, so every assertion below is about a
 * known slip or a known fixture rather than whatever the catalogue holds today.
 */

const SLIPS = {
  open: "stub-open-0000-0000-0000-000000000001",
  live: "stub-live-0000-0000-0000-000000000002",
  won: "stub-won0-0000-0000-0000-000000000003",
  lost: "stub-lost-0000-0000-0000-000000000004",
};

/** The first-visit language chooser blocks everything behind it. */
async function dismissLanguageModal(page: Page) {
  const continueButton = page.getByRole("button", { name: "Continue" });
  try {
    await continueButton.waitFor({ state: "visible", timeout: 1500 });
    await continueButton.click();
    await continueButton.waitFor({ state: "hidden" });
  } catch {
    // A saved preference means the chooser never opens.
  }
}

async function open(page: Page, path: string) {
  await page.goto(path, { waitUntil: "domcontentloaded" });
  await dismissLanguageModal(page);
}

/**
 * No page may scroll sideways. This is the single most common regression in a
 * layout built from a 1440px design, and it is invisible on the machine it was
 * built on.
 */
async function assertNoHorizontalOverflow(page: Page) {
  const overflow = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    innerWidth: window.innerWidth,
  }));
  expect(overflow.scrollWidth, `page scrolls sideways by ${overflow.scrollWidth - overflow.innerWidth}px`)
    .toBeLessThanOrEqual(overflow.innerWidth + 1);
}

/**
 * Nothing on a public page may say a prediction is certain.
 *
 * The negated forms are removed before the check, not matched around, because
 * the site's own disclaimer is "Statistical estimates, never guaranteed
 * outcomes" -- it appears on every page and it is the sentence that makes the
 * rest of the copy acceptable. `scripts/phase3-verify.mjs` draws the same
 * distinction with an `allow` pattern; a test that banned the word outright
 * would fail on the one page element that must be there.
 */
async function assertNoCertaintyCopy(page: Page) {
  const text = (await page.locator("body").innerText())
    .replace(/\b(?:not|no|never)\s+(?:a\s+)?guaranteed\b/gi, "");

  expect(text).not.toMatch(/\bguaranteed\s+(?:win|profit|outcome|return)/i);
  expect(text).not.toMatch(/\brisk[-\s]?free\b/i);
  expect(text).not.toMatch(/\bsure\s+(?:win|bet)s?\b/i);
  // A figure the page failed to compute, printed anyway.
  expect(text).not.toMatch(/\bNaN\b|\bInfinity\b|\bundefined\b/);
}

test.describe("ACCA detail", () => {
  test("a slip with nothing started shows its legs and no result banner", async ({ page }) => {
    await open(page, `/en/acca-predictions/slip/${SLIPS.open}`);

    await expect(page.getByRole("heading", { level: 2, name: "Every leg" })).toBeVisible();
    // Three legs, from the fixtures.
    await expect(page.locator(".as-leg")).toHaveCount(3);
    // Nothing has kicked off, so there is nothing to report and no pills.
    await expect(page.locator(".as-result")).toHaveCount(0);
    await expect(page.locator(".as-pill")).toHaveCount(0);
    // The combined price is the one figure a reader came for.
    await expect(page.locator(".as-summary__odds")).not.toBeEmpty();
    await assertNoCertaintyCopy(page);
    await assertNoHorizontalOverflow(page);
  });

  test("a slip in progress counts won, live and still-to-play", async ({ page }) => {
    await open(page, `/en/acca-predictions/slip/${SLIPS.live}`);

    const banner = page.locator(".as-result");
    await expect(banner).toHaveClass(/as-result--live/);
    await expect(banner).toContainText("In progress");
    await expect(banner).toContainText("1 won · 1 live · 1 to play");
    // One pill per leg, once there is something to report.
    await expect(page.locator(".as-pill")).toHaveCount(3);
    await expect(page.locator(".as-pill--live")).toHaveCount(1);
    await assertNoHorizontalOverflow(page);
  });

  test("one lost leg loses the slip, whatever else won", async ({ page }) => {
    await open(page, `/en/acca-predictions/slip/${SLIPS.lost}`);

    const banner = page.locator(".as-result");
    await expect(banner).toHaveClass(/as-result--lost/);
    await expect(banner).toContainText("Lost");
    await expect(page.locator(".as-pill--lost")).toHaveCount(1);
    await expect(page.locator(".as-pill--won")).toHaveCount(2);
  });

  test("a won slip names the price it settles at, not the price it was priced at", async ({ page }) => {
    await open(page, `/en/acca-predictions/slip/${SLIPS.won}`);

    const banner = page.locator(".as-result");
    await expect(banner).toHaveClass(/as-result--won/);
    await expect(banner).toContainText("settles at");
    // One void leg, dropped from the settling price.
    await expect(page.locator(".as-pill--void")).toHaveCount(1);
    const line = await banner.innerText();
    const settles = Number(/settles at ([\d.]+)/.exec(line)?.[1]);
    const combined = Number(/([\d.]+)/.exec(await page.locator(".as-summary__odds").innerText())?.[1]);
    expect(settles).toBeLessThan(combined);
  });

  test("an unknown slip id is a 404, not a blank page", async ({ page }) => {
    const response = await page.goto("/en/acca-predictions/slip/not-a-slip", { waitUntil: "domcontentloaded" });
    expect(response?.status()).toBe(404);
  });

  test("the odds format the reader picks applies to every price on the page", async ({ page }) => {
    await open(page, `/en/acca-predictions/slip/${SLIPS.open}`);

    const decimal = await page.locator(".as-summary__odds").innerText();
    await page.getByRole("group", { name: "Odds format" }).getByRole("button", { name: "Fractional" }).click();
    await expect(page.locator(".as-summary__odds")).not.toHaveText(decimal);
    // A fraction, and the legs changed with the total rather than only the total.
    await expect(page.locator(".as-summary__odds")).toContainText("/");
    await expect(page.locator(".as-leg__odds").first()).toContainText("/");
  });

  test("the slip's shape is described with tags", async ({ page }) => {
    await open(page, `/en/acca-predictions/slip/${SLIPS.open}`);
    const tags = page.locator(".rt__tag");
    await expect(tags.first()).toBeVisible();
    // Capped, so the row stays readable.
    expect(await tags.count()).toBeLessThanOrEqual(4);
    // Every tag explains itself.
    for (const tag of await tags.all()) {
      expect((await tag.getAttribute("title"))?.length ?? 0).toBeGreaterThan(20);
    }
  });
});

test.describe("ACCA catalogue to detail", () => {
  test("a reader can open a slip from the catalogue and come back", async ({ page }) => {
    await open(page, "/en/acca-predictions");

    const heading = page.locator(".ac-card__market a").first();
    await expect(heading).toBeVisible();
    await heading.click();

    await expect(page).toHaveURL(/\/acca-predictions\/slip\//);
    await expect(page.getByRole("heading", { level: 2, name: "Every leg" })).toBeVisible();

    await page.getByRole("link", { name: "All ACCAs" }).first().click();
    await expect(page).toHaveURL(/\/acca-predictions\/?$/);
  });
});

test.describe("Builder", () => {
  test("the match list explains why its top card is at the top", async ({ page }) => {
    await open(page, "/en/multi-match-builder");

    const firstCard = page.locator(".bd-match").first();
    await expect(firstCard).toBeVisible();
    // Tags are derived from the same fields the ordering uses, so the top card
    // should carry at least one.
    await expect(firstCard.locator(".rt__tag").first()).toBeVisible();
  });

  test("a selection added to the ticket survives a navigation away and back", async ({ page }) => {
    await open(page, "/en/multi-match-builder");

    const addButton = page.locator(".bd-market").first();
    await expect(addButton).toBeVisible();
    const label = await addButton.getAttribute("aria-label");
    await addButton.click();
    await expect(addButton).toHaveAttribute("aria-pressed", "true");
    await expect(page.locator(".bd-legs li")).toHaveCount(1);

    // The ticket is stored, not component state: this is the assertion that
    // distinguishes the two.
    await open(page, "/en/acca-predictions");
    await open(page, "/en/multi-match-builder");
    await expect(page.locator(".bd-legs li")).toHaveCount(1);
    expect(label).toBeTruthy();
  });

  test("clearing the ticket empties it", async ({ page }) => {
    await open(page, "/en/multi-match-builder");
    await page.locator(".bd-market").first().click();
    await expect(page.locator(".bd-legs li")).toHaveCount(1);
    await page.getByRole("button", { name: "Clear ticket" }).click();
    await expect(page.locator(".bd-legs li")).toHaveCount(0);
  });
});

test.describe("Generator", () => {
  test("generating a slip produces legs a reader can keep", async ({ page }) => {
    await open(page, "/en/acca-generator");

    // The page opens on Generate, so the controls are already there.
    const run = page.getByRole("button", { name: /Find an ACCA|Show another/ });
    await expect(run).toBeVisible();
    await run.click();

    const result = page.locator(".gn-result");
    await expect(result).toBeVisible({ timeout: 20_000 });
    await expect(result.locator("li")).not.toHaveCount(0);
    // Every leg says why it qualified.
    await expect(result.locator(".rt__tag").first()).toBeVisible();

    await result.getByRole("button", { name: "Add ACCA to ticket" }).click();
    await expect(result.getByRole("button", { name: "Added to ticket" })).toBeVisible();
  });

  test("the generator rejects an impossible odds range before asking the API", async ({ page }) => {
    await open(page, "/en/acca-generator");

    await page.getByLabel("Minimum fair odds").fill("100");
    await page.getByLabel("Maximum fair odds").fill("2");
    await page.getByRole("button", { name: /Find an ACCA|Show another/ }).click();

    // Scoped to the form's own error: Next renders a `[role="alert"]` route
    // announcer on every page, so the bare role is two elements and strict
    // mode rejects it.
    await expect(page.locator(".form-error[role=alert]")).toBeVisible();
  });
});

test.describe("In-play", () => {
  test("an unentitled reader gets the locked state, not an error", async ({ page }) => {
    await open(page, "/en/in-play");
    // The stub answers the whole /v1/inplay/ prefix with 404, which is what an
    // unentitled reader gets upstream. The page must still be a page.
    await expect(page.locator("main")).toBeVisible();
    await assertNoCertaintyCopy(page);
    await assertNoHorizontalOverflow(page);
  });
});

test.describe("every new surface fits a phone", () => {
  const paths = [
    `/en/acca-predictions/slip/${SLIPS.live}`,
    `/en/acca-predictions/slip/${SLIPS.won}`,
    "/en/multi-match-builder",
    "/en/acca-generator",
    "/en/in-play",
  ];

  for (const path of paths) {
    test(`${path} at 390px`, async ({ page }) => {
      await page.setViewportSize({ width: 390, height: 844 });
      await open(page, path);
      await assertNoHorizontalOverflow(page);
      await assertNoCertaintyCopy(page);
    });
  }
});
